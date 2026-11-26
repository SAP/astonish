package api

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/entstore"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
	"google.golang.org/adk/model"
	adksession "google.golang.org/adk/session"
	"google.golang.org/genai"
)

func TestBotPresentationApprovalsEmptyPollingDoesNotHydrate(t *testing.T) {
	repo, ss := presentationReviewFixture(t)
	ss.rows = nil
	ctx, err := execution.WithPrincipal(t.Context(), execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: repo.b.OwnerID.String(), OrgSlug: "org", TeamSlug: "team", Authenticated: true})
	if err != nil {
		t.Fatal(err)
	}
	ctx = store.WithTenantContext(ctx, &store.TenantContext{UserID: repo.b.OwnerID.String(), OrgSlug: "org", TeamSlug: "team"})
	ctx = store.WithServices(ctx, &store.Services{PersonalBots: repo, PersonalSessions: ss})
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	w := &emptyApprovalReplayWriter{ResponseRecorder: httptest.NewRecorder(), cancel: cancel}
	r := mux.SetURLVars(httptest.NewRequest("GET", "/?scope=personal", nil).WithContext(ctx), map[string]string{"botID": repo.b.ID.String(), "sessionID": repo.b.HomeSessionID.String()})
	BotEventsHandler(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "replay_complete") || len(repo.pages) != 0 {
		t.Fatalf("empty replay hydrated approvals: %d %s pages=%v", w.Code, w.Body.String(), repo.pages)
	}
}

type emptyApprovalReplayWriter struct {
	*httptest.ResponseRecorder
	cancel context.CancelFunc
}

func (w *emptyApprovalReplayWriter) Flush() { w.ResponseRecorder.Flush(); w.cancel() }

func TestBotEventsReviewsBeyondFirstEight(t *testing.T) {
	testPresentationReviewStream(t, false, false)
}
func TestBotEventsReconnectReviewsBeyondFirstEight(t *testing.T) {
	testPresentationReviewStream(t, false, true)
}

func testPresentationReviewStream(t *testing.T, history, reconnect bool) {
	t.Helper()
	repo, ss := presentationReviewFixture(t)
	ctx, err := execution.WithPrincipal(t.Context(), execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: repo.b.OwnerID.String(), OrgSlug: "org", TeamSlug: "team", Authenticated: true})
	if err != nil {
		t.Fatal(err)
	}
	ctx = store.WithTenantContext(ctx, &store.TenantContext{UserID: repo.b.OwnerID.String(), OrgSlug: "org", TeamSlug: "team"})
	ctx = store.WithServices(ctx, &store.Services{PersonalBots: repo, PersonalSessions: ss})
	if reconnect {
		ss.rows[0].Cursor = 2
	}
	router := mux.NewRouter()
	registerBotRoutes(router)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bound, cancel := context.WithCancel(ctx)
		stop := context.AfterFunc(r.Context(), cancel)
		defer stop()
		defer cancel()
		router.ServeHTTP(w, r.WithContext(bound))
	}))
	defer server.Close()
	client := server.Client()
	client.Timeout = 3 * time.Second
	endpoint := "events"
	if history {
		endpoint = "stream"
	}
	req, _ := http.NewRequest("GET", server.URL+"/api/studio/bots/"+repo.b.ID.String()+"/sessions/"+repo.b.HomeSessionID.String()+"/"+endpoint+"?scope=personal", nil)
	if reconnect {
		req.Header.Set("Last-Event-ID", botEventCursor(repo.b.HomeSessionID, 1))
	}
	res, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatal(res.Status)
	}
	rd := bufio.NewReader(res.Body)
	frame := readBotSSE(t, rd)
	if frame.event != "bot_review" || !strings.Contains(frame.data, "Late saved question?") {
		t.Fatalf("missing late authoritative review: %+v", frame)
	}
	frame = readBotSSE(t, rd)
	want := botReplayComplete
	if history {
		want = botTranscriptDone
	}
	if frame.event != want {
		t.Fatalf("missing replay boundary: %+v", frame)
	}
}

type botReplayFixture struct {
	*botSessionMetaFixture
	read func() ([]store.BotTranscriptEvent, error)
}

