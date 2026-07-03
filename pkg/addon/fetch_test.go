package addon

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// makeZip builds an in-memory zip archive containing the given path -> content
// entries.
func makeZip(t *testing.T, files map[string]string) []byte {
	t.Helper()
	buf := &bytes.Buffer{}
	zw := zip.NewWriter(buf)
	for name, content := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func makeTarGz(t *testing.T, files map[string]string) []byte {
	t.Helper()
	buf := &bytes.Buffer{}
	gw := gzip.NewWriter(buf)
	tw := tar.NewWriter(gw)
	for name, content := range files {
		hdr := &tar.Header{
			Name:     name,
			Mode:     0644,
			Size:     int64(len(content)),
			Typeflag: tar.TypeReg,
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	tw.Close()
	gw.Close()
	return buf.Bytes()
}

func newFetchManager(t *testing.T) *Manager {
	t.Helper()
	return &Manager{
		DataDir:    t.TempDir(),
		InstallDir: t.TempDir(),
		Logger:     log.New(os.Stderr, "[fetch-test] ", 0),
	}
}

func TestStageFetched_zipWithNestedFile(t *testing.T) {
	zipBytes := makeZip(t, map[string]string{
		"MS/x86/D3D8.dll":         "DGVOODOO-DLL-CONTENT",
		"MS/x64/D3D8.dll":         "WRONG-ARCH",
		"3Dfx/Glide.dll":          "UNRELATED",
		"dgVoodooCpl.exe":         "CPL-EXE",
		"License/dgVoodoo.license": "LICENSE-TEXT",
	})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(zipBytes)
	}))
	defer srv.Close()

	m := newFetchManager(t)
	cacheDir := filepath.Join(m.DataDir, "cache")
	os.MkdirAll(cacheDir, 0755)

	entry := FetchEntry{
		From:    srv.URL + "/dgVoodoo2.zip",
		Extract: "zip",
		Files: []FileEntry{
			{Src: "MS/x86/D3D8.dll", Dst: "D3D8.dll"},
		},
	}

	if err := m.stageFetched(entry, cacheDir, nil); err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(filepath.Join(cacheDir, "D3D8.dll"))
	if err != nil {
		t.Fatalf("expected D3D8.dll in cache: %v", err)
	}
	if string(got) != "DGVOODOO-DLL-CONTENT" {
		t.Errorf("wrong x86 D3D8.dll selected — got %q", got)
	}

	// Other files MUST NOT have leaked into cache.
	for _, leaked := range []string{"MS/x64/D3D8.dll", "3Dfx/Glide.dll", "dgVoodooCpl.exe"} {
		if _, err := os.Stat(filepath.Join(cacheDir, leaked)); err == nil {
			t.Errorf("file %q should not have been staged", leaked)
		}
	}
}

func TestStageFetched_tarGz(t *testing.T) {
	tarBytes := makeTarGz(t, map[string]string{
		"shaders/Shaders/SMAA.fx":  "SMAA-FX",
		"shaders/Textures/lut.png": "LUT-PNG",
		"shaders/README.md":        "README",
	})

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(tarBytes)
	}))
	defer srv.Close()

	m := newFetchManager(t)
	cacheDir := filepath.Join(m.DataDir, "cache")
	os.MkdirAll(cacheDir, 0755)

	entry := FetchEntry{
		From:    srv.URL + "/shaders.tar.gz",
		Extract: "tar.gz",
		Files: []FileEntry{
			{Src: "shaders/Shaders", Dst: "reshade-shaders/Shaders"},
			{Src: "shaders/Textures", Dst: "reshade-shaders/Textures"},
		},
	}

	if err := m.stageFetched(entry, cacheDir, nil); err != nil {
		t.Fatal(err)
	}

	if got, _ := os.ReadFile(filepath.Join(cacheDir, "reshade-shaders/Shaders/SMAA.fx")); string(got) != "SMAA-FX" {
		t.Errorf("Shaders/SMAA.fx not staged correctly: got %q", got)
	}
	if got, _ := os.ReadFile(filepath.Join(cacheDir, "reshade-shaders/Textures/lut.png")); string(got) != "LUT-PNG" {
		t.Errorf("Textures/lut.png not staged correctly: got %q", got)
	}
	// README should NOT have been staged — not in entry.Files.
	if _, err := os.Stat(filepath.Join(cacheDir, "reshade-shaders/README.md")); err == nil {
		t.Errorf("README should not have been staged")
	}
}

