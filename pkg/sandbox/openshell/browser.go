// Package openshell — browser.go implements browser-in-sandbox support for
// the OpenShell backend. CloakBrowser + KasmVNC are pre-installed in the
// sandbox image (docker/sandbox-openshell/Dockerfile); this file provides
// the runtime wiring to launch them and tunnel CDP connections through the
// gateway's gRPC exec API.
//
// Architecture mirrors pkg/sandbox/browser_install.go and
// backend_browser_wire.go but uses the OpenShell GatewayClient instead
// of the Incus exec API.

package openshell

import (
	"bufio"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"strings"
	"time"
)

// kasmvncConfigYAML returns the user overlay for KasmVNC. With allow_resize
// false, KasmVNC locks to desktop.resolution (package default 1024×768), so
// width/height must match the viewport / -geometry.
func kasmvncConfigYAML(width, height int) string {
	if width <= 0 {
		width = 1920
	}
	if height <= 0 {
		height = 1080
	}
	return fmt.Sprintf(`network:
  protocol: http
  interface: 0.0.0.0
  use_ipv4: true
  use_ipv6: false
  ssl:
    require_ssl: false
    pem_certificate: /home/browser/.vnc/snakeoil.pem
    pem_key: /home/browser/.vnc/snakeoil.key
desktop:
  allow_resize: false
  resolution:
    width: %d
    height: %d
user_session:
  concurrent_connections_prompt: false
logging:
  log_writer_name: all
  log_dest: logfile
  level: 30
command_line:
  prompt: false
`, width, height)
}

// ---------------------------------------------------------------------------
// Constants (same values as pkg/sandbox/browser_install.go)
// ---------------------------------------------------------------------------

const (
	// cdpPort is the externally-reachable CDP port inside the sandbox.
	// A socat bridge forwards from 0.0.0.0:cdpPort to 127.0.0.1:cdpInternalPort.
	cdpPort = 9222

	// cdpInternalPort is the port Chromium's --remote-debugging-port binds to
	// (localhost only). Socat bridges this to cdpPort on all interfaces.
	cdpInternalPort = 9223

	// kasmVNCDisplay is the X11 display number used by KasmVNC.
	kasmVNCDisplay = "0"

	// defaultKasmVNCPort is the KasmVNC websocket port (6901 for display :0).
	defaultKasmVNCPort = 6901
)

// debugLog writes to /tmp/browser_debug.log and stderr for diagnostics.
var debugLogger = log.New(os.Stderr, "[openshell-browser] ", log.LstdFlags)

func debugLog(format string, args ...any) {
	debugLogger.Printf(format, args...)
}

// ---------------------------------------------------------------------------
// BrowserLaunchConfig
// ---------------------------------------------------------------------------

// BrowserLaunchConfig holds the parameters for launching the browser inside
// an OpenShell sandbox. Passed from the browser Manager's config.
type BrowserLaunchConfig struct {
	ViewportWidth       int
	ViewportHeight      int
	FingerprintSeed     string
	FingerprintPlatform string
	Proxy               string
	KasmVNCPort         int
}

// ---------------------------------------------------------------------------
// StartBrowserInSandbox
// ---------------------------------------------------------------------------