func (s *botReplayFixture) ReadBotTranscript(context.Context, uuid.UUID, int64, int) ([]store.BotTranscriptEvent, error) {
	return s.read()
}

type delayedReconnectRows struct {
	*botSessionMetaFixture
	rows    []store.BotTranscriptEvent
	started chan struct{}
	release chan struct{}
}

func (s *delayedReconnectRows) ReadBotTranscript(ctx context.Context, _ uuid.UUID, after int64, _ int) ([]store.BotTranscriptEvent, error) {
	if after > 0 {
		select {
		case s.started <- struct{}{}:
		default:
		}
		select {
		case <-s.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	var out []store.BotTranscriptEvent
	for _, row := range s.rows {
		if row.Cursor > after {
			out = append(out, row)
		}
	}
	return out, nil
}

func TestBotEventsReconnectFlushesBeforeRetainedDeltaHydration(t *testing.T) {
	owner, bid, sid := uuid.New(), uuid.New(), uuid.New()
	repo := &botSessionFixtureStore{b: store.Bot{ID: bid, OwnerID: owner, Scope: store.BotScopePersonal, HomeSessionID: sid}}
	ss := &delayedReconnectRows{botSessionMetaFixture: &botSessionMetaFixture{meta: &store.SessionMeta{ID: sid.String(), UserID: owner.String(), BotID: bid.String(), Purpose: store.SessionPurposeBotHome, Retention: store.SessionRetentionBot}}, started: make(chan struct{}, 1), release: make(chan struct{})}
	ctx, err := execution.WithPrincipal(context.Background(), execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: owner.String(), OrgSlug: "org", TeamSlug: "team", Authenticated: true})
	if err != nil {
		t.Fatal(err)
	}
	ctx = store.WithTenantContext(ctx, &store.TenantContext{UserID: owner.String(), OrgSlug: "org", TeamSlug: "team"})
	ctx = store.WithServices(ctx, &store.Services{PersonalBots: repo, PersonalSessions: ss})
	router := mux.NewRouter()
	registerBotRoutes(router)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bound, cancel := context.WithCancel(ctx)
		stop := context.AfterFunc(r.Context(), cancel)
		defer stop()
		defer cancel()
		router.ServeHTTP(w, r.WithContext(bound))
	}))
	defer server.Close()
	requestCtx, cancelRequest := context.WithCancel(context.Background())
	defer cancelRequest()
	req, _ := http.NewRequestWithContext(requestCtx, "GET", server.URL+"/api/studio/bots/"+bid.String()+"/sessions/"+sid.String()+"/events?scope=personal", nil)
	req.Header.Set("Last-Event-ID", botEventCursor(sid, 42))
	response := make(chan *http.Response, 1)
	fail := make(chan error, 1)
	go func() {
		res, requestErr := server.Client().Do(req)
		if requestErr != nil {
			fail <- requestErr
			return
		}
		response <- res
	}()
	select {
	case <-ss.started:
	case <-time.After(time.Second):
		t.Fatal("retained delta read did not start")
	}
	select {
	case err := <-fail:
		t.Fatal(err)
	case res := <-response:
		if res.StatusCode != http.StatusOK || res.Header.Get("Content-Type") != "text/event-stream" {
			res.Body.Close()
			t.Fatalf("reconnect did not attach: %s %v", res.Status, res.Header)
		}
		cancelRequest()
		res.Body.Close()
	case <-time.After(time.Second):
		t.Fatal("SSE headers waited for retained delta hydration")
	}
	close(ss.release)
}

