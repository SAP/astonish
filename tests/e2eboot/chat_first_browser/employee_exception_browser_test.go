package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/store"
	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/google/uuid"
)

// This is an isolated DOM-observer contract test, not employee/live acceptance.
func TestEmployeeExceptionDOMObserverBrowser(t *testing.T) {
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
	page := browser.MustPage().Timeout(10 * time.Second)
	id := uuid.New()
	op := store.BotOperationView{SchemaVersion: 1, BotID: uuid.New(), HomeSessionID: uuid.New(), OperationID: id, Revision: 1, StateSequence: 1, Operation: store.BotControlMemoryForget, Status: store.BotOperationBlocked, Decision: store.BotOperationRequireAuthorization, Exception: &store.BotOperationExceptionView{OperationID: id, PayloadDigest: "digest", ExpiresAt: time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC), Fields: []store.BotOperationFieldView{{Caption: "Change", Value: "memory_forget"}, {Caption: "Memory", Value: "memory-id"}}}}
	html := fmt.Sprintf(`<article data-operation-id="%s"><h3>Forget information · Blocked</h3><dl><div><dt>Memory</dt><dd>memory-id</dd></div></dl><p>Permission requested for this saved action only.</p><dl><dt>Expires</dt><dd><time>2099-01-01T00:00:00Z</time></dd></dl></article>`, id)
	set := func() { page.MustEval(`html => document.body.innerHTML=html`, html) }
	set()
	card, err := employeeReadExceptionCard(page, op)
	if err != nil {
		t.Fatal(err)
	}
	if err = employeeExceptionCard(op, card); err != nil {
		t.Fatal(err)
	}
	t.Run("delayed-card-after-durable-readiness", func(t *testing.T) {
		// The durable op above is ready while the independently rendered Home
		// still shows Working. This is observer timing, not a synthetic live run.
		page.MustEval(`html => {
		  document.body.innerHTML = '<p>Working…</p>';
		  window.deniedOperation = '';
		  setTimeout(() => {
		    document.body.insertAdjacentHTML('beforeend', html);
		    const card = document.querySelector('article');
		    const deny = document.createElement('button');
		    deny.textContent = 'Deny';
		    deny.onclick = () => { window.deniedOperation = card.dataset.operationId; };
		    card.append(deny);
		  }, 300);
		}`, html)
		card, err := employeeReadExceptionCard(page, op)
		if err != nil {
			t.Fatal(err)
		}
		if err = employeeExceptionCard(op, card); err != nil {
			t.Fatal(err)
		}
		page.MustElementR(`article[data-operation-id="`+id.String()+`"] button`, "^Deny$").MustClick()
		if got := page.MustEval(`() => window.deniedOperation`).Str(); got != id.String() {
			t.Fatalf("denied %q, want exact operation %s", got, id)
		}
	})
	for _, name := range []string{"missing", "foreign", "hidden"} {
		t.Run("bounded-"+name, func(t *testing.T) {
			set()
			switch name {
			case "missing":
				page.MustEval(`() => document.querySelector('article').remove()`)
			case "foreign":
				page.MustEval(`() => document.querySelector('article').dataset.operationId='foreign'`)
			case "hidden":
				page.MustEval(`() => document.querySelector('article').style.opacity='0'`)
			}
			bounded := page.Timeout(250 * time.Millisecond)
			defer bounded.CancelTimeout()
			_, err := employeeReadExceptionCard(bounded, op)
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("missing exact visible card must time out: %v", err)
			}
		})
	}
	t.Run("duplicate-is-not-retried", func(t *testing.T) {
		set()
		page.MustEval(`() => document.body.append(document.querySelector('article').cloneNode(true))`)
		_, err := employeeReadExceptionCard(page, op)
		if err == nil || !strings.Contains(err.Error(), "found 2") {
			t.Fatalf("duplicate error must be preserved: %v", err)
		}
	})
	t.Run("browser-error-is-not-retried", func(t *testing.T) {
		set()
		page.MustEval(`() => { window.originalQuery = document.querySelectorAll; document.querySelectorAll = () => { throw new Error('observer probe failed'); }; }`)
		defer page.MustEval(`() => { document.querySelectorAll = window.originalQuery; }`)
		_, err := employeeReadExceptionCard(page, op)
		if err == nil || !strings.Contains(err.Error(), "observer probe failed") {
			t.Fatalf("browser error must be preserved: %v", err)
		}
	})
	for _, mutate := range []string{
		`() => document.querySelector('dd').style.display='none'`,
		`() => document.querySelector('dd').style.visibility='hidden'`,
		`() => document.querySelector('dd').style.opacity='0'`,
		`() => document.querySelector('dd').innerHTML='<span style="opacity:0">memory-id</span>'`,
		`() => document.querySelector('time').style.opacity='0'`,
		`() => document.querySelector('article').dataset.operationId='foreign'`,
		`() => document.querySelector('h3').innerText='Remember information · Blocked'`,
		`() => document.querySelector('time').innerText='2099-01-02T00:00:00Z'`,
		`() => document.body.append(document.querySelector('article').cloneNode(true))`,
		`() => { const dd=document.querySelector('dd'); document.body.append(dd); }`,
	} {
		set()
		page.MustEval(mutate)
		card, err = employeeReadExceptionCardOnce(page, op)
		if err == nil && employeeExceptionCard(op, card) == nil {
			t.Fatalf("unsafe DOM accepted: %s", mutate)
		}
	}
}
