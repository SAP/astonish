package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"google.golang.org/adk/model"
	adksession "google.golang.org/adk/session"
	"google.golang.org/genai"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/mux"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/entstore"
)

type botBoundaryStore struct {
	store.BotStore
	calls int
}

func (s *botBoundaryStore) List(context.Context, store.BotPage) ([]store.Bot, error) {
	s.calls++
	return nil, errors.New("database secret detail must not escape")
}
func TestBotHTTPBoundary(t *testing.T) {
	actor := uuid.NewString()
	principal, err := execution.WithPrincipal(context.Background(), execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: actor, OrgSlug: "org", TeamSlug: "team", Authenticated: true})
	if err != nil {
		t.Fatal(err)
	}
	authenticated := store.WithTenantContext(principal, &store.TenantContext{OrgSlug: "org", TeamSlug: "team", UserID: actor})
	for _, test := range []struct {
		name   string
		ctx    context.Context
		scope  string
		status int
	}{
		{"anonymous", context.Background(), "", 401},
		{"missing tenant", principal, "", 403},
		{"mismatched tenant", store.WithTenantContext(principal, &store.TenantContext{OrgSlug: "other", TeamSlug: "team", UserID: actor}), "", 403},
		{"missing personal", authenticated, "", 503},
		{"unknown scope", authenticated, "?scope=other", 400},
		{"sanitized error", authenticated, "?scope=team", 500},
	} {
		t.Run(test.name, func(t *testing.T) {
			repo := &botBoundaryStore{}
			c := store.WithServices(test.ctx, &store.Services{Bots: repo})
			r := httptest.NewRequest("GET", "/api/studio/bots"+test.scope, nil).WithContext(c)
			w := httptest.NewRecorder()
			BotListHandler(w, r)
			if w.Code != test.status {
				t.Fatalf("status %d want %d: %s", w.Code, test.status, w.Body.String())
			}
			if strings.Contains(w.Body.String(), "secret") {
				t.Fatal("internal error leaked")
			}
			if test.status != 500 && repo.calls != 0 {
				t.Fatal("repository called before authorization")
			}
		})
	}
	r := httptest.NewRequest("POST", "/", strings.NewReader(`{"name":"`+strings.Repeat("x", 256<<10)+`"}`))
	w := httptest.NewRecorder()
	var q botCreateDTO
	if botDecodeRequest(w, r, &q) || w.Code != 400 {
		t.Fatal("oversized body accepted")
	}
}