func TestStageFetched_rawSingleFile(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("RAW-DLL-BYTES"))
	}))
	defer srv.Close()

	m := newFetchManager(t)
	cacheDir := filepath.Join(m.DataDir, "cache")
	os.MkdirAll(cacheDir, 0755)

	entry := FetchEntry{
		From: srv.URL + "/d3d8.dll",
		// Extract: "" — raw mode
		Files: []FileEntry{
			{Src: "d3d8.dll", Dst: "D3D8.dll"},
		},
	}

	if err := m.stageFetched(entry, cacheDir, nil); err != nil {
		t.Fatal(err)
	}

	if got, _ := os.ReadFile(filepath.Join(cacheDir, "D3D8.dll")); string(got) != "RAW-DLL-BYTES" {
		t.Errorf("raw download not staged: got %q", got)
	}
}

func TestStageFetched_httpErrorPropagates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	m := newFetchManager(t)
	cacheDir := filepath.Join(m.DataDir, "cache")
	os.MkdirAll(cacheDir, 0755)

	err := m.stageFetched(FetchEntry{
		From:    srv.URL + "/missing.zip",
		Extract: "zip",
		Files:   []FileEntry{{Src: "x", Dst: "x"}},
	}, cacheDir, nil)

	if err == nil || !strings.Contains(err.Error(), "404") {
		t.Errorf("expected 404 error, got %v", err)
	}
}

func TestStageFetched_rejectsNonHTTPURL(t *testing.T) {
	m := newFetchManager(t)
	err := m.stageFetched(FetchEntry{
		From:    "file:///etc/passwd",
		Extract: "",
		Files:   []FileEntry{{Src: "passwd", Dst: "x"}},
	}, t.TempDir(), nil)
	if err == nil || !strings.Contains(err.Error(), "http") {
		t.Errorf("file:// URL should be rejected, got %v", err)
	}
}

func TestUnzip_pathTraversalIgnored(t *testing.T) {
	zipBytes := makeZip(t, map[string]string{
		"../escape.txt":      "ESCAPED",
		"normal/inside.txt":  "OK",
	})
	tmp := t.TempDir()
	archive := filepath.Join(tmp, "a.zip")
	os.WriteFile(archive, zipBytes, 0644)
	dest := filepath.Join(tmp, "extracted")

	if err := unzipTo(archive, dest); err != nil {
		t.Fatal(err)
	}
	// The traversal entry must not have written outside dest.
	if _, err := os.Stat(filepath.Join(tmp, "escape.txt")); err == nil {
		t.Errorf("path traversal entry escaped destDir")
	}
	if _, err := os.Stat(filepath.Join(dest, "normal/inside.txt")); err != nil {
		t.Errorf("legitimate entry should have been extracted: %v", err)
	}
}