// Revocation after a persisted read must be checked before any payload is sent.
func TestBotEventsReplayRevocationAndStorageFailure(t *testing.T) {
	for _, revoke := range []bool{true, false} {
		t.Run(fmt.Sprintf("revoke=%t", revoke), func(t *testing.T) {
			owner, viewer, id, sid := uuid.New(), uuid.New(), uuid.New(), uuid.New()
			repo := &botSessionFixtureStore{b: store.Bot{ID: id, OwnerID: owner, Scope: store.BotScopeTeam, HomeSessionID: sid}}
			ss := &botReplayFixture{botSessionMetaFixture: &botSessionMetaFixture{meta: &store.SessionMeta{ID: sid.String(), UserID: owner.String(), BotID: id.String(), Purpose: store.SessionPurposeBotHome, Retention: store.SessionRetentionBot}}}
			ss.read = func() ([]store.BotTranscriptEvent, error) {
				if revoke {
					repo.denied = true
					return []store.BotTranscriptEvent{{Cursor: 1, Event: &adksession.Event{ID: "revoked-secret"}}}, nil
				}
				return nil, errors.New("storage unavailable")
			}
			ctx, err := execution.WithPrincipal(context.Background(), execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: viewer.String(), OrgSlug: "org", TeamSlug: "team", Authenticated: true})
			if err != nil {
				t.Fatal(err)
			}
			ctx = store.WithTenantContext(ctx, &store.TenantContext{UserID: viewer.String(), OrgSlug: "org", TeamSlug: "team"})
			ctx = store.WithServices(ctx, &store.Services{Bots: repo, Sessions: ss})
			router := mux.NewRouter()
			registerBotRoutes(router)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, httptest.NewRequest("GET", "/api/studio/bots/"+id.String()+"/sessions/"+sid.String()+"/events?scope=team", nil).WithContext(ctx))
			want := http.StatusInternalServerError
			if revoke {
				want = http.StatusNotFound
			}
			if w.Code != want || strings.Contains(w.Body.String(), "revoked-secret") || strings.Contains(w.Body.String(), "event: transcript") {
				t.Fatalf("unsafe replay response: %d %s", w.Code, w.Body.String())
			}
		})
	}
}

type canonicalPresentationRows struct {
	*botSessionMetaFixture
	rows []store.BotTranscriptEvent
}

func (s *canonicalPresentationRows) ReadBotTranscript(_ context.Context, _ uuid.UUID, after int64, _ int) ([]store.BotTranscriptEvent, error) {
	var rows []store.BotTranscriptEvent
	for _, row := range s.rows {
		if row.Cursor > after {
			rows = append(rows, row)
		}
	}
	return rows, nil
}

