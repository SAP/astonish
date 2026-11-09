package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/store"
	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/google/uuid"
)

// Controlled native-browser sender check. This is not paid-provider acceptance.
func TestEmployeeLegacyAdoptionBrowser(t *testing.T) {
	if os.Getenv("ASTONISH_MEMORY_BROWSER_ACCEPTANCE") != "1" {
		t.Skip("explicit native browser opt-in required")
	}
	bin := os.Getenv("ASTONISH_BROWSER_BIN")
	if bin == "" {
		var ok bool
		bin, ok = launcher.LookPath()
		if !ok {
			t.Fatal("native test browser unavailable")
		}
	}
	launch := launcher.New().Bin(bin).UserDataDir(t.TempDir()).Headless(true).Leakless(false)
	control, err := launch.Launch()
	if err != nil {
		t.Fatal(err)
	}
	defer launch.Cleanup()
	browser := rod.New().ControlURL(control).MustConnect()
	defer browser.Close()
	var submitted atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/admit" {
			submitted.Add(1)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		fmt.Fprint(w, `<!doctype html><section aria-label="Bot workspace"><textarea data-testid="chat-input"></textarea><button data-testid="send-button">Send</button></section><script>document.querySelector('button').onclick=async()=>{await fetch('/admit',{method:'POST'});document.querySelector('textarea').value='';};</script>`)
	}))
	defer server.Close()
	d, _, f, _ := legacyFixture(t)
	home := server.URL + "/#/chat/bots/personal/" + f.BotID.String()
	page := browser.MustPage(home).Timeout(15 * time.Second)
	if err := employeeLegacyUnmandated(d, f); err != nil {
		t.Fatal(err)
	}
	if submitted.Load() != 0 {
		t.Fatal("opening Home submitted work")
	}
	b := store.Bot{ID: f.BotID, HomeSessionID: f.HomeSessionID}
	r := store.BotRun{ID: uuid.New(), BotID: f.BotID, UserEventID: "fresh-owner-event", HistoryCursor: 1, Trigger: store.TriggerManual, Presentation: store.BotRunPresentationHomeInteractive, Status: store.RunSucceeded}
	runs := func() []store.BotRun {
		if submitted.Load() == 0 {
			return nil
		}
		return []store.BotRun{r}
	}
	capture := func(label, path string) string {
		return "data: " + employeeDiskJSON(t, map[string]any{"id": r.UserEventID, "author": "user", "persisted": true, "content": map[string]any{"role": "user", "parts": []map[string]string{{"text": employeeLegacyMessage}}}}) + "\n\n"
	}
	attempts := 0
	got := employeeSend(func() *rod.Page { return page }, home, "legacy-schedule", employeeLegacyMessage, func() store.BotSnapshot { return store.BotSnapshot{Bot: b} }, runs, capture, func() { attempts++ }, func(string, any) {})
	if got.ID != r.ID || got.UserEventID != r.UserEventID || attempts != 1 || submitted.Load() != 1 {
		t.Fatal("adoption did not bind exactly one fresh owner turn")
	}
	if page.MustInfo().URL != home {
		t.Fatal("adoption changed Home")
	}
}
