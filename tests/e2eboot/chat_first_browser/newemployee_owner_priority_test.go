package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
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

// Ordinary owner conversation must not inherit the physical deletion barrier.
func TestEmployeeOwnerSendDoesNotWaitForGlobalIdle(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "newemployee.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	ast.Inspect(f, func(n ast.Node) bool {
		a, ok := n.(*ast.AssignStmt)
		if !ok || len(a.Lhs) != 1 || len(a.Rhs) != 1 {
			return true
		}
		name, ok := a.Lhs[0].(*ast.Ident)
		if !ok || name.Name != "send" {
			return true
		}
		fn, ok := a.Rhs[0].(*ast.FuncLit)
		if !ok {
			return true
		}
		found = true
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if ok {
				if name, ok := call.Fun.(*ast.Ident); ok && name.Name == "idle" {
					t.Error("ordinary owner send waits for global idle; recurring work can starve admission")
				}
			}
			return true
		})
		return false
	})
	if !found {
		t.Fatal("owner send closure not found")
	}
}

// Controlled native composer proof, not a paid-provider or scheduler execution test.
func TestEmployeeOwnerSendBusyBrowser(t *testing.T) {
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
	const message = "Change the report cadence to every two minutes."
	var submitted atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/admit" {
			if r.Method != http.MethodPost {
				t.Error("non-POST admission")
				w.WriteHeader(405)
				return
			}
			if err := r.ParseForm(); err != nil || r.Form.Get("message") != message {
				t.Error("wrong native message payload")
				w.WriteHeader(400)
				return
			}
			submitted.Add(1)
			w.WriteHeader(http.StatusNoContent)
			return
		}
		fmt.Fprint(w, `<!doctype html><section aria-label="Bot workspace"><p>Scheduled work running; next recurrence queued</p><textarea data-testid="chat-input"></textarea><button data-testid="send-button">Send</button></section><script>document.querySelector('button').onclick=async()=>{const input=document.querySelector('textarea');await fetch('/admit',{method:'POST',body:new URLSearchParams({message:input.value})});input.value='';};</script>`)
	}))
	defer server.Close()
	b := store.Bot{ID: uuid.New(), HomeSessionID: uuid.New()}
	home := server.URL + "/#/chat/bots/personal/" + b.ID.String()
	page := browser.MustPage(home).Timeout(15 * time.Second)
	running := store.BotRun{ID: uuid.New(), BotID: b.ID, Trigger: store.TriggerCron, Status: store.RunRunning}
	queued := store.BotRun{ID: uuid.New(), BotID: b.ID, Trigger: store.TriggerCron, Status: store.RunQueued}
	owner := store.BotRun{ID: uuid.New(), BotID: b.ID, Trigger: store.TriggerManual, Status: store.RunQueued, Presentation: store.BotRunPresentationHomeInteractive, UserEventID: "owner-cadence-event", HistoryCursor: 42}
	oldOwner := owner
	oldOwner.ID = uuid.New()
	oldOwner.UserEventID = "previous-owner-event"
	runs := func() []store.BotRun {
		rr := []store.BotRun{running, queued, oldOwner}
		if submitted.Load() > 0 {
			rr = append(rr, owner)
		}
		return rr
	}
	captures, attempts := 0, 0
	capture := func(label, path string) string {
		captures++
		if path != "/api/studio/bots/"+b.ID.String()+"/sessions/"+b.HomeSessionID.String()+"/stream?scope=personal" {
			t.Fatal("wrong Home source")
		}
		return admissionHistory(oldOwner.UserEventID, "user", message) + admissionHistory(owner.UserEventID, "user", message)
	}
	got := employeeSend(func() *rod.Page { return page }, home, "cadence-change", message, func() store.BotSnapshot { return store.BotSnapshot{Bot: b} }, runs, capture, func() { attempts++ }, func(string, any) {})
	if got.ID != owner.ID || got.UserEventID != owner.UserEventID || got.HistoryCursor != owner.HistoryCursor || got.Status != store.RunQueued {
		t.Fatalf("wrong owner admission: %+v", got)
	}
	if attempts != 1 || submitted.Load() != 1 || captures == 0 {
		t.Fatalf("attempts=%d posts=%d captures=%d", attempts, submitted.Load(), captures)
	}
	rr := runs()
	if rr[0].Status != store.RunRunning || rr[1].Status != store.RunQueued {
		t.Fatal("recurring occupancy was cleared to admit owner")
	}
	if employeeComposer(page).Input != "" || page.MustInfo().URL != home {
		t.Fatal("send did not clear composer in owned Home")
	}
}