func TestBotCanonicalPresentationHTTPFields(t *testing.T) {
	owner, bid, sid, home, run, turn := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	repo := &botSessionFixtureStore{b: store.Bot{ID: bid, OwnerID: owner, Scope: store.BotScopePersonal, HomeSessionID: home}, run: store.BotRun{ID: run, BotID: bid, SessionID: sid}}
	ss := &canonicalPresentationRows{botSessionMetaFixture: &botSessionMetaFixture{meta: &store.SessionMeta{ID: sid.String(), UserID: owner.String(), BotID: bid.String(), BotRunID: run.String(), Purpose: store.SessionPurposeBotRun, Retention: store.SessionRetentionBot}}}
	payloads := []struct {
		kind string
		data map[string]any
	}{
		{"artifact", map[string]any{"path": "/report.md", "fileName": "report.md", "fileType": "Markdown", "toolName": "write_file", "isReport": true, "reportTitle": "Findings"}},
		{"report_marker", map[string]any{"path": "/report.md", "title": "Findings"}},
		{"subtask_progress", map[string]any{"event_type": "delegation_start", "batch_id": "batch", "task_name": "Research"}},
	}
	for i, p := range payloads {
		raw, _ := json.Marshal(p.data)
		ss.rows = append(ss.rows, store.BotTranscriptEvent{Cursor: int64(i + 1), Event: &adksession.Event{ID: fmt.Sprint("row-", i), Author: "bot_runtime", Actions: adksession.EventActions{StateDelta: map[string]any{"bot_runtime_event": bot.RuntimeEvent{SessionID: sid, RunID: run, TurnID: turn, Sequence: int64(i + 1), Type: p.kind, Payload: raw}}}}})
	}
	ctx, err := execution.WithPrincipal(t.Context(), execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: owner.String(), OrgSlug: "org", TeamSlug: "team", Authenticated: true})
	if err != nil {
		t.Fatal(err)
	}
	ctx = store.WithTenantContext(ctx, &store.TenantContext{UserID: owner.String(), OrgSlug: "org", TeamSlug: "team"})
	ctx = store.WithServices(ctx, &store.Services{PersonalBots: repo, PersonalSessions: ss})
	router := mux.NewRouter()
	registerBotRoutes(router)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bound, cancel := context.WithCancel(ctx)
		stop := context.AfterFunc(r.Context(), cancel)
		defer stop()
		defer cancel()
		router.ServeHTTP(w, r.WithContext(bound))
	}))
	defer server.Close()
	client := server.Client()
	client.Timeout = 5 * time.Second
	res, err := client.Get(server.URL + "/api/studio/bots/" + bid.String() + "/sessions/" + sid.String() + "/stream?scope=personal")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatal(res.Status)
	}
	readPresentation := func(rd *bufio.Reader, start int) []botSSEFrame {
		t.Helper()
		var frames []botSSEFrame
		for i := start; i < len(payloads); i++ {
			p := payloads[i]
			frame := readBotSSE(t, rd)
			var data map[string]any
			if json.Unmarshal([]byte(frame.data), &data) != nil || frame.event != p.kind || frame.id != botEventCursor(sid, int64(i+1)) {
				t.Fatalf("unexpected frame %+v", frame)
			}
			for k, v := range p.data {
				if data[k] != v {
					t.Fatalf("lost %s/%s: %+v", p.kind, k, data)
				}
			}
			frames = append(frames, frame)
		}
		return frames
	}
	rd := bufio.NewReader(res.Body)
	history := readPresentation(rd, 0)
	if frame := readBotSSE(t, rd); frame.event != "done" {
		t.Fatal(frame)
	}
	// Live replay and reconnect must deliver the same payloads and identities,
	// including renderer metadata that used to be stripped by the allowlist.
	for _, start := range []int{0, 1} {
		req, err := http.NewRequest("GET", server.URL+"/api/studio/bots/"+bid.String()+"/sessions/"+sid.String()+"/events?scope=personal", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Last-Event-ID", botEventCursor(sid, int64(start)))
		live, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		reader := bufio.NewReader(live.Body)
		frames := readPresentation(reader, start)
		for i, frame := range frames {
			if frame != history[start+i] {
				t.Fatalf("live/history diverged: %+v != %+v", frame, history[start+i])
			}
		}
		if frame := readBotSSE(t, reader); frame.event != botReplayComplete || frame.id != botEventCursor(sid, int64(len(payloads))) {
			t.Fatal(frame)
		}
		live.Body.Close()
	}
}

type botSSEFrame struct{ id, event, data string }

func readBotSSE(t *testing.T, r *bufio.Reader) botSSEFrame {
	t.Helper()
	var frame botSSEFrame
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		line = strings.TrimSuffix(line, "\n")
		if line == "" {
			return frame
		}
		if strings.HasPrefix(line, "id: ") {
			frame.id = strings.TrimPrefix(line, "id: ")
		}
		if strings.HasPrefix(line, "event: ") {
			frame.event = strings.TrimPrefix(line, "event: ")
		}
		if strings.HasPrefix(line, "data: ") {
			frame.data = strings.TrimPrefix(line, "data: ")
		}
	}
}

