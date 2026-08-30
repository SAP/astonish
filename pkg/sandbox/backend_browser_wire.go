package sandbox

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	"github.com/SAP/astonish/pkg/browser"
)

const (
	backendBrowserCDPPort         = 9222
	backendBrowserInternalCDPPort = 9223
	backendBrowserDefaultVNCPort  = 6901
	backendBrowserKasmDisplay     = "0"
	backendBrowserXvfbDisplay     = "99"
)

func backendKasmVNCConfigYAML(width, height int) string {
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

// WireBackendBrowserManager configures mgr so browser tools launch Chromium
// inside a backend-managed session. This is used by the direct K8s backend,
// where Browser Manager callbacks can route through Backend.ExecStreaming.
func WireBackendBrowserManager(mgr *browser.Manager, backend Backend, sessReg *SessionRegistry, touchActivity func(sessionID string)) bool {
	if mgr == nil || backend == nil || sessReg == nil {
		return false
	}
	if backend.Kind() != BackendKindK8s {
		return false
	}

	bcfg := mgr.Config()
	mgr.SandboxEnabled = true
	mgr.ContainerResolveFunc = func(sessionID string) (string, string, error) {
		rec, err := sessReg.GetSession(sessionID)
		if err != nil || rec == nil || rec.PodName == "" {
			return "", "", fmt.Errorf("no running sandbox for session %q", sessionID)
		}
		// Use the session ID as the tunnel handle. Backend.ExecStreaming needs
		// session IDs, not pod names; the returned IP is only used to build the
		// CDP URL and is tunneled via ContainerDialFunc below.
		return sessionID, "127.0.0.1", nil
	}
	mgr.ContainerStartBrowserFunc = func(sessionID string) (io.Closer, error) {
		return startBackendBrowser(context.Background(), backend, sessionID, bcfg)
	}
	mgr.ContainerDialFunc = func(sessionID string, port int) (net.Conn, error) {
		return dialBackendSessionPort(context.Background(), backend, sessionID, port)
	}
	if touchActivity != nil {
		mgr.ActivityTouchFunc = touchActivity
	}
	return true
}

func startBackendBrowser(ctx context.Context, backend Backend, sessionID string, cfg browser.BrowserConfig) (io.Closer, error) {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()

	width := cfg.ViewportWidth
	if width <= 0 {
		width = 1920
	}
	height := cfg.ViewportHeight
	if height <= 0 {
		height = 1080
	}

	script := buildBackendBrowserLaunchScript(cfg, width, height)
	encoded := base64.StdEncoding.EncodeToString([]byte(script))
	wrapper := fmt.Sprintf("eval \"$(echo %s | base64 -d)\"", encoded)

	var stderr bytes.Buffer
	stream, err := backend.ExecStreaming(ctx, sessionID, ExecStreamSpec{
		Command:        []string{"/usr/local/bin/astonish-shell", "sh", "-c", wrapper},
		SeparateStderr: &stderr,
	})
	if err != nil {
		return nil, fmt.Errorf("backend browser: start in session %s: %w", shortSession(sessionID), err)
	}

	const doneSentinel = "DONE: browser ready"
	buf := make([]byte, 4096)
	var stdout strings.Builder
	for {
		n, readErr := stream.Read(buf)
		if n > 0 {
			_, _ = stdout.Write(buf[:n])
			if strings.Contains(stdout.String(), doneSentinel) {
				return &backendBrowserHandle{stream: stream}, nil
			}
		}
		if readErr == nil {
			continue
		}

		if ctx.Err() != nil {
			_ = stream.Close()
			return nil, fmt.Errorf("backend browser: launch timed out in session %s: %w\nStdout: %s\nStderr: %s", shortSession(sessionID), ctx.Err(), stdout.String(), stderr.String())
		}
		exitCode, waitErr := stream.Wait()
		_ = stream.Close()
		return nil, fmt.Errorf("backend browser: launch failed in session %s: %w (exit code %d, wait error: %v)\nStdout: %s\nStderr: %s", shortSession(sessionID), readErr, exitCode, waitErr, stdout.String(), stderr.String())
	}
}

type backendBrowserHandle struct {
	stream ExecStream
}

func (h *backendBrowserHandle) Close() error {
	if h.stream != nil {
		return h.stream.Close()
	}
	return nil
}

func buildBackendBrowserLaunchScript(cfg browser.BrowserConfig, width, height int) string {
	kasmPort := cfg.KasmVNCPort
	if kasmPort <= 0 {
		kasmPort = backendBrowserDefaultVNCPort
	}

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

proc_running() {
  for p in /proc/[0-9]*/cmdline; do
    [ -f "$p" ] || continue
    if tr '\0' ' ' < "$p" 2>/dev/null | grep -q "$1"; then
      return 0
    fi
  done
  return 1
}

port_hex() {
  printf '%%04X' "$1"
}

echo "STEP 1: Display" >&2
export XAUTHORITY=/tmp/.Xauthority
touch /tmp/.Xauthority
mkdir -p /tmp/.X11-unix
if command -v Xkasmvnc >/dev/null 2>&1; then
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
      cat "$VNC_LOG" >&2 2>/dev/null || true
      exit 1
    fi
  fi
  export DISPLAY=:%s
else
  export DISPLAY=:%s
  if command -v Xvfb >/dev/null 2>&1 && ! proc_running 'Xvfb.*:%s'; then
    setsid Xvfb :%s -screen 0 %dx%dx24 -nolisten tcp >/tmp/xvfb.log 2>&1 &
    sleep 1
  fi
fi

echo "STEP 2: Resolve browser" >&2
BROWSER_BIN=""
if command -v python3 >/dev/null 2>&1; then
  BROWSER_BIN=$(HOME=/home/browser python3 -c 'from cloakbrowser.config import get_binary_path; print(get_binary_path())' 2>/dev/null) || true
fi
if [ -z "$BROWSER_BIN" ] || [ ! -x "$BROWSER_BIN" ]; then
  for base in /home/browser/.cloakbrowser /root/.cache/rod/browser /usr/bin /usr/lib/chromium; do
    candidate=$(find "$base" -name chrome -type f 2>/dev/null | head -1)
    if [ -n "$candidate" ] && [ -x "$candidate" ]; then
      BROWSER_BIN="$candidate"
      break
    fi
  done
fi
if [ -z "$BROWSER_BIN" ] || [ ! -x "$BROWSER_BIN" ]; then
  if command -v chromium >/dev/null 2>&1; then
    BROWSER_BIN=$(command -v chromium)
  else
    echo "No browser binary found" >&2
    exit 1
  fi
fi

echo "STEP 3: Launch browser (bin=$BROWSER_BIN)" >&2
if ! proc_running 'remote-debugging-port'; then
  BROWSER_LOG=/tmp/astonish-browser.log
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
  if ! proc_running 'remote-debugging-port'; then
    echo "Browser died on startup. Log:" >&2
    cat "$BROWSER_LOG" >&2 2>/dev/null || true
    exit 1
  fi
fi

echo "STEP 4: socat CDP bridge" >&2
if ! proc_running 'socat.*TCP-LISTEN:%d'; then
  setsid socat TCP-LISTEN:%d,fork,bind=0.0.0.0,reuseaddr TCP:127.0.0.1:%d >/tmp/astonish-browser-socat.log 2>&1 &
fi

echo "STEP 5: Verify CDP port" >&2
CDP_HEX=$(port_hex %d)
CDP_READY=0
for i in 1 2 3 4 5 6 7 8 9 10; do
  if grep -qi ":$CDP_HEX " /proc/net/tcp 2>/dev/null || grep -qi ":$CDP_HEX " /proc/net/tcp6 2>/dev/null; then
    CDP_READY=1
    break
  fi
  sleep 0.5
done
if [ "$CDP_READY" = "0" ]; then
  echo "CDP port %d not listening after 5s" >&2
  cat /proc/net/tcp >&2 2>/dev/null || true
  exit 1
fi

echo "DONE: browser ready"
exec sleep infinity
`,
		backendBrowserKasmDisplay,
		backendKasmVNCConfigYAML(width, height),
		backendBrowserKasmDisplay, width, height, kasmPort,
		backendBrowserKasmDisplay,
		backendBrowserKasmDisplay,
		backendBrowserXvfbDisplay,
		backendBrowserXvfbDisplay,
		backendBrowserXvfbDisplay, width, height,
		backendBrowserInternalCDPPort, width, height, fingerprintFlags, proxyFlag,
		backendBrowserCDPPort, backendBrowserCDPPort, backendBrowserInternalCDPPort,
		backendBrowserCDPPort, backendBrowserCDPPort)
}

func dialBackendSessionPort(ctx context.Context, backend Backend, sessionID string, port int) (net.Conn, error) {
	stream, err := backend.ExecStreaming(ctx, sessionID, ExecStreamSpec{
		Command: []string{"/usr/local/bin/astonish-shell", "sh", "-c", fmt.Sprintf("exec socat STDIO TCP:127.0.0.1:%d", port)},
	})
	if err != nil {
		return nil, fmt.Errorf("backend browser: dial session %s:%d: %w", shortSession(sessionID), port, err)
	}
	return &backendExecConn{
		stream: stream,
		reader: bufio.NewReader(stream),
		local:  backendExecAddr{sessionID: sessionID, port: 0},
		remote: backendExecAddr{sessionID: sessionID, port: port},
	}, nil
}

type backendExecConn struct {
	stream ExecStream
	reader *bufio.Reader
	local  backendExecAddr
	remote backendExecAddr
}

func (c *backendExecConn) Read(b []byte) (int, error)         { return c.reader.Read(b) }
func (c *backendExecConn) Write(b []byte) (int, error)        { return c.stream.Write(b) }
func (c *backendExecConn) Close() error                       { return c.stream.Close() }
func (c *backendExecConn) LocalAddr() net.Addr                { return c.local }
func (c *backendExecConn) RemoteAddr() net.Addr               { return c.remote }
func (c *backendExecConn) SetDeadline(_ time.Time) error      { return nil }
func (c *backendExecConn) SetReadDeadline(_ time.Time) error  { return nil }
func (c *backendExecConn) SetWriteDeadline(_ time.Time) error { return nil }

type backendExecAddr struct {
	sessionID string
	port      int
}

func (a backendExecAddr) Network() string { return "backend-exec" }
func (a backendExecAddr) String() string {
	return fmt.Sprintf("%s:%d", shortSession(a.sessionID), a.port)
}
