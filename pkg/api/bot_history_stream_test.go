package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/entstore"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
	adksession "google.golang.org/adk/session"
)

type botTranscriptFixture struct {
	*botSessionMetaFixture
	events  []*adksession.Event
	readErr error
	owner   string
	viewer  string
	reads   int
}

func (s *botTranscriptFixture) ReadTranscriptEvents(ctx context.Context, app, owner, sid string) ([]*adksession.Event, error) {
	s.reads++
	p, _ := execution.PrincipalFromContext(ctx)
	if app != studioChatAppName || owner != s.owner || p.Subject != s.viewer || sid != s.meta.ID {
		return nil, errors.New("wrong execution attribution")
	}
	return s.events, s.readErr
}

func TestBotHistoryStreamStates(t *testing.T) {
	owner, viewer, id, sid := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	repo := &botSessionFixtureStore{b: store.Bot{ID: id, OwnerID: owner, Scope: store.BotScopeTeam, HomeSessionID: sid}}
	meta := &store.SessionMeta{ID: sid.String(), UserID: owner.String(), BotID: id.String(), Purpose: store.SessionPurposeBotHome, Retention: store.SessionRetentionBot}
	ss := &botTranscriptFixture{botSessionMetaFixture: &botSessionMetaFixture{meta: meta}, owner: owner.String(), viewer: viewer.String()}
	svc := &store.Services{Bots: repo, Sessions: ss}
	ctx, err := execution.WithPrincipal(context.Background(), execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: viewer.String(), OrgSlug: "org", TeamSlug: "team", Authenticated: true})
	if err != nil {
		t.Fatal(err)
	}
	ctx = store.WithTenantContext(ctx, &store.TenantContext{UserID: viewer.String(), OrgSlug: "org", TeamSlug: "team"})
	ctx = store.WithServices(ctx, svc)
	router := mux.NewRouter()
	registerBotRoutes(router)
	request := func(status int) string {
		t.Helper()
		r := httptest.NewRequest("GET", "/api/studio/bots/"+id.String()+"/sessions/"+sid.String()+"/stream?scope=team", nil).WithContext(ctx)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, r)
		if w.Code != status {
			t.Fatalf("status %d want %d: %s", w.Code, status, w.Body.String())
		}
		if status == 200 && (w.Header().Get("Content-Type") != "text/event-stream" || w.Header().Get("Cache-Control") != "no-store") {
			t.Fatal("bad SSE headers")
		}
		return w.Body.String()
	}
	if body := request(200); !strings.Contains(body, `"count":0`) || !strings.Contains(body, "event: done") {
		t.Fatal(body)
	}
	ss.events = []*adksession.Event{{ID: "saved-event", Author: "execution-owner"}}
	if body := request(200); !strings.Contains(body, "saved-event") || !strings.Contains(body, "event: transcript") {
		t.Fatal(body)
	}
	ss.readErr = errors.New("storage failed")
	if body := request(500); strings.Contains(body, "saved-event") {
		t.Fatal("error leaked transcript")
	}
	ss.readErr = nil
	before := ss.reads
	meta.BotID = uuid.NewString()
	request(404)
	meta.BotID = id.String()
	repo.denied = true
	request(404)
	repo.denied = false
	if ss.reads != before {
		t.Fatal("denied read reached transcript store")
	}
	meta.Purpose = store.SessionPurposeBotRun
	meta.BotRunID = uuid.NewString()
	repo.run = store.BotRun{ID: uuid.MustParse(meta.BotRunID), BotID: id, SessionID: sid}
	request(200)
	repo.run.SessionID = uuid.New()
	request(404)
	if getChatRunnerRegistry().Get(sid.String()) != nil {
		t.Fatal("read started runner")
	}
}

