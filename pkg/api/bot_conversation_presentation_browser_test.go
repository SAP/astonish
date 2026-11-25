package api_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/api"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/store"
	"github.com/go-rod/rod"
	browserlauncher "github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
	"google.golang.org/adk/model"
	"google.golang.org/adk/session"
	"google.golang.org/genai"
)

// Controlled immutable receipt and disk object; production HTTP authorization,
// schema validation, preview redaction and download tickets remain exercised.
type mixedPresentationRepo struct {
	browserPublicationRepo
	receipt store.BotPublication
}

func (r mixedPresentationRepo) ReadBotPublication(ctx context.Context, b store.BotID, run store.RunID, seq int64) (*store.BotPublication, error) {
	if _, err := r.ReadFilePublication(ctx, b, run, seq); err != nil {
		return nil, err
	}
	p := r.receipt
	return &p, nil
}

type mixedPresentationFiles struct {
	*browserWorkspaceFiles
	ref store.BotContentRef
}

func (f *mixedPresentationFiles) OpenContent(ctx context.Context, ref store.BotContentRef, br store.BotFileRange) (*store.BotFileStream, error) {
	if ref != f.ref {
		return nil, store.ErrBotForbidden
	}
	stream, err := f.Open(ctx, ref.Scope.BotID, "mixed-reviewed.md", br)
	if err == nil {
		stream.Entry.SHA256 = ref.SHA256
		stream.Entry.Freshness = ref.SHA256
	}
	return stream, err
}