// TestBotStorageHTTPAcceptance (formerly TestBotDraftHTTPAcceptance) uses real
// HTTP, production routes/middleware and scoped SQLite repositories.
// Authentication is a controlled fixture; no worker or LLM is installed.
func TestBotStorageHTTPAcceptance(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	cfg := entstore.Config{DSN: filepath.Join(dir, "platform.db"), DataDir: dir}
	base, db, err := entstore.NewPlatformServices(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	org := &store.Organization{ID: uuid.NewString(), Slug: "bot-http", Name: "Bot HTTP", Status: "active"}
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
	team := &store.Team{ID: uuid.NewString(), Slug: "team-a", Name: "Team A", SchemaName: "team_team-a"}
	if err := ods.Teams().CreateTeam(ctx, team); err != nil {
		t.Fatal(err)
	}
	owner, member := uuid.NewString(), uuid.NewString()
	for _, id := range []string{owner, member} {
		if err := db.Users().Create(ctx, &store.User{ID: id, Email: id + "@example.test", DisplayName: "Tester", Status: "active"}); err != nil {
			t.Fatal(err)
		}
		if err := db.Organizations().AddMember(ctx, id, org.ID, "member"); err != nil {
			t.Fatal(err)
		}
		if err := ods.Teams().AddMember(ctx, &store.TeamMembership{UserID: id, TeamID: team.ID, Role: "member"}); err != nil {
			t.Fatal(err)
		}
	}
	registryStore, err := sandbox.NewLocalSessionStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	registry := sandbox.NewSessionRegistryFromStore(registryStore)
	start := func() *httptest.Server {
		router := mux.NewRouter()
		RegisterRoutes(router, base, db, entstore.TenantMiddleware(db))
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id := r.Header.Get("X-Test-Actor")
			c := r.Context()
			if id != "" {
				var err error
				c, err = execution.WithPrincipal(c, execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: id, OrgSlug: org.Slug, TeamSlug: team.Slug, Authenticated: true})
				if err != nil {
					http.Error(w, "bad fixture", 500)
					return
				}
				c = context.WithValue(c, platformUserKey{}, &PlatformUser{ID: id, OrgSlug: org.Slug, TeamSlug: team.Slug, Role: "member"})
				c = store.WithTenantContext(c, &store.TenantContext{OrgSlug: org.Slug, TeamSlug: team.Slug, UserID: id})
			}
			router.ServeHTTP(w, withSandboxRegistry(r.WithContext(c), registry))
		}))
	}
	server := start()
	defer func() { server.Close() }()
	request := func(method, path, actor, body string, status int) []byte {
		t.Helper()
		q, err := http.NewRequest(method, server.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		if actor != "" {
			q.Header.Set("X-Test-Actor", actor)
		}
		q.Header.Set("Content-Type", "application/json")
		resp, err := server.Client().Do(q)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		data, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != status {
			t.Fatalf("%s %s: %d want %d: %s", method, path, resp.StatusCode, status, data)
		}
		return data
	}
	root := "/api/studio/bots"
	request("POST", root, "", `broken`, 401)
	for _, body := range []string{`{"name":"Unsafe","owner_id":"forged"}`, `{"name":"Unsafe","definition":{"charter":"Watch","autonomy":{"can_publish":true}}}`, `{} {}`, `null`} {
		request("POST", root, owner, body, 400)
	}
	body := `{"name":"Private watcher","idempotency_key":"private","definition":{"charter":"Observe public changes"}}`
	var b store.Bot
	if err := json.Unmarshal(request("POST", root, owner, body, 201), &b); err != nil {
		t.Fatal(err)
	}
	if b.Status != store.BotDraft || b.Scope != store.BotScopePersonal || b.OwnerID.String() != owner {
		t.Fatalf("unsafe draft: %+v", b)
	}
	var retry store.Bot
	_ = json.Unmarshal(request("POST", root, owner, body, 201), &retry)
	if retry.ID != b.ID {
		t.Fatal("duplicate create")
	}
	path := root + "/" + b.ID.String()
	request("GET", path, owner, "", 200)
	for _, suffix := range []string{"/snapshot", "/read"} {
		request("GET", path+suffix, "", "", 401)
		request("GET", path+suffix, owner, "", 200)
		request("GET", path+suffix, member, "", 404)
		request("GET", path+suffix+"?scope=team", owner, "", 404)
	}

	request("GET", path, member, "", 404)
	request("GET", path+"?scope=team", owner, "", 404)
	request("GET", root+"?scope=unknown", owner, "", 400)
	request("GET", root+"?limit=201", owner, "", 400)
	request("GET", root+"?scope=personal&scope=team", owner, "", 400)
	request("GET", root+"/bad-id", owner, "", 400)
	request("POST", path+"/versions", owner, `{"expected_revision":1,"change_reason":"Correct source","definition":{"charter":"Observe official sources only"}}`, 200)
	request("POST", path+"/versions", owner, `{"expected_revision":1,"change_reason":"Stale edit","definition":{"charter":"Old text"}}`, 409)
	var v store.BotVersion
	_ = json.Unmarshal(request("GET", path+"/versions/1", owner, "", 200), &v)
	if v.Charter != "Observe public changes" || v.Autonomy.CanPublish || len(v.Triggers) != 0 {
		t.Fatalf("history or authority changed: %+v", v)
	}
	sharedBody := `{"name":"Team watcher","idempotency_key":"team","definition":{"charter":"Observe team sources"}}`
	var shared store.Bot
	_ = json.Unmarshal(request("POST", root+"?scope=team", owner, sharedBody, 201), &shared)
	sharedPath := root + "/" + shared.ID.String()
	var ownerSnapshot, memberSnapshot store.BotSnapshot
	_ = json.Unmarshal(request("GET", sharedPath+"/snapshot?scope=team", owner, "", 200), &ownerSnapshot)
	request("POST", sharedPath+"/read?scope=team", owner, fmt.Sprintf(`{"sequence":%d}`, ownerSnapshot.LatestSequence), 204)
	_ = json.Unmarshal(request("GET", sharedPath+"/snapshot?scope=team", owner, "", 200), &ownerSnapshot)
	_ = json.Unmarshal(request("GET", sharedPath+"/snapshot?scope=team", member, "", 200), &memberSnapshot)
	if ownerSnapshot.UnreadCount != 0 || memberSnapshot.ReadCursor.Sequence != 0 || memberSnapshot.UnreadCount != memberSnapshot.LatestSequence || memberSnapshot.ReadCursor.ViewerID.String() != member {
		t.Fatalf("viewer snapshot mismatch: owner=%+v member=%+v", ownerSnapshot, memberSnapshot)
	}
	request("GET", sharedPath+"/read?scope=team", member, "", 200)

	artifactPath := "/reports/shared-bot.md"
	sharedSession := ods.ForTeam(team.Slug).Sessions()
	for _, event := range []*adksession.Event{
		{LLMResponse: model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{ID: "bot-report", Name: "write_file", Args: map[string]any{"path": artifactPath, "content": "# Retained Bot report\nVerified source."}}}}}}},
		{LLMResponse: model.LLMResponse{Content: &genai.Content{Role: "user", Parts: []*genai.Part{{FunctionResponse: &genai.FunctionResponse{ID: "bot-report", Name: "write_file", Response: map[string]any{"success": true}}}}}}},
	} {
		if err := sharedSession.AppendFleetEvent(ctx, shared.HomeSessionID.String(), event); err != nil {
			t.Fatal(err)
		}
	}
	if err := registry.PutSession(&store.SandboxSession{SessionID: shared.HomeSessionID.String(), ChatSessionID: shared.HomeSessionID.String(), CreatedBy: owner, Backend: "docker", State: store.SandboxSessionStateEvicted}); err != nil {
		t.Fatal(err)
	}
	artifactQuery := "?session=" + shared.HomeSessionID.String() + "&path=" + url.QueryEscape(artifactPath)
	for _, endpoint := range []string{"/api/studio/artifacts", "/api/studio/artifacts/content"} {
		data := request("GET", endpoint+artifactQuery, member, "", 200)
		if string(data) != "# Retained Bot report\nVerified source." {
			t.Fatalf("artifact content: %s", data)
		}
	}
	request("GET", "/api/studio/artifacts/pdf"+artifactQuery, member, "", 503)
	request("GET", sharedPath+"?scope=team", member, "", 200)
	request("GET", "/api/studio/sessions/"+shared.HomeSessionID.String(), member, "", 200)
	request("GET", "/api/studio/sessions/"+b.HomeSessionID.String(), member, "", 404)
	// Bot home model routes are a narrow configuration adapter.
	for _, home := range []store.Bot{b, shared} {
		modelPath := "/api/studio/sessions/" + home.HomeSessionID.String()
		request("GET", modelPath+"/model-status", owner, "", 200)
		request("PATCH", modelPath+"/model", owner, `{"provider":"controlled","model":"home-model"}`, 200)
		var status SessionModelStatusResponse
		_ = json.Unmarshal(request("GET", modelPath+"/model-status", owner, "", 200), &status)
		if status.PinnedProvider != "controlled" || status.PinnedModel != "home-model" {
			t.Fatalf("home pin lost: %+v", status)
		}
		request("GET", modelPath+"/model-status?scope="+string(home.Scope), owner, "", 200)
		wrongScope := "team"
		if home.Scope == store.BotScopeTeam {
			wrongScope = "personal"
		}
		request("GET", modelPath+"/model-status?scope="+wrongScope, owner, "", 404)
		request("PATCH", modelPath+"/model?scope="+wrongScope, owner, `{}`, 404)
		request("PATCH", modelPath+"/model", owner, `{"provider":"controlled"}`, 400)
		// Reopen the HTTP surface, then read the durable pin again.
		server.Close()
		server = start()
		if err := json.Unmarshal(request("GET", modelPath+"/model-status", owner, "", 200), &status); err != nil || status.PinnedProvider != "controlled" || status.PinnedModel != "home-model" || status.EffectiveModel != "home-model" {
			t.Fatalf("reloaded home pin: %+v %v", status, err)
		}
		if home.Scope == store.BotScopeTeam {
			if err := ods.Teams().SetRole(ctx, member, team.ID, "admin"); err != nil {
				t.Fatal(err)
			}
			request("PATCH", modelPath+"/model", member, `{"provider":"controlled","model":"home-model"}`, 200)
			if err := ods.Teams().SetRole(ctx, member, team.ID, "viewer"); err != nil {
				t.Fatal(err)
			}
			request("GET", modelPath+"/model-status", member, "", 200)
			request("PATCH", modelPath+"/model", member, `{}`, 404)
			if err := ods.Teams().SetRole(ctx, member, team.ID, "member"); err != nil {
				t.Fatal(err)
			}
		}
		request("PATCH", modelPath+"/model", member, `{}`, 404)
		if home.Scope == store.BotScopeTeam {
			request("GET", modelPath+"/model-status", member, "", 200)
		} else {
			request("GET", modelPath+"/model-status", member, "", 404)
		}
	}
	// History access does not grant generic Chat execution authority.
	for _, sessionID := range []string{shared.HomeSessionID.String(), b.HomeSessionID.String()} {
		for _, actor := range []string{owner, member} {
			for _, suffix := range []string{"stream", "status"} {
				request("GET", "/api/studio/sessions/"+sessionID+"/"+suffix, actor, "", 404)
			}
			request("POST", "/api/studio/sessions/"+sessionID+"/stop", actor, `{}`, 404)
		}
	}
	// Generic delete preserves its existing idempotent 204 for inaccessible
	// sessions, but must not delete or clean up the Bot conversation.
	request("DELETE", "/api/studio/sessions/"+shared.HomeSessionID.String(), owner, "", 204)
	request("GET", "/api/studio/sessions/"+shared.HomeSessionID.String(), member, "", 200)
	// Exercise goals in both scopes through the same HTTP/store path.
	var persistedGoal store.BotGoal
	var correctionBody string
	var reconciliationRetries []struct{ path, scope, memoryID, sessionID string }
	for _, tc := range []struct{ path, scope string }{{path, ""}, {sharedPath, "?scope=team"}} {
		goalsPath := tc.path + "/goals"
		request("POST", goalsPath+tc.scope, "", `{}`, 401)
		request("POST", goalsPath+tc.scope, owner, `{"title":"Receive replacement","completion_criteria":"Arrived and works","status":"completed"}`, 400)
		var g store.BotGoal
		if err := json.Unmarshal(request("POST", goalsPath+tc.scope, owner, `{"title":"Receive replacement","completion_criteria":"Arrived and works"}`, 201), &g); err != nil {
			t.Fatal(err)
		}
		goalPath := goalsPath + "/" + g.ID.String()
		request("PUT", goalPath+tc.scope, owner, `{"expected_revision":1,"title":"Receive correct replacement","completion_criteria":"Arrived and works"}`, 200)
		request("PUT", goalPath+tc.scope, owner, `{"expected_revision":1,"title":"Stale","completion_criteria":"Received"}`, 409)
		request("POST", goalPath+"/complete"+tc.scope, owner, `{"expected_revision":2}`, 400)
		evidence := fmt.Sprintf(`{"summary":"Owner confirms arrival and operation","source":"owner-review","observed_at":%q}`, time.Now().UTC().Format(time.RFC3339Nano))
		itemPath := goalPath + "/tracked-items/" + uuid.NewString()
		request("PUT", itemPath+tc.scope, owner, `{"expected_goal_revision":2,"title":"Verify operation","status":"open"}`, 204)
		request("PUT", itemPath+tc.scope, owner, `{"expected_goal_revision":2,"title":"Stale","status":"open"}`, 409)
		completion := `{"expected_revision":3,"evidence":` + evidence + `}`
		request("POST", goalPath+"/complete"+tc.scope, owner, completion, 409)
		request("PUT", itemPath+tc.scope, owner, `{"expected_goal_revision":3,"title":"Verify operation","status":"completed"}`, 400)
		request("PUT", itemPath+tc.scope, owner, `{"expected_goal_revision":3,"title":"Verify operation","status":"completed","evidence":[`+evidence+`]}`, 204)
		var tracked struct {
			Items []store.BotTrackedItem `json:"items"`
		}
		if err := json.Unmarshal(request("GET", goalPath+"/tracked-items"+tc.scope, owner, "", 200), &tracked); err != nil {
			t.Fatal(err)
		}
		if len(tracked.Items) != 1 || tracked.Items[0].Status != store.BotTrackedCompleted || len(tracked.Items[0].Evidence) != 1 || tracked.Items[0].Evidence[0].ActorID.String() != owner {
			t.Fatalf("tracked evidence: %+v", tracked)
		}
		completion = `{"expected_revision":4,"evidence":` + evidence + `}`
		if err := json.Unmarshal(request("POST", goalPath+"/complete"+tc.scope, owner, completion, 200), &g); err != nil {
			t.Fatal(err)
		}
		if g.Status != store.BotGoalCompleted || len(g.Evidence) != 1 || g.Evidence[0].ActorID.String() != owner || g.Evidence[0].Kind != store.BotEvidenceOwnerConfirmation {
			t.Fatalf("completion attribution: %+v", g)
		}
		request("PUT", goalPath+tc.scope, owner, `{"expected_revision":5,"title":"Reopen silently","completion_criteria":"More work"}`, 403)
		request("POST", goalPath+"/complete"+tc.scope, owner, `{"expected_revision":3,"evidence":{"actor_id":"forged"}}`, 400)
		request("GET", goalsPath+tc.scope, owner, "", 200)
		var current store.Bot
		if err := json.Unmarshal(request("GET", tc.path+tc.scope, owner, "", 200), &current); err != nil {
			t.Fatal(err)
		}
		memories := ods.ForUser(owner).Memories()
		if tc.scope != "" {
			memories = ods.ForTeam(team.Slug).Memories()
		}
		if err := memories.Add(ctx, store.MemoryEntry{Content: "Thursday", Category: "delivery", SessionID: current.HomeSessionID.String(), CreatedBy: owner}); err != nil {
			t.Fatal(err)
		}
		rows, err := memories.ListBySession(ctx, current.HomeSessionID.String())
		if err != nil || len(rows) != 1 {
			t.Fatalf("seed memory: %+v %v", rows, err)
		}
		memoryID, correctionID := rows[0].ID, uuid.NewString()
		correction := fmt.Sprintf(`{"id":%q,"expected_revision":%d,"superseded_source":%q,"replacement":"Friday, not Thursday","goal_ids":[%q],"evidence":%s}`, correctionID, current.Revision, "memory:"+memoryID, g.ID, evidence)
		request("POST", tc.path+"/corrections"+tc.scope, owner, correction, 202)
		request("POST", tc.path+"/corrections"+tc.scope, owner, correction, 202)
		request("POST", tc.path+"/corrections"+tc.scope, owner, strings.Replace(correction, "Friday, not Thursday", "Changed retry", 1), 409)
		reconcilePath := tc.path + "/corrections/" + correctionID + "/reconcile"
		request("POST", reconcilePath+tc.scope, "", "", 401)
		denial := 404
		if tc.scope != "" {
			denial = 403
		}
		request("POST", reconcilePath+tc.scope, member, "", denial)
		for _, field := range []string{"replacement", "actor_id", "superseded_source", "memory_id"} {
			request("POST", reconcilePath+tc.scope, owner, fmt.Sprintf(`{%q:"forged"}`, field), 400)
		}
		request("POST", tc.path+"/corrections/"+uuid.NewString()+"/reconcile"+tc.scope, owner, "", 404)
		pending, err := memories.Get(ctx, memoryID)
		if err != nil || pending.Snippet != "Thursday" {
			t.Fatalf("intent or denied request changed memory: %+v %v", pending, err)
		}
		// An existing same-scope Bot cannot reconcile another Bot's intent.
		var foreign store.Bot
		if err := json.Unmarshal(request("POST", root+tc.scope, owner, `{"name":"Other correction owner","idempotency_key":"foreign-correction","definition":{"charter":"Separate memory"}}`, 201), &foreign); err != nil {
			t.Fatal(err)
		}
		request("POST", root+"/"+foreign.ID.String()+"/corrections/"+correctionID+"/reconcile"+tc.scope, owner, "", 404)
		foreignCorrectionID := uuid.NewString()
		foreignCorrection := fmt.Sprintf(`{"id":%q,"expected_revision":%d,"superseded_source":%q,"replacement":"Unauthorized overwrite","evidence":%s}`, foreignCorrectionID, foreign.Revision, "memory:"+memoryID, evidence)
		foreignPath := root + "/" + foreign.ID.String()
		request("POST", foreignPath+"/corrections"+tc.scope, owner, foreignCorrection, 202)
		// A recorded intent cannot authorize changing memory attributed to another Bot.
		request("POST", foreignPath+"/corrections/"+foreignCorrectionID+"/reconcile"+tc.scope, owner, "", 403)
		request("POST", foreignPath+"/corrections"+tc.scope, owner, foreignCorrection, 202)
		pending, err = memories.Get(ctx, memoryID)
		if err != nil || pending.Snippet != "Thursday" {
			t.Fatalf("foreign intent changed memory: %+v %v", pending, err)
		}
		for range 2 {
			var receipt struct {
				ID            uuid.UUID `json:"id"`
				MemoryApplied bool      `json:"memory_applied"`
			}
			if err := json.Unmarshal(request("POST", reconcilePath+tc.scope, owner, "", 200), &receipt); err != nil || receipt.ID.String() != correctionID || !receipt.MemoryApplied {
				t.Fatalf("receipt: %+v %v", receipt, err)
			}
		}
		applied, err := memories.Get(ctx, memoryID)
		if err != nil || applied.Snippet != "Friday, not Thursday" || applied.SessionID != current.HomeSessionID.String() || applied.Category != "delivery" {
			t.Fatalf("corrected memory: %+v %v", applied, err)
		}
		results, err := memories.Search(ctx, "Friday", 10, 0)
		if err != nil || len(results) != 1 || results[0].ID != memoryID || results[0].Snippet != applied.Snippet {
			t.Fatalf("corrected retrieval: %+v %v", results, err)
		}
		if err := memories.Update(ctx, memoryID, "Saturday", "delivery"); err != nil {
			t.Fatal(err)
		}
		request("POST", reconcilePath+tc.scope, owner, "", 200)
		newer, err := memories.Get(ctx, memoryID)
		if err != nil || newer.Snippet != "Saturday" {
			t.Fatalf("retry overwrote newer memory: %+v %v", newer, err)
		}
		reconciliationRetries = append(reconciliationRetries, struct{ path, scope, memoryID, sessionID string }{reconcilePath, tc.scope, memoryID, current.HomeSessionID.String()})
		var events struct {
			Activity []store.BotActivity `json:"activity"`
		}
		if err := json.Unmarshal(request("GET", tc.path+"/activity"+tc.scope, owner, "", 200), &events); err != nil {
			t.Fatal(err)
		}
		count, appliedCount := 0, 0
		for i, a := range events.Activity {
			if a.Sequence != int64(i+1) {
				t.Fatal("activity order")
			}
			var payload struct {
				Intent        store.BotOutboxKind `json:"intent"`
				MemoryApplied bool                `json:"memory_applied"`
			}
			if err := json.Unmarshal(a.Payload, &payload); err != nil {
				t.Fatal(err)
			}
			if payload.MemoryApplied {
				appliedCount++
			}
			if payload.Intent == store.BotOutboxReconcile {
				count++
			}
		}
		if appliedCount != 1 {
			t.Fatalf("duplicate applied receipts: %d", appliedCount)
		}
		if count != 1 {
			t.Fatalf("duplicate reconciliation intents: %d", count)
		}
		separator := "?"
		if tc.scope != "" {
			separator = "&"
		}
		cursor := events.Activity[len(events.Activity)-1].Sequence
		var replay struct {
			Activity []store.BotActivity `json:"activity"`
		}
		if err := json.Unmarshal(request("GET", fmt.Sprintf("%s/activity%s%safter=%d", tc.path, tc.scope, separator, cursor), owner, "", 200), &replay); err != nil {
			t.Fatal(err)
		}
		if len(replay.Activity) != 0 {
			t.Fatal("activity cursor replayed old events")
		}
		futureEvidence := fmt.Sprintf(`{"summary":"Future claim","source":"owner-review","observed_at":%q}`, time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano))
		futureCorrection := fmt.Sprintf(`{"id":%q,"expected_revision":%d,"superseded_source":"memory:date","replacement":"Future","evidence":%s}`, uuid.NewString(), current.Revision+1, futureEvidence)
		request("POST", tc.path+"/corrections"+tc.scope, owner, futureCorrection, 400)
		if tc.scope == "" {
			persistedGoal = g
			correctionBody = correction
		} else {
			request("GET", goalsPath+tc.scope, member, "", 200)
			request("POST", goalsPath+tc.scope, member, `{"title":"Unauthorized","completion_criteria":"Done"}`, 403)
			request("POST", tc.path+"/corrections"+tc.scope, member, correction, 403)
			// An existing goal belonging to another Bot is not writable here.
			request("PUT", goalsPath+"/"+persistedGoal.ID.String()+tc.scope, owner, `{"expected_revision":3,"title":"Foreign","completion_criteria":"Done"}`, 404)
		}
	}
	request("GET", path+"/goals", member, "", 404)
	request("GET", path+"/activity?after=-1", owner, "", 400)
	request("GET", path+"/activity?limit=201", owner, "", 400)
	request("POST", path+"/goals/bad/complete", owner, `{}`, 400)
	lifecyclePaths := []string{}
	// Lifecycle operations on separate drafts must not enable execution.
	for _, scope := range []string{"", "?scope=team"} {
		var lifecycle store.Bot
		decodeBot := func(data []byte) {
			t.Helper()
			lifecycle = store.Bot{}
			if err := json.Unmarshal(data, &lifecycle); err != nil {
				t.Fatal(err)
			}
		}
		decodeBot(request("POST", root+scope, owner, `{"name":"Lifecycle","idempotency_key":"lifecycle","definition":{"charter":"Observe"}}`, 201))
		lp := root + "/" + lifecycle.ID.String()
		lifecyclePaths = append(lifecyclePaths, lp+scope)
		request("POST", lp+"/read"+scope, owner, `{}`, 400)
		request("POST", lp+"/restore"+scope, "", `broken`, 401)
		request("POST", lp+"/pause"+scope, owner, `{"expected_revision":1,"reason":"Pause draft"}`, 409)
		request("POST", lp+"/stop"+scope, owner, `{"expected_revision":1,"reason":"Stop draft"}`, 409)
		request("DELETE", lp+scope, owner, `{"expected_revision":1,"reason":"Delete","actor_id":"forged"}`, 400)
		request("DELETE", lp+scope, owner, `{"expected_revision":1,"reason":"Delete","now":"2099-01-01T00:00:00Z"}`, 400)
		request("DELETE", lp+scope, member, `{"expected_revision":1,"reason":"Not mine"}`, map[bool]int{true: 403, false: 404}[scope != ""])
		decodeBot(request("DELETE", lp+scope, owner, `{"expected_revision":1,"reason":"Retire"}`, 200))
		if lifecycle.Status != store.BotDeleted || lifecycle.DeletedAt == nil || lifecycle.RestoreUntil == nil || lifecycle.RestoreUntil.Sub(*lifecycle.DeletedAt) != store.BotRetention {
			t.Fatalf("retention: %+v", lifecycle)
		}
		request("DELETE", lp+scope, owner, `{"expected_revision":1,"reason":"Stale retry"}`, 409)
		request("POST", lp+"/goals"+scope, owner, `{"title":"New work","completion_criteria":"Done"}`, 403)
		request("POST", lp+"/restore"+scope, owner, `{"expected_revision":1,"reason":"Stale restore"}`, 409)
		decodeBot(request("POST", lp+"/restore"+scope, owner, `{"expected_revision":2,"reason":"Restore retained Bot"}`, 200))
		if lifecycle.Status != store.BotPaused || lifecycle.RestoreUntil != nil || lifecycle.DeletedAt != nil {
			t.Fatalf("restore activated: %+v", lifecycle)
		}
		request("POST", lp+"/read"+scope, owner, `{"sequence":999999}`, 400)
		request("POST", lp+"/read"+scope, owner, `{"sequence":1,"viewer_id":"forged"}`, 400)
		request("POST", lp+"/read"+scope, owner, `{"sequence":1}`, 204)
		request("POST", lp+"/read"+scope, owner, `{"sequence":1}`, 204)
		request("POST", lp+"/read"+scope, owner, `{"sequence":0}`, 204)
		decodeBot(request("GET", lp+scope, owner, "", 200))
		if lifecycle.Revision != 4 {
			t.Fatalf("read retries changed revision: %+v", lifecycle)
		}
		decodeBot(request("POST", lp+"/stop"+scope, owner, `{"expected_revision":4,"reason":"Stop restored Bot"}`, 200))
		if lifecycle.Status != store.BotStopped {
			t.Fatalf("stop: %+v", lifecycle)
		}
	}
	request("GET", path+"/activity?after=-1", owner, "", 400)
	request("POST", sharedPath+"/versions?scope=team", member, `{"expected_revision":1,"change_reason":"Unauthorized","definition":{"charter":"Change"}}`, 403)
	templateRoot := root + "/templates"
	request("GET", templateRoot, owner, "", 400)
	request("GET", templateRoot+"?scope=team", owner, "", 200)
	definition := `{"schema_version":1,"name":"Reviewed watcher","charter":"Observe public information only","language":"en","suggested_goals":[{"title":"Find change","completion_criteria":"Owner confirms"}],"suggested_schedules":[{"cron":"0 9 * * *","timezone":"UTC","instructions":"Check source"}]}`
	publish := `{"expected_revision":0,"reviewed_for_team":true,"definition":` + definition + `}`
	request("POST", templateRoot+"?scope=team", owner, publish, 403)
	if err := ods.Teams().SetRole(ctx, owner, team.ID, "admin"); err != nil {
		t.Fatal(err)
	}
	request("POST", templateRoot+"?scope=team", owner, strings.Replace(publish, `"reviewed_for_team":true`, `"reviewed_for_team":false`, 1), 400)
	request("POST", templateRoot+"?scope=team", owner, strings.Replace(publish, `"language":"en"`, `"credentials":{"token":"not-accepted"}`, 1), 400)
	var template store.BotTemplate
	if err := json.Unmarshal(request("POST", templateRoot+"?scope=team", owner, publish, 201), &template); err != nil {
		t.Fatal(err)
	}
	tp := templateRoot + "/" + template.ID.String()
	request("GET", tp+"?scope=team", member, "", 200)
	forkBody := `{"template_revision":1,"name":"My private watcher","idempotency_key":"fork-1"}`
	var fork, again store.Bot
	if err := json.Unmarshal(request("POST", tp+"/fork?scope=team", member, forkBody, 201), &fork); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(request("POST", tp+"/fork?scope=team", member, forkBody, 201), &again); err != nil {
		t.Fatal(err)
	}
	if fork.ID != again.ID || fork.Scope != store.BotScopePersonal || fork.Status != store.BotDraft || fork.OwnerID.String() != member {
		t.Fatalf("unsafe fork: %+v %+v", fork, again)
	}
	forkPath := root + "/" + fork.ID.String()
	var fv store.BotVersion
	if err := json.Unmarshal(request("GET", forkPath+"/versions/1", member, "", 200), &fv); err != nil {
		t.Fatal(err)
	}
	if fv.Autonomy.Level != execution.AutonomySupervised || fv.Autonomy.CanContactPeople || fv.Autonomy.CanPublish || fv.Autonomy.MaxSpendPerRunMinor != 0 || len(fv.Delivery.Bindings) != 0 || len(fv.Triggers) != 1 || fv.Triggers[0].Enabled || fv.Triggers[0].ScheduleID != nil || fv.Triggers[0].CredentialRef != "" {
		t.Fatalf("fork authority: %+v", fv)
	}
	request("GET", forkPath, owner, "", 404)
	request("POST", tp+"/fork?scope=team", member, strings.Replace(forkBody, `"template_revision":1`, `"template_revision":9`, 1), 409)
	request("POST", templateRoot+"?scope=team", owner, strings.Replace(publish, `"expected_revision":0`, `"expected_revision":1`, 1), 200)
	request("POST", templateRoot+"?scope=team", owner, strings.Replace(publish, `"expected_revision":0`, `"expected_revision":1`, 1), 409)
	request("POST", tp+"/fork?scope=team", member, forkBody, 409)
	if err := ods.Teams().SetRole(ctx, owner, team.ID, "member"); err != nil {
		t.Fatal(err)
	}
	request("POST", templateRoot+"?scope=team", owner, publish, 403)
	if err := ods.Teams().RemoveMember(ctx, member, team.ID); err != nil {
		t.Fatal(err)
	}
	request("GET", sharedPath+"?scope=team", member, "", 403)
	request("GET", "/api/studio/sessions/"+shared.HomeSessionID.String(), member, "", 404)
	request("GET", tp+"?scope=team", member, "", 403)
	request("GET", "/api/studio/artifacts/content"+artifactQuery, member, "", 404)
	request("POST", tp+"/fork?scope=team", member, forkBody, 403)
	server.Close()
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	base, db, err = entstore.NewPlatformServices(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	server = start()
	ods, err = db.ForOrg(org.Slug)
	if err != nil {
		t.Fatal(err)
	}
	for _, retry := range reconciliationRetries {
		request("POST", retry.path+retry.scope, owner, "", 200)
		memories := ods.ForUser(owner).Memories()
		if retry.scope != "" {
			memories = ods.ForTeam(team.Slug).Memories()
			request("POST", retry.path+retry.scope, member, "", 403)
		}
		row, err := memories.Get(ctx, retry.memoryID)
		if err != nil || row.Snippet != "Saturday" || row.SessionID != retry.sessionID {
			t.Fatalf("reopened receipt replay: %+v %v", row, err)
		}
	}
	if data := request("GET", "/api/studio/artifacts/content"+artifactQuery, owner, "", 200); string(data) != "# Retained Bot report\nVerified source." {
		t.Fatalf("artifact after reopen: %s", data)
	}
	var restoredTemplate store.BotTemplate
	if err := json.Unmarshal(request("GET", tp+"?scope=team", owner, "", 200), &restoredTemplate); err != nil {
		t.Fatal(err)
	}
	if restoredTemplate.Revision != 2 || restoredTemplate.ID != template.ID {
		t.Fatalf("template reopen: %+v", restoredTemplate)
	}
	var restoredFork store.Bot
	if err := json.Unmarshal(request("GET", forkPath, member, "", 200), &restoredFork); err != nil {
		t.Fatal(err)
	}
	if restoredFork.Status != store.BotDraft || restoredFork.Version != 1 {
		t.Fatalf("fork reopened active: %+v", restoredFork)
	}
	request("GET", forkPath+"/goals", member, "", 200)
	for _, lp := range lifecyclePaths {
		var restored store.Bot
		if err := json.Unmarshal(request("GET", lp, owner, "", 200), &restored); err != nil {
			t.Fatal(err)
		}
		if restored.Status != store.BotStopped || restored.Revision != 5 {
			t.Fatalf("lifecycle lost after reopen: %+v", restored)
		}
	}
	_ = json.Unmarshal(request("GET", path, owner, "", 200), &b)
	if b.Revision != 3 || b.Version != 2 || b.Status != store.BotDraft {
		t.Fatalf("reopen: %+v", b)
	}
	_ = json.Unmarshal(request("GET", path+"/versions/2", owner, "", 200), &v)
	if v.Charter != "Observe official sources only" {
		t.Fatalf("lost edit: %+v", v)
	}
	request("POST", path+"/corrections", owner, correctionBody, 202)
	var restoredGoals struct {
		Goals []store.BotGoal `json:"goals"`
	}
	if err := json.Unmarshal(request("GET", path+"/goals", owner, "", 200), &restoredGoals); err != nil {
		t.Fatal(err)
	}
	if len(restoredGoals.Goals) != 1 || restoredGoals.Goals[0].ID != persistedGoal.ID || restoredGoals.Goals[0].Status != store.BotGoalCompleted {
		t.Fatalf("lost completion: %+v", restoredGoals)
	}
	request("GET", root, owner, "", 200)
}
