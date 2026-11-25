package tools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/SAP/astonish/pkg/browser"
	"github.com/go-rod/rod/lib/proto"
	"golang.org/x/net/websocket"
)

// Protocol fixture only: no Chrome, user's profile, or sandbox is launched.
func sessionArtifactBrowser(t *testing.T, mode string) (*browser.Manager, func(string) int) {
	t.Helper()
	var mu sync.Mutex
	methods := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/json/version" {
			_ = json.NewEncoder(w).Encode(map[string]any{"webSocketDebuggerUrl": "ws://127.0.0.1:9222/devtools/browser/test"})
			return
		}
		websocket.Server{Handshake: func(*websocket.Config, *http.Request) error { return nil }, Handler: func(c *websocket.Conn) {
			defer c.Close()
			for {
				var msg struct {
					ID        int    `json:"id"`
					Method    string `json:"method"`
					SessionID string `json:"sessionId"`
				}
				if websocket.JSON.Receive(c, &msg) != nil {
					return
				}
				mu.Lock()
				methods[msg.Method]++
				mu.Unlock()
				result := map[string]any{}
				target := map[string]any{"targetId": "page", "type": "page", "url": "https://session.invalid/", "attached": true}
				switch msg.Method {
				case "Browser.getVersion":
					result = map[string]any{"product": "Chrome/130.0", "protocolVersion": "1.3"}
				case "Target.getTargets":
					result = map[string]any{"targetInfos": []any{target}}
				case "Target.attachToTarget":
					result = map[string]any{"sessionId": "page-session"}
				case "Target.getTargetInfo":
					result = map[string]any{"targetInfo": target}
				case "Network.getResponseBody":
					if msg.SessionID != "page-session" {
						t.Errorf("foreign response CDP session %q", msg.SessionID)
					}
					result = map[string]any{"body": base64.StdEncoding.EncodeToString([]byte("actual-browser-response")), "base64Encoded": true}
				case "Page.printToPDF":
					result = map[string]any{"stream": "pdf-stream"}
				case "IO.read":
					if msg.SessionID != "page-session" {
						t.Errorf("foreign PDF CDP session %q", msg.SessionID)
					}
					result = map[string]any{"data": base64.StdEncoding.EncodeToString([]byte("%PDF-session")), "base64Encoded": true, "eof": true}
					if mode == "stalled" {
						result = map[string]any{"data": "", "eof": false}
					}
					if mode == "empty" {
						result = map[string]any{"data": "", "eof": true}
					}
					if mode == "invalid" {
						result = map[string]any{"data": "!", "base64Encoded": true, "eof": true}
					}
				}
				if websocket.JSON.Send(c, map[string]any{"id": msg.ID, "sessionId": msg.SessionID, "result": result}) != nil {
					return
				}
				if msg.Method == "Page.navigate" {
					_ = websocket.JSON.Send(c, map[string]any{"method": "Network.responseReceived", "sessionId": "page-session", "params": map[string]any{"requestId": "request", "type": "Fetch", "response": map[string]any{"url": "https://session.invalid/data", "status": 201, "headers": map[string]string{"X-Session": "owner"}}}})
					_ = websocket.JSON.Send(c, map[string]any{"method": "Network.loadingFinished", "sessionId": "page-session", "params": map[string]any{"requestId": "request", "encodedDataLength": 23}})
				}
			}
		}}.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)
	mgr := browser.NewManager(browser.DefaultConfig())
	mgr.SandboxEnabled = true
	mgr.SetSessionID("owner-session")
	mgr.ContainerResolveFunc = func(id string) (string, string, error) {
		if id != "owner-session" {
			t.Errorf("foreign session %q", id)
		}
		return id, "127.0.0.1", nil
	}
	mgr.ContainerStartBrowserFunc = func(string) (io.Closer, error) { return io.NopCloser(strings.NewReader("")), nil }
	mgr.ContainerDialFunc = func(string, int) (net.Conn, error) { return net.Dial("tcp", server.Listener.Addr().String()) }
	t.Cleanup(mgr.Cleanup)
	return mgr, func(method string) int { mu.Lock(); defer mu.Unlock(); return methods[method] }
}

func TestSessionBrowserPDFCDP(t *testing.T) {
	for _, mode := range []string{"success", "empty", "stalled", "invalid", "write-error"} {
		t.Run(mode, func(t *testing.T) {
			mgr, calls := sessionArtifactBrowser(t, mode)
			written := false
			mgr.WriteArtifactFunc = func(ctx context.Context, id, p string, data []byte) error {
				if id != "owner-session" || p != "/workspace/proof.pdf" || string(data) != "%PDF-session" {
					t.Fatalf("wrong session artifact: %s %s %q", id, p, data)
				}
				if mode == "write-error" {
					return errors.New("backend unavailable")
				}
				written = true
				return nil
			}
			ctx := &toolContextStub{Context: context.Background(), sessionID: "owner-session"}
			res, err := BrowserPDF(mgr)(ctx, BrowserPDFArgs{Path: "proof.pdf"})
			if mode == "success" {
				if err != nil || !written || res.SizeBytes != 12 {
					t.Fatalf("result %+v written=%v err=%v", res, written, err)
				}
			} else if err == nil || written {
				t.Fatalf("invalid stream published: %+v %v", res, err)
			}
			if calls("IO.close") != 1 {
				t.Fatal("PDF stream not closed")
			}
		})
	}
}