// Physical presentation proof: built Studio, isolated Chrome profile, real scoped
// SQLite and production transcript/history HTTP handlers. Controlled public events
// are persisted, not intercepted in browser fetch or injected into React state.
func TestBotConversationPresentationBrowserAcceptance(t *testing.T) {
	if os.Getenv("ASTONISH_MEMORY_BROWSER_ACCEPTANCE") != "1" {
		t.Skip("set ASTONISH_MEMORY_BROWSER_ACCEPTANCE=1 after building Studio")
	}
	bin := os.Getenv("ASTONISH_BOT_BROWSER_PATH")
	if bin == "" {
		t.Fatal("explicit browser executable required")
	}
	var pkg struct {
		Version string `json:"version"`
	}
	raw, err := os.ReadFile("../../web/package.json")
	if err != nil {
		t.Fatal(err)
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
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			f := newInteractiveHTTPFixture(t, "", nil, []string{"chat", "tool:execute"}, scope)
			defer f.worker.Stop()
			files := &mixedPresentationFiles{browserWorkspaceFiles: &browserWorkspaceFiles{root: t.TempDir()}}
			reviewed := []byte("# Reviewed mixed deliverable\nPublic mixed receipt verified.\n")
			if err := os.WriteFile(filepath.Join(files.root, "mixed-reviewed.md"), reviewed, 0600); err != nil {
				t.Fatal(err)
			}
			runID := uuid.New()
			publication := store.BotFilePublication{SchemaVersion: 1, BotID: f.b.ID, RunID: runID, HomeSessionID: f.b.HomeSessionID, HomeID: f.b.Home.ID, ReportSequence: 1, EventID: store.BotPublicationEventID(runID, 1), Summary: "Reviewed mixed delivery.", Artifacts: []store.BotPublishedArtifact{{ID: uuid.New(), HomeID: f.b.Home.ID, RelativePath: "mixed-reviewed.md", SHA256: fmt.Sprintf("%x", sha256.Sum256(reviewed)), SizeBytes: int64(len(reviewed)), MediaType: "text/markdown", Preview: store.BotFilePreviewText}}}
			files.ref = store.BotContentRef{Format: 1, Scope: store.BotContentScope{OrgID: "interactive", TeamID: "team", OwnerID: f.b.OwnerID.String(), BotID: f.b.ID}, Kind: store.BotContentArtifact, Key: "mixed-object", SHA256: publication.Artifacts[0].SHA256, SizeBytes: int64(len(reviewed)), Revision: 1}
			receipt := store.BotPublication{BotID: f.b.ID, RunID: runID, HomeSessionID: f.b.HomeSessionID, ReportSequence: 1, EventID: publication.EventID, Text: publication.Summary, Artifacts: []store.BotArtifact{{ID: publication.Artifacts[0].ID, HomeID: f.b.Home.ID, RelativePath: "mixed-reviewed.md", SHA256: files.ref.SHA256, SizeBytes: files.ref.SizeBytes, ContentRef: &files.ref}}}
			org, err := f.db.ForOrg("interactive")
			if err != nil {
				t.Fatal(err)
			}
			personal := org.ForUser(f.b.OwnerID.String())
			sessions := personal.Sessions()
			if scope == store.BotScopeTeam {
				sessions = org.ForTeam("team").Sessions()
			}
			target, _ := url.Parse(f.server.URL)
			proxy := httputil.NewSingleHostReverseProxy(target)
			routes := mux.NewRouter()
			root := "/api/studio/bots"
			for _, r := range []struct {
				path, method string
				handler      http.HandlerFunc
			}{
				{root, "GET", api.BotListHandler}, {root + "/{botID}/activity", "GET", api.BotActivityHandler},
				{root + "/{botID}/read", "GET", api.BotReadCursorHandler}, {root + "/{botID}/read", "POST", api.BotMarkReadHandler},
				{root + "/{botID}/versions/{version}", "GET", api.BotGetVersionHandler}, {root + "/{botID}/runs", "GET", api.BotRunsHandler},
				{root + "/{botID}/approvals", "GET", api.BotApprovalsHandler}, {root + "/{botID}/proposals", "GET", api.BotControlProposalsHandler},
			} {
				routes.HandleFunc(r.path, r.handler).Methods(r.method)
			}
			routes.NotFoundHandler = proxy
			svc := &store.Services{TenantRouter: f.db, Bots: org.ForTeam("team").Bots(), Sessions: org.ForTeam("team").Sessions(), PersonalBots: personal.Bots(), PersonalSessions: personal.Sessions(), BotInteractiveReady: true, BotTurnRegistry: f.registry}
			if scope == store.BotScopeTeam {
				svc.Bots = mixedPresentationRepo{browserPublicationRepo{svc.Bots, publication}, receipt}
			} else {
				svc.PersonalBots = mixedPresentationRepo{browserPublicationRepo{svc.PersonalBots, publication}, receipt}
			}
			svc.Credentials = org.ForTeam("team").Credentials()
			svc.PersonalCredentials = personal.Credentials()
			artifactRoot := root + "/{botID}/runs/{runID}/reports/{reportSequence}/artifacts/{artifactID}"
			routes.HandleFunc(artifactRoot+"/preview", api.BotFilePreviewHandler).Methods("GET")
			routes.HandleFunc(artifactRoot+"/download-ticket", api.BotFileDownloadTicketHandler).Methods("POST")
			routes.HandleFunc(artifactRoot+"/download", api.BotFileDownloadHandler).Methods("GET", "HEAD")
			svc.BotFilesFactory = func(context.Context, *store.Services, store.BotScope) (store.BotFiles, error) { return files, nil }
			routes.HandleFunc(root+"/{botID}/sessions/{sessionID}/events", api.BotEventsHandler).Methods("GET")
			routes.HandleFunc(root+"/{botID}/files/preview", api.BotFilePreviewHandler).Methods("GET")
			routes.HandleFunc(root+"/{botID}/files/download-ticket", api.BotFileDownloadTicketHandler).Methods("POST")
			routes.HandleFunc(root+"/{botID}/files/download", api.BotFileDownloadHandler).Methods("GET", "HEAD")
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasPrefix(r.URL.Path, root) {
					ctx, cancel := context.WithCancel(f.ctx)
					stop := context.AfterFunc(r.Context(), cancel)
					defer func() { stop(); cancel() }()
					routes.ServeHTTP(w, r.WithContext(store.WithServices(ctx, svc)))
					return
				}
				var value any
				switch r.URL.Path {
				case "/api/auth/setup-status":
					value = map[string]any{"initialized": true, "auth_mode": "builtin"}
				case "/api/auth/me":
					value = map[string]any{"user": map[string]any{"id": f.b.OwnerID.String(), "email": "mixed@example.invalid", "display_name": "Mixed acceptance", "role": "owner", "platform_role": "superadmin"}, "org": map[string]any{"slug": "interactive", "name": "Interactive"}, "team": "team"}
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
					_ = json.NewEncoder(w).Encode(value)
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
			downloads := t.TempDir()
			if err := (proto.BrowserSetDownloadBehavior{Behavior: proto.BrowserSetDownloadBehaviorBehaviorAllow, DownloadPath: downloads}).Call(browser); err != nil {
				t.Fatal(err)
			}
			page := browser.MustPage(server.URL + "/#/chat/bots/" + string(scope) + "/" + f.b.ID.String()).Timeout(30 * time.Second)
			page = page.WithPanic(func(v any) {
				text, _ := page.CancelTimeout().Eval(`() => document.body.innerText`)
				t.Fatalf("browser failure: %v\n%v", v, text)
			})
			page.MustElement(`[aria-label="Bot workspace"]`).MustVisible()
			input := page.MustElement(`[data-testid="chat-input"]`)
			input.MustInput("Retain mixed draft during reconnect")
			events := []struct {
				kind string
				data map[string]any
			}{
				{"user_message", map[string]any{"text": "Compare public mixed evidence"}},
				{"text", map[string]any{"text": "Mixed public acknowledgement"}},
				{"tool_call", map[string]any{"id": "mixed-read", "name": "read_file", "args": map[string]any{"path": "/workspace/public-receipt.txt"}}},
				{"tool_result", map[string]any{"id": "mixed-read", "name": "read_file", "result": map[string]any{"content": "Public mixed receipt"}}},
				{"subtask_progress", map[string]any{"event_type": "delegation_start", "batch_id": "mixed-batch", "tasks": []any{map[string]any{"task_id": "research", "name": "Mixed research", "description": "Compare public evidence"}}}},
				{"subtask_progress", map[string]any{"event_type": "task_start", "batch_id": "mixed-batch", "task_id": "research", "task_name": "Mixed research"}},
				{"subtask_progress", map[string]any{"event_type": "task_complete", "batch_id": "mixed-batch", "task_id": "research", "task_name": "Mixed research", "status": "success", "summary": "Public evidence compared"}},
				{"subtask_progress", map[string]any{"event_type": "plan_snapshot", "snapshot": map[string]any{"plan_id": "mixed-plan", "goal": "Mixed verification plan", "revision": 1, "sequence": 1, "document": map[string]any{"lifecycle": "approved"}, "steps": []any{map[string]any{"name": "check", "description": "Verify public receipt", "status": "complete"}}}}},
				{"error", map[string]any{"message": "Public mixed recoverable error"}},
				{"text", map[string]any{"text": "Mixed presentation complete"}},
				{"turn_complete", map[string]any{}},
			}
			for i, event := range events {
				event.data["private_checkpoint"] = "PRIVATE_MIXED_CHECKPOINT"
				payload, err := json.Marshal(event.data)
				if err != nil {
					t.Fatal(err)
				}
				row := &session.Event{ID: fmt.Sprintf("mixed-%d", i), Author: "bot_runtime", Timestamp: time.Now(), Actions: session.EventActions{StateDelta: map[string]any{"bot_runtime_event": bot.RuntimeEvent{SessionID: f.b.HomeSessionID, RunID: runID, TurnID: runID, Sequence: int64(i + 1), Type: event.kind, Payload: payload}}}}
				if event.kind == "user_message" {
					row.Author = "user"
					row.Actions = session.EventActions{}
					row.LLMResponse = model.LLMResponse{Content: genai.NewContentFromText("Compare public mixed evidence", "user")}
				}
				if err = sessions.AppendFleetEvent(f.ctx, f.b.HomeSessionID.String(), row); err != nil {
					t.Fatal(err)
				}
			}
			publishedRow := &session.Event{ID: publication.EventID, InvocationID: "bot-report:" + publication.RunID.String(), Author: "assistant", Timestamp: time.Now(), LLMResponse: model.LLMResponse{Content: genai.NewContentFromText(publication.Summary, "model")}, Actions: session.EventActions{StateDelta: map[string]any{store.BotFilePublicationKey: publication}}}
			if err := sessions.AppendFleetEvent(f.ctx, f.b.HomeSessionID.String(), publishedRow); err != nil {
				t.Fatal(err)
			}
			page.MustElement(`[data-publication-id="` + publication.EventID + `"]`).MustVisible()
			page.MustElementR("p", "Mixed presentation complete").MustVisible()
			assertMixed := func(mode string) {
				t.Helper()
				page.MustElementR("button", "Explored 1 file").MustClick()
				text := page.MustElement(`[aria-label="Bot workspace"]`).MustText()
				for _, marker := range []string{"Compare public mixed evidence", "Mixed public acknowledgement", "read_file", "Public mixed receipt", "Mixed research", "Mixed verification plan", "I can’t confirm the latest outcome right now.", "Mixed presentation complete"} {
					if !strings.Contains(text, marker) {
						t.Errorf("%s missing public presentation %q: %s", mode, marker, text)
					}
				}
				if strings.Contains(text, "PRIVATE_MIXED_CHECKPOINT") {
					t.Fatal("private payload leaked")
				}
				if strings.Count(text, "Mixed presentation complete") != 1 {
					t.Errorf("%s duplicated final: %s", mode, text)
				}
			}
			assertMixed("live")
			liveDelivery := page.MustElement(`[data-publication-id="` + publication.EventID + `"]`)
			liveDelivery.MustElement(`[aria-label="Open in Files panel"]`).MustClick()
			page.MustElementR("h1", "^Reviewed mixed deliverable$").MustVisible()
			page.MustElementR("button", "^Close preview$").MustClick()
			if got := input.MustProperty("value").Str(); got != "Retain mixed draft during reconnect" {
				t.Fatalf("event stream replaced composer draft: %q", got)
			}
			page.MustReload()
			page.MustElementR("p", "Mixed presentation complete").MustVisible()
			assertMixed("restore")
			delivery := page.MustElement(`[data-publication-id="` + publication.EventID + `"]`)
			if len(page.MustElements(`[data-artifact-id="`+publication.Artifacts[0].ID.String()+`"]`)) != 1 {
				t.Fatal("reviewed delivery duplicated on restore")
			}
			delivery.MustElement(`[aria-label="Open in Files panel"]`).MustClick()
			page.MustElementR("h1", "^Reviewed mixed deliverable$").MustVisible()
			delivery.MustElementR("button", "^Download$").MustClick()
			page.MustElementR("button", "^Download as Markdown$").MustClick()
			var downloaded []byte
			deadline := time.Now().Add(10 * time.Second)
			for time.Now().Before(deadline) {
				downloaded, err = os.ReadFile(filepath.Join(downloads, "mixed-reviewed.md"))
				if err == nil && bytes.Equal(downloaded, reviewed) {
					break
				}
				time.Sleep(50 * time.Millisecond)
			}
			if !bytes.Equal(downloaded, reviewed) {
				t.Fatalf("reviewed native download mismatch: %d bytes %v", len(downloaded), err)
			}
			t.Logf("reviewed mixed preview and native download bytes=%d sha256=%x", len(downloaded), sha256.Sum256(downloaded))
			page.MustElementR("button", "^Close preview$").MustClick()
			artifactDir := "../../.tmp/studio-conversation-artifacts"
			if err := os.MkdirAll(artifactDir, 0755); err != nil {
				t.Fatal(err)
			}
			page.MustElement(`[aria-label="Bot workspace"]`).MustScreenshot(filepath.Join(artifactDir, string(scope)+".png"))
			rows, err := f.repo.ListRuns(f.ctx, f.b.ID, store.BotPage{})
			if err != nil || len(rows) != 0 {
				t.Fatalf("view/reload admitted work: %+v %v", rows, err)
			}
			current, err := f.repo.Get(f.ctx, f.b.ID)
			if err != nil || current.Status != store.BotDraft {
				t.Fatalf("view/reload changed lifecycle: %+v %v", current, err)
			}
			t.Logf("built Studio scope=%s endpoint=%s isolated_profile=true live/restore mixed public frames; no run admission", scope, server.URL)
		})
	}
}
