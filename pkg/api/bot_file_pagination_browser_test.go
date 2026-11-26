package api_test

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/api"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
)

// Controlled retained filesystem only; authorization, pagination HTTP and SPA
// remain production. Requests are recorded on the server, not patched in JS.
type paginationBrowserFiles struct {
	store.BotFiles
	root    string
	mu      sync.Mutex
	cursors []string
}

func (f *paginationBrowserFiles) List(ctx context.Context, _ store.BotID, path, cursor string, limit int) (*store.BotFilePage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if path != "" {
		return nil, store.ErrBotNotFound
	}
	f.mu.Lock()
	f.cursors = append(f.cursors, cursor)
	f.mu.Unlock()
	rows, err := os.ReadDir(f.root)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(rows))
	for _, r := range rows {
		names = append(names, r.Name())
	}
	sort.Strings(names)
	start := 0
	if cursor != "" {
		start, err = strconv.Atoi(cursor)
		if err != nil {
			return nil, store.ErrBotInvalid
		}
	}
	// Explicit two-page fixture independent of the service's maximum page size.
	const size = 100
	end := start + size
	if end > len(names) {
		end = len(names)
	}
	if start > end {
		return nil, store.ErrBotInvalid
	}
	result := &store.BotFilePage{Entries: []store.BotFileEntry{}}
	for _, name := range names[start:end] {
		st, e := os.Stat(filepath.Join(f.root, name))
		if e != nil {
			return nil, e
		}
		result.Entries = append(result.Entries, store.BotFileEntry{Path: name, Kind: store.BotFileRegular, SizeBytes: st.Size(), Freshness: fmt.Sprint(st.Size())})
	}
	if end < len(names) {
		result.Next = strconv.Itoa(end)
	}
	return result, nil
}
func (f *paginationBrowserFiles) requests() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.cursors...)
}

func TestBotFilePaginationBrowserAcceptance(t *testing.T) {
	dist, bin := replayBrowserPrerequisites(t)
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			f := newInteractiveHTTPFixture(t, "", nil, []string{"chat", "tool:execute"}, scope)
			files := &paginationBrowserFiles{root: t.TempDir()}
			for i := 0; i < 105; i++ {
				if err := os.WriteFile(filepath.Join(files.root, fmt.Sprintf("file-%03d.md", i)), []byte("old"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			run, err := f.repo.RequestRun(f.ctx, store.BotRunRequest{BotID: f.b.ID, Version: f.b.Version, Trigger: store.TriggerManual, Instructions: "Directory revision fixture", IdempotencyKey: uuid.NewString()})
			if err != nil {
				t.Fatal(err)
			}
			worker := execution.WithBotRunIdentity(f.ctx, execution.BotRunIdentity{BotID: f.b.ID, RunID: run.ID, Version: f.b.Version, ActorID: f.b.OwnerID.String(), OrgSlug: "interactive", TeamSlug: "team"})
			lease, err := f.repo.ClaimRun(worker, store.BotClaimRequest{BotID: f.b.ID, RunID: run.ID, WorkerID: "browser-files", ExpiresAt: time.Now().Add(5 * time.Minute)})
			if err != nil {
				t.Fatal(err)
			}
			server := generationHealthBrowserServer(t, f, dist, func(routes *mux.Router, svc *store.Services) {
				svc.BotFilesFactory = func(context.Context, *store.Services, store.BotScope) (store.BotFiles, error) { return files, nil }
				routes.HandleFunc("/api/studio/bots/{botID}/files", api.BotFilesHandler).Methods(http.MethodGet)
			})
			page := replayBrowserPage(t, bin, server.URL+"/#/chat/bots/"+string(scope)+"/"+f.b.ID.String())
			page.MustElement(`[aria-label="Bot workspace"]`).MustVisible()
			if len(files.requests()) != 0 {
				t.Fatal("closed Details eagerly listed files")
			}
			open := func() {
				page.MustElementR("button", "^Details$").MustClick()
				page.MustElement(`[aria-label="Bot files"]`).MustVisible()
			}
			listing := func() string {
				return page.MustEval(`() => [...document.querySelectorAll('[aria-label="Bot files"] li')].map(e=>e.innerText).join('\n')`).Str()
			}
			waitCount := func(n int) {
				t.Helper()
				page.MustWait(`n => document.querySelectorAll('[aria-label="Bot files"] li').length===n && !document.querySelector('[aria-label="Bot files"]').innerText.includes('Loading directory')`, n)
			}
			open()
			waitCount(100)
			first := listing()
			page.MustElementR("button", "^Load more files$").MustClick()
			waitCount(105)
			if got := files.requests(); !reflect.DeepEqual(got, []string{"", "100"}) {
				t.Fatalf("page2 was not requested: %v", got)
			}
			if err := os.Remove(filepath.Join(files.root, "file-000.md")); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(filepath.Join(files.root, "file-104.md")); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(files.root, "file-001.md"), []byte("changed revision payload"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(files.root, "aaa-new.md"), []byte("new"), 0600); err != nil {
				t.Fatal(err)
			}
			// A fenced repository mutation changes latest_sequence; the real snapshot SSE
			// supplies BotFilesPanel's revision prop while page two is still selected.
			before := len(files.requests())
			_, err = f.repo.AppendActivity(worker, lease, store.BotActivity{BotID: f.b.ID, RunID: &run.ID, Kind: store.BotActivityRun, Payload: []byte(`{"summary":"Directory revision changed"}`)})
			if err != nil {
				t.Fatal(err)
			}
			waitCount(100)
			page.MustElementR("button", "^aaa-new.md$").MustVisible()
			check := func() {
				t.Helper()
				if !page.MustEval(`() => {const rows=[...document.querySelectorAll('[aria-label="Bot files"] li')];const names=rows.map(e=>e.querySelector('button').textContent);return rows.length===100 && new Set(names).size===100 && names[0]==='aaa-new.md' && !names.includes('file-000.md') && !names.includes('file-104.md') && rows.find(e=>e.textContent.includes('file-001.md')).textContent.includes('24 bytes')}`).Bool() {
					t.Fatalf("stale/duplicated refreshed rows: %s", listing())
				}
			}
			check()
			if first == listing() {
				t.Fatal("revision refresh did not replace listing")
			}
			requests := files.requests()
			if len(requests) <= before || requests[len(requests)-1] != "" {
				t.Fatalf("refresh retained page2 cursor: %v", requests)
			}
			page.MustElementR("button", "^Load more files$").MustClick()
			waitCount(104)
			page.MustElementR("button", "^Close$").MustClick()
			open()
			waitCount(100)
			check()
			requests = files.requests()
			if len(requests) <= before || requests[len(requests)-1] != "" {
				t.Fatalf("reopen did not start page1: %v", requests)
			}
			page.MustElementR("button", "^Refresh files$").MustClick()
			waitCount(100)
			check()
			if f.model.productions != 0 || f.pool.calls != 0 {
				t.Fatal("file listing invoked provider/tools")
			}
			t.Logf("built SPA %s: explicit page2; fenced repository activity refreshed changed/deleted/new disk rows from page1 with no duplicates; close/reopen and Refresh files page1; cursors=%v", scope, files.requests())
		})
	}
}