// Real HTTP, production tenant middleware and SQLite. No runner, model provider
// or credentials are installed; an open stream must never start execution.
func TestBotEventsHTTPAcceptance(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	base, db, err := entstore.NewPlatformServices(ctx, entstore.Config{DSN: filepath.Join(dir, "platform.db"), DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	org := &store.Organization{ID: uuid.NewString(), Slug: "live-org", Name: "Live", Status: "active"}
	if err := db.Organizations().Create(ctx, org); err != nil {
		t.Fatal(err)
	}
	if err := db.ProvisionOrg(ctx, org.ID, org.Slug); err != nil {
		t.Fatal(err)
	}
	ods, err := db.ForOrg(org.Slug)
	if err != nil {
		t.Fatal(err)
	}
	team := &store.Team{ID: uuid.NewString(), Slug: "live-team", Name: "Live", SchemaName: "team_live-team"}
	if err := ods.Teams().CreateTeam(ctx, team); err != nil {
		t.Fatal(err)
	}
	owner, viewer := uuid.NewString(), uuid.NewString()
	for _, id := range []string{owner, viewer} {
		if err := db.Users().Create(ctx, &store.User{ID: id, Email: id + "@test.example", DisplayName: "Reader", Status: "active"}); err != nil {
			t.Fatal(err)
		}
		if err := db.Organizations().AddMember(ctx, id, org.ID, "member"); err != nil {
			t.Fatal(err)
		}
		if err := ods.Teams().AddMember(ctx, &store.TeamMembership{UserID: id, TeamID: team.ID, Role: "member"}); err != nil {
			t.Fatal(err)
		}
	}
	router := mux.NewRouter()
	RegisterRoutes(router, base, db, entstore.TenantMiddleware(db))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Test-Actor")
		c := r.Context()
		if id != "" {
			var err error
			c, err = execution.WithPrincipal(c, execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: id, OrgSlug: org.Slug, TeamSlug: team.Slug, Authenticated: true})
			if err != nil {
				http.Error(w, "fixture", 500)
				return
			}
			c = context.WithValue(c, platformUserKey{}, &PlatformUser{ID: id, OrgSlug: org.Slug, TeamSlug: team.Slug, Role: "member"})
			c = store.WithTenantContext(c, &store.TenantContext{UserID: id, OrgSlug: org.Slug, TeamSlug: team.Slug})
		}
		router.ServeHTTP(w, r.WithContext(c))
	}))
	defer server.Close()
	client := server.Client()
	client.Timeout = 8 * time.Second
	request := func(method, path, actor, body, cursor string, status int) *http.Response {
		t.Helper()
		req, err := http.NewRequest(method, server.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("X-Test-Actor", actor)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Last-Event-ID", cursor)
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		if res.StatusCode != status {
			data, _ := io.ReadAll(res.Body)
			res.Body.Close()
			t.Fatalf("%s: status=%d want=%d %s", path, res.StatusCode, status, data)
		}
		return res
	}
	for _, scope := range []string{"personal", "team"} {
		t.Run(scope, func(t *testing.T) {
			res := request("POST", "/api/studio/bots?scope="+scope, owner, `{"name":"Live","idempotency_key":"live","definition":{"charter":"Observe"}}`, "", 201)
			var b store.Bot
			err := json.NewDecoder(res.Body).Decode(&b)
			res.Body.Close()
			if err != nil {
				t.Fatal(err)
			}
			path := "/api/studio/bots/" + b.ID.String() + "/sessions/" + b.HomeSessionID.String() + "/events?scope=" + scope
			var ss store.SessionStore = ods.ForUser(owner).Sessions()
			actor := owner
			if scope == "team" {
				ss = ods.ForTeam(team.Slug).Sessions()
				actor = viewer
			}
			appendEvent := func(id string) {
				t.Helper()
				e := &adksession.Event{ID: id, Author: "execution-owner", Timestamp: time.Now(), LLMResponse: model.LLMResponse{Content: genai.NewContentFromText(id, "model")}}
				e.Actions.StateDelta = map[string]any{"private_checkpoint": "PRIVATE_CHECKPOINT_SECRET"}
				if id == "first" {
					e.Author = "bot_runtime"
					e.Content = nil
					e.Actions.StateDelta = map[string]any{"bot_runtime_event": map[string]any{"type": "tool_call", "payload": map[string]any{"name": "web_fetch", "private_checkpoint": "HISTORICAL_PRIVATE_CHECKPOINT", "args": map[string]any{"url": "https://historical.example.invalid/public"}}}}
				}
				if err := ss.AppendFleetEvent(ctx, b.HomeSessionID.String(), e); err != nil {
					t.Fatal(err)
				}
			}
			// Empty snapshot remains open, then catches up without a hub or new compute.
			live := request("GET", path, actor, "", "", 200)
			defer live.Body.Close()
			rd := bufio.NewReader(live.Body)
			if f := readBotSSE(t, rd); f.event != botReplayComplete {
				t.Fatalf("empty: %+v", f)
			}
			appendEvent("first")
			first := readBotSSE(t, rd)
			if first.event != "tool_call" || first.id != botEventCursor(b.HomeSessionID, 1) || !strings.Contains(first.data, "https://historical.example.invalid/public") || !strings.Contains(first.data, "web_fetch") || strings.Contains(first.data, "HISTORICAL_PRIVATE_CHECKPOINT") {
				t.Fatalf("live: %+v", first)
			}
			live.Body.Close()
			appendEvent("second")
			resumed := request("GET", path, actor, "", first.id, 200)
			defer resumed.Body.Close()
			rr := bufio.NewReader(resumed.Body)
			second := readBotSSE(t, rr)
			if second.id == first.id || !strings.Contains(second.data, "second") || strings.Contains(second.data, "PRIVATE_CHECKPOINT_SECRET") {
				t.Fatalf("resume: %+v", second)
			}
			if f := readBotSSE(t, rr); f.event != botReplayComplete {
				t.Fatal("duplicate replay", f)
			}
			appendEvent("third")
			if f := readBotSSE(t, rr); !strings.Contains(f.data, "third") {
				t.Fatal("replay/live race lost delta", f)
			}
			if getChatRunnerRegistry().Get(b.HomeSessionID.String()) != nil {
				t.Fatal("feed started compute")
			}
			for _, bad := range []string{"-1", uuid.NewString() + ":1", b.HomeSessionID.String() + ":-1"} {
				r := request("GET", path, actor, "", bad, 400)
				r.Body.Close()
			}
			r := request("GET", path, "", "", "", 401)
			r.Body.Close()
			if scope == "personal" {
				r := request("GET", path, viewer, "", "", 404)
				r.Body.Close()
			}
			if scope == "team" {
				// Revocation affects an already subscribed response, not just middleware.
				if err := ods.Teams().RemoveMember(ctx, viewer, team.ID); err != nil {
					t.Fatal(err)
				}
				appendEvent("revoked-secret")
				rest, err := io.ReadAll(rr)
				if err != nil {
					t.Fatalf("revoked stream did not close: %v", err)
				}
				if strings.Contains(string(rest), "revoked-secret") {
					t.Fatal("revoked viewer received event")
				}
				denied := request("GET", path, viewer, "", second.id, 404)
				denied.Body.Close()
			}
			resumed.Body.Close()
			// A full snapshot uses the same ordered source and IDs as live delivery.
			full := request("GET", path, owner, "", "", 200)
			fr := bufio.NewReader(full.Body)
			if f := readBotSSE(t, fr); f.id != first.id || f.event != "tool_call" || f.data != first.data {
				t.Fatal("snapshot differs", f)
			}
			full.Body.Close()

			// Finite restore carries identical sanitized rows; only transport IDs differ.
			history := request("GET", strings.Replace(path, "/events?", "/stream?", 1), owner, "", "", 200)
			hr := bufio.NewReader(history.Body)
			if restored := readBotSSE(t, hr); restored.data != first.data {
				t.Fatalf("first history/live mismatch: %s != %s", restored.data, first.data)
			}
			if restored := readBotSSE(t, hr); restored.data != second.data || strings.Contains(restored.data, "PRIVATE_CHECKPOINT_SECRET") {
				t.Fatalf("reconnected history/live mismatch: %s != %s", restored.data, second.data)
			}
			history.Body.Close()

			// Replay crosses a storage page without changing event identity/order.
			for i := 0; i < botEventPageSize+2; i++ {
				appendEvent(fmt.Sprintf("paged-%03d", i))
			}
			paged := request("GET", path, owner, "", "", 200)
			defer paged.Body.Close()
			pr := bufio.NewReader(paged.Body)
			var previous int64
			count := 0
			for {
				frame := readBotSSE(t, pr)
				position, err := parseBotEventCursor(b.HomeSessionID, frame.id)
				if err != nil {
					t.Fatal(err)
				}
				if frame.event == botReplayComplete {
					if position != previous {
						t.Fatal("replay marker advanced cursor")
					}
					break
				}
				if (frame.event != "text" && frame.event != "tool_call" && frame.event != botCursorEvent) || position <= previous {
					t.Fatalf("unordered replay: %+v after %d", frame, previous)
				}
				previous = position
				count++
			}
			want := 3 + botEventPageSize + 2
			if scope == "team" {
				want++ // The owner remains authorized for the post-revocation row.
			}
			if count != want {
				t.Fatalf("paged replay count=%d want=%d", count, want)
			}
			paged.Body.Close()
			queryReplay := request("GET", path+"&after="+first.id, owner, "", "", 200)
			if f := readBotSSE(t, bufio.NewReader(queryReplay.Body)); f.id != second.id {
				t.Fatal("query cursor ignored", f)
			}
			queryReplay.Body.Close()
			headerReplay := request("GET", path+"&after=invalid", owner, "", first.id, 200)
			if f := readBotSSE(t, bufio.NewReader(headerReplay.Body)); f.id != second.id {
				t.Fatal("header cursor did not take precedence", f)
			}
			headerReplay.Body.Close()

			// A complete model row fans out atomically, including its completion.
			answer := &adksession.Event{ID: "owner-answer", Author: "user", LLMResponse: model.LLMResponse{Content: genai.NewContentFromText(`{"type":"bot_owner_answer","display":{"answer":"Use the reviewed option"},"response":{"private":"not display"}}`, "user")}}
			complete := &adksession.Event{ID: "multipart", Author: "assistant", InvocationID: "turn-multipart", LLMResponse: model.LLMResponse{TurnComplete: true, Content: &genai.Content{Role: "model", Parts: []*genai.Part{{Text: "repeat"}, {Text: "repeat"}}}}}
			for _, event := range []*adksession.Event{answer, complete} {
				if err := ss.AppendFleetEvent(ctx, b.HomeSessionID.String(), event); err != nil {
					t.Fatal(err)
				}
			}
			multi := request("GET", path, owner, "", botEventCursor(b.HomeSessionID, previous), 200)
			mr := bufio.NewReader(multi.Body)
			var expected []botSSEFrame
			for i, kind := range []string{"user_message", "text", "text", "turn_complete"} {
				frame := readBotSSE(t, mr)
				wantCursor := previous + 1
				if i == 3 {
					wantCursor++
				}
				if frame.event != kind || frame.id != botEventCursor(b.HomeSessionID, wantCursor) {
					t.Fatalf("non-atomic multipart: %+v", frame)
				}
				if i == 0 && (!strings.Contains(frame.data, "Use the reviewed option") || strings.Contains(frame.data, "not display") || strings.Contains(frame.data, "bot_owner_answer")) {
					t.Fatalf("raw owner envelope: %s", frame.data)
				}
				expected = append(expected, frame)
			}
			if expected[1].data == expected[2].data || expected[2].data == expected[3].data {
				t.Fatal("multipart display identity collapsed")
			}
			if f := readBotSSE(t, mr); f.event != botReplayComplete {
				t.Fatalf("live completion was not catch-up: %+v", f)
			}
			multi.Body.Close()
			// Disconnect after either text: final part has not committed the row.
			replay := request("GET", path, owner, "", expected[1].id, 200)
			replayReader := bufio.NewReader(replay.Body)
			for _, want := range expected[1:] {
				if got := readBotSSE(t, replayReader); got != want {
					t.Fatalf("unstable multipart reconnect: %+v != %+v", got, want)
				}
			}
			replay.Body.Close()
			restored := request("GET", strings.Replace(path, "/events?", "/stream?", 1), owner, "", "", 200)
			restoredReader := bufio.NewReader(restored.Body)
			for i := int64(0); i < previous; i++ {
				readBotSSE(t, restoredReader)
			}
			for _, want := range expected {
				if got := readBotSSE(t, restoredReader); got != want {
					t.Fatalf("history/live contract mismatch: %+v != %+v", got, want)
				}
			}
			if f := readBotSSE(t, restoredReader); f.event != botTranscriptDone || !strings.Contains(f.data, `"persisted":true`) {
				t.Fatalf("finite history missing persisted completion: %+v", f)
			}
			restored.Body.Close()
		})
	}
}
