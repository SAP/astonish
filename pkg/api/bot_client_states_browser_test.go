package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/api"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/input"
	browserlauncher "github.com/go-rod/rod/lib/launcher"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
	"google.golang.org/adk/model"
	"google.golang.org/adk/session"
	"google.golang.org/genai"
)

// clientStateGate only controls fixture-owned transport. It never replaces browser
// fetch, installs React state, or changes production stream/renderer code.
type clientStateGate struct {
	mu        sync.Mutex
	cancel    context.CancelFunc
	cursors   []string
	closed    chan struct{}
	hold      chan struct{}
	fail      atomic.Bool
	mutations atomic.Int32
}

func (g *clientStateGate) disconnect(t *testing.T) {
	t.Helper()
	g.mu.Lock()
	cancel := g.cancel
	g.mu.Unlock()
	if cancel == nil {
		t.Fatal("no fixture-owned SSE connection to disconnect")
	}
	cancel()
	select {
	case <-g.closed:
	case <-time.After(5 * time.Second):
		t.Fatal("SSE handler did not detach")
	}
}
func clientStateStudio(t *testing.T) (string, string) {
	t.Helper()
	bin := os.Getenv("ASTONISH_BOT_BROWSER_PATH")
	if bin == "" {
		t.Fatal("explicit ASTONISH_BOT_BROWSER_PATH required")
	}
	raw, err := os.ReadFile("../../web/package.json")
	if err != nil {
		t.Fatal(err)
	}
	var pkg struct {
		Version string `json:"version"`
	}
	if err = json.Unmarshal(raw, &pkg); err != nil {
		t.Fatal(err)
	}
	dist, err := filepath.Abs(filepath.Join("../../web/dist", pkg.Version))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(dist, "index.html")); err != nil {
		t.Fatal("build Studio first", err)
	}
	return bin, dist
}

// Controlled public task, committed to BOTH an ordinary Chat session and a Bot
// Home in the same disposable SQLite tenant. ADK tool identities and accepted plan
// snapshots are identical; Bot runtime envelopes add no extra public meaning.
func clientStateRows(t *testing.T, sid, run uuid.UUID) []*session.Event {
	t.Helper()
	rows := []*session.Event{}
	add := func(content *genai.Content, delta map[string]any) {
		rows = append(rows, &session.Event{ID: fmt.Sprintf("shared-state-%02d", len(rows)), InvocationID: run.String(), Author: "assistant", Timestamp: time.Unix(1700000000+int64(len(rows)), 0), LLMResponse: model.LLMResponse{Content: content}, Actions: session.EventActions{StateDelta: delta}})
	}
	add(genai.NewContentFromText("Compare shared visual evidence", "user"), nil)
	rows[0].Author = "user"
	add(genai.NewContentFromText("Shared public acknowledgement", "model"), nil)
	call := func(id, name string, args map[string]any) {
		add(&genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{ID: id, Name: name, Args: args}}}}, nil)
	}
	result := func(id, name string, response map[string]any) {
		add(&genai.Content{Role: "model", Parts: []*genai.Part{{FunctionResponse: &genai.FunctionResponse{ID: id, Name: name, Response: response}}}}, nil)
	}
	call("shared-read-a", "read_file", map[string]any{"path": "/workspace/a.txt"})
	call("shared-read-b", "read_file", map[string]any{"path": "/workspace/b.txt"})
	result("shared-read-b", "read_file", map[string]any{"content": "Shared B evidence"})
	result("shared-read-a", "read_file", map[string]any{"content": "Shared A evidence"})
	call("shared-delegate", "delegate_tasks", map[string]any{"tasks": []any{map[string]any{"name": "Shared research", "task": "Compare public evidence"}}})
	result("shared-delegate", "delegate_tasks", map[string]any{"status": "complete", "results": []any{map[string]any{"name": "Shared research", "status": "success", "result": "Public evidence compared"}}})
	snapshot := map[string]any{"plan_id": "shared-state-plan", "goal": "Shared verification plan", "revision": 1, "sequence": 1, "document": map[string]any{"lifecycle": "approved"}, "steps": []any{map[string]any{"name": "check", "description": "Verify shared receipt", "status": "complete"}}}
	add(nil, map[string]any{agent.PlanSnapshotStateKey: snapshot, "private_checkpoint": "PRIVATE_CLIENT_STATE"})
	call("shared-write", "write_file", map[string]any{"path": "/workspace/shared.txt", "content": "Shared artifact bytes"})
	result("shared-write", "write_file", map[string]any{"status": "success", "path": "/workspace/shared.txt"})
	// A failed ordinary tool is shared error presentation, not an unreviewed Bot
	// delivery or a private runtime failure promoted to a public model answer.
	call("shared-error", "read_file", map[string]any{"path": "/workspace/missing.txt"})
	result("shared-error", "read_file", map[string]any{"error": "Shared recoverable source error"})
	add(genai.NewContentFromText("Shared presentation complete", "model"), nil)
	rows[len(rows)-1].TurnComplete = true
	_ = sid // Session linkage is deliberately outside the shared public row payload.
	return rows
}

