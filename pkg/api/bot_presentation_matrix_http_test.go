package api

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
	"google.golang.org/adk/model"
	"google.golang.org/adk/session"
	"google.golang.org/genai"
)

// This fixture stores canonical ADK/runtime rows through AppendEvent, then serves
// their persisted positions through the production HTTP handlers. No raw event
// view, model, renderer, Home provisioning or execution admission is installed.
type presentationMatrixSessions struct {
	*botSessionMetaFixture
	session.Service
	current     session.Session
	unavailable atomic.Bool
	writes      atomic.Int64
}

func (s *presentationMatrixSessions) AppendEvent(ctx context.Context, current session.Session, event *session.Event) error {
	if err := s.Service.AppendEvent(ctx, current, event); err != nil {
		return err
	}
	s.writes.Add(1)
	return nil
}

func (s *presentationMatrixSessions) Get(ctx context.Context, q *session.GetRequest) (*session.GetResponse, error) {
	if s.unavailable.Load() {
		return nil, store.ErrBotUnsupported
	}
	return s.Service.Get(ctx, q)
}
func (s *presentationMatrixSessions) ReadBotTranscript(ctx context.Context, sid uuid.UUID, after int64, limit int) ([]store.BotTranscriptEvent, error) {
	if s.unavailable.Load() {
		return nil, store.ErrBotUnsupported
	}
	if sid.String() != s.current.ID() {
		return nil, store.ErrBotNotFound
	}
	got, err := s.Service.Get(ctx, &session.GetRequest{AppName: s.current.AppName(), UserID: s.current.UserID(), SessionID: s.current.ID()})
	if err != nil {
		return nil, err
	}
	var out []store.BotTranscriptEvent
	for i := int(after); i < got.Session.Events().Len() && len(out) < limit; i++ {
		out = append(out, store.BotTranscriptEvent{Cursor: int64(i + 1), Event: got.Session.Events().At(i)})
	}
	return out, nil
}

type presentationMatrixRepo struct {
	*filePublicationRepo
	revoked atomic.Bool
}

func (r *presentationMatrixRepo) Get(ctx context.Context, id store.BotID) (*store.Bot, error) {
	if r.revoked.Load() {
		return nil, store.ErrBotForbidden
	}
	if id != r.b.ID {
		return nil, store.ErrBotNotFound
	}
	return r.filePublicationRepo.Get(ctx, id)
}

type presentationMatrixFixture struct {
	server              *httptest.Server
	repo                *presentationMatrixRepo
	ss                  *presentationMatrixSessions
	bid, sid, run, turn uuid.UUID
	scope               store.BotScope
	rows                []*session.Event
	delegation          []map[string]any
	plan                map[string]any
}