// StartBrowserInSandbox launches CloakBrowser + KasmVNC + socat inside an
// OpenShell sandbox pod. The browser becomes reachable via CDP on port 9222
// (tunneled through the socat bridge). KasmVNC provides visual handoff
// via websocket on port 6901.
//
// This is the OpenShell equivalent of incus.StartChromiumInContainer.
//
// Important: all exec commands inside the sandbox run as user "sandbox"
// (the OpenShell supervisor demotes from root). The script uses
// HOME=/home/browser to access KasmVNC config and CloakBrowser binary
// without needing su/runuser (which require root privileges).
//
// Prerequisites (baked into the sandbox image):
//   - KasmVNC installed, "browser" user exists, ~/.vnc/ pre-configured
//   - CloakBrowser installed via `python3 -m cloakbrowser install`
//   - socat installed
//   - /home/browser/.cloakbrowser/ is chmod 755 (accessible by sandbox user)
//   - /home/browser/.vnc/ is chmod 755 (accessible by sandbox user)
func StartBrowserInSandbox(ctx context.Context, gateway GatewayClient, podName string, cfg BrowserLaunchConfig) (io.Closer, error) {
	width := cfg.ViewportWidth
	if width == 0 {
		width = 1920
	}
	height := cfg.ViewportHeight
	if height == 0 {
		height = 1080
	}
	kasmPort := cfg.KasmVNCPort
	if kasmPort == 0 {
		kasmPort = defaultKasmVNCPort
	}

	script := buildBrowserLaunchScript(cfg, width, height, kasmPort)

	// The OpenShell gateway rejects command arguments containing newline
	// characters. Base64-encode the multi-line script and decode it inline
	// so the actual argv passed to the gateway is a single-line string.
	encoded := base64.StdEncoding.EncodeToString([]byte(script))
	wrapper := fmt.Sprintf("eval \"$(echo %s | base64 -d)\"", encoded)

	// Retry loop: the supervisor returns "sandbox is not ready" while its
	// internal initialization is still in progress (typically 30-90s after
	// pod start). Retry with backoff so the browser tool doesn't fail on
	// the first call to a freshly allocated sandbox.
	const maxRetries = 10
	const notReadyMsg = "sandbox is not ready"
	for attempt := range maxRetries {
		closer, err := startBrowserOnce(ctx, gateway, podName, wrapper)
		if err == nil {
			return closer, nil
		}
		if !strings.Contains(err.Error(), notReadyMsg) {
			// Non-transient error — don't retry
			return nil, err
		}
		if attempt == maxRetries-1 {
			return nil, err
		}
		backoff := time.Duration(2+attempt) * time.Second
		debugLog("sandbox %s not ready (attempt %d/%d), retrying in %v", podName, attempt+1, maxRetries, backoff)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(backoff):
		}
	}
	// unreachable
	return nil, fmt.Errorf("openshell: start browser in %s: exhausted retries", podName)
}

// startBrowserOnce performs a single attempt to launch the browser via ExecStream.
func startBrowserOnce(ctx context.Context, gateway GatewayClient, podName, wrapper string) (io.Closer, error) {
	// Use ExecStream (interactive) instead of ExecCommand (non-interactive).
	// The OpenShell supervisor kills all processes in the exec session's cgroup
	// when the non-interactive ExecSandbox RPC completes. By using the
	// interactive ExecSandboxInteractive RPC, the shell (and its child processes:
	// KasmVNC, Chrome, socat) stays alive as long as the gRPC stream is open.
	// The caller must keep the returned io.Closer alive and close it on cleanup.
	stream, err := gateway.ExecStream(ctx, podName, ExecRequest{
		Command: []string{"sh", "-c", wrapper},
		TTY:     false,
	})
	if err != nil {
		return nil, fmt.Errorf("openshell: start browser in %s: %w", podName, err)
	}

	// Wait for the "DONE: browser ready" output that signals success.
	// The script prints this to stdout before exec'ing sleep infinity.
	doneSentinel := "DONE: browser ready"
	buf := make([]byte, 4096)
	var accumulated string
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		n, readErr := stream.Read(buf)
		if n > 0 {
			accumulated += string(buf[:n])
			if strings.Contains(accumulated, doneSentinel) {
				debugLog("browser ready in sandbox %s", podName)
				return &browserProcessHandle{stream: stream}, nil
			}
		}
		if readErr != nil {
			stream.Close()
			return nil, fmt.Errorf("openshell: browser launch in %s failed (stream error): %w\nOutput: %s",
				podName, readErr, accumulated)
		}
	}

	stream.Close()
	return nil, fmt.Errorf("openshell: browser launch in %s timed out (30s)\nOutput: %s",
		podName, accumulated)
}

