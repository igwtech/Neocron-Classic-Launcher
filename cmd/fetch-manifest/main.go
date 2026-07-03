// Command fetch-manifest fetches the Neocron Classic layout manifest the way
// the official launcher does — no Discord login required (confirmed by RE +
// live mitmproxy capture, docs/RE_LAUNCHER.md §6.11):
//
//	GET https://areamc5.neocron.org/{channel}/{SERVER_KEY}/latest
//
// SERVER_KEY is a static string hardcoded in the launcher's ApiClient ctor.
// The response is {releaseId, tag, fileManifest:[{filePath, link, size, hash}]}.
// Each file's `link` is a direct, public download from cdn.neocron.org; `hash`
// is uppercase SHA-256. Optionally verifies/downloads against an install dir.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ServerKey is the static content-API key baked into NeocronLauncher.exe
// (ApiClient ctor, FUN_140117730). Not a secret — extractable from the binary;
// it just gates the public content API.
const ServerKey = "a96f4803d335e41c23486a43ea22a2bf5226cb8a2501da2b25867bccf42be6e1"

const userAgent = "NeocronLauncher-cpp/2.2.6"

// ManifestFile mirrors ncl::dto::ManifestFile.
type ManifestFile struct {
	FilePath string `json:"filePath"`
	Link     string `json:"link"`
	Size     int64  `json:"size"`
	Hash     string `json:"hash"` // uppercase SHA-256 hex
}

// Manifest is the areamc5 /{channel}/{key}/latest response.
type Manifest struct {
	ReleaseID    int            `json:"releaseId"`
	Tag          string         `json:"tag"`
	FileManifest []ManifestFile `json:"fileManifest"`
}

func main() {
	channel := flag.String("channel", "ncc-pts", "release channel")
	api := flag.String("api", "https://areamc5.neocron.org", "content API base")
	key := flag.String("key", ServerKey, "static server key")
	out := flag.String("out", "manifest.json", "save manifest JSON here")
	verify := flag.String("verify", "", "install dir to verify against the manifest (optional)")
	flag.Parse()

	url := fmt.Sprintf("%s/%s/%s/latest", *api, *channel, *key)
	m, raw, err := fetch(url)
	if err != nil {
		fmt.Fprintln(os.Stderr, "fetch:", err)
		os.Exit(1)
	}
	_ = os.WriteFile(*out, raw, 0o644)
	fmt.Printf("release %d (tag %s): %d files — saved to %s\n", m.ReleaseID, m.Tag, len(m.FileManifest), *out)

	if *verify != "" {
		ok, bad, missing := 0, 0, 0
		for _, f := range m.FileManifest {
			h, err := sha256File(filepath.Join(*verify, filepath.FromSlash(f.FilePath)))
			switch {
			case os.IsNotExist(err):
				missing++
				fmt.Printf("  MISSING  %s\n", f.FilePath)
			case err != nil:
				bad++
			case strings.EqualFold(h, f.Hash):
				ok++
			default:
				bad++
				fmt.Printf("  MISMATCH %s\n", f.FilePath)
			}
		}
		fmt.Printf("verify: %d ok, %d mismatch, %d missing of %d\n", ok, bad, missing, len(m.FileManifest))
	}
}

func fetch(url string) (*Manifest, []byte, error) {
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	req.Header.Set("User-Agent", userAgent)
	c := &http.Client{Timeout: 30 * time.Second}
	resp, err := c.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return nil, raw, fmt.Errorf("HTTP %d: %s", resp.StatusCode, truncate(raw, 200))
	}
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, raw, fmt.Errorf("bad JSON: %w", err)
	}
	if m.FileManifest == nil {
		return nil, raw, fmt.Errorf("empty/null manifest (body=%s)", truncate(raw, 80))
	}
	return &m, raw, nil
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

func truncate(b []byte, n int) string {
	s := strings.TrimSpace(string(b))
	if len(s) <= n {
		return s
	}
	return s[:n]
}