func TestStageFetched_globRoutesByExtension(t *testing.T) {
	// Mirrors the crosire/reshade-shaders nvidia branch layout: shaders
	// and textures share one ShadersAndTextures/ dir; ReShade expects them
	// split. One fetch entry, multiple globs routed to different dst dirs.
	tarBytes := makeTarGz(t, map[string]string{
		"reshade-shaders-nvidia/ShadersAndTextures/SMAA.fx":      "SMAA-FX",
		"reshade-shaders-nvidia/ShadersAndTextures/SMAA.fxh":     "SMAA-FXH",
		"reshade-shaders-nvidia/ShadersAndTextures/Bloom.fx":     "BLOOM-FX",
		"reshade-shaders-nvidia/ShadersAndTextures/lut.png":      "LUT-PNG",
		"reshade-shaders-nvidia/ShadersAndTextures/dirt.dds":     "DIRT-DDS",
		"reshade-shaders-nvidia/ShadersAndTextures/noise.tga":    "NOISE-TGA",
		"reshade-shaders-nvidia/README.md":                       "README",
		"reshade-shaders-nvidia/LICENSE":                         "LICENSE",
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(tarBytes)
	}))
	defer srv.Close()

	m := newFetchManager(t)
	cacheDir := filepath.Join(m.DataDir, "cache")
	os.MkdirAll(cacheDir, 0755)

	entry := FetchEntry{
		From:    srv.URL + "/shaders.tar.gz",
		Extract: "tar.gz",
		Files: []FileEntry{
			{Src: "reshade-shaders-nvidia/ShadersAndTextures/*.fx", Dst: "reshade-shaders/Shaders"},
			{Src: "reshade-shaders-nvidia/ShadersAndTextures/*.fxh", Dst: "reshade-shaders/Shaders"},
			{Src: "reshade-shaders-nvidia/ShadersAndTextures/*.png", Dst: "reshade-shaders/Textures"},
			{Src: "reshade-shaders-nvidia/ShadersAndTextures/*.dds", Dst: "reshade-shaders/Textures"},
			{Src: "reshade-shaders-nvidia/ShadersAndTextures/*.tga", Dst: "reshade-shaders/Textures"},
		},
	}

	if err := m.stageFetched(entry, cacheDir, nil); err != nil {
		t.Fatal(err)
	}

	wantShaders := map[string]string{
		"reshade-shaders/Shaders/SMAA.fx":   "SMAA-FX",
		"reshade-shaders/Shaders/SMAA.fxh":  "SMAA-FXH",
		"reshade-shaders/Shaders/Bloom.fx":  "BLOOM-FX",
		"reshade-shaders/Textures/lut.png":  "LUT-PNG",
		"reshade-shaders/Textures/dirt.dds": "DIRT-DDS",
		"reshade-shaders/Textures/noise.tga": "NOISE-TGA",
	}
	for rel, want := range wantShaders {
		got, err := os.ReadFile(filepath.Join(cacheDir, rel))
		if err != nil {
			t.Errorf("%s not staged: %v", rel, err)
			continue
		}
		if string(got) != want {
			t.Errorf("%s: got %q want %q", rel, got, want)
		}
	}
	// README and LICENSE must NOT have leaked — no glob matches them.
	for _, leaked := range []string{
		"reshade-shaders/Shaders/README.md",
		"reshade-shaders/Textures/README.md",
		"reshade-shaders/Shaders/LICENSE",
	} {
		if _, err := os.Stat(filepath.Join(cacheDir, leaked)); err == nil {
			t.Errorf("%s should not have been staged", leaked)
		}
	}
}

func TestStageFetched_globNoMatchesIsNotError(t *testing.T) {
	tarBytes := makeTarGz(t, map[string]string{
		"root/file.txt": "ok",
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(tarBytes)
	}))
	defer srv.Close()

	m := newFetchManager(t)
	cacheDir := filepath.Join(m.DataDir, "cache")
	os.MkdirAll(cacheDir, 0755)

	// The glob matches nothing — that's a soft skip, not a hard error.
	// Otherwise an empty branch (e.g. shader pack with no .tga textures)
	// would refuse to install.
	err := m.stageFetched(FetchEntry{
		From:    srv.URL + "/empty.tar.gz",
		Extract: "tar.gz",
		Files: []FileEntry{
			{Src: "root/*.nonexistent", Dst: "out"},
		},
	}, cacheDir, nil)
	if err != nil {
		t.Errorf("non-matching glob should be a soft skip, got %v", err)
	}
}