// browserProcessHandle keeps the interactive exec stream alive. Closing it
// terminates the shell and all its child processes (KasmVNC, Chrome, socat).
type browserProcessHandle struct {
	stream ExecStreamConn
}

func (h *browserProcessHandle) Close() error {
	if h.stream != nil {
		return h.stream.Close()
	}
	return nil
}

// portToHex returns the uppercase hex representation of a port number,
// matching the format used in /proc/net/tcp (4 hex digits, uppercase).
func portToHex(port int) string {
	return fmt.Sprintf("%04X", port)
}

// buildBrowserLaunchScript generates the shell script that starts KasmVNC,
// CloakBrowser, and the socat CDP bridge inside the sandbox.
//
// All commands run as the sandbox user (no privilege escalation available).
// KasmVNC config and CloakBrowser binary are accessed via HOME=/home/browser
// override — both directories must be chmod 755 in the image.
//
// Dependencies: KasmVNC (vncserver), socat, python3, grep, /proc filesystem.
func buildBrowserLaunchScript(cfg BrowserLaunchConfig, width, height, kasmPort int) string {
	fingerprintFlags := ""
	if cfg.FingerprintSeed != "" {
		fingerprintFlags += fmt.Sprintf(" --fingerprint %s", cfg.FingerprintSeed)
	}
	if cfg.FingerprintPlatform != "" {
		fingerprintFlags += fmt.Sprintf(" --fingerprint-platform %s", cfg.FingerprintPlatform)
	}

	proxyFlag := ""
	if cfg.Proxy != "" {
		proxyFlag = fmt.Sprintf(" --proxy-server=%s", cfg.Proxy)
	}

	return fmt.Sprintf(`#!/bin/sh
set -e

# Helper: check if a process matching a pattern is running.
# Uses /proc (always available) instead of pgrep (needs procps).
proc_running() {
  for p in /proc/[0-9]*/cmdline; do
    [ -f "$p" ] || continue
    if tr '\0' ' ' < "$p" 2>/dev/null | grep -q "$1"; then
      return 0
    fi
  done
  return 1
}

echo "STEP 1: KasmVNC" >&2

# --- 1. Start KasmVNC (X display server + web VNC for visual handoff) ---
# Call Xkasmvnc directly (bypass vncserver Perl wrapper which requires
# root-owned /tmp/.X11-unix). Use -nolisten local to skip Unix sockets.
# -SecurityTypes None + no auth = no .kasmpasswd needed at runtime.
# /tmp/.X11-unix is pre-created as root:root 1777 in the Docker image to
# satisfy libxtrans ownership checks (the sandbox user cannot create it
# with root ownership at runtime).
# Skip if already running (idempotent).
#
# System /etc/kasmvnc/kasmvnc.yaml (image) sets require_ssl: false. Xkasmvnc
# HOME is a writable /tmp overlay (sandbox user cannot write /home/browser)
# so desktop.resolution can match the viewport when allow_resize is false.
# HOME is NOT exported globally — Chrome/crashpad needs its own writable HOME.
export XAUTHORITY=/tmp/.Xauthority
touch /tmp/.Xauthority
mkdir -p /tmp/.X11-unix
if ! proc_running 'Xkasmvnc.*:%s'; then
  mkdir -p /tmp/astonish-kasmvnc/.vnc
  cat > /tmp/astonish-kasmvnc/.vnc/kasmvnc.yaml << 'KASMCFG'
%s
KASMCFG
  VNC_LOG=/tmp/kasmvnc_start.log
  HOME=/tmp/astonish-kasmvnc setsid Xkasmvnc :%s \
    -geometry %dx%d \
    -depth 24 \
    -rfbport 5900 \
    -websocketPort %d \
    -httpd /usr/share/kasmvnc/www \
    -nolisten local \
    -auth /tmp/.Xauthority \
    -AlwaysShared \
    -DisableBasicAuth \
    -AcceptSetDesktopSize 0 \
    -SecurityTypes None \
    -interface 0.0.0.0 \
    -publicIP 127.0.0.1 \
    -Log *:stderr:30 \
    >"$VNC_LOG" 2>&1 &
  sleep 1
  if ! proc_running 'Xkasmvnc.*:%s'; then
    echo "KasmVNC failed to start. Log:" >&2
    cat "$VNC_LOG" >&2 2>/dev/null
    exit 1
  fi
fi
export DISPLAY=:%s

echo "STEP 2: Trust proxy CA" >&2

# --- 2. Trust the OpenShell proxy CA ---
# The OpenShell supervisor runs a TLS-intercepting proxy. Its CA certificate
# is provided at /etc/openshell-tls/openshell-ca.pem. Append it to the system
# CA bundle AND add it to Chrome's NSS database so Chromium trusts the
# re-signed certificates.
#
# Wrapped in a subshell so failures (Permission denied on /etc/ssl/certs or
# /home/browser/.pki) don't abort the script via set -e. CA trust is
# best-effort — the browser still launches without it.
if [ -f /etc/openshell-tls/openshell-ca.pem ]; then
  (
    cat /etc/openshell-tls/openshell-ca.pem >> /etc/ssl/certs/ca-certificates.crt 2>/dev/null
    # Use /tmp for NSS db — guaranteed writable by the sandbox user.
    # Then symlink from $HOME/.pki so Chrome finds it at its default path.
    NSSDB=/tmp/.pki/nssdb
    mkdir -p "$NSSDB" 2>/dev/null
    mkdir -p "$HOME/.pki" 2>/dev/null && ln -sf /tmp/.pki/nssdb "$HOME/.pki/nssdb" 2>/dev/null
    if command -v certutil >/dev/null 2>&1; then
      certutil -d sql:"$NSSDB" -N --empty-password 2>/dev/null
      certutil -d sql:"$NSSDB" -A -t "C,," -n "openshell-proxy-ca" -i /etc/openshell-tls/openshell-ca.pem 2>/dev/null
      echo "Added proxy CA to NSS db at $NSSDB" >&2
    else
      echo "certutil not available, skipping NSS trust" >&2
    fi
  ) || echo "CA trust setup failed (non-fatal)" >&2
fi

echo "STEP 3: Resolve CloakBrowser" >&2

# --- 3. Resolve CloakBrowser binary ---
# CloakBrowser was installed at build time with HOME=/home/browser.
# Force HOME=/home/browser so get_binary_path() resolves correctly.
BROWSER_BIN=$(HOME=/home/browser /usr/bin/python3 -c 'from cloakbrowser.config import get_binary_path; print(get_binary_path())' 2>/dev/null) || true
if [ -z "$BROWSER_BIN" ] || [ ! -f "$BROWSER_BIN" ]; then
  echo "CloakBrowser not at python path (got: '$BROWSER_BIN'), searching..." >&2
  # Fallback: find the chrome binary under known install locations.
  for base in /home/browser/.cloakbrowser /sandbox/.cloakbrowser; do
    candidate=$(find "$base" -name chrome -type f 2>/dev/null | head -1)
    if [ -n "$candidate" ] && [ -f "$candidate" ]; then
      BROWSER_BIN="$candidate"
      echo "Found CloakBrowser at: $BROWSER_BIN" >&2
      break
    fi
  done
  if [ -z "$BROWSER_BIN" ] || [ ! -f "$BROWSER_BIN" ]; then
    echo "No CloakBrowser binary found anywhere" >&2
    ls -la /home/browser/.cloakbrowser/ >&2 2>/dev/null || true
    exit 1
  fi
fi

echo "STEP 4: Launch CloakBrowser (bin=$BROWSER_BIN)" >&2

# --- 4. Launch CloakBrowser ---
# Runs as sandbox user directly. Uses /tmp/chromium as user-data-dir
# (writable by sandbox user; /home/browser/.config may not be).
# Skip if already running (idempotent for reconnection after CDP timeout).
if ! proc_running 'remote-debugging-port'; then
  BROWSER_LOG=/tmp/cloakbrowser.log
  setsid "$BROWSER_BIN" \
    --no-sandbox \
    --test-type \
    --disable-gpu \
    --disable-dev-shm-usage \
    --remote-debugging-port=%d \
    --window-size=%d,%d \
    --user-data-dir=/tmp/chromium \
    --disable-background-timer-throttling \
    --disable-backgrounding-occluded-windows \
    --disable-renderer-backgrounding \
    --disable-blink-features=AutomationControlled \
    --no-first-run \
    --no-default-browser-check \
    --noerrdialogs \
    --disable-features=TranslateUI%s%s \
    about:blank >"$BROWSER_LOG" 2>&1 &
  sleep 2

  echo "STEP 4b: Verify browser running" >&2

  # Verify browser started.
  if ! proc_running 'remote-debugging-port'; then
    echo "CloakBrowser died on startup. Log:" >&2
    cat /tmp/cloakbrowser.log >&2 2>/dev/null
    exit 1
  fi
fi

echo "STEP 5: socat CDP bridge" >&2

# --- 5. Start socat CDP bridge ---
# Skip if already running (idempotent).
if ! proc_running 'socat.*TCP-LISTEN:%d'; then
  setsid socat TCP-LISTEN:%d,fork,bind=0.0.0.0,reuseaddr TCP:127.0.0.1:%d &
fi

echo "STEP 6: Verify CDP port" >&2

# --- 6. Verify CDP port is listening ---
# Use /proc/net/tcp instead of ss (avoids iproute2 dependency).
CDP_READY=0
for i in 1 2 3 4 5 6 7 8 9 10; do
  if grep -qi ':%s ' /proc/net/tcp 2>/dev/null || grep -qi ':%s ' /proc/net/tcp6 2>/dev/null; then
    CDP_READY=1
    break
  fi
  sleep 0.5
done
if [ "$CDP_READY" = "0" ]; then
  echo "CDP port %d not listening after 5s" >&2
  cat /proc/net/tcp >&2 2>/dev/null
  exit 1
fi

echo "DONE: browser ready"

# Keep this shell alive so the supervisor doesn't kill our child processes.
# The caller holds the exec stream open; when it closes, this shell exits
# and the supervisor reaps KasmVNC + Chrome + socat.
exec sleep infinity
`,
		// KasmVNC proc_running + writable yaml overlay + start + verify
		kasmVNCDisplay,
		kasmvncConfigYAML(width, height),
		kasmVNCDisplay, width, height, kasmPort,
		kasmVNCDisplay,
		// DISPLAY export
		kasmVNCDisplay,
		// CloakBrowser launch
		cdpInternalPort, width, height,
		fingerprintFlags, proxyFlag,
		// socat proc_running + launch
		cdpPort, cdpPort, cdpInternalPort,
		// Verify CDP — port in hex for /proc/net/tcp
		portToHex(cdpPort), portToHex(cdpPort),
		// final error message
		cdpPort,
	)
}

