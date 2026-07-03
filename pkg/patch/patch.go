// Package patch installs and updates the game files.
//
// The official launcher delegates game patching to patchy.dll — a bespoke,
// signed binary-diff format whose version-blob layout and per-channel content
// URL are not yet fully recovered (see docs/RE_LAUNCHER.md §6). This package
// exposes a Patcher interface and ships a hash-manifest implementation that
// mirrors the launcher's own manifest format (uppercase "HASH:path" lines,
// SHA-256). When the patchy format is reversed (or the real DLL is bridged over
// its C ABI), a second Patcher can drop in behind the same interface.
package patch

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"
)

// Step is a coarse phase reported to the UI (maps to nclSetStep).
type Step struct {
	Label string
	Index int
	Total int
}

// Progress is a fine-grained byte/file progress (maps to nclSetProgress).
type Progress struct {
	Label   string
	Current int64
	Total   int64
}

// Callbacks receive patch progress. Any may be nil.
type Callbacks struct {
	OnStep     func(Step)
	OnProgress func(Progress)
}

// Patcher installs/updates a channel's game files into a directory.
type Patcher interface {
	// CheckUpdate reports whether the install differs from the channel's latest.
	CheckUpdate(ctx context.Context) (needsUpdate bool, err error)
	// Apply brings the install to the channel's latest, downloading as needed.
	Apply(ctx context.Context, cb Callbacks) error
	// Verify re-hashes every file against the manifest (the "File Check" action).
	Verify(ctx context.Context, cb Callbacks) ([]string, error)
}

// entry is one manifest line: sha256 hex (lowercased) + relative path.
type entry struct {
	Hash string
	Path string
}

// HashManifestPatcher patches by comparing local file hashes to a remote
// "HASH:path" manifest and downloading changed files from a file base URL.
type HashManifestPatcher struct {
	InstallDir  string
	ManifestURL string       // e.g. https://cdn.neocron.org/<channel>/manifest.txt
	FileBaseURL string       // files fetched from FileBaseURL/<path>
	UserAgent   string
	HTTP        *http.Client
}

// NewHashManifestPatcher constructs a patcher with sane HTTP defaults.
func NewHashManifestPatcher(installDir, manifestURL, fileBaseURL, ua string) *HashManifestPatcher {
	return &HashManifestPatcher{
		InstallDir:  installDir,
		ManifestURL: manifestURL,
		FileBaseURL: strings.TrimRight(fileBaseURL, "/"),
		UserAgent:   ua,
		HTTP:        &http.Client{Timeout: 0}, // large downloads: no overall timeout
	}
}

var _ Patcher = (*HashManifestPatcher)(nil)

// CheckUpdate returns true if any manifest file is missing or hash-mismatched.
func (p *HashManifestPatcher) CheckUpdate(ctx context.Context) (bool, error) {
	entries, err := p.fetchManifest(ctx)
	if err != nil {
		return false, err
	}
	for _, e := range entries {
		ok, err := p.fileMatches(e)
		if err != nil {
			return false, err
		}
		if !ok {
			return true, nil
		}
	}
	return false, nil
}

// Apply downloads every missing/mismatched file, then verifies it.
func (p *HashManifestPatcher) Apply(ctx context.Context, cb Callbacks) error {
	if p.FileBaseURL == "" {
		return fmt.Errorf("patch: file base URL not configured for this channel")
	}
	entries, err := p.fetchManifest(ctx)
	if err != nil {
		return err
	}
	step(cb, "Checking files", 0, 2)

	var todo []entry
	for _, e := range entries {
		ok, err := p.fileMatches(e)
		if err != nil {
			return err
		}
		if !ok {
			todo = append(todo, e)
		}
	}
	if len(todo) == 0 {
		return nil
	}

	step(cb, "Downloading update", 1, 2)
	for i, e := range todo {
		if err := ctx.Err(); err != nil {
			return err
		}
		label := fmt.Sprintf("%s (%d/%d)", e.Path, i+1, len(todo))
		if err := p.download(ctx, e, cb, label); err != nil {
			return fmt.Errorf("download %s: %w", e.Path, err)
		}
		if ok, _ := p.fileMatches(e); !ok {
			return fmt.Errorf("verify failed after download: %s", e.Path)
		}
	}
	return nil
}

