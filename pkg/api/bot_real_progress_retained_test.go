package api_test

import (
	"context"
	"github.com/go-rod/rod"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Read-only original-tab diagnostics. Does not start a worker or call a provider.
func TestLiveProgressInspectRetainedBrowser(t *testing.T) {
	cdp := os.Getenv("ASTONISH_RETAINED_CDP")
	if cdp == "" {
		t.Skip("explicit retained browser required")
	}
	dir := os.Getenv("ASTONISH_RETAINED_DIR")
	browser := rod.New().ControlURL(cdp).MustConnect()
	pages := browser.MustPages()
	if len(pages) != 1 {
		t.Fatalf("expected original tab, got %d", len(pages))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	page := pages[0].Context(ctx)
	capture := func(name string) {
		page.MustElement(`[aria-label="Bot workspace"]`).MustVisible()
		page.MustWait(`()=>document.querySelector('[aria-label="Bot workspace"]').innerText.length>15000`)
		text := page.MustEval(`()=>document.querySelector('[aria-label="Bot workspace"]').innerText`).Str()
		html := page.MustEval(`()=>document.querySelector('[aria-label="Bot workspace"]').outerHTML`).Str()
		if err := os.WriteFile(filepath.Join(dir, name+".txt"), []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name+".html"), []byte(html), 0600); err != nil {
			t.Fatal(err)
		}
		page.MustScreenshot(filepath.Join(dir, name+".png"))
		t.Logf("%s original_tab=%s chars=%d state=%s", name, page.TargetID, len(text), page.MustEval(`()=>JSON.stringify({diagrams:document.querySelectorAll('.mermaid-diagram').length,errors:document.querySelectorAll('.mermaid-error').length,questions:document.querySelectorAll('textarea[aria-label="Custom answer"]').length,answered:document.querySelectorAll('article[aria-label="Answered question"]').length})`).Str())
	}
	capture("retained-live")
	page.MustReload()
	capture("retained-reload")
}
