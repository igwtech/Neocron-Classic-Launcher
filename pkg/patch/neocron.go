package patch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// DefaultServerKey is the static content-API key hardcoded in the official
// launcher's ApiClient constructor (see docs/RE_LAUNCHER.md §6.11). It is not a
// secret — it just gates the public content API — and is required as a URL path
// segment. No Discord/Bearer auth is needed to read the manifest or content.
const DefaultServerKey = "a96f4803d335e41c23486a43ea22a2bf5226cb8a2501da2b25867bccf42be6e1"

// ManifestFile mirrors ncl::dto::ManifestFile — one entry in the layout manifest.
type ManifestFile struct {
	FilePath string `json:"filePath"` // install-relative path
	Link     string `json:"link"`     // direct public download URL
	Size     int64  `json:"size"`
	Hash     string `json:"hash"` // uppercase SHA-256 hex
}

// Manifest is the areamc5 /{channel}/{key}/latest response.
type Manifest struct {
	ReleaseID    int            `json:"releaseId"`
	Tag          string         `json:"tag"`
	FileManifest []ManifestFile `json:"fileManifest"`
}

// NeocronPatcher installs/updates a channel by fetching the official layout
// manifest from areamc5.neocron.org and downloading each file from its published
// direct link, verifying SHA-256. No authentication required.
type NeocronPatcher struct {
	InstallDir string
	APIBase    string // https://areamc5.neocron.org
	Channel    string // ncc-pts
	ServerKey  string // static key (DefaultServerKey)
	UserAgent  string
	HTTP       *http.Client
}

var _ Patcher = (*NeocronPatcher)(nil)

// NewNeocronPatcher builds a patcher with sane defaults.
func NewNeocronPatcher(installDir, apiBase, channel, serverKey, ua string) *NeocronPatcher {
	if serverKey == "" {
		serverKey = DefaultServerKey
	}
	if apiBase == "" {
		apiBase = "https://areamc5.neocron.org"
	}
	return &NeocronPatcher{
		InstallDir: installDir,
		APIBase:    strings.TrimRight(apiBase, "/"),
		Channel:    channel,
		ServerKey:  serverKey,
		UserAgent:  ua,
		HTTP:       &http.Client{}, // large downloads: no overall timeout
	}
}

// FetchManifest GETs {api}/{channel}/{key}/latest and parses it.
func (p *NeocronPatcher) FetchManifest(ctx context.Context) (*Manifest, error) {
	url := fmt.Sprintf("%s/%s/%s/latest", p.APIBase, p.Channel, p.ServerKey)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
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
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("manifest %s: HTTP %d", url, resp.StatusCode)
	}
	var m Manifest
	if err := json.Unmarshal(body, &m); err != nil {
		return nil, fmt.Errorf("manifest: bad JSON: %w", err)
	}
	if m.FileManifest == nil {
		return nil, fmt.Errorf("manifest: empty/null (channel/key wrong?): %s", truncateStr(string(body), 80))
	}
	return &m, nil
}

// CheckUpdate reports whether any manifest file is missing or hash-mismatched.
func (p *NeocronPatcher) CheckUpdate(ctx context.Context) (bool, error) {
	m, err := p.FetchManifest(ctx)
	if err != nil {
		return false, err
	}
	for _, f := range m.FileManifest {
		ok, err := p.fileOK(f)
		if err != nil {
			return false, err
		}
		if !ok {
			return true, nil
		}
	}
	return false, nil
}

// Apply downloads every missing/changed file, verifies it, and writes it in place.
func (p *NeocronPatcher) Apply(ctx context.Context, cb Callbacks) error {
	m, err := p.FetchManifest(ctx)
	if err != nil {
		return err
	}
	step(cb, "Checking files", 0, 2)
	var todo []ManifestFile
	for _, f := range m.FileManifest {
		ok, err := p.fileOK(f)
		if err != nil {
			return err
		}
		if !ok {
			todo = append(todo, f)
		}
	}
	if len(todo) == 0 {
		return nil
	}
	step(cb, fmt.Sprintf("Downloading %d file(s)", len(todo)), 1, 2)
	for i, f := range todo {
		if err := ctx.Err(); err != nil {
			return err
		}
		label := fmt.Sprintf("%s (%d/%d)", f.FilePath, i+1, len(todo))
		if err := p.downloadFile(ctx, f, cb, label); err != nil {
			return fmt.Errorf("download %s: %w", f.FilePath, err)
		}
		if ok, _ := p.fileOK(f); !ok {
			return fmt.Errorf("verify failed after download: %s", f.FilePath)
		}
	}
	return nil
}

// Verify re-hashes every manifest file, returning the paths that fail.
func (p *NeocronPatcher) Verify(ctx context.Context, cb Callbacks) ([]string, error) {
	m, err := p.FetchManifest(ctx)
	if err != nil {
		return nil, err
	}
	var bad []string
	for i, f := range m.FileManifest {
		if err := ctx.Err(); err != nil {
			return bad, err
		}
		progress(cb, Progress{Label: f.FilePath, Current: int64(i + 1), Total: int64(len(m.FileManifest))})
		ok, err := p.fileOK(f)
		if err != nil {
			return bad, err
		}
		if !ok {
			bad = append(bad, f.FilePath)
		}
	}
	return bad, nil
}

// --- internals --------------------------------------------------------------

func (p *NeocronPatcher) fileOK(f ManifestFile) (bool, error) {
	full := filepath.Join(p.InstallDir, filepath.FromSlash(f.FilePath))
	fi, err := os.Stat(full)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	if f.Size > 0 && fi.Size() != f.Size {
		return false, nil // size mismatch — skip the expensive hash
	}
	h, err := sha256File(full)
	if err != nil {
		return false, err
	}
	return strings.EqualFold(h, f.Hash), nil
}

func (p *NeocronPatcher) downloadFile(ctx context.Context, f ManifestFile, cb Callbacks, label string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.Link, nil)
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
	dest := filepath.Join(p.InstallDir, filepath.FromSlash(f.FilePath))
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dest), ".dl-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	total := f.Size
	if total == 0 {
		total = resp.ContentLength
	}
	pw := &progressWriter{cb: cb, label: label, total: total}
	if _, err := io.Copy(io.MultiWriter(tmp, pw), resp.Body); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, dest)
}

func (p *NeocronPatcher) client() *http.Client {
	if p.HTTP != nil {
		return p.HTTP
	}
	return http.DefaultClient
}

func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func truncateStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