// Verify re-hashes all files, returning the relative paths that fail.
func (p *HashManifestPatcher) Verify(ctx context.Context, cb Callbacks) ([]string, error) {
	entries, err := p.fetchManifest(ctx)
	if err != nil {
		return nil, err
	}
	var bad []string
	for i, e := range entries {
		if err := ctx.Err(); err != nil {
			return bad, err
		}
		progress(cb, Progress{Label: e.Path, Current: int64(i + 1), Total: int64(len(entries))})
		ok, err := p.fileMatches(e)
		if err != nil {
			return bad, err
		}
		if !ok {
			bad = append(bad, e.Path)
		}
	}
	return bad, nil
}

// --- internals --------------------------------------------------------------

func (p *HashManifestPatcher) fetchManifest(ctx context.Context) ([]entry, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.ManifestURL, nil)
	if err != nil {
		return nil, err
	}
	if p.UserAgent != "" {
		req.Header.Set("User-Agent", p.UserAgent)
	}
	resp, err := p.client().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("manifest %s: HTTP %d", p.ManifestURL, resp.StatusCode)
	}
	return parseManifest(resp.Body)
}

// parseManifest reads "HASH:path" lines (BOM- and case-tolerant).
func parseManifest(r io.Reader) ([]entry, error) {
	var out []entry
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	first := true
	for sc.Scan() {
		line := sc.Text()
		if first {
			line = strings.TrimPrefix(line, "\uFEFF") // UTF-8 BOM
			first = false
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		i := strings.IndexByte(line, ':')
		if i <= 0 {
			continue
		}
		out = append(out, entry{
			Hash: strings.ToLower(strings.TrimSpace(line[:i])),
			Path: strings.TrimSpace(line[i+1:]),
		})
	}
	return out, sc.Err()
}

func (p *HashManifestPatcher) fileMatches(e entry) (bool, error) {
	f, err := os.Open(filepath.Join(p.InstallDir, filepath.FromSlash(e.Path)))
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return false, err
	}
	return hex.EncodeToString(h.Sum(nil)) == e.Hash, nil
}

func (p *HashManifestPatcher) download(ctx context.Context, e entry, cb Callbacks, label string) error {
	url := p.FileBaseURL + "/" + e.Path
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	if p.UserAgent != "" {
		req.Header.Set("User-Agent", p.UserAgent)
	}
	resp, err := p.client().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	dest := filepath.Join(p.InstallDir, filepath.FromSlash(e.Path))
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dest), ".dl-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	pw := &progressWriter{cb: cb, label: label, total: resp.ContentLength}
	if _, err := io.Copy(io.MultiWriter(tmp, pw), resp.Body); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, dest)
}

func (p *HashManifestPatcher) client() *http.Client {
	if p.HTTP != nil {
		return p.HTTP
	}
	return http.DefaultClient
}

// progressWriter throttles Progress callbacks to ~20/s.
type progressWriter struct {
	cb     Callbacks
	label  string
	total  int64
	done   int64
	lastNS int64
}

func (w *progressWriter) Write(b []byte) (int, error) {
	n := len(b)
	cur := atomic.AddInt64(&w.done, int64(n))
	now := time.Now().UnixNano()
	if now-w.lastNS > int64(50*time.Millisecond) || cur == w.total {
		w.lastNS = now
		progress(w.cb, Progress{Label: w.label, Current: cur, Total: w.total})
	}
	return n, nil
}

func step(cb Callbacks, label string, i, total int) {
	if cb.OnStep != nil {
		cb.OnStep(Step{Label: label, Index: i, Total: total})
	}
}

func progress(cb Callbacks, p Progress) {
	if cb.OnProgress != nil {
		cb.OnProgress(p)
	}
}
