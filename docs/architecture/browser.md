# Browser Automation

## Overview

Astonish includes a comprehensive browser automation system built on Chrome DevTools Protocol (CDP) via the go-rod library. It provides the AI agent with the ability to navigate websites, fill forms, take screenshots, read page content, and interact with web applications -- all while employing extensive anti-detection measures to avoid being blocked as a bot.

In sandboxed Studio chat, fleet, and drill sessions the browser runs **inside the session container** (CloakBrowser + KasmVNC + socat CDP bridge). CloakBrowser is a patched Chromium binary at `/home/browser/.cloakbrowser/*/chrome`. It is **not** the Debian `chromium` / `chromium-browser` / `google-chrome` package and is **not** on `PATH`. `which chromium` staying empty is expected and is not proof the overlay is unmounted. Agents must call `browser_navigate` to prove the capability. The host process drives go-rod over CDP; it does not launch host Chrome for sandboxed work. Host Chrome is only used for non-sandbox local runs.

## Key Design Decisions

### Why Headed Mode with Xvfb

Most browser automation tools default to headless Chrome. Astonish defaults to **headed mode** running on a virtual display (Xvfb) on Linux servers. This was chosen because:

- **Better fingerprint**: Headless Chrome has detectable differences in WebGL rendering, plugin enumeration, and various JavaScript APIs. Headed Chrome with a virtual display is indistinguishable from a real user's browser.
- **Fallback**: If Xvfb is unavailable, the system falls back to headless mode.
- **Desktop support**: On macOS/Windows with a real display, Chrome runs normally without Xvfb.

### Why Multi-Layer Anti-Detection

Web bot detection has become increasingly sophisticated. Astonish employs multiple layers of stealth:

1. **go-rod/stealth JS evasions**: Injected via `EvalOnNewDocument` before any page scripts run. Patches `navigator.webdriver`, `navigator.plugins`, `chrome.runtime`, and other commonly checked properties.

2. **Automation flag removal**: The `--enable-automation` flag is stripped and the `AutomationControlled` Blink feature is disabled. These are the most basic detection signals.

3. **User-Agent + Client Hints**: The UA string and the newer `Sec-CH-UA-*` headers are dynamically matched to the actual Chrome version. Platform is spoofed as "Win32" with Windows 10 metadata (the most common platform, least suspicious).

4. **WebGL fingerprint patching**: One of the most reliable bot detection methods checks WebGL renderer/vendor strings. Headless Chrome reports "SwiftShader" (a software renderer). The stealth layer patches `getParameter()`, `getSupportedExtensions()`, and `getExtension()` to report an Intel Iris GPU profile with consistent capability values.

5. **Deferred event listeners**: Chrome's `Runtime.enable` command (needed for console/network events) is a detection signal. Event listeners are not attached until the first tool call that needs them, avoiding unnecessary signals.

6. **Human-like input**: `TypeHuman()` types characters one at a time with 50-150ms random jitter between keystrokes. `HumanDelay()` adds random pauses between actions.

### Why Accessibility Snapshots Over DOM Parsing

Instead of returning raw HTML for the LLM to parse, the browser provides **accessibility tree snapshots**:

- The Chrome AX (accessibility) tree is a structured representation of what's visible and interactive on the page.
- Each interactive element gets a `ref` ID that the agent can use for subsequent interactions (click, type, select).
- The AX tree is much smaller than the DOM while containing the semantically important content.
- **DOM fallback**: For sites with minimal AX trees (heavily div-based layouts), a JavaScript DOM walker provides equivalent output.

### Why CDP Handoff for Human-in-the-Loop

Some operations (CAPTCHAs, complex login flows, 2FA) can't be automated. The handoff system:

1. Starts a CDP WebSocket proxy server on the host.
2. The agent pauses and gives the user a URL to connect with `chrome://inspect` or a DevTools frontend.
3. The user completes the manual step in the live browser session.
4. Auto-done detection: when all DevTools WebSocket connections close (with a 10-second grace period), control returns to the agent.
5. Alternative: manual POST/GET to `/handoff/done`.

This keeps the browser session alive and all cookies/state intact throughout the handoff.

### Why SSRF Prevention with Sandbox Override