func TestStageFetched_exeRequires7z(t *testing.T) {
	// Skip the test transparently if 7z isn't installed in this CI env —
	// the production code returns a clear error to the user, but covering
	// that path requires PATH-mocking we don't bother with here.
	if _, err := exec.LookPath("7z"); err != nil {
		t.Skip("7z not in PATH")
	}

	// Build a real .zip and serve it as if it were a .exe — 7z handles
	// both formats, so this exercises the same code path the production
	// ReShade installer fetch would hit.
	zipBytes := makeZip(t, map[string]string{
		"ReShade32.dll": "RESHADE-32-PAYLOAD",
		"ReShade64.dll": "RESHADE-64-PAYLOAD",
		"License.txt":   "BSD-3-CLAUSE",
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(zipBytes)
	}))
	defer srv.Close()

	m := newFetchManager(t)
	cacheDir := filepath.Join(m.DataDir, "cache")
	os.MkdirAll(cacheDir, 0755)

	entry := FetchEntry{
		From:    srv.URL + "/ReShade_Setup.exe",
		Extract: "exe",
		Files: []FileEntry{
			{Src: "ReShade32.dll", Dst: "dxgi.dll"},
		},
	}
	if err := m.stageFetched(entry, cacheDir, nil); err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(filepath.Join(cacheDir, "dxgi.dll"))
	if err != nil {
		t.Fatalf("dxgi.dll not staged: %v", err)
	}
	if string(got) != "RESHADE-32-PAYLOAD" {
		t.Errorf("wrong DLL selected: got %q", got)
	}
	// 64-bit DLL and license must not leak — only ReShade32 was requested.
	if _, err := os.Stat(filepath.Join(cacheDir, "ReShade64.dll")); err == nil {
		t.Errorf("ReShade64.dll should not have been staged")
	}
}

// TestStageFetched_liveURLs is the end-to-end smoke test for the graphics
// pack's three real upstream sources. It hits the live network, so it's
// gated on RUN_LIVE_FETCH=1 and skipped by default. Run with:
//
//	RUN_LIVE_FETCH=1 go test ./pkg/addon/ -run TestStageFetched_liveURLs -v
//
// Each fetch entry is asserted independently — when an upstream URL changes
// or extraction breaks, this is the test that catches it before a release.
func TestStageFetched_liveURLs(t *testing.T) {
	if os.Getenv("RUN_LIVE_FETCH") == "" {
		t.Skip("RUN_LIVE_FETCH not set — skipping live network test")
	}

	type expect struct {
		name     string
		entry    FetchEntry
		minBytes map[string]int // dst -> minimum byte count to accept
	}

	cases := []expect{
		{
			name: "dgVoodoo2 zip",
			entry: FetchEntry{
				From:    "https://github.com/dege-diosg/dgVoodoo2/releases/download/v2.87.1/dgVoodoo2_87_1.zip",
				Extract: "zip",
				Files:   []FileEntry{{Src: "MS/x86/D3D8.dll", Dst: "D3D8.dll"}},
			},
			minBytes: map[string]int{"D3D8.dll": 200_000},
		},
		{
			name: "ReShade exe (7z)",
			entry: FetchEntry{
				From:    "https://reshade.me/downloads/ReShade_Setup_6.7.3_Addon.exe",
				Extract: "exe",
				Files:   []FileEntry{{Src: "ReShade32.dll", Dst: "dxgi.dll"}},
			},
			minBytes: map[string]int{"dxgi.dll": 1_000_000},
		},
		{
			name: "shader tarball glob",
			entry: FetchEntry{
				From:    "https://github.com/crosire/reshade-shaders/archive/refs/heads/nvidia.tar.gz",
				Extract: "tar.gz",
				Files: []FileEntry{
					{Src: "reshade-shaders-nvidia/ShadersAndTextures/*.fx", Dst: "reshade-shaders/Shaders"},
					{Src: "reshade-shaders-nvidia/ShadersAndTextures/*.fxh", Dst: "reshade-shaders/Shaders"},
					{Src: "reshade-shaders-nvidia/ShadersAndTextures/*.png", Dst: "reshade-shaders/Textures"},
				},
			},
			minBytes: map[string]int{
				"reshade-shaders/Shaders/SMAA.fx": 100,
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := newFetchManager(t)
			cacheDir := filepath.Join(m.DataDir, "cache")
			os.MkdirAll(cacheDir, 0755)

			if err := m.stageFetched(tc.entry, cacheDir, func(msg string) {
				t.Log(msg)
			}); err != nil {
				t.Fatalf("stageFetched: %v", err)
			}

			for rel, minSize := range tc.minBytes {
				st, err := os.Stat(filepath.Join(cacheDir, rel))
				if err != nil {
					t.Errorf("expected %s in cache: %v", rel, err)
					continue
				}
				if st.Size() < int64(minSize) {
					t.Errorf("%s: size %d < expected min %d", rel, st.Size(), minSize)
				}
			}
		})
	}
}

