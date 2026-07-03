package patch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNeocronPatcherEndToEnd(t *testing.T) {
	// Content the "server" serves.
	files := map[string]string{
		"Neocron.exe":          "fake exe bytes",
		"ini/net.ini":          "[net]\nserver=1\n",
		"data/fonts/arial.ttf": strings.Repeat("A", 5000),
	}
	hashOf := func(s string) string {
		sum := sha256.Sum256([]byte(s))
		return strings.ToUpper(hex.EncodeToString(sum[:])) // manifest hashes are uppercase
	}

	mux := http.NewServeMux()
	// content endpoints
	for path, body := range files {
		b := body
		mux.HandleFunc("/content/"+path, func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(b))
		})
	}
	var srv *httptest.Server
	// manifest endpoint: /{channel}/{key}/latest
	mux.HandleFunc("/ncc-pts/testkey/latest", func(w http.ResponseWriter, r *http.Request) {
		m := Manifest{ReleaseID: 1, Tag: "1.0.0"}
		for path, body := range files {
			m.FileManifest = append(m.FileManifest, ManifestFile{
				FilePath: path, Link: srv.URL + "/content/" + path,
				Size: int64(len(body)), Hash: hashOf(body),
			})
		}
		json.NewEncoder(w).Encode(m)
	})
	srv = httptest.NewServer(mux)
	defer srv.Close()

	dir := t.TempDir()
	p := NewNeocronPatcher(dir, srv.URL, "ncc-pts", "testkey", "test-ua")
	ctx := context.Background()

	// Fresh dir → update needed.
	need, err := p.CheckUpdate(ctx)
	if err != nil || !need {
		t.Fatalf("CheckUpdate on empty dir = %v, %v; want true", need, err)
	}

	// Apply downloads everything.
	var steps int
	if err := p.Apply(ctx, Callbacks{OnStep: func(Step) { steps++ }}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	// Every file present with correct content.
	for path, body := range files {
		got, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(path)))
		if err != nil || string(got) != body {
			t.Errorf("file %s: got %q (err %v), want %q", path, got, err, body)
		}
	}

	// Now up-to-date.
	need, err = p.CheckUpdate(ctx)
	if err != nil || need {
		t.Fatalf("CheckUpdate after Apply = %v, %v; want false", need, err)
	}
	// Verify reports no bad files.
	bad, err := p.Verify(ctx, Callbacks{})
	if err != nil || len(bad) != 0 {
		t.Fatalf("Verify = %v, %v; want no bad files", bad, err)
	}

	// Corrupt a file → detected + repaired.
	corrupt := filepath.Join(dir, "ini", "net.ini")
	os.WriteFile(corrupt, []byte("tampered"), 0o644)
	bad, _ = p.Verify(ctx, Callbacks{})
	if len(bad) != 1 || bad[0] != "ini/net.ini" {
		t.Fatalf("Verify after corruption = %v; want [ini/net.ini]", bad)
	}
	if err := p.Apply(ctx, Callbacks{}); err != nil {
		t.Fatalf("repair Apply: %v", err)
	}
	if got, _ := os.ReadFile(corrupt); string(got) != files["ini/net.ini"] {
		t.Errorf("repair did not restore net.ini: %q", got)
	}
}

func TestNeocronPatcherNullManifestErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "null") // the catch-all/no-auth response we saw during RE
	}))
	defer srv.Close()
	p := NewNeocronPatcher(t.TempDir(), srv.URL, "ncc-pts", "k", "ua")
	if _, err := p.FetchManifest(context.Background()); err == nil {
		t.Fatal("expected error on null manifest body")
	}
}