// Physical built-Studio client state proof. Context authentication is supplied by
// the disposable fixture (including real membership/store checks), NOT a claim
// that production JWT issuance or paid-model execution has been exercised.
func TestBotClientStatesSharedVisualBrowserAcceptance(t *testing.T) {
	if os.Getenv("ASTONISH_MEMORY_BROWSER_ACCEPTANCE") != "1" {
		t.Skip("set ASTONISH_MEMORY_BROWSER_ACCEPTANCE=1 after building Studio")
	}
	bin, dist := clientStateStudio(t)
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			f := newInteractiveHTTPFixture(t, "", nil, []string{"chat", "tool:execute"}, scope)
			defer f.worker.Stop()
			org, err := f.db.ForOrg("interactive")
			if err != nil {
				t.Fatal(err)
			}
			personal := org.ForUser(f.b.OwnerID.String())
			ss := personal.Sessions()
			if scope == store.BotScopeTeam {
				ss = org.ForTeam("team").Sessions()
			}
			ordinaryID := uuid.New()
			run := uuid.New()
			ordinary, err := personal.Sessions().Create(f.ctx, &session.CreateRequest{AppName: "astonish", UserID: f.b.OwnerID.String(), SessionID: ordinaryID.String()})
			if err != nil {
				t.Fatal(err)
			}
			rows := clientStateRows(t, f.b.HomeSessionID, run)
			gate := &clientStateGate{closed: make(chan struct{}, 1), hold: make(chan struct{})}
			viewerID := uuid.NewString()
			if scope == store.BotScopeTeam {
				if e := f.db.Users().Create(f.ctx, &store.User{ID: viewerID, Email: viewerID + "@example.invalid", DisplayName: "Team viewer", Status: "active"}); e != nil {
					t.Fatal(e)
				}
				storedOrg, e := f.db.Organizations().GetBySlug(f.ctx, "interactive")
				if e != nil {
					t.Fatal(e)
				}
				if e := f.db.Organizations().AddMember(f.ctx, viewerID, storedOrg.ID, "member"); e != nil {
					t.Fatal(e)
				}
				teams, e := org.Teams().ListTeams(f.ctx)
				if e != nil || len(teams) == 0 {
					t.Fatalf("team lookup: %v", e)
				}
				if e := org.Teams().AddMember(f.ctx, &store.TeamMembership{UserID: viewerID, TeamID: teams[0].ID, Role: "member"}); e != nil {
					t.Fatal(e)
				}
			}
			viewer := atomic.Bool{}
			target, _ := url.Parse(f.server.URL)
			proxy := httputil.NewSingleHostReverseProxy(target)
			routes := mux.NewRouter()
			root := "/api/studio/bots"
			for _, r := range []struct {
				path, method string
				handler      http.HandlerFunc
			}{
				{root + "/{botID}/snapshot", "GET", api.BotSnapshotHandler},
				{root, "GET", api.BotListHandler}, {root + "/{botID}/activity", "GET", api.BotActivityHandler}, {root + "/{botID}/read", "GET", api.BotReadCursorHandler}, {root + "/{botID}/read", "POST", api.BotMarkReadHandler}, {root + "/{botID}/versions/{version}", "GET", api.BotGetVersionHandler}, {root + "/{botID}/runs", "GET", api.BotRunsHandler}, {root + "/{botID}/approvals", "GET", api.BotApprovalsHandler}, {root + "/{botID}/proposals", "GET", api.BotControlProposalsHandler},
				{"/api/studio/sessions/{id}", "GET", api.StudioSessionHandler}, {"/api/studio/sessions", "GET", api.StudioSessionsHandler},
			} {
				routes.HandleFunc(r.path, r.handler).Methods(r.method)
			}
			routes.HandleFunc(root+"/{botID}/sessions/{sessionID}/events", func(w http.ResponseWriter, r *http.Request) {
				ctx, cancel := context.WithCancel(r.Context())
				defer cancel()
				gate.mu.Lock()
				gate.cancel = cancel
				gate.cursors = append(gate.cursors, r.Header.Get("Last-Event-ID"))
				gate.mu.Unlock()
				defer func() {
					select {
					case gate.closed <- struct{}{}:
					default:
					}
				}()
				api.BotEventsHandler(w, r.WithContext(ctx))
			}).Methods("GET")
			// Ordinary Chat restoration is the production REST handler. Live ChatRunner
			// admission is intentionally not fabricated by this transport fixture.
			routes.HandleFunc("/api/studio/sessions/{id}/status", func(w http.ResponseWriter, r *http.Request) {
				json.NewEncoder(w).Encode(map[string]any{"running": false})
			}).Methods("GET")

			routes.NotFoundHandler = proxy
			svc := &store.Services{TenantRouter: f.db, Bots: org.ForTeam("team").Bots(), Sessions: org.ForTeam("team").Sessions(), PersonalBots: personal.Bots(), PersonalSessions: personal.Sessions(), BotInteractiveReady: true, BotTurnRegistry: f.registry, Credentials: org.ForTeam("team").Credentials(), PersonalCredentials: personal.Credentials()}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasPrefix(r.URL.Path, root) || strings.HasPrefix(r.URL.Path, "/api/studio/sessions") {
					if r.Method != "GET" && !(strings.HasSuffix(r.URL.Path, "/read") && r.Method == "POST") {
						gate.mutations.Add(1)
					}
					if strings.HasSuffix(r.URL.Path, "/snapshot") {
						select {
						case <-gate.hold:
						case <-r.Context().Done():
							return
						}
						if gate.fail.Load() {
							http.Error(w, "Fixture read unavailable", 503)
							return
						}
					}
					ctx, cancel := context.WithCancel(f.ctx)
					stop := context.AfterFunc(r.Context(), cancel)
					defer func() { stop(); cancel() }()
					if viewer.Load() {
						p, _ := execution.PrincipalFromContext(ctx)
						p.Subject = viewerID
						var authErr error
						ctx, authErr = execution.WithPrincipal(ctx, p)
						if authErr != nil {
							http.Error(w, authErr.Error(), 500)
							return
						}
						ctx, authErr = execution.WithAuthenticationEvidence(ctx, execution.AuthenticationEvidence{Method: p.Authentication, ExpiresAt: time.Now().Add(time.Hour)})
						if authErr != nil {
							http.Error(w, authErr.Error(), 500)
							return
						}
						ctx = store.WithTenantContext(ctx, &store.TenantContext{UserID: viewerID, OrgSlug: "interactive", TeamSlug: "team"})
					}
					routes.ServeHTTP(w, r.WithContext(store.WithServices(ctx, svc)))
					return
				}
				var value any
				switch r.URL.Path {
				case "/api/auth/setup-status":
					value = map[string]any{"initialized": true, "auth_mode": "builtin"}
				case "/api/auth/me":
					id := f.b.OwnerID.String()
					if viewer.Load() {
						id = viewerID
					}
					value = map[string]any{"user": map[string]any{"id": id, "email": "states@example.invalid", "display_name": "Client states", "role": "owner", "platform_role": "superadmin"}, "org": map[string]any{"slug": "interactive", "name": "Interactive"}, "team": "team"}
				case "/api/orgs":
					value = map[string]any{"orgs": []any{}, "active_org": "interactive"}
				case "/api/teams":
					value = []any{map[string]any{"slug": "team", "name": "Team"}}
				case "/api/settings/status":
					value = map[string]any{"setupRequired": false}
				case "/api/version":
					value = map[string]any{"version": "dev"}
				case "/api/agents", "/api/tools":
					value = []any{}
				case "/api/settings/config":
					value = map[string]any{}
				case "/api/settings/providers/effective":
					value = map[string]any{"providers": map[string]any{}}
				}
				if value != nil {
					w.Header().Set("Content-Type", "application/json")
					json.NewEncoder(w).Encode(value)
					return
				}
				if strings.HasPrefix(r.URL.Path, "/api/") {
					http.NotFound(w, r)
					return
				}
				path := filepath.Join(dist, filepath.Clean("/"+r.URL.Path))
				if st, e := os.Stat(path); e == nil && !st.IsDir() {
					http.FileServer(http.Dir(dist)).ServeHTTP(w, r)
					return
				}
				http.ServeFile(w, r, filepath.Join(dist, "index.html"))
			}))
			defer func() { server.CloseClientConnections(); server.Close() }()
			launch := browserlauncher.New().Bin(bin).UserDataDir(t.TempDir()).Headless(true).Leakless(false)
			control, err := launch.Launch()
			if err != nil {
				t.Fatal(err)
			}
			defer launch.Cleanup()
			browser := rod.New().ControlURL(control).MustConnect()
			defer browser.Close()
			page := browser.MustPage(server.URL + "/#/chat/bots/" + string(scope) + "/" + f.b.ID.String()).Timeout(25 * time.Second)
			page = page.WithPanic(func(v any) {
				text, _ := page.CancelTimeout().Eval(`() => document.body.innerText`)
				t.Fatalf("browser failure: %v\n%v", v, text)
			})
			t.Run("loading_read_failure_retry_empty", func(t *testing.T) {
				page.MustElementR("p", "^Loading Bot…$").MustVisible()
				gate.fail.Store(true)
				close(gate.hold)
				page.MustElementR("button", "^Retry workspace$").MustVisible()
				page.MustWait(`() => !!document.querySelector('[role="alert"]')`)
				gate.fail.Store(false)
				page.MustElementR("button", "^Retry workspace$").MustClick()
				page.MustElement(`[aria-label="Bot workspace"]`).MustVisible()
				page.MustElement(`[data-testid="chat-input"]`).MustVisible()
				if strings.Contains(page.MustElement(`[data-testid="message-area"]`).MustText(), "Shared presentation complete") {
					t.Fatal("empty session contains stale transcript")
				}
			})
			draft := "Unsent shared reconnect draft"
			page.MustElement(`[data-testid="chat-input"]`).MustInput(draft)
			appendRows := func(part []*session.Event) {
				t.Helper()
				for _, row := range part {
					if e := ss.AppendFleetEvent(f.ctx, f.b.HomeSessionID.String(), row); e != nil {
						t.Fatal(e)
					}
					if e := personal.Sessions().AppendEvent(f.ctx, ordinary.Session, row); e != nil {
						t.Fatal(e)
					}
				}
			}
			appendRows(rows[:2])
			page.MustElementR("p", "Shared public acknowledgement").MustVisible()
			t.Run("disconnect_cursor_draft_busy_catchup", func(t *testing.T) {
				// Disconnect after both parallel calls were delivered, before their
				// reversed results. Correlation must survive the actual SSE cursor.
				appendRows(rows[2:4])
				page.MustElementR("button", "^Explored ").MustVisible()
				gate.disconnect(t)
				appendRows(rows[4:12])
				page.MustElementR("button", "^Edited ").MustVisible()
				// Reconnect once more between write call and result: the file
				// artifact must not disappear just because its call predates the cursor.
				gate.disconnect(t)
				appendRows(rows[12:])
				page.MustElementR("p", "Shared presentation complete").MustVisible()
				gate.mu.Lock()
				cursors := append([]string(nil), gate.cursors...)
				gate.mu.Unlock()
				if len(cursors) < 2 || cursors[len(cursors)-1] == "" || !strings.HasPrefix(cursors[len(cursors)-1], f.b.HomeSessionID.String()+":") {
					t.Fatalf("no actual cursor reconnect: %v", cursors)
				}
				if got := page.MustElement(`[data-testid="chat-input"]`).MustProperty("value").Str(); got != draft {
					t.Fatalf("reconnect replaced unsent draft %q", got)
				}
				if strings.Count(page.MustElement(`[data-testid="message-area"]`).MustText(), "Shared presentation complete") != 1 {
					t.Fatal("reconnect duplicated final identity")
				}
				t.Logf("fixture socket detached; Last-Event-ID=%q; draft retained; catchup did not admit work", cursors[len(cursors)-1])
			})
			t.Run("shared_public_visual_comparison", func(t *testing.T) {
				// Compare named public content rows and chronological position, not generated
				// DOM IDs, elapsed-time labels or the intentionally separate management shell.
				markers := []string{"Compare shared visual evidence", "Shared public acknowledgement", "Shared research", "Shared verification plan", "/workspace/shared.txt", "Shared presentation complete"}
				visual := func(t *testing.T, mode string) []string {
					t.Helper()
					page.MustWait(`() => document.querySelector('[data-testid="message-area"]')?.innerText.includes('Shared presentation complete')`)
					// Expand the existing activity controls before comparing all public rows.
					buttons := page.MustElement(`[data-testid="message-area"]`).MustElements("button")
					for _, button := range buttons {
						label := button.MustText()
						if strings.HasPrefix(label, "Explored ") || strings.HasPrefix(label, "Ran ") || strings.HasPrefix(label, "Edited ") {
							button.MustClick()
						}
					}
					text := page.MustElement(`[data-testid="message-area"]`).MustText()
					if strings.Contains(text, "PRIVATE_CLIENT_STATE") {
						t.Fatal("private checkpoint leaked")
					}
					positions := []int{}
					out := []string{}
					for _, marker := range markers {
						n := strings.Count(text, marker)
						if marker == "/workspace/shared.txt" {
							n = 0
							for _, line := range strings.Split(text, "\n") {
								if strings.TrimSpace(line) == marker {
									n++
								}
							}
						}
						if n != 1 {
							t.Fatalf("%s %q count=%d: %s", mode, marker, n, text)
						}
						out = append(out, marker)
						positions = append(positions, strings.Index(text, marker))
					}
					for i := 1; i < len(positions); i++ {
						if positions[i] <= positions[i-1] {
							t.Fatalf("%s public chronology changed: %v", mode, positions)
						}
					}
					// Actual rendered tools/error are checked in the expanded group.
					expanded := page.MustElement(`[data-testid="message-area"]`).MustText()
					for _, marker := range []string{"read_file", "Shared A evidence", "Shared B evidence", "Shared recoverable source error"} {
						if !strings.Contains(expanded, marker) {
							t.Fatalf("%s missing rendered tool/error %q: %s", mode, marker, expanded)
						}
					}
					t.Logf("comparison rows mode=%s: text/tool/delegate/plan/error/artifact ordered", mode)
					return out
				}
				t.Run("ordinary_comparison", func(t *testing.T) {
					ordinaryPage := page
					page = browser.MustPage(server.URL + "/#/chat/" + ordinaryID.String()).Timeout(25 * time.Second)
					defer func() { page.MustClose(); page = ordinaryPage }()
					baseline := visual(t, "ordinary Chat restore baseline")
					page.MustReload()
					if !reflect.DeepEqual(baseline, visual(t, "ordinary Chat repeated restore")) {
						t.Fatal("ordinary restored rows differ")
					}
				})
				var botLive, botRestore []string
				t.Run("Bot_live_reconnect", func(t *testing.T) { botLive = visual(t, "Bot live reconnect") })
				page.MustReload()
				t.Run("Bot_restore", func(t *testing.T) { botRestore = visual(t, "Bot restore") })
				page.MustNavigate(server.URL + "/#/chat/" + ordinaryID.String())
				ordinaryRestore := visual(t, "ordinary Chat restore")
				if !reflect.DeepEqual(botLive, botRestore) || !reflect.DeepEqual(botLive, ordinaryRestore) {
					t.Fatal("shared public comparison rows differ")
				}
				if page.MustEval(`() => !!document.querySelector('[aria-label="Bot workspace"]')`).Bool() {
					t.Fatal("Bot management chrome leaked to ordinary Chat")
				}
			})
			t.Run("Escape_existing_details", func(t *testing.T) {
				page.MustNavigate(server.URL + "/#/chat/bots/" + string(scope) + "/" + f.b.ID.String())
				page.MustElementR("button", "^Details$").MustClick()
				page.MustElement(`[role="dialog"]`).MustVisible()
				page.MustElement(`[role="dialog"]`).MustType(input.Escape)
				page.MustWait(`() => [...document.querySelectorAll('[role="dialog"]')].every(e=>e.getAttribute('aria-hidden')==='true' || e.getBoundingClientRect().height===0)`)
			})
			runs, err := f.repo.ListRuns(f.ctx, f.b.ID, store.BotPage{})
			if err != nil || len(runs) != 0 {
				t.Fatalf("view/catchup/Escape admitted work: %+v %v", runs, err)
			}
			current, err := f.repo.Get(f.ctx, f.b.ID)
			if err != nil || current.Status != store.BotActive {
				t.Fatalf("read-only client activity mutated lifecycle: %+v %v", current, err)
			}
			if gate.mutations.Load() != 0 {
				t.Fatalf("unintended client mutation requests=%d", gate.mutations.Load())
			}
			t.Run("scope_cancellation_no_stale_transcript", func(t *testing.T) {
				page.MustNavigate(server.URL + "/#/chat")
				page.MustWait(`() => !document.querySelector('[aria-label="Bot workspace"]')`)
				// The Chat menu restores its previous ordinary conversation by design.
				// Exercise the actual New conversation control to request an empty one.
				page.MustElement(`[aria-label="New conversation"]`).MustClick()
				// Route navigation is asynchronous: wait for the new conversation
				// surface, not merely disappearance of the management shell. Sidebar
				// history previews are intentionally outside transcript isolation.
				page.MustWait(`() => !document.querySelector('[aria-label="Bot workspace"]') && !!document.querySelector('[data-testid="message-area"]') && !document.querySelector('[data-testid="message-area"]').innerText.includes('Shared presentation complete')`)
				if strings.Contains(page.MustElement(`[data-testid="message-area"]`).MustText(), "Shared presentation complete") {
					t.Fatal("Bot transcript leaked into fresh ordinary Chat")
				}
			})
			t.Run("populated_paused_stopped_deleted", func(t *testing.T) {
				transition := func(fn func(context.Context, store.BotLifecycleRequest) (*store.Bot, error)) *store.Bot {
					t.Helper()
					b, e := f.repo.Get(f.ctx, f.b.ID)
					if e != nil {
						t.Fatal(e)
					}
					b, e = fn(f.ctx, store.BotLifecycleRequest{BotID: b.ID, ActorID: b.OwnerID, ExpectedRevision: b.Revision, Reason: "fixture-owned client lifecycle"})
					if e != nil {
						t.Fatal(e)
					}
					return b
				}
				transition(f.repo.Pause)
				page.MustNavigate(server.URL + "/#/chat/bots/" + string(scope) + "/" + f.b.ID.String())
				page.MustElementR("p", "^paused ·").MustVisible()
				page.MustElementR("p", "Shared presentation complete").MustVisible()
				if page.MustElement(`[data-testid="chat-input"]`).MustProperty("disabled").Bool() {
					t.Fatal("paused owner lost conversational input")
				}
				if scope == store.BotScopeTeam {
					t.Run("team_viewer_paused_readonly", func(t *testing.T) {
						viewer.Store(true)
						defer viewer.Store(false)
						page.MustReload()
						page.MustElementR("p", "^paused ·").MustVisible()
						page.MustElementR("p", "Shared presentation complete").MustVisible()
						if !page.MustElement(`[data-testid="chat-input"]`).MustProperty("disabled").Bool() {
							t.Fatal("paused non-owner permits sending")
						}
					})
				}
				transition(f.repo.Stop)
				page.MustReload()
				page.MustElementR("p", "^stopped ·").MustVisible()
				page.MustElementR("p", "Shared presentation complete").MustVisible()
				if !page.MustElement(`[data-testid="chat-input"]`).MustProperty("disabled").Bool() {
					t.Fatal("stopped conversation permits sending")
				}
				transition(f.repo.BeginDelete)
				page.MustReload()
				page.MustElementR("p", "^deleted ·").MustVisible()
				if !page.MustElement(`[data-testid="chat-input"]`).MustProperty("disabled").Bool() {
					t.Fatal("soft-deleted conversation permits sending")
				}
				// Soft tombstones intentionally retain owner-readable history until purge.
			})
			t.Logf("built Studio %s isolated profile SQLite owned HTTP; no user daemon/model/cluster", scope)
		})
	}
	// Authentication issuance and ChatRunner admission remain separate acceptance tiers.
}