// ---------------------------------------------------------------------------
// DialSandboxPort — exec-based TCP tunnel
// ---------------------------------------------------------------------------

// DialSandboxPort creates a net.Conn to host:port inside an OpenShell sandbox
// by running `socat STDIO TCP:127.0.0.1:<port>` through the gateway's
// bidirectional exec stream (gRPC ExecSandboxInteractive).
//
// This is the OpenShell equivalent of incus.DialViaExec. The tunnel works
// regardless of pod-to-pod network connectivity because it routes through
// the gateway's gRPC API.
//
// Race condition mitigation: The OpenShell gateway unconditionally allocates a
// PTY for ExecSandboxInteractive (ignores tty=false). The PTY starts with echo
// enabled, so any bytes written to stdin before "stty raw -echo" executes get
// echoed back on stdout, corrupting the tunneled TCP stream. To prevent this,
// the shell prints a sentinel line after stty completes; DialSandboxPort reads
// and discards all output until the sentinel, guaranteeing echo is disabled
// before the connection is returned to the caller.
const dialSentinel = "ASTONISH_TUNNEL_READY"

func DialSandboxPort(ctx context.Context, gateway GatewayClient, podName string, port int) (net.Conn, error) {
	cmd := fmt.Sprintf(
		"stty raw -echo 2>/dev/null; printf '%s\\n'; exec socat STDIO TCP:127.0.0.1:%d",
		dialSentinel, port,
	)
	conn, err := gateway.ExecStream(ctx, podName, ExecRequest{
		Command: []string{"sh", "-c", cmd},
		TTY:     false,
	})
	if err != nil {
		return nil, fmt.Errorf("openshell: dial %s:%d: %w", podName, port, err)
	}

	// Wait for the sentinel — discard any PTY echo or shell preamble.
	// The sentinel signals that stty has disabled echo and socat is connected.
	reader := bufio.NewReader(conn)
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			conn.Close()
			return nil, fmt.Errorf("openshell: dial %s:%d: waiting for tunnel ready: %w", podName, port, err)
		}
		if strings.TrimSpace(line) == dialSentinel {
			break
		}
	}

	return &ExecConn{
		conn:       conn,
		readPrefix: reader,
		local:      execAddr{pod: podName, port: 0},
		remote:     execAddr{pod: podName, port: port},
	}, nil
}

