package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/google/uuid"
)

// Isolated native DOM observer test, not live employee acceptance or replay.
func TestEmployeePeerNavigationBrowser(t *testing.T) {
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
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("<!doctype html><body></body>")) }))
	defer server.Close()
	original, peer := uuid.NewString(), uuid.NewString()
	page := browser.MustPage(server.URL + "/#/chat/bots/personal/" + original).Timeout(10 * time.Second)
	page.MustEval(`([original, peer]) => {
   document.body.innerHTML = '<section aria-label="Bot workspace"><h1>Original</h1></section><div role="dialog"><input aria-label="Bot name"><button>Start conversation</button></div>';
   const workspace = document.querySelector('section');
   workspace.__reactProps$contract = {children:[{props:{botThread:{botId:original,scope:'personal'}}}]};
   window.creates = 0;
   document.querySelector('button').onclick = e => {
    window.creates++;
    e.target.disabled = true;
    e.target.textContent = 'Creating…';
    setTimeout(() => { location.hash = '/chat/bots/personal/'+peer; document.querySelector('[role="dialog"]').remove(); }, 200);
    setTimeout(() => { workspace.__reactProps$contract = {children:[{props:{botThread:{botId:peer,scope:'personal'}}}]}; workspace.querySelector('h1').textContent='Preservation witness'; }, 450);
   };
 }`, []string{original, peer})
	page.MustElementR(`[role="dialog"] button`, "^Start conversation$").MustClick()
	got, err := employeeWaitPeerHome(page, server.URL, original)
	if err != nil || got != peer {
		t.Fatalf("delayed peer navigation: got=%q err=%v", got, err)
	}
	if page.MustElement("h1").MustText() != employeePeerTitle {
		t.Fatal("route changed but old workspace was accepted")
	}
	if got := page.MustEval(`() => window.creates`).Int(); got != 1 {
		t.Fatalf("create count = %d", got)
	}
	for _, test := range []struct{ name, mutate, want string }{
		{"validation-error", `document.body.insertAdjacentHTML('beforeend','<p role="alert">creation rejected</p>')`, "creation rejected"},
		{"foreign-origin-same-id", ``, "canonical owned"},
		{"foreign-route-same-id", `location.hash='/chat/bots/team/'+peer`, "canonical owned"},
		{"route-extra-suffix", `location.hash='/chat/bots/personal/'+peer+'/runs'`, "canonical owned"},
		{"multiple-workspaces", `document.body.append(document.querySelector('section').cloneNode(true))`, "multiple preservation peer workspaces"},
		{"multiple-identities", `document.querySelector('section').__reactProps$contract.children.push({props:{botThread:{botId:peer,scope:'personal'}}})`, "multiple preservation peer workspace identities"},
		{"wrong-identity", `document.querySelector('section').__reactProps$contract.children[0].props.botThread.botId='foreign'`, "identity mismatch"},
		{"wrong-scope", `document.querySelector('section').__reactProps$contract.children[0].props.botThread.scope='team'`, "scope mismatch"},
	} {
		t.Run(test.name, func(t *testing.T) {
			page.MustEval(`peer => { location.hash='/chat/bots/personal/'+peer; document.body.innerHTML='<section aria-label="Bot workspace"></section>'; document.querySelector('section').__reactProps$contract={children:[{props:{botThread:{botId:peer,scope:'personal'}}}]}; }`, peer)
			page.MustEval(`peer => {`+test.mutate+`}`, peer)
			base := server.URL
			if test.name == "foreign-origin-same-id" {
				base = "http://foreign.invalid"
			}
			if _, err := employeeWaitPeerHome(page, base, original); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("want %q, got %v", test.want, err)
			}
			if page.MustEval(`() => window.creates`).Int() != 1 {
				t.Fatal("observer recreated peer")
			}
		})
	}
	t.Run("old-workspace-cannot-pass", func(t *testing.T) {
		page.MustEval(`original => {location.hash='/chat/bots/personal/'+original;document.body.innerHTML='<section aria-label="Bot workspace"></section><div role="dialog"><input aria-label="Bot name"><button>Creating…</button></div>';}`, original)
		bounded := page.Timeout(150 * time.Millisecond)
		defer bounded.CancelTimeout()
		if _, err := employeeWaitPeerHome(bounded, server.URL, original); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("old workspace must wait: %v", err)
		}
	})
}
