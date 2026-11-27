# Remote Codex

Use your desktop **ChatGPT / Codex** app from your phone or any browser — without modifying the official app.

[简体中文](README.md) · [Security Model](docs/SECURITY.md)

---

## Why this exists

Desktop ChatGPT (especially Work / Codex mode with your local projects, tools, and session) is stuck on your Mac or Windows machine. Leaving the desk shouldn't mean losing access to an active thread or kicking off a quick task from your phone.

**Remote Codex** attaches to your running desktop app in memory, serves its exact native UI over HTTPS/WSS, and wraps everything into a standard installable app for macOS and Windows. Open it, scan the QR code on your phone, and you're in.

No domain, no Cloudflare account, no Python, and no terminal setup required out of the box.

## What you get

- **Install and scan (Zero Config by default)** — Ships as a macOS `.dmg` and Windows `.exe` installer. On first launch it generates a strong random password, brings up a temporary HTTPS tunnel (`trycloudflare.com`, auto-downloading `cloudflared` if you don't have it), and shows a one-tap login QR code.
- **Keep your own domain when you want speed & permanence** — Switch to Custom Domain mode for a fixed URL, persistent login sessions (survives restarts, slides forward automatically), and bookmarkable permanent login links. Built-in Let's Encrypt auto-TLS (`TLS-ALPN-01`) works directly behind TLS-passthrough tunnels like frp / SakuraFRP, or pair it with Cloudflare Named Tunnel / Access.
- **Actually usable on a phone** — Desktop Electron apps behave terribly on mobile browsers out of the box. Remote Codex fixes the parts that hurt in real daily use:
  - Syncs layout height with `visualViewport` and `interactive-widget=resizes-content` so the input box sits flush above your soft keyboard instead of disappearing beneath it.
  - Blocks the desktop app's aggressive auto-focus calls on mobile so your keyboard stops popping up randomly when switching chats, closing menus, or returning to the tab.
  - Lets Android open your actual local file manager (not just Photos & Gallery), and routes Work/Codex attachments back to your desktop host through the browser attachment channel.
- **Does not modify the official app** — Reads static assets directly from `app.asar` (read-only) and attaches a small in-memory shim via bundled Node.js Frida. Code signatures and ASAR integrity stay untouched.
- **Single-controller safety** — Only one browser tab controls the desktop host at a time. Opening it on another device offers a clean one-click takeover instead of scrambling state.

## Quick Start

### 1. Download & open

Grab the latest installer from [GitHub Releases](../../releases):

- **macOS (Apple Silicon)**: `Remote-Codex-*-mac-arm64.dmg`
- **Windows (x64)**: `Remote-Codex-Setup-*.exe`

Make sure the official ChatGPT desktop app is running, then launch **Remote Codex**.

> **macOS Gatekeeper note**: Release builds are currently unsigned. If macOS says the app is damaged or from an unidentified developer, run `xattr -dr com.apple.quarantine "/Applications/Remote Codex.app"` once.

### 2. Scan the QR code

Remote Codex starts in **Quick Tunnel** mode by default:

1. Wait a few seconds for the temporary `https://*.trycloudflare.com` address to come up.
2. Scan the QR code in the control panel with your phone.
3. Done — you're logged in with a one-time magic link (the secret lives in the URL `#fragment`, so proxies and servers never see it).

Closing the window keeps Remote Codex quietly running in your menu bar / system tray.

---

## Using Your Own Domain (Optional)

Quick Tunnel is great for getting started, though Cloudflare rotates the URL every restart and overseas nodes can be slow depending on your network.

If you want a **fixed URL**, **permanent login across restarts**, and **fast direct routing**, switch to **Custom Domain** in the Control Panel:

- **With frp / SakuraFRP (End-to-end TLS)**:
  1. Point your domain (`chat.example.com`) to your frp node (DNS-only, no proxy).
  2. Configure an HTTPS tunnel with **TLS passthrough** forwarding to `127.0.0.1:18443` (disable proxy-side auto-HTTPS).
  3. Enable `tlsPort: 18443` and `tlsAutocert: true` in Remote Codex. It obtains and renews a Let's Encrypt certificate directly on your machine — the relay node only sees encrypted TLS bytes.
- **With Cloudflare Named Tunnel / Access**:
  - Point your tunnel ingress to `http://127.0.0.1:18765` (never expose admin port `18766`). The Control Panel includes a safe `config.yml` diff preview before touching anything.

Once on a fixed domain, you can also generate a **Permanent Login Link** from the Control Panel (or `remote-codex ticket --permanent`) to bookmark across devices. Clicking **Revoke All Sessions & Links** or changing your password kills every active session and every permanent link immediately.

---

## How it works

```text
Phone / Browser (HTTPS + WSS)
        │
        ▼
Cloudflare Quick Tunnel (default)  ──or──  Your Domain (frp + built-in Auto-TLS / CF Tunnel)
        │
        ▼
Remote Codex Desktop (Electron)
  ├── Control Panel Window & System Tray
  ├── Bundled Node.js Frida Bootstrapper (zero Python needed)
  └── Go Core Sidecar (127.0.0.1 only)
        ├── Public Gateway (:18765 / TLS :18443)  ── Auth, ASAR reader, CBOR WS bridge
        └── Local Admin API (:18766)              ── Never exposed publicly
        │
        ▼  Loopback WebSocket (per-session bearer token)
Unmodified ChatGPT Desktop App (in-memory shim)
```

Everything stays split along clear boundaries:

- **`desktop/`** — Electron shell (`SidecarManager`, `WindowManager`, `TrayManager`, `CloudflaredManager`, and the zero-Python `Node-Frida` bootstrapper).
- **`cmd/remote-codex/` & `internal/`** — Go core engine with pluggable `tunnel.Provider` (`QuickProvider` / `CustomProvider`) and `host.Bootstrapper` interfaces, pure-Go ASAR reader, Let's Encrypt `autocert`, persistent hash-only `auth-state.json` (`0600`), and single-controller arbiter.
- **`web/`** — Browser runtime shim (`browser-transport.ts`, `mobile-runtime.ts`, `mobile-layout.css`), Electron host shim, and local Admin UI.

---

## Security Highlights

- **Admin panel is strictly loopback-only** (`127.0.0.1:18766`) and rejected on any public host.
- **Never runs unauthenticated** — Quick Tunnel auto-generates a 256-bit `scrypt`-hashed password on first run.
- **No plaintext session or ticket secrets on disk** — `auth-state.json` (`0600`) stores only SHA-256 digests.
- **Fragment-only magic links** — `#ticket=...` is stripped immediately via `history.replaceState` and exchanged over CSRF-protected `POST /auth/ticket` for an `HttpOnly; Secure; SameSite=Strict` cookie.
- **Official app stays untouched** — macOS `codesign --verify --deep --strict` passes before and after use.

More details in [docs/SECURITY.md](docs/SECURITY.md).

---

## Build from Source

Requires **Go 1.24+** and **Node.js 22+**.

```bash
# Build Go core + embedded web assets
make build

# Run Go race tests & vet
make test
go vet ./...

# Package macOS installer (.dmg & .zip -> desktop/release/)
make package-mac

# Package Windows installer (on Windows: .exe -> desktop/release/)
make package-windows
```

### Headless CLI Mode (Optional)

If you prefer running the standalone Go binary without Electron:

```bash
./scripts/setup-python.sh      # creates a tiny local venv for Frida (only needed without Electron)
./bin/remote-codex             # starts Quick Tunnel + opens control panel in browser
./bin/remote-codex status
./bin/remote-codex doctor
./bin/remote-codex ticket --permanent
./bin/remote-codex revoke-all
```

---

## Compatibility & Disclaimer

- Verified against ChatGPT / Codex Desktop `26.1117.41037` on **macOS (Apple Silicon)** and **Windows 11 (x64)**. Newer desktop builds may require updating host hooks if internal Electron/V8 layouts change.
- **Unofficial project**: Remote Codex is an independent open-source tool and is not affiliated with or endorsed by OpenAI. It is intended for personal access to your own running desktop app.

## License

[MIT](LICENSE)
