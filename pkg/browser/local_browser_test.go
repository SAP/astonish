package browser

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
)

func TestLocalProfileCDPURL(t *testing.T) {
	for _, tc := range []struct{ name, content, want string }{
		{"valid", "12345\n/devtools/browser/abc-123", "ws://127.0.0.1:12345/devtools/browser/abc-123"},
		{"empty", "", ""},
		{"bad port", "0\n/devtools/browser/id", ""},
		{"large port", "65536\n/devtools/browser/id", ""},
		{"external URL", "12345\nws://example.com/devtools/browser/id", ""},
		{"query", "12345\n/devtools/browser/id?x=y", ""},
		{"empty id", "12345\n/devtools/browser/", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "DevToolsActivePort"), []byte(tc.content), 0600); err != nil {
				t.Fatal(err)
			}
			if got := localProfileCDPURL(dir); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
	if got := localProfileCDPURL(""); got != "" {
		t.Fatalf("unconfigured profile: %q", got)
	}
	if got := localProfileCDPURL(t.TempDir()); got != "" {
		t.Fatalf("missing endpoint: %q", got)
	}
}

func TestLocalStaleProfileFallsBackToLaunch(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "DevToolsActivePort"), []byte("1\n/devtools/browser/stale"), 0600); err != nil {
		t.Fatal(err)
	}
	m := NewManager(BrowserConfig{Headless: true, ChromePath: filepath.Join(dir, "missing-chrome"), UserDataDir: dir})
	defer m.Cleanup()
	if _, err := m.GetOrLaunch(); err == nil || !strings.Contains(err.Error(), "missing-chrome") {
		t.Fatalf("expected real launch after stale endpoint: %v", err)
	}
}

func TestLocalLaunchPreservesOriginalError(t *testing.T) {
	m := NewManager(BrowserConfig{ChromePath: filepath.Join(t.TempDir(), "missing-chrome")})
	defer m.Cleanup()
	_, err := m.GetOrLaunch()
	if err == nil || strings.Contains(err.Error(), "already launched") || !strings.Contains(err.Error(), "missing-chrome") {
		t.Fatalf("expected original launch failure, got %v", err)
	}
}

// This exercises real Chromium and the same manager used by browser_navigate.
func TestLocalBrowserReuseAcceptance(t *testing.T) {
	if os.Getenv("ASTONISH_LOCAL_BROWSER_ACCEPTANCE") != "1" {
		t.Skip("set ASTONISH_LOCAL_BROWSER_ACCEPTANCE=1 for live Chromium acceptance")
	}
	bin, ok := launcher.LookPath()
	if !ok {
		t.Fatal("Chromium required for opted-in acceptance")
	}
	profile := t.TempDir()
	owner := NewManager(BrowserConfig{ChromePath: bin, Headless: true, UserDataDir: profile})
	defer owner.Cleanup()
	b, err := owner.GetOrLaunch()
	if err != nil {
		t.Fatal(err)
	}
	pg, err := b.Page(proto.TargetCreateTarget{URL: "about:blank"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pg.Eval(`() => { window.reuseMarker = 'preserved'; }`); err != nil {
		t.Fatal(err)
	}
	endpoint := owner.CDPURL()
	pid := owner.launch.PID()

	// An unusable launch binary proves the second manager must attach, not launch.
	reuse := NewManager(BrowserConfig{ChromePath: filepath.Join(t.TempDir(), "must-not-launch"), UserDataDir: profile})
	defer reuse.Cleanup()
	reused, err := reuse.GetOrLaunch()
	if err != nil {
		t.Fatalf("reuse existing browser: %v", err)
	}
	if reuse.CDPURL() != endpoint || owner.launch.PID() != pid {
		t.Fatal("browser endpoint/process changed")
	}
	again, err := reuse.GetOrLaunch()
	if err != nil || again != reused {
		t.Fatalf("repeated call did not reuse connection: %v", err)
	}
	current, err := reuse.CurrentPage()
	if err != nil {
		t.Fatal(err)
	}
	marker, err := current.Eval(`() => window.reuseMarker`)
	if err != nil || marker.Value.Str() != "preserved" {
		t.Fatalf("tab state lost: %v %v", marker, err)
	}
	if err := current.Navigate("data:text/html,<title>browser reuse works</title>"); err != nil {
		t.Fatal(err)
	}
	title, err := current.Timeout(5 * time.Second).Eval(`() => document.title`)
	if err != nil || title.Value.Str() != "browser reuse works" {
		t.Fatalf("navigation failed: %v %v", title, err)
	}
	reuse.ResetForReconnect()
	if _, err := b.Version(); err != nil {
		t.Fatalf("reset killed owner browser: %v", err)
	}
	if _, err := reuse.GetOrLaunch(); err != nil {
		t.Fatalf("reattach: %v", err)
	}
	reuse.Cleanup()
	if _, err := b.Version(); err != nil {
		t.Fatalf("cleanup killed owner browser: %v", err)
	}
	t.Logf("reused Chromium PID %d, preserved tab state, navigated, reattached, and detached without closing owner", pid)
}