func newPresentationMatrixFixture(t *testing.T, scope store.BotScope, home bool) *presentationMatrixFixture {
	t.Helper()
	owner, viewer, bid, sid, run, turn := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	if scope == store.BotScopePersonal {
		viewer = owner
	}
	homeID := uuid.New()
	purpose := store.SessionPurposeBotRun
	if home {
		homeID = sid
		purpose = store.SessionPurposeBotHome
	}
	meta := &store.SessionMeta{ID: sid.String(), AppName: studioChatAppName, UserID: owner.String(), BotID: bid.String(), Purpose: purpose, Retention: store.SessionRetentionBot}
	if !home {
		meta.BotRunID = run.String()
	}
	ss := &presentationMatrixSessions{botSessionMetaFixture: &botSessionMetaFixture{meta: meta}, Service: session.InMemoryService()}
	created, err := ss.Create(t.Context(), &session.CreateRequest{AppName: studioChatAppName, UserID: owner.String(), SessionID: sid.String()})
	if err != nil {
		t.Fatal(err)
	}
	ss.current = created.Session
	p := &store.BotFilePublication{SchemaVersion: store.BotFilePublicationSchemaVersion, BotID: bid, RunID: run, HomeSessionID: sid, HomeID: uuid.New(), ReportSequence: 19, EventID: store.BotPublicationEventID(run, 19), Summary: "Reviewed report ready.", Artifacts: []store.BotPublishedArtifact{{ID: uuid.New(), RelativePath: "reports/reviewed.md", SHA256: strings.Repeat("a", 64), SizeBytes: 55, MediaType: "text/markdown", Title: "Reviewed report", Preview: store.BotFilePreviewText}}}
	p.Artifacts[0].HomeID = p.HomeID
	repo := &presentationMatrixRepo{filePublicationRepo: &filePublicationRepo{botSessionFixtureStore: &botSessionFixtureStore{b: store.Bot{ID: bid, OwnerID: owner, Scope: scope, HomeSessionID: homeID}, run: store.BotRun{ID: run, BotID: bid, SessionID: sid}}, publication: p}}
	f := &presentationMatrixFixture{repo: repo, ss: ss, bid: bid, sid: sid, run: run, turn: turn, scope: scope}
	text := func(id, s string, partial bool) *session.Event {
		return &session.Event{ID: id, InvocationID: turn.String(), Author: "assistant", Timestamp: time.Unix(100, 0), LLMResponse: model.LLMResponse{Partial: partial, Content: genai.NewContentFromText(s, "model")}}
	}
	runtime := func(id, kind string, data map[string]any) *session.Event {
		data["private_checkpoint"] = "PRIVATE_MATRIX_SECRET"
		raw, err := json.Marshal(data)
		if err != nil {
			t.Fatal(err)
		}
		return &session.Event{ID: id, InvocationID: turn.String(), Author: "bot_runtime", Timestamp: time.Unix(100, 0), Actions: session.EventActions{StateDelta: map[string]any{"bot_runtime_event": bot.RuntimeEvent{SessionID: sid, RunID: run, TurnID: turn, Sequence: int64(len(f.rows) + 1), Type: kind, Payload: raw}}}}
	}
	add := func(e *session.Event) { f.rows = append(f.rows, e) }
	add(runtime("partial", "text", map[string]any{"text": "Starting "}))
	add(runtime("partial-next", "text", map[string]any{"text": "research."}))
	add(&session.Event{ID: "calls", InvocationID: turn.String(), Author: "assistant", LLMResponse: model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{ID: "read-a", Name: "read_file", Args: map[string]any{"path": "a.md"}}}, {FunctionCall: &genai.FunctionCall{ID: "read-b", Name: "read_file", Args: map[string]any{"path": "b.md"}}}}}}})
	add(text("between", "While both reads are running.", false))
	add(runtime("result-b", "tool_result", map[string]any{"id": "read-b", "name": "read_file", "result": "B result"}))
	add(text("same-one", "same", false))
	add(runtime("result-a", "tool_result", map[string]any{"id": "read-a", "name": "read_file", "result": "A result"}))
	add(text("same-two", "same", false))
	batch, task, child := uuid.NewString(), uuid.NewString(), uuid.NewString()
	for i, kind := range []string{"delegation_start", "task_start", "task_complete", "delegation_complete"} {
		status := "running"
		if kind == "task_complete" || kind == "delegation_complete" {
			status = "complete"
		}
		payload := map[string]any{"event_type": kind, "batch_id": batch, "task_id": task, "child_session_id": child, "task_name": "Research", "attempt": 1, "sequence": i + 1, "status": status}
		f.delegation = append(f.delegation, matrixJSONMap(t, payload))
		add(runtime(fmt.Sprintf("delegate-%d", i), "subtask_progress", payload))
	}
	snapshot := map[string]any{"plan_id": "matrix-plan", "goal": "Review findings", "revision": 1, "sequence": 1, "document": map[string]any{"lifecycle": "approved"}, "steps": []any{map[string]any{"name": "review", "status": "running"}}}
	var expectedSnapshot agent.PlanSnapshot
	rawSnapshot, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(rawSnapshot, &expectedSnapshot); err != nil {
		t.Fatal(err)
	}
	f.plan = matrixJSONMap(t, map[string]any{"event_type": "plan_snapshot", "snapshot": expectedSnapshot, "plan_id": "matrix-plan", "revision": 1, "sequence": 1})
	add(&session.Event{ID: "plan", Author: "system", Actions: session.EventActions{StateDelta: map[string]any{agent.PlanSnapshotStateKey: snapshot, "private_checkpoint": "PRIVATE_MATRIX_SECRET"}}})
	add(runtime("recoverable-error", "error", map[string]any{"error": "One optional source was unavailable."}))
	add(&session.Event{ID: "private", Author: "bot_runtime", Actions: session.EventActions{StateDelta: map[string]any{"private_checkpoint": "PRIVATE_MATRIX_SECRET"}}})
	add(&session.Event{ID: "unreviewed", Author: "bot_runtime", Actions: session.EventActions{StateDelta: map[string]any{"bot_unreviewed_report": "UNREVIEWED_MATRIX_SECRET"}}})
	add(&session.Event{ID: p.EventID, InvocationID: "bot-report:" + run.String(), Author: "assistant", LLMResponse: model.LLMResponse{Content: genai.NewContentFromText(p.Summary, "model")}, Actions: session.EventActions{StateDelta: map[string]any{store.BotFilePublicationKey: p, "private_checkpoint": "PRIVATE_MATRIX_SECRET"}}})
	svc := &store.Services{}
	if scope == store.BotScopePersonal {
		svc.PersonalBots = repo
		svc.PersonalSessions = ss
	} else {
		svc.Bots = repo
		svc.Sessions = ss
	}
	router := mux.NewRouter()
	registerBotRoutes(router)
	router.HandleFunc("/api/studio/sessions/{id}", StudioSessionHandler).Methods("GET")
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, err := execution.WithPrincipal(r.Context(), execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: viewer.String(), OrgSlug: "matrix-org", TeamSlug: "matrix-team", Authenticated: true})
		if err != nil {
			panic(err)
		}
		ctx = store.WithTenantContext(ctx, &store.TenantContext{UserID: viewer.String(), OrgSlug: "matrix-org", TeamSlug: "matrix-team"})
		ctx = store.WithServices(ctx, svc)
		router.ServeHTTP(w, r.WithContext(ctx))
	}))
	f.server.Client().Timeout = 5 * time.Second
	t.Cleanup(f.server.Close)
	return f
}
func (f *presentationMatrixFixture) append(t *testing.T, rows []*session.Event) {
	t.Helper()
	for _, e := range rows {
		if err := f.ss.AppendEvent(t.Context(), f.ss.current, e); err != nil {
			t.Fatal(err)
		}
	}
}
func (f *presentationMatrixFixture) get(t *testing.T, route, after string, status int) *http.Response {
	t.Helper()
	req, err := http.NewRequest("GET", f.server.URL+route, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Last-Event-ID", after)
	writes := f.ss.writes.Load()
	res, err := f.server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if f.ss.writes.Load() != writes {
		res.Body.Close()
		t.Fatalf("GET %s appended execution state", route)
	}
	if res.StatusCode != status {
		res.Body.Close()
		t.Fatalf("%s: status %d want %d", route, res.StatusCode, status)
	}
	return res
}
func (f *presentationMatrixFixture) route(suffix string) string {
	return "/api/studio/bots/" + f.bid.String() + "/sessions/" + f.sid.String() + suffix + "?scope=" + string(f.scope)
}
func matrixJSONMap(t *testing.T, value any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func matrixReadUntil(t *testing.T, rd *bufio.Reader, terminal string) []botSSEFrame {
	t.Helper()
	var frames []botSSEFrame
	for {
		frame := readBotSSE(t, rd)
		if frame.event == terminal {
			return frames
		}
		frames = append(frames, frame)
	}
}
func matrixAssertSafe(t *testing.T, raw string) {
	t.Helper()
	for _, secret := range []string{"PRIVATE_MATRIX_SECRET", "UNREVIEWED_MATRIX_SECRET", "private_checkpoint", "bot_unreviewed_report"} {
		if strings.Contains(raw, secret) {
			t.Fatalf("private/unreviewed content leaked: %s", secret)
		}
	}
}
func TestBotPresentationMatrixHTTPAcceptance(t *testing.T) {
	runBotPresentationMatrixHTTPAcceptance(t, false)
}

// Home exercises the same mixed sequence through the Home-bound session store,
// including writes after the live subscription and durable multipart reconnects.
func TestBotHomePresentationMatrixHTTPAcceptance(t *testing.T) {
	runBotPresentationMatrixHTTPAcceptance(t, true)
}

func runBotPresentationMatrixHTTPAcceptance(t *testing.T, home bool) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			f := newPresentationMatrixFixture(t, scope, home)
			t.Cleanup(func() {
				if got := f.ss.writes.Load(); got != int64(len(f.rows)) {
					t.Errorf("read-only HTTP matrix changed transcript: %d writes want %d fixture appends", got, len(f.rows))
				}
			})
			t.Run("live", func(t *testing.T) {
				live := f.get(t, f.route("/events"), "", 200)
				defer live.Body.Close()
				rd := bufio.NewReader(live.Body)
				if frame := readBotSSE(t, rd); frame.event != botReplayComplete {
					t.Fatal(frame)
				}
				// Append after subscription: this is delta delivery, not just initial replay.
				f.append(t, f.rows)
				var observed []botSSEFrame
				for len(observed) < 18 {
					observed = append(observed, readBotSSE(t, rd))
				}
				history := f.get(t, f.route("/stream"), "", 200)
				defer history.Body.Close()
				want := matrixReadUntil(t, bufio.NewReader(history.Body), botTranscriptDone)
				if !reflect.DeepEqual(observed, want) {
					t.Fatalf("live/history changed ordered frames: got %d want %d", len(observed), len(want))
				}
			})
			if f.ss.current.Events().Len() == 0 {
				f.append(t, f.rows)
			}
			t.Run("history", func(t *testing.T) {
				res := f.get(t, f.route("/stream"), "", 200)
				defer res.Body.Close()
				frames := matrixReadUntil(t, bufio.NewReader(res.Body), botTranscriptDone)
				kinds := []string{"text", "text", "tool_call", "tool_call", "text", "tool_result", "text", "tool_result", "text", "subtask_progress", "subtask_progress", "subtask_progress", "subtask_progress", "subtask_progress", "error", "cursor", "cursor", store.BotFilePublicationKey}
				if len(frames) != len(kinds) {
					t.Fatalf("lost mixed frames: %d want %d", len(frames), len(kinds))
				}
				exact := map[int]map[string]any{
					0: {"text": "Starting "}, 1: {"text": "research."},
					2: {"id": "read-a", "name": "read_file", "args": map[string]any{"path": "a.md"}},
					3: {"id": "read-b", "name": "read_file", "args": map[string]any{"path": "b.md"}},
					4: {"text": "While both reads are running.", "invocationId": f.turn.String()},
					5: {"id": "read-b", "name": "read_file", "result": "B result"},
					6: {"text": "same", "invocationId": f.turn.String()},
					7: {"id": "read-a", "name": "read_file", "result": "A result"},
					8: {"text": "same", "invocationId": f.turn.String()},
					9: f.delegation[0], 10: f.delegation[1], 11: f.delegation[2], 12: f.delegation[3],
					13: f.plan,
					// Runtime prose has no safe provenance, even when it sounds benign.
					14: {"error": publicChatFailure},
					15: {}, 16: {}, 17: matrixJSONMap(t, f.repo.publication),
				}
				if home {
					// Home runtime narration retains turn provenance; run-local text
					// already carries its turn in projection metadata.
					exact[0]["invocationId"] = f.turn.String()
					exact[1]["invocationId"] = f.turn.String()
				}
				seen := map[string]bool{}
				rowIDs := []string{"partial", "partial-next", "calls", "calls", "between", "result-b", "same-one", "result-a", "same-two", "delegate-0", "delegate-1", "delegate-2", "delegate-3", "plan", "recoverable-error", "private", "unreviewed", f.repo.publication.EventID}
				cursors := []int64{1, 2, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17}
				for i, frame := range frames {
					matrixAssertSafe(t, frame.data)
					if frame.event != kinds[i] {
						t.Fatalf("frame %d: %s want %s", i, frame.event, kinds[i])
					}
					var data map[string]any
					if err := json.Unmarshal([]byte(frame.data), &data); err != nil {
						t.Fatal(err)
					}
					meta := data["_projection"].(map[string]any)
					if meta["session_id"] != f.sid.String() || meta["cursor"] != botEventCursor(f.sid, cursors[i]) || frame.id != meta["cursor"] {
						t.Fatalf("frame %d transport provenance changed: %+v", i, meta)
					}
					if kinds[i] != botCursorEvent {
						part := 0
						if i == 3 {
							part = 1
						}
						identity := fmt.Sprintf("%s/%s/%d", f.sid, rowIDs[i], part)
						if meta["event_id"] != identity {
							t.Fatalf("frame %d identity %v want %s", i, meta["event_id"], identity)
						}
					}
					delete(data, "_projection")
					if expected, ok := exact[i]; ok && !reflect.DeepEqual(data, expected) {
						t.Fatalf("frame %d payload: %+v want %+v", i, data, expected)
					}
					if id, ok := meta["event_id"].(string); ok {
						if seen[id] {
							t.Fatal("identity collapsed", id)
						}
						seen[id] = true
					}
				}
				for _, i := range []int{9, 10, 11, 12} {
					if !strings.Contains(frames[i].data, "child_session_id") || !strings.Contains(frames[i].data, "batch_id") || !strings.Contains(frames[i].data, "task_id") {
						t.Fatal("delegation identity lost", frames[i])
					}
				}
				if !strings.Contains(frames[17].data, f.repo.publication.Artifacts[0].ID.String()) {
					t.Fatal("reviewed artifact delivery lost")
				}
			})
			t.Run("reconnect", func(t *testing.T) {
				res := f.get(t, f.route("/stream"), "", 200)
				want := matrixReadUntil(t, bufio.NewReader(res.Body), botTranscriptDone)
				res.Body.Close()
				// Reconnect in the middle of a multipart row must replay BOTH calls with
				// unchanged identities, because its first part cannot commit the row cursor.
				after := want[2].id
				live := f.get(t, f.route("/events"), after, 200)
				defer live.Body.Close()
				got := matrixReadUntil(t, bufio.NewReader(live.Body), botReplayComplete)
				if !reflect.DeepEqual(got, want[2:]) {
					t.Fatal("mid-row reconnect dropped/reidentified a concurrent call")
				}
			})
			t.Run("revoked", func(t *testing.T) {
				f.repo.revoked.Store(true)
				defer f.repo.revoked.Store(false)
				for _, suffix := range []string{"/stream", "/events"} {
					res := f.get(t, f.route(suffix), "", 404)
					res.Body.Close()
				}
			})
		})
	}
}