func TestSessionBrowserPDFFailsClosedBeforeLaunch(t *testing.T) {
	mgr := browser.NewManager(browser.DefaultConfig())
	mgr.SandboxEnabled = true
	ctx := &toolContextStub{Context: context.Background(), sessionID: "owner-session"}
	if _, err := BrowserPDF(mgr)(ctx, BrowserPDFArgs{}); err == nil {
		t.Fatal("missing transport accepted")
	}
	mgr.WriteArtifactFunc = func(context.Context, string, string, []byte) error { t.Fatal("invalid path written"); return nil }
	for _, p := range []string{"/tmp/out.pdf", "../out.pdf", "/workspace/sub/out.pdf", "bad\\out.pdf"} {
		if _, err := BrowserPDF(mgr)(ctx, BrowserPDFArgs{Path: p}); err == nil {
			t.Fatalf("accepted %q", p)
		}
	}
}

func TestSessionBrowserHandoffRequiresScopedRegistration(t *testing.T) {
	mgr := browser.NewManager(browser.DefaultConfig())
	mgr.SetSessionID("owner-session")
	mgr.ScopedHandoff = true
	if _, err := buildVNCHandoffResult(mgr, "container", "help", "", ""); err == nil {
		t.Fatal("fell back to global registry")
	}
	mgr.RegisterHandoffFunc = func(container, session string) (string, error) {
		if container != "container" || session != "owner-session" {
			t.Fatal("wrong handoff scope")
		}
		return "test-token", nil
	}
	res, err := buildVNCHandoffResult(mgr, "container", "help", "", "")
	if err != nil || !strings.Contains(res.VNCProxyURL, "test-token") {
		t.Fatalf("handoff %+v %v", res, err)
	}
}

func TestSessionBrowserRecordingTools(t *testing.T) {
	mgr, _ := sessionArtifactBrowser(t, "recording")
	stopped := false
	mgr.ContainerStartRecordingFunc = func(container, display, p string) (func() error, int, int, error) {
		if container != "owner-session" || display != ":0" || p != "/tmp/astonish-recordings/demo.mp4" {
			t.Fatalf("wrong recorder scope %s %s %s", container, display, p)
		}
		return func() error { stopped = true; return nil }, 1920, 1080, nil
	}
	mgr.StatArtifactFunc = func(ctx context.Context, id, p string) (int64, error) {
		if !stopped || id != "owner-session" || p != "/tmp/astonish-recordings/demo.mp4" {
			t.Fatal("incorrect recorder stat scope")
		}
		return 123, nil
	}
	ctx := &toolContextStub{Context: context.Background(), sessionID: "owner-session"}
	res, err := BrowserStartRecording(mgr)(ctx, BrowserStartRecordingArgs{Filename: "demo.mp4"})
	if err != nil || res.Width != 1920 {
		t.Fatalf("start %+v %v", res, err)
	}
	status, err := BrowserRecordingStatus(mgr)(ctx, BrowserRecordingStatusArgs{})
	if err != nil || !status.Recording {
		t.Fatalf("status %+v %v", status, err)
	}
	result, err := BrowserStopRecording(mgr)(ctx, BrowserStopRecordingArgs{})
	if err != nil || result.SizeBytes != 123 || !stopped {
		t.Fatalf("stop %+v %v", result, err)
	}
}

func TestSessionBrowserRecordingStatusRebinds(t *testing.T) {
	mgr := browser.NewManager(browser.DefaultConfig())
	mgr.SetSessionID("old-session")
	_, err := BrowserRecordingStatus(mgr)(&toolContextStub{Context: context.Background(), sessionID: "new-session"}, BrowserRecordingStatusArgs{})
	if err != nil || mgr.SessionID() != "new-session" {
		t.Fatal("status did not bind current session")
	}
}

func TestSessionBrowserResponseReadCancellation(t *testing.T) {
	mgr, calls := sessionArtifactBrowser(t, "response")
	ctx := &toolContextStub{Context: context.Background(), sessionID: "owner-session"}
	fn := BrowserResponseBody(mgr)
	if _, err := fn(ctx, BrowserResponseBodyArgs{Action: "listen", URLPattern: "*session.invalid*"}); err != nil {
		t.Fatal(err)
	}
	pg, err := mgr.CurrentPage()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = (proto.PageNavigate{URL: "https://session.invalid/data"}).Call(pg); err != nil {
		t.Fatal(err)
	}
	res, err := fn(ctx, BrowserResponseBodyArgs{Action: "read", Timeout: 1000})
	if err != nil || res.Body != "actual-browser-response" || res.Status != 201 || res.Headers["X-Session"] != "owner" {
		t.Fatalf("capture %+v %v", res, err)
	}
	if calls("Network.getResponseBody") != 1 || calls("Fetch.enable") != 0 {
		t.Fatal("capture did not use passive CDP network body")
	}
	if _, err = fn(ctx, BrowserResponseBodyArgs{Action: "stop"}); err != nil {
		t.Fatal(err)
	}
	if _, err = fn(ctx, BrowserResponseBodyArgs{Action: "read"}); err == nil {
		t.Fatal("stopped capture readable")
	}
	if _, err = fn(ctx, BrowserResponseBodyArgs{Action: "listen", URLPattern: "*"}); err != nil {
		t.Fatal(err)
	}
	cancelCtx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = fn(&toolContextStub{Context: cancelCtx, sessionID: "owner-session"}, BrowserResponseBodyArgs{Action: "read"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled read: %v", err)
	}
	mgr.EnsureSessionID("other-session")
	if mgr.ResponseCaptureFor(pg).IsActive() {
		t.Fatal("capture crossed session")
	}
}