// ---------------------------------------------------------------------------
// ExecConn — net.Conn adapter over ExecStreamConn
// ---------------------------------------------------------------------------

// ExecConn wraps an OpenShell ExecStreamConn as a net.Conn. It tunnels a
// single TCP connection through the gateway's gRPC exec stream (socat STDIO).
//
// Same pattern as backend ExecStreaming tunnels.
type ExecConn struct {
	conn       ExecStreamConn
	readPrefix *bufio.Reader // buffered reader from sentinel drain; may hold leftover bytes
	local      execAddr
	remote     execAddr
}

// execAddr is a minimal net.Addr for informational purposes.
type execAddr struct {
	pod  string
	port int
}

func (a execAddr) Network() string { return "openshell-exec" }
func (a execAddr) String() string  { return fmt.Sprintf("%s:%d", a.pod, a.port) }

// Read reads data from the tunneled TCP connection (via socat stdout).
// If the bufio.Reader used during sentinel detection has buffered extra bytes,
// those are returned first before reading from the underlying stream.
func (c *ExecConn) Read(b []byte) (int, error) {
	if c.readPrefix != nil && c.readPrefix.Buffered() > 0 {
		return c.readPrefix.Read(b)
	}
	return c.conn.Read(b)
}

// Write sends data to the tunneled TCP connection (via socat stdin).
func (c *ExecConn) Write(b []byte) (int, error) {
	return c.conn.Write(b)
}

// Close terminates the tunnel by closing the exec stream.
func (c *ExecConn) Close() error {
	return c.conn.Close()
}

// LocalAddr returns a stub address identifying the tunnel.
func (c *ExecConn) LocalAddr() net.Addr { return c.local }

// RemoteAddr returns a stub address identifying the tunnel target.
func (c *ExecConn) RemoteAddr() net.Addr { return c.remote }

// SetDeadline is a no-op. The underlying gRPC stream does not support deadlines.
func (c *ExecConn) SetDeadline(_ time.Time) error { return nil }

// SetReadDeadline is a no-op.
func (c *ExecConn) SetReadDeadline(_ time.Time) error { return nil }

// SetWriteDeadline is a no-op.
func (c *ExecConn) SetWriteDeadline(_ time.Time) error { return nil }