Browser navigation checks resolved DNS addresses against private IP ranges. However, in sandbox mode this guard is **disabled** because:

- Services started inside containers (e.g., for drill testing) listen on the container bridge network (private IPs like 10.x.x.x).
- The browser needs to reach these services for end-to-end testing.
- The container itself provides the security boundary.

A hostname allowlist override is available for specific use cases.

## Architecture

### Browser Lifecycle

```
First browser tool call
    |
    v
Manager.GetOrLaunch():
  1. Check for existing browser instance
  2. If none: launch Chrome
     - Try Xvfb display (Linux) or real display
     - Apply stealth flags (disable automation, set UA)
     - Persistent user data dir (~/.config/astonish/browser/)
  3. Return browser instance
    |
    v
Tool execution (navigate, click, type, etc.)
    |
    v
Browser persists across tool calls within a session
    |
    v
Explicit close or session end: browser cleanup
```

### CAPTCHA Detection

JavaScript-based detection checks for:

- **reCAPTCHA v2/v3**: `grecaptcha` global, `.g-recaptcha` elements, `www.google.com/recaptcha/api.js` scripts.
- **hCaptcha**: `hcaptcha` global, `.h-captcha` elements.
- **Cloudflare Turnstile**: `turnstile` global, `challenges.cloudflare.com` scripts.
- **Generic text**: Case-insensitive search for "captcha", "I'm not a robot", "verify you're human".

Detection returns a `CAPTCHADetection` struct with type, site key, and CSS selector. A solver interface is stubbed for future integration with solving services (2captcha, anti-captcha, capsolver).

### Account Store

The browser tracks web portal accounts in a JSON-persisted registry:

- **Entries**: Portal URL, username, status (`pending`, `verifying`, `active`, `suspended`, `failed`), credential reference.
- **Purpose**: The agent can check if it already has an account on a portal before attempting registration.
- **Thread-safe**: Mutex-protected CRUD operations.

### Tool Organization

Browser tools are organized into functional groups:

| Group | Tools |
|---|---|
| **Navigation** | `browser_navigate`, `browser_navigate_back` |
| **Interaction** | `browser_click`, `browser_type`, `browser_hover`, `browser_drag`, `browser_press_key`, `browser_select_option`, `browser_fill_form`, `browser_highlight`, `browser_clear_highlights`, `browser_move_cursor` |
| **Observation** | `browser_snapshot`, `browser_take_screenshot`, `browser_console_messages`, `browser_network_requests` |
| **Management** | `browser_tabs`, `browser_close`, `browser_resize`, `browser_fullscreen`, `browser_wait_for`, `browser_file_upload`, `browser_handle_dialog` |
| **Advanced** | `browser_evaluate`, `browser_run_code`, `browser_pdf`, `browser_response_body` |
| **State/Emulation** | `browser_cookies`, `browser_storage`, `browser_set_offline`, `browser_set_headers`, `browser_set_credentials`, `browser_set_geolocation`, `browser_set_media`, `browser_set_timezone`, `browser_set_locale`, `browser_set_device` |
| **Human Handoff** | `browser_request_human`, `browser_handoff_complete` |
| **Recording** | `browser_start_recording`, `browser_stop_recording`, `browser_recording_status` |

### Session recording

Browser display recording uses **ffmpeg `x11grab`** of the real X display (KasmVNC `:0` in sandbox, Xvfb/`DISPLAY` locally) — not CDP screencast.

