# nc1-launcher — cross-platform Neocron Classic launcher

A Go + [Wails v2](https://wails.io) reimplementation of the official **Neocron
Classic (NC1)** launcher from neocron.org, built by reverse-engineering the
Windows launcher. Runs natively on Windows and on Linux/macOS (launching the
Win32 game client under Proton/Wine).

> This is a separate project from the NC2 emulation launcher in `../launcher/`.
> NC1 (neocron.org "Neocron Classic") is a different server, client and account
> system.

## What the official launcher does (and this one reproduces)

The official launcher is a two-stage, CEF-based app:

- `LauncherMcLaunchy.exe` — a bootstrap/self-updater that pulls the real
  launcher bundle (`launcher.zip`, a Chromium Embedded Framework app) from the
  DigitalOcean CDN.
- `NeocronLauncher.exe` — the CEF host whose UI is a jQuery/Bootstrap web page
  (`web.pak`). It handles **Discord sign-in**, account selection, game
  patching (`patchy.dll`), and launching `neocron.exe`.

This project keeps the **same web UI** (ported verbatim) and reimplements the
native side in Go:

| Feature | Official | This launcher |
|---|---|---|
| UI | CEF + jQuery/Bootstrap web page | Same page, hosted by Wails' webview |
| JS↔native bridge | CEF `cefQuery` / `window.ncl*` | `js/bridge.js` → `App.Dispatch` / `ncl` events |
| Sign-in | Discord OAuth (PKCE + loopback) | `pkg/auth` — identical contract |
| Accounts / ticket | `/me/accounts`, `/launch-tickets` | `pkg/auth` |
| Game launch | `neocron.exe -ticketuser "<n>" -ticket <t>` | `pkg/game` (native / Proton / Wine) |
| Patching | `patchy.dll` (signed binary diff) | `pkg/patch` (hash-manifest; see caveat) |

### Service topology (recovered)

- `https://auth.neocron.org` — Discord OAuth + accounts + launch tickets.
- `https://cdn.neocron.org` — launcher self-update + game content.
- `https://neocron.org/api/launcher` — public news/banner feed.

### Discord sign-in flow (byte-for-byte)

1. PKCE: random `code_verifier`, `code_challenge = base64url(SHA256(verifier))`,
   method `S256`.
2. Bind a loopback listener on `127.0.0.1:<port>/callback`.
3. `POST /auth/discord/start {code_challenge, code_challenge_method,
   loopback_port, launcher_version}` → `{authorize_url, state, expires_in}`.
4. Open `authorize_url` in the system browser; catch the `/callback` redirect.
5. `POST /auth/discord/exchange {code, code_verifier, state}` →
   `{session_token, expires_at, discord{…}, accounts[…]}`.
6. `POST /launch-tickets {user_id}` (Bearer) → `{ticket}`, then launch.

## Build & run

```sh
# native build (embeds the frontend via go:embed)
make build && ./build/bin/nc1-launcher

# or with the Wails toolchain for packaged bundles
make wails

# tests for the RE-derived logic (PKCE, manifest parsing, launch args)
make test
```

Requirements: Go 1.23+, and on Linux the WebKit2GTK dev libraries Wails needs
(`libwebkit2gtk-4.1-dev`, `libgtk-3-dev`). On Linux, configure a Proton/Wine
build in the launcher settings to run the Win32 client.

## Layout

```
main.go            Wails entry point (embeds frontend/dist)
app.go             App: cefQuery dispatcher + ncl* state push
pkg/auth           Discord OAuth (PKCE loopback), session, accounts, tickets
pkg/game           neocron.exe launch (native / Proton / Wine)
pkg/patch          game file patcher (hash-manifest Patcher interface)
pkg/api            neocron.org banner/news feed
pkg/config         config.json under the user config dir
pkg/proton         Proton/Wine runtime + prefix management (shared with NC2)
frontend/dist      the ported web UI + js/bridge.js (Wails↔CEF shim)
docs/RE_LAUNCHER.md full reverse-engineering write-up
re/                 Ghidra scripts + decompiler output + extracted artifacts
```

## Caveat: game patching

The official patcher (`patchy.dll`) uses a bespoke **signed binary-diff** format
(`patchy_load_version`/`patchy_diff`→"wounds"/`patchy_apply`). Its version-blob
layout, signature scheme, and per-channel content URL are **not yet recovered**
(the content paths are non-public). `pkg/patch` ships a working hash-manifest
downloader behind a clean `Patcher` interface; when the patchy format is reversed
— or the real DLL is bridged over its C ABI — it drops in without touching the
rest of the app. Auth and launch are fully implemented and byte-faithful.