// Real HTTP + production routing/tenant middleware + SQLite, with no runtime
// installed. Authentication is a controlled test fixture, never viewer credentials.
func TestBotHistoryStreamHTTPAcceptance(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	base, db, err := entstore.NewPlatformServices(ctx, entstore.Config{DSN: filepath.Join(dir, "platform.db"), DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	org := &store.Organization{ID: uuid.NewString(), Slug: "stream-org", Name: "Stream", Status: "active"}
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
	team := &store.Team{ID: uuid.NewString(), Slug: "stream-team", Name: "Stream", SchemaName: "team_stream-team"}
	if err := ods.Teams().CreateTeam(ctx, team); err != nil {
		t.Fatal(err)
	}
	owner, reader := uuid.NewString(), uuid.NewString()
	for _, id := range []string{owner, reader} {
		if err := db.Users().Create(ctx, &store.User{ID: id, Email: id + "@example.test", DisplayName: "Reader", Status: "active"}); err != nil {
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
	server.Client().Timeout = 10 * time.Second
	request := func(method, path, actor, body string, status int) string {
		t.Helper()
		req, err := http.NewRequest(method, server.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("X-Test-Actor", actor)
		req.Header.Set("Content-Type", "application/json")
		res, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		data, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatal(err)
		}
		if res.StatusCode != status {
			t.Fatalf("%s %s: %d want %d: %s", method, path, res.StatusCode, status, data)
		}
		if status == 200 && strings.Contains(path, "/stream") && res.Header.Get("Content-Type") != "text/event-stream" {
			t.Fatal("not SSE")
		}
		return string(data)
	}
	var teamStream string
	for _, scope := range []string{"personal", "team"} {
		suffix := "?scope=" + scope
		var b store.Bot
		data := request("POST", "/api/studio/bots"+suffix, owner, `{"name":"Replay","idempotency_key":"replay","definition":{"charter":"Observe"}}`, 201)
		if err := json.Unmarshal([]byte(data), &b); err != nil {
			t.Fatal(err)
		}
		path := "/api/studio/bots/" + b.ID.String()
		stream := path + "/sessions/" + b.HomeSessionID.String() + "/stream" + suffix
		if body := request("GET", stream, owner, "", 200); !strings.Contains(body, `"count":0`) {
			t.Fatal(body)
		}
		var ss store.SessionStore
		if scope == "team" {
			ss = ods.ForTeam(team.Slug).Sessions()
			teamStream = stream
		} else {
			ss = ods.ForUser(owner).Sessions()
		}
		if err := ss.AppendFleetEvent(ctx, b.HomeSessionID.String(), &adksession.Event{ID: "persisted-" + scope, Author: "owner", Timestamp: time.Now()}); err != nil {
			t.Fatal(err)
		}
		if body := request("GET", stream, owner, "", 200); !strings.Contains(body, "persisted-"+scope) || !strings.Contains(body, "event: done") {
			t.Fatal(body)
		}
		if scope == "team" {
			request("GET", stream, reader, "", 200)
		} else {
			request("GET", stream, reader, "", 404)
		}
		request("GET", stream, "", "", 401)
		otherScope := "team"
		if scope == "team" {
			otherScope = "personal"
		}
		request("GET", strings.Replace(stream, suffix, "?scope="+otherScope, 1), owner, "", 404)
		request("GET", path+"/sessions/"+uuid.NewString()+"/stream"+suffix, owner, "", 404)
		// Neither transcript replay nor its SSE completion creates runtime work.
		if getChatRunnerRegistry().Get(b.HomeSessionID.String()) != nil {
			t.Fatal("runner started")
		}
		ownerCtx, err := execution.WithPrincipal(ctx, execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: owner, OrgSlug: org.Slug, TeamSlug: team.Slug, Authenticated: true})
		if err != nil {
			t.Fatal(err)
		}
		var bots store.BotStore
		if scope == "team" {
			bots = ods.ForTeam(team.Slug).Bots()
		} else {
			bots = ods.ForUser(owner).Bots()
		}
		runs, err := bots.ListRuns(ownerCtx, b.ID, store.BotPage{})
		if err != nil || len(runs) != 0 {
			t.Fatalf("stream started runs: %+v %v", runs, err)
		}
	}
	if err := ods.Teams().RemoveMember(ctx, reader, team.ID); err != nil {
		t.Fatal(err)
	}
	request("GET", teamStream, reader, "", 404)
	request("GET", teamStream, owner, "", 200)
}
