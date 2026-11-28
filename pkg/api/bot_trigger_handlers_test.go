package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/channels"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/entstore"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
)

// Real HTTP, production route/middleware and SQLite admission. Only authenticated
// transport and initial activation are fixtures; no model, worker or sends run.
func TestBotTriggersHTTPAdmission(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	base, db, e := entstore.NewPlatformServices(ctx, entstore.Config{DSN: filepath.Join(dir, "platform.db"), DataDir: dir})
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	org := &store.Organization{ID: uuid.NewString(), Slug: "trigger-http", Name: "Trigger HTTP", Status: "active"}
	if e := db.Organizations().Create(ctx, org); e != nil {
		t.Fatal(e)
	}
	if e := db.ProvisionOrg(ctx, org.ID, org.Slug); e != nil {
		t.Fatal(e)
	}
	ods, e := db.ForOrg(org.Slug)
	if e != nil {
		t.Fatal(e)
	}
	team := &store.Team{ID: uuid.NewString(), Slug: "team-a", Name: "Team", SchemaName: "team_team-a"}
	if e := ods.Teams().CreateTeam(ctx, team); e != nil {
		t.Fatal(e)
	}
	owner := uuid.NewString()
	if e := db.Users().Create(ctx, &store.User{ID: owner, Email: owner + "@example.test", DisplayName: "Fixture", Status: "active"}); e != nil {
		t.Fatal(e)
	}
	if e := db.Organizations().AddMember(ctx, owner, org.ID, "member"); e != nil {
		t.Fatal(e)
	}
	if e := ods.Teams().AddMember(ctx, &store.TeamMembership{UserID: owner, TeamID: team.ID, Role: "member"}); e != nil {
		t.Fatal(e)
	}
	const secret = "fixture-http-signing-key-32-bytes-never-live"
	if e := ods.ForUser(owner).Credentials().Set(ctx, "trigger-signing", &store.Credential{Type: store.CredAPIKey, Value: secret}); e != nil {
		t.Fatal(e)
	}
	router := mux.NewRouter()
	RegisterRoutes(router, base, db, entstore.TenantMiddleware(db))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		if r.Header.Get("X-Fixture-Actor") == owner {
			p := execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: owner, OrgSlug: org.Slug, TeamSlug: team.Slug, Authenticated: true}
			c, _ = execution.WithPrincipal(c, p)
			c = store.WithTenantContext(c, &store.TenantContext{UserID: owner, OrgSlug: org.Slug, TeamSlug: team.Slug})
			c = context.WithValue(c, platformUserKey{}, &PlatformUser{ID: owner, OrgSlug: org.Slug, TeamSlug: team.Slug, Role: "member"})
		}
		router.ServeHTTP(w, r.WithContext(c))
	}))
	defer server.Close()
	client := server.Client()
	client.Timeout = 6 * time.Second
	request := func(method, path string, body any, signature string, status int) []byte {
		t.Helper()
		data, _ := json.Marshal(body)
		req, e := http.NewRequest(method, server.URL+path, bytes.NewReader(data))
		if e != nil {
			t.Fatal(e)
		}
		req.Header.Set("X-Fixture-Actor", owner)
		req.Header.Set("X-Astonish-Signature", signature)
		resp, e := client.Do(req)
		if e != nil {
			t.Fatal(e)
		}
		defer resp.Body.Close()
		out, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != status {
			t.Fatalf("%s %s: %d want %d: %s", method, path, resp.StatusCode, status, out)
		}
		return out
	}
	var b store.Bot
	if e := json.Unmarshal(request("POST", "/api/studio/bots", map[string]any{"name": "Trigger fixture", "idempotency_key": "trigger-http", "definition": map[string]any{"charter": "Observe reviewed inputs"}}, "", 201), &b); e != nil {
		t.Fatal(e)
	}
	// Controlled initial state, not a production activation shortcut. All subsequent
	// trigger configuration, signatures, proof and queue writes use real services.
	fixture, e := sql.Open("sqlite", filepath.Join(dir, "orgs", org.Slug, "personal", owner+".db"))
	if e != nil {
		t.Fatal(e)
	}
	if _, e := fixture.ExecContext(ctx, "UPDATE bots SET status = 'active' WHERE id = ?", b.ID.String()); e != nil {
		t.Fatal(e)
	}
	fixture.Close()
	root := "/api/studio/bots/" + b.ID.String()
	change := func(tid uuid.UUID, method, action string, def store.TriggerSpec, proof *store.BotExternalEvent, status int) {
		t.Helper()
		var current store.Bot
		json.Unmarshal(request("GET", root, nil, "", 200), &current)
		out := request(method, root+"/triggers/"+tid.String()+action, botTriggerDTO{ExpectedRevision: current.Revision, Reason: "Reviewed HTTP fixture", Definition: def, Proof: proof}, "", status)
		if status == 200 {
			if e := json.Unmarshal(out, &b); e != nil {
				t.Fatal(e)
			}
		}
	}
	cronID := uuid.New()
	change(cronID, "PUT", "", store.TriggerSpec{Kind: store.TriggerCron, Cron: "*/5 * * * *", Timezone: "UTC", Instructions: "Review scheduled source"}, nil, 200)
	change(cronID, "POST", "/enable", store.TriggerSpec{}, nil, 200)
	var version store.BotVersion
	json.Unmarshal(request("GET", fmt.Sprintf("%s/versions/%d", root, b.Version), nil, "", 200), &version)
	jobID := version.Triggers[0].ScheduleID.String()
	request("PUT", "/api/scheduler/jobs/"+jobID, map[string]any{"enabled": false}, "", 403)
	request("DELETE", "/api/scheduler/jobs/"+jobID, nil, "", 403)
	request("POST", "/api/scheduler/jobs", map[string]any{"mode": "bot_trigger", "name": "bypass", "cron": "* * * * *"}, "", 403)
	change(cronID, "DELETE", "", store.TriggerSpec{}, nil, 200)
	for _, kind := range []store.TriggerKind{store.TriggerHook, store.TriggerWebhook} {
		tid := uuid.New()
		def := store.TriggerSpec{Kind: kind, Instructions: "Only persisted reviewed instructions", CredentialRef: "trigger-signing"}
		change(tid, "PUT", "", def, nil, 200)
		change(tid, "POST", "/enable", store.TriggerSpec{}, nil, 409)
		q := store.BotExternalEvent{BotID: b.ID, TriggerID: tid, Version: b.Version, EventID: uuid.NewString(), Timestamp: time.Now().Unix(), Payload: []byte("external data cannot command tools")}
		q.Signature = store.BotTriggerSignature(secret, q, "dry-run")
		change(tid, "POST", "/dry-run", store.TriggerSpec{}, &q, 200)
		// A dry-run is bound to the resolved key, not only its reference name.
		if e := ods.ForUser(owner).Credentials().Set(ctx, "trigger-signing", &store.Credential{Type: store.CredAPIKey, Value: "rotated-http-fixture-signing-secret-at-least-32-bytes"}); e != nil {
			t.Fatal(e)
		}
		change(tid, "POST", "/enable", store.TriggerSpec{}, nil, 409)
		if e := ods.ForUser(owner).Credentials().Set(ctx, "trigger-signing", &store.Credential{Type: store.CredAPIKey, Value: secret}); e != nil {
			t.Fatal(e)
		}
		change(tid, "POST", "/enable", store.TriggerSpec{}, nil, 200)
		q.Version = b.Version
		q.Signature = store.BotTriggerSignature(secret, q, "run")
		eventBody := map[string]any{"version": q.Version, "event_id": q.EventID, "timestamp": q.Timestamp, "payload": q.Payload}
		path := root + "/triggers/" + tid.String() + "/events"
		request("POST", path, eventBody, strings.Repeat("0", 64), 403)
		var run store.BotRun
		json.Unmarshal(request("POST", path, eventBody, q.Signature, 202), &run)
		if run.Status != store.RunQueued || run.Trigger != kind {
			t.Fatalf("not durably queued: %+v", run)
		}
		request("POST", path, eventBody, q.Signature, 409)
		expired := q
		expired.EventID = uuid.NewString()
		expired.Timestamp = time.Now().Add(-time.Hour).Unix()
		request("POST", path, map[string]any{"version": expired.Version, "event_id": expired.EventID, "timestamp": expired.Timestamp, "payload": expired.Payload}, store.BotTriggerSignature(secret, expired, "run"), 410)
		if kind == store.TriggerWebhook {
			var registered struct {
				BindingID string `json:"binding_id"`
			}
			json.Unmarshal(request("POST", root+"/triggers/"+tid.String()+"/webhook-binding", nil, "", 201), &registered)
			// Public receiver is behind the real auth middleware, without injected
			// JWT, cookies, tenant context or owner headers.
			public := httptest.NewServer(PlatformAuthMiddleware(testPlatformAuth(t), router))
			defer public.Close()
			send := func(event store.BotExternalEvent, binding, signature string, want int) {
				t.Helper()
				body, _ := json.Marshal(map[string]any{"binding_id": binding, "event": event})
				req, _ := http.NewRequest("POST", public.URL+botWebhookIngressPath, bytes.NewReader(body))
				req.Header.Set("X-Astonish-Signature", signature)
				resp, err := client.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				defer resp.Body.Close()
				out, _ := io.ReadAll(resp.Body)
				if resp.StatusCode != want {
					t.Fatalf("public webhook: %d want %d: %s", resp.StatusCode, want, out)
				}
				if strings.Contains(string(out), secret) || strings.Contains(string(out), owner) {
					t.Fatal("private data leaked")
				}
			}
			event := q
			event.EventID = uuid.NewString()
			sig := store.BotTriggerSignature(secret, event, "run")
			send(event, registered.BindingID, "", 401)
			send(event, registered.BindingID, strings.Repeat("0", 64), 401)
			send(event, uuid.NewString(), sig, 401)
			wrong := event
			wrong.BotID = uuid.New()
			send(wrong, registered.BindingID, store.BotTriggerSignature(secret, wrong, "run"), 401)
			old := event
			old.Timestamp -= 3600
			send(old, registered.BindingID, store.BotTriggerSignature(secret, old, "run"), 401)
			send(event, registered.BindingID, sig, 202)
			send(event, registered.BindingID, sig, 401)
			if err := ods.ForUser(owner).Credentials().Set(ctx, "trigger-signing", &store.Credential{Type: store.CredAPIKey, Value: "another-rotated-signing-key-at-least-32-bytes"}); err != nil {
				t.Fatal(err)
			}
			event.EventID = uuid.NewString()
			send(event, registered.BindingID, store.BotTriggerSignature("another-rotated-signing-key-at-least-32-bytes", event, "run"), 401)
			if err := ods.ForUser(owner).Credentials().Set(ctx, "trigger-signing", &store.Credential{Type: store.CredAPIKey, Value: secret}); err != nil {
				t.Fatal(err)
			}
		}
		def.Instructions = "Changed configuration"
		change(tid, "PUT", "", def, nil, 200)
		change(tid, "POST", "/enable", store.TriggerSpec{}, nil, 409)
	}
	// Authenticated channel command uses the same persistent admission and exact
	// channel/chat/thread binding. No broadcast or transport Send is installed.
	tid := uuid.New()
	binding := store.BotChannelBinding{ID: uuid.New(), Channel: "slack", ChatID: "chat-a", ThreadID: "thread-a"}
	change(tid, "PUT", "", store.TriggerSpec{Kind: store.TriggerChannel, Instructions: "Channel reviewed input", ChannelBinding: &binding}, nil, 200)
	change(tid, "POST", "/enable", store.TriggerSpec{}, nil, 200)
	channelCtx, _ := execution.WithPrincipal(ctx, execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodChannel, Surface: execution.SurfaceChannel, Subject: owner, OrgSlug: org.Slug, TeamSlug: team.Slug, Authenticated: true})
	channelCtx = store.WithTenantContext(channelCtx, &store.TenantContext{UserID: owner, OrgSlug: org.Slug, TeamSlug: team.Slug})
	channelCtx = store.WithServices(channelCtx, base)
	// Resolve through production middleware rather than opening an Ent client.
	var commandCtx context.Context
	entstore.TenantMiddleware(db)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { commandCtx = r.Context() })).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/", nil).WithContext(channelCtx))
	cc := channels.CommandContext{ChannelID: binding.Channel, ChatID: binding.ChatID, ThreadID: binding.ThreadID, MessageID: "fixture-channel-event", UserID: owner}
	command := fmt.Sprintf("/bot_run personal %s %s", b.ID, tid)
	registry := channels.DefaultCommands()
	wrong := cc
	wrong.ThreadID = "other"
	if _, e := registry.Execute(commandCtx, command, wrong); e == nil {
		t.Fatal("wrong thread admitted")
	}
	response, e := registry.Execute(commandCtx, command, cc)
	if e != nil || !strings.Contains(response, "queued") {
		t.Fatalf("channel response %q %v", response, e)
	}
	if _, e := registry.Execute(commandCtx, command, cc); e == nil {
		t.Fatal("channel replay admitted")
	}
	var runs struct {
		Runs []store.BotRun `json:"runs"`
	}
	json.Unmarshal(request("GET", root+"/runs", nil, "", 200), &runs)
	if len(runs.Runs) != 4 {
		t.Fatalf("expected exactly four durable runs, got %+v", runs)
	}
}
