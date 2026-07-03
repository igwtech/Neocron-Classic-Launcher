package addon

import (
	"encoding/json"
	"testing"
)

// The published ReShade addon manifest (igwtech/neocron-classic-reshade) must
// pass the same validation the installer runs, or install fails. Keep this in
// sync with that repo's addon.json.
const reshadeAddonJSON = `{
  "id": "neocron-classic-reshade",
  "name": "ReShade — Enhanced Visuals (DX11)",
  "version": "0.1.0",
  "author": "igwtech / Neocron Community",
  "description": "ReShade post-processing for the DX11 Neocron Classic client.",
  "category": "graphics",
  "tags": ["reshade", "dx11", "dxgi"],
  "files": [
    { "src": "ReShade.ini",        "dst": "ReShade.ini" },
    { "src": "NeocronClassic.ini", "dst": "NeocronClassic.ini" }
  ],
  "fetch": [
    {
      "from": "https://reshade.me/downloads/ReShade_Setup_6.7.3_Addon.exe",
      "extract": "exe",
      "files": [ { "src": "ReShade64.dll", "dst": "dxgi.dll" } ]
    },
    {
      "from": "https://github.com/crosire/reshade-shaders/archive/refs/heads/nvidia.tar.gz",
      "extract": "tar.gz",
      "files": [
        { "src": "reshade-shaders-nvidia/ShadersAndTextures/*.fx",  "dst": "reshade-shaders/Shaders" },
        { "src": "reshade-shaders-nvidia/ShadersAndTextures/*.png", "dst": "reshade-shaders/Textures" }
      ]
    }
  ],
  "wineDllOverrides": ["dxgi"],
  "conflicts": [],
  "requires": []
}`

// The Quality addon (igwtech/neocron-classic-reshade-quality) requires the base
// ReShade addon and adds depth-based AO/SSR. Must validate the same way.
const reshadeQualityAddonJSON = `{
  "id": "neocron-classic-reshade-quality",
  "name": "ReShade Quality — Ambient Occlusion + SSR (DX11)",
  "version": "0.1.0",
  "author": "igwtech / Neocron Community",
  "description": "Depth-based AO (qUINT MXAO) + SSR on the base ReShade addon.",
  "category": "graphics",
  "files": [
    { "src": "ReShade.ini",                "dst": "ReShade.ini" },
    { "src": "NeocronClassic-Quality.ini", "dst": "NeocronClassic-Quality.ini" }
  ],
  "fetch": [
    {
      "from": "https://github.com/martymcmodding/qUINT/archive/refs/heads/master.tar.gz",
      "extract": "tar.gz",
      "files": [
        { "src": "qUINT-master/Shaders/qUINT_mxao.fx",    "dst": "reshade-shaders/Shaders" },
        { "src": "qUINT-master/Shaders/qUINT_ssr.fx",     "dst": "reshade-shaders/Shaders" },
        { "src": "qUINT-master/Shaders/qUINT_common.fxh", "dst": "reshade-shaders/Shaders" }
      ]
    }
  ],
  "requires": ["neocron-classic-reshade"],
  "conflicts": []
}`

func TestReShadeQualityManifestValidates(t *testing.T) {
	var m AddonManifest
	if err := json.Unmarshal([]byte(reshadeQualityAddonJSON), &m); err != nil {
		t.Fatalf("quality addon.json does not parse: %v", err)
	}
	if err := validateManifest(m); err != nil {
		t.Fatalf("quality addon.json fails installer validation: %v", err)
	}
	if len(m.Requires) != 1 || m.Requires[0] != "neocron-classic-reshade" {
		t.Errorf("quality must require the base addon, got %v", m.Requires)
	}
}

func TestReShadeAddonManifestValidates(t *testing.T) {
	var m AddonManifest
	if err := json.Unmarshal([]byte(reshadeAddonJSON), &m); err != nil {
		t.Fatalf("addon.json does not parse: %v", err)
	}
	if err := validateManifest(m); err != nil {
		t.Fatalf("addon.json fails installer validation: %v", err)
	}
	if m.ID != "neocron-classic-reshade" {
		t.Errorf("id = %q", m.ID)
	}
	if len(m.WineDLLOverrides) != 1 || m.WineDLLOverrides[0] != "dxgi" {
		t.Errorf("expected single dxgi override, got %v", m.WineDLLOverrides)
	}
	// The DX11 client is 64-bit: the proxy must come from ReShade64.dll.
	if m.Fetch[0].Files[0].Src != "ReShade64.dll" || m.Fetch[0].Files[0].Dst != "dxgi.dll" {
		t.Errorf("expected ReShade64.dll -> dxgi.dll, got %+v", m.Fetch[0].Files[0])
	}
}
