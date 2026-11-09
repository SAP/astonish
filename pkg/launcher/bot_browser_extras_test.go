package launcher

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
	"golang.org/x/net/websocket"
)

// This fixture implements CDP on the wire. The production Manager, rod,
// Backend.ExecStreaming tunnel and concrete Chat handlers are all exercised.
// No installed Chrome or live user session is contacted.
type botBrowserFixture struct {
	sandbox.Backend
	mu                   sync.Mutex
	server               *httptest.Server
	ids                  []string
	methods              map[string][]json.RawMessage
	pushedID, pushedPath string
	data                 []byte
	pdfMode              string
	pushErr              error
}
type botBrowserFixtureStream struct{ net.Conn }

func (*botBrowserFixtureStream) Wait() (int, error)    { return 0, nil }
func (*botBrowserFixtureStream) Resize(int, int) error { return nil }
func (b *botBrowserFixture) Kind() sandbox.BackendKind { return sandbox.BackendKindDocker }
func (b *botBrowserFixture) ExecStreaming(_ context.Context, id string, spec sandbox.ExecStreamSpec) (sandbox.ExecStream, error) {
	b.mu.Lock()
	b.ids = append(b.ids, id)
	b.mu.Unlock()
	if strings.Contains(strings.Join(spec.Command, " "), "socat") {
		conn, err := net.Dial("tcp", b.server.Listener.Addr().String())
		if err != nil {
			return nil, err
		}
		return &botBrowserFixtureStream{conn}, nil
	}
	a, c := net.Pipe()
	go func() { defer c.Close(); fmt.Fprintln(c, "DONE: browser ready") }()
	return &botBrowserFixtureStream{a}, nil
}
func (b *botBrowserFixture) PushFile(ctx context.Context, id, p string, r io.Reader, mode os.FileMode) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if mode != 0600 {
		return fmt.Errorf("unsafe permissions")
	}
	if b.pushErr != nil {
		return b.pushErr
	}
	b.pushedID, b.pushedPath = id, p
	var err error
	b.data, err = io.ReadAll(r)
	return err
}
func newBotBrowserCDPFixture(t *testing.T) *botBrowserFixture {
	t.Helper()
	b := &botBrowserFixture{methods: map[string][]json.RawMessage{}}
	b.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/json/version" {
			json.NewEncoder(w).Encode(map[string]any{"webSocketDebuggerUrl": "ws://127.0.0.1:9222/devtools/browser/fixture"})
			return
		}
		websocket.Server{Handshake: func(*websocket.Config, *http.Request) error { return nil }, Handler: func(c *websocket.Conn) {
			defer c.Close()
			for {
				var msg struct {
					ID        int             `json:"id"`
					Method    string          `json:"method"`
					SessionID string          `json:"sessionId"`
					Params    json.RawMessage `json:"params"`
				}
				if websocket.JSON.Receive(c, &msg) != nil {
					return
				}
				if (strings.HasPrefix(msg.Method, "Network.") || strings.HasPrefix(msg.Method, "Emulation.") || strings.HasPrefix(msg.Method, "Runtime.") || strings.HasPrefix(msg.Method, "IO.")) && msg.SessionID != "owner-cdp-session" {
					t.Errorf("CDP %s used foreign page session %q", msg.Method, msg.SessionID)
				}
				b.mu.Lock()
				b.methods[msg.Method] = append(b.methods[msg.Method], msg.Params)
				b.mu.Unlock()
				result := map[string]any{}
				target := map[string]any{"targetId": "owner-page", "type": "page", "title": "fixture", "url": "https://example.test", "attached": true}
				switch msg.Method {
				case "Browser.getVersion":
					result = map[string]any{"product": "Chrome/130.0", "protocolVersion": "1.3"}
				case "Target.getTargets":
					result = map[string]any{"targetInfos": []any{target}}
				case "Target.attachToTarget":
					result = map[string]any{"sessionId": "owner-cdp-session"}
				case "Target.getTargetInfo":
					result = map[string]any{"targetInfo": target}
				case "Network.getCookies":
					result = map[string]any{"cookies": []any{}}
				case "Runtime.evaluate":
					result = map[string]any{"result": map[string]any{"type": "object", "objectId": "window"}}
				case "Runtime.callFunctionOn":
					result = map[string]any{"result": map[string]any{"type": "string", "value": "owner-value"}}
				case "Page.printToPDF":
					result = map[string]any{"stream": "pdf-stream"}
				case "IO.read":
					result = map[string]any{"data": "%PDF-1.7 fixture", "eof": true}
					switch b.pdfMode {
					case "empty":
						result = map[string]any{"data": "", "eof": true}
					case "stalled":
						result = map[string]any{"data": "", "eof": false}
					case "invalid-base64":
						result = map[string]any{"data": "!", "base64Encoded": true, "eof": true}
					case "oversized":
						result = map[string]any{"data": strings.Repeat("x", 64<<10), "eof": false}
					}
				}
				if websocket.JSON.Send(c, map[string]any{"id": msg.ID, "sessionId": msg.SessionID, "result": result}) != nil {
					return
				}
			}
		}}.ServeHTTP(w, r)
	}))
	t.Cleanup(b.server.Close)
	return b
}
func TestBotBrowserExtrasCDP(t *testing.T) {
	b := newBotBrowserCDPFixture(t)
	ts, cleanup, err := BotBrowserTools(b, "owner-run")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	byName := map[string]botRunnableTool{}
	for _, entry := range ts {
		byName[entry.Name()] = entry.(botRunnableTool)
	}
	ctx := botFactoryHandoffContext{base: context.Background(), session: "viewer-child"}
	requestScopeChecked := false
	byName["browser_set_headers"].(*botPinnedSessionTool).browserManager.ContainerEnsureReadyFunc = func(c context.Context, id string) error {
		requestScopeChecked = true
		if id != "owner-run" || c.(interface{ SessionID() string }).SessionID() != "owner-run" {
			return fmt.Errorf("unbound browser request context")
		}
		return nil
	}
	cases := []struct {
		name, method string
		args         map[string]any
	}{
		{"browser_set_headers", "Network.setExtraHTTPHeaders", map[string]any{"headers": map[string]any{"X-Run": "owner"}}},
		{"browser_set_geolocation", "Emulation.setGeolocationOverride", map[string]any{"latitude": 1.0, "longitude": 2.0}},
		{"browser_set_offline", "Network.emulateNetworkConditions", map[string]any{"offline": true}},
		{"browser_set_timezone", "Emulation.setTimezoneOverride", map[string]any{"timezone": "UTC"}},
		{"browser_set_locale", "Emulation.setLocaleOverride", map[string]any{"locale": "en-US"}},
		{"browser_set_media", "Emulation.setEmulatedMedia", map[string]any{"colorScheme": "dark"}},
		{"browser_cookies", "Network.getCookies", map[string]any{"action": "get"}},
		{"browser_cookies", "Network.setCookie", map[string]any{"action": "set", "name": "run", "value": "owner", "url": "https://example.test"}},
		{"browser_storage", "Runtime.callFunctionOn", map[string]any{"action": "set", "kind": "local", "key": "run", "value": "owner"}},
		{"browser_storage", "Runtime.callFunctionOn", map[string]any{"action": "get", "kind": "local", "key": "run"}},
		{"browser_run_code", "Runtime.callFunctionOn", map[string]any{"code": "return 'owner-value'"}},
		{"browser_pdf", "Page.printToPDF", map[string]any{"path": "proof.pdf"}},
	}
	for _, tt := range cases {
		t.Run(tt.name+tt.method, func(t *testing.T) {
			target := byName[tt.name]
			if target == nil {
				t.Fatalf("missing %s", tt.name)
			}
			b.mu.Lock()
			before := len(b.methods[tt.method])
			b.mu.Unlock()
			if _, err := target.Run(ctx, tt.args); err != nil {
				t.Fatal(err)
			}
			b.mu.Lock()
			defer b.mu.Unlock()
			if len(b.methods[tt.method]) <= before {
				t.Fatalf("real handler never invoked %s", tt.method)
			}
		})
	}
	if !requestScopeChecked {
		t.Fatal("request-local readiness context not exercised")
	}
	if b.pushedID != "owner-run" || b.pushedPath != "/workspace/proof.pdf" || string(b.data) != "%PDF-1.7 fixture" {
		t.Fatalf("wrong artifact owner/path/data: %s %s %q", b.pushedID, b.pushedPath, b.data)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, id := range b.ids {
		if id != "owner-run" {
			t.Fatalf("cross-run backend access: %s", id)
		}
	}
}
func TestBotBrowserPDFRejectsInvalidStreamAndBackendFailure(t *testing.T) {
	for _, mode := range []string{"empty", "stalled", "invalid-base64", "oversized", "push-failed"} {
		t.Run(mode, func(t *testing.T) {
			b := newBotBrowserCDPFixture(t)
			b.pdfMode = mode
			if mode == "push-failed" {
				b.pushErr = fmt.Errorf("sandbox unavailable")
			}
			ts, cleanup, err := BotBrowserTools(b, "owner-run")
			if err != nil {
				t.Fatal(err)
			}
			defer cleanup()
			for _, entry := range ts {
				if entry.Name() == "browser_pdf" {
					_, err := entry.(botRunnableTool).Run(botFactoryHandoffContext{base: context.Background(), session: "viewer"}, map[string]any{"path": "/workspace/result.pdf"})
					if err == nil {
						t.Fatal("invalid PDF succeeded")
					}
				}
			}
			if b.pushedPath != "" {
				t.Fatal("failed PDF published")
			}
			b.mu.Lock()
			defer b.mu.Unlock()
			if len(b.methods["IO.close"]) != 1 {
				t.Fatal("PDF stream was not closed")
			}
		})
	}
}

func TestBotBrowserExtrasBoundaries(t *testing.T) {
	b := newBotBrowserCDPFixture(t)
	ts, cleanup, err := BotBrowserTools(b, "owner-run")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	ctx := botFactoryHandoffContext{base: context.Background(), session: "viewer-child"}
	for _, entry := range ts {
		target := entry.(botRunnableTool)
		if botBrowserUnsupported(entry.Name()) != "" {
			if _, err := target.Run(ctx, map[string]any{}); !errors.Is(err, store.ErrBotUnsupported) {
				t.Fatalf("%s: %v", entry.Name(), err)
			}
		}
		if entry.Name() == "browser_pdf" {
			for _, p := range []string{"/tmp/daemon.pdf", "../escape.pdf", "/workspace/../escape.pdf", "dir/file.pdf", "bad\\file.pdf"} {
				if _, err := target.Run(ctx, map[string]any{"path": p}); err == nil {
					t.Errorf("accepted %s", p)
				}
			}
		}
		canceled, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := target.Run(botFactoryHandoffContext{base: canceled, session: "viewer"}, map[string]any{}); !errors.Is(err, context.Canceled) {
			t.Fatalf("%s ignored cancellation: %v", entry.Name(), err)
		}
		action, err := (BotCatalogueClassifier{}).DescribeAction(context.Background(), entry.Name(), json.RawMessage(`{}`))
		if err != nil || action.Effect != execution.EffectUnknown || action.ConstraintsVerified {
			t.Fatalf("browser action bypassed exact review: %s", entry.Name())
		}
	}
	if len(b.ids) != 0 || b.pushedPath != "" {
		t.Fatal("rejected calls reached backend")
	}
}
