package launcher

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-rod/rod/lib/launcher"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/toolconfirmation"

	"github.com/SAP/astonish/pkg/browser"
	"github.com/SAP/astonish/pkg/config"
)

type browserToolContext = tool.Context

type codeBrowserTestContext struct {
	browserToolContext
	context.Context
}

func (c codeBrowserTestContext) Deadline() (time.Time, bool)                        { return c.Context.Deadline() }
func (c codeBrowserTestContext) Done() <-chan struct{}                              { return c.Context.Done() }
func (c codeBrowserTestContext) Err() error                                         { return c.Context.Err() }
func (c codeBrowserTestContext) Value(k any) any                                    { return c.Context.Value(k) }
func (codeBrowserTestContext) ToolConfirmation() *toolconfirmation.ToolConfirmation { return nil }
func (codeBrowserTestContext) SessionID() string                                    { return "code-browser-test" }

func runFactoryBrowserTool(t *testing.T, available []tool.Tool, name string, args map[string]any) (map[string]any, error) {
	t.Helper()
	for _, candidate := range available {
		if candidate.Name() == name {
			runnable, ok := candidate.(interface {
				Run(tool.Context, any) (map[string]any, error)
			})
			if !ok {
				t.Fatalf("%s is not runnable", name)
			}
			return runnable.Run(codeBrowserTestContext{Context: context.Background()}, args)
		}
	}
	t.Fatalf("missing %s", name)
	return nil, nil
}

func TestBrowserToolsForFactoryNavigationPolicy(t *testing.T) {
	enabled := true
	for _, tc := range []struct {
		name            string
		cfg             ChatFactoryConfig
		loopbackAllowed bool
		lanAllowed      bool
	}{
		{name: "normal host"},
		{name: "platform host", cfg: ChatFactoryConfig{PlatformMode: true}},
		{name: "Code host", cfg: ChatFactoryConfig{CodeMode: true}, loopbackAllowed: true},
		{name: "sandbox", cfg: ChatFactoryConfig{AppConfig: &config.AppConfig{Sandbox: config.SandboxConfig{Enabled: &enabled}}}, loopbackAllowed: true, lanAllowed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// A nil Manager is a deterministic sentinel after guard validation;
			// blocked addresses never reach it and no Chrome is launched.
			var mgr *browser.Manager
			available, err := browserToolsForFactory(&tc.cfg, mgr)
			if err != nil {
				t.Fatal(err)
			}
			for _, input := range []struct {
				url     string
				allowed bool
			}{
				{"http://localhost:9393/#/settings", tc.loopbackAllowed},
				{"http://127.0.0.1:9393", tc.loopbackAllowed},
				{"http://[::1]:9393", tc.loopbackAllowed},
				{"http://192.168.1.1", tc.lanAllowed},
				{"http://169.254.169.254/latest/meta-data", tc.lanAllowed},
				{"file://localhost/etc/passwd", false},
			} {
				_, err := runFactoryBrowserTool(t, available, "browser_navigate", map[string]any{"url": input.url})
				if err == nil {
					t.Fatalf("expected guard or Manager sentinel error for %s", input.url)
				}
				blocked := strings.Contains(err.Error(), "blocked:")
				if blocked == input.allowed {
					t.Fatalf("%s: allowed=%v, error=%v", input.url, input.allowed, err)
				}
				if input.allowed && strings.Contains(err.Error(), "blocked:") {
					t.Fatalf("guard did not reach Manager: %v", err)
				}
			}
		})
	}
}

// Explicit opt-in makes missing Chrome a failure during acceptance, not a
// silently successful skip. No LLM, daemon, platform, or persistent profile.
func TestCodeBrowserLoopbackAcceptance(t *testing.T) {
	if os.Getenv("ASTONISH_CODE_BROWSER_ACCEPTANCE") != "1" {
		t.Skip("set ASTONISH_CODE_BROWSER_ACCEPTANCE=1 to exercise real host Chrome")
	}
	bin := os.Getenv("ASTONISH_CODE_BROWSER_PATH")
	if bin == "" {
		var ok bool
		bin, ok = launcher.LookPath()
		if !ok {
			t.Fatal("host Code browser prerequisite unavailable; set ASTONISH_CODE_BROWSER_PATH")
		}
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = fmt.Fprint(w, "<!doctype html><title>Code loopback acceptance</title><h1>Local Code browser works</h1>")
	}))
	defer server.Close()
	mgr := browser.NewManager(browser.BrowserConfig{ChromePath: bin, Headless: true, NoSandbox: true, UserDataDir: t.TempDir(), NavigationTimeout: 10 * time.Second})
	t.Cleanup(mgr.Cleanup)
	available, err := browserToolsForFactory(&ChatFactoryConfig{CodeMode: true}, mgr)
	if err != nil {
		t.Fatal(err)
	}
	for _, host := range []string{"localhost", "127.0.0.1", "[::1]"} {
		url := strings.Replace(server.URL, "127.0.0.1", host, 1) + "/#/settings"
		result, err := runFactoryBrowserTool(t, available, "browser_navigate", map[string]any{"url": url})
		if err != nil {
			t.Fatalf("navigate %s: %v", url, err)
		}
		if result["title"] != "Code loopback acceptance" || result["url"] != server.URL+"/#/settings" {
			t.Fatalf("unexpected loaded page: %#v", result)
		}
		t.Logf("real Chrome loaded %s: %#v", url, result)
	}
	for _, url := range []string{server.URL + "/#tab", "http://192.168.1.1", "http://169.254.169.254/latest/meta-data"} {
		_, err := runFactoryBrowserTool(t, available, "browser_tabs", map[string]any{"action": "new", "url": url})
		if url == server.URL+"/#tab" {
			if err != nil {
				t.Fatalf("Code new loopback tab: %v", err)
			}
		} else if err == nil || !strings.Contains(err.Error(), "blocked:") {
			t.Fatalf("Code tab must block private URL %s: %v", url, err)
		}
	}
	ordinary, err := browserToolsForFactory(&ChatFactoryConfig{}, mgr)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"browser_navigate", "browser_tabs"} {
		args := map[string]any{"url": server.URL}
		if name == "browser_tabs" {
			args["action"] = "new"
		}
		_, err := runFactoryBrowserTool(t, ordinary, name, args)
		if err == nil || !strings.Contains(err.Error(), "blocked:") {
			t.Fatalf("normal %s allowed loopback: %v", name, err)
		}
	}
}