Invariants:
- Capture size is the **live X root window** (probed via `xdpyinfo` at record start), not config/`browser_resize`. The start tool returns the actual `width`/`height`.
- KasmVNC must **not** client-resize the desktop: `desktop.allow_resize: false` in `kasmvnc.yaml` and `-AcceptSetDesktopSize 0` on launch. Otherwise Studio’s noVNC iframe shrinks the framebuffer to the host canvas (e.g. ~1728×996 on a Mac) and a 1920×1080 capture request fails.
- When resize is locked, KasmVNC uses `desktop.resolution` (package default **1024×768**). That yaml must be rewritten at VNC start to the configured viewport (default **1920×1080**), matching `-geometry`. Leaving only `-AcceptSetDesktopSize 0` without a matching resolution freezes sessions at 1024×768.
- Launch geometry stays at the configured viewport: KasmVNC `-geometry`, yaml `desktop.resolution`, Chrome `--window-size`, and CDP `SetViewport` should match. `browser_resize` only changes CDP metrics. The KasmVNC web UI may show a smaller client canvas (e.g. ~960×540 under HiDPI scaling); `xdpyinfo` / recording probe is authoritative.
- Only one recording at a time; `Cleanup` / reconnect finalize an orphan recording.
- Sandbox recordings write to `/tmp/astonish-recordings/*.mp4` (downloadable via sandbox PullFile). Local recordings write under `recordings/`.
- Local Linux requires `ffmpeg` + `xdpyinfo` (`x11-utils`) (+ Xvfb when headed without a physical display). Sandboxes install `ffmpeg` as a core tool (`CoreToolInstallCommands`); `x11-utils` comes with the browser stack.

### Tutorial recording polish

| Tool | Role |
|---|---|
| `browser_highlight` / `browser_clear_highlights` | DOM overlay box (+ optional label) on a ref or CSS selector |
| `browser_move_cursor` | Visible demo cursor + real mouse move to ref/selector/x,y (`EvalOnNewDocument` so it survives navigations) |
| `browser_click` `animate_cursor` | Optional; default false for CI — set true in tutorials to move the demo cursor then click |
| `browser_fullscreen` | CDP `Browser.setWindowBounds` fullscreen (F11 fallback). Minimizes Chromium chrome; x11grab still captures the display |

Recommended order: open app → fullscreen → start recording / segment → highlight → move/click → hold. See `docs/architecture/drill.md` (tutorial recording order).

### Screenshot Handling

- Auto-compression for large screenshots: images over 2000px are resized, JPEG quality is reduced progressively until under 5MB.
- Screenshots are extracted from tool results by the `AfterToolCallback` and delivered via a side-channel (images in Telegram, attachments in email) rather than keeping base64 blobs in the conversation history.

## Key Files

| File | Purpose |
|---|---|
| `pkg/browser/manager.go` | Singleton browser management, page state tracking, launch logic |
| `pkg/browser/recording.go` | ffmpeg x11grab session recording (start/stop/status) |
| `pkg/browser/demo_overlay.go` | Tutorial highlight overlays, demo cursor, fullscreen helper |
| `pkg/browser/stealth.go` | Anti-detection: go-rod/stealth evasions, automation flag removal, UA/Client Hints |
| `pkg/browser/stealth_webgl.go` | WebGL fingerprint patching (Intel Iris GPU profile) |
| `pkg/browser/xvfb.go` | Virtual display management for headed Chrome on Linux |
| `pkg/browser/snapshot.go` | Accessibility tree snapshots with DOM fallback |
| `pkg/browser/refs.go` | Ref ID system for element resolution |
| `pkg/browser/handoff.go` | CDP WebSocket proxy for human-in-the-loop |
| `pkg/browser/captcha.go` | CAPTCHA detection (reCAPTCHA, hCaptcha, Turnstile) |
| `pkg/browser/humanize.go` | Human-like typing and delay patterns |
| `pkg/browser/navigation_guard.go` | SSRF prevention for browser navigation |
| `pkg/browser/accounts.go` | Web portal account registry |
| `pkg/browser/emulation.go` | Device emulation, geolocation, timezone, locale |
| `pkg/browser/events.go` | Console/network event collection with ring buffer |
| `pkg/browser/screenshot.go` | Screenshot capture with auto-compression |
| `pkg/browser/storage.go` | Cookie/localStorage/sessionStorage manipulation |
| `pkg/tools/browser_*.go` | 8 files implementing the 35+ browser tools |

## Interactions

- **Agent Engine**: Browser tools are invoked from the host process; with sandbox enabled Chromium runs in-container over CDP. Screenshots are extracted from tool results via image side-channels.
- **Channels**: Screenshots are delivered as photos (Telegram) or attachments (email).
- **Credentials**: Browser credential tools (`browser_set_credentials`) handle HTTP auth. Account store references the credential store.
- **Drills**: Browser tools run inside the sandbox session container (same Chromium + KasmVNC path as Studio chat).
- **Skills**: The web registration skill teaches the agent how to use browser tools for form filling and account creation.