func TestIsGlobPattern(t *testing.T) {
	cases := map[string]bool{
		"":                false,
		"plain/path.txt":  false,
		"a/b/c.dll":       false,
		"*.fx":            true,
		"shaders/*.fxh":   true,
		"a?.dll":          true,
		"a[0-9].dll":      true,
		"escaped\\*.dll":  true, // we treat any glob char as a glob, even if escaped — addons shouldn't author escaped patterns
	}
	for s, want := range cases {
		if got := isGlobPattern(s); got != want {
			t.Errorf("isGlobPattern(%q) = %v, want %v", s, got, want)
		}
	}
}

func TestValidateManifest_fetch(t *testing.T) {
	cases := []struct {
		name      string
		m         AddonManifest
		wantError string
	}{
		{
			name: "valid fetch entry",
			m: AddonManifest{
				ID: "x", Name: "X", Version: "1",
				Fetch: []FetchEntry{
					{From: "https://example.com/a.zip", Extract: "zip", Files: []FileEntry{{Src: "a", Dst: "a"}}},
				},
			},
		},
		{
			name: "fetch missing from",
			m: AddonManifest{
				ID: "x", Name: "X", Version: "1",
				Fetch: []FetchEntry{{Extract: "zip", Files: []FileEntry{{Src: "a", Dst: "a"}}}},
			},
			wantError: "'fetch' entries need 'from'",
		},
		{
			name: "fetch non-http url",
			m: AddonManifest{
				ID: "x", Name: "X", Version: "1",
				Fetch: []FetchEntry{{From: "file:///etc/passwd", Extract: "", Files: []FileEntry{{Src: "x", Dst: "x"}}}},
			},
			wantError: "must be http(s) URL",
		},
		{
			name: "fetch unsupported extract",
			m: AddonManifest{
				ID: "x", Name: "X", Version: "1",
				Fetch: []FetchEntry{{From: "https://x/y.7z", Extract: "7z", Files: []FileEntry{{Src: "x", Dst: "x"}}}},
			},
			wantError: "fetch.extract",
		},
		{
			name: "fetch exe extract is supported",
			m: AddonManifest{
				ID: "x", Name: "X", Version: "1",
				Fetch: []FetchEntry{{From: "https://x/y.exe", Extract: "exe", Files: []FileEntry{{Src: "ReShade32.dll", Dst: "dxgi.dll"}}}},
			},
		},
		{
			name: "fetch missing files",
			m: AddonManifest{
				ID: "x", Name: "X", Version: "1",
				Fetch: []FetchEntry{{From: "https://x/y.zip", Extract: "zip"}},
			},
			wantError: "need at least one 'files' mapping",
		},
		{
			name: "fetch traversal in dst",
			m: AddonManifest{
				ID: "x", Name: "X", Version: "1",
				Fetch: []FetchEntry{{From: "https://x/y.zip", Extract: "zip", Files: []FileEntry{{Src: "x", Dst: "../../etc/passwd"}}}},
			},
			wantError: "must not contain '..'",
		},
		{
			name: "fetch-only manifest (no files)",
			m: AddonManifest{
				ID: "x", Name: "X", Version: "1",
				Fetch: []FetchEntry{{From: "https://x/y.zip", Extract: "zip", Files: []FileEntry{{Src: "x", Dst: "x"}}}},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateManifest(tc.m)
			if tc.wantError == "" {
				if err != nil {
					t.Errorf("expected success, got %v", err)
				}
				return
			}
			if err == nil {
				t.Errorf("expected error containing %q, got nil", tc.wantError)
				return
			}
			if !strings.Contains(err.Error(), tc.wantError) {
				t.Errorf("expected error containing %q, got %q", tc.wantError, err.Error())
			}
		})
	}
}
