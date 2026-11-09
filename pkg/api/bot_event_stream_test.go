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

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/entstore"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
	adksession "google.golang.org/adk/session"
)

type botReplayFixture struct {
	*botSessionMetaFixture
	read func() ([]store.BotTranscriptEvent, error)
}

func (s *botReplayFixture) ReadBotTranscript(context.Context, uuid.UUID, int64, int) ([]store.BotTranscriptEvent, error) {
	return s.read()
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
				if err := ss.AppendFleetEvent(ctx, b.HomeSessionID.String(), &adksession.Event{ID: id, Author: "execution-owner", Timestamp: time.Now()}); err != nil {
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
			if first.event != botTranscriptEvent || !strings.Contains(first.data, "first") {
				t.Fatalf("live: %+v", first)
			}
			live.Body.Close()
			appendEvent("second")
			resumed := request("GET", path, actor, "", first.id, 200)
			defer resumed.Body.Close()
			rr := bufio.NewReader(resumed.Body)
			second := readBotSSE(t, rr)
			if second.id == first.id || !strings.Contains(second.data, "second") {
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
			if f := readBotSSE(t, fr); f.id != first.id || !strings.Contains(f.data, "first") {
				t.Fatal("snapshot differs", f)
			}
			full.Body.Close()

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
				if frame.event != botTranscriptEvent || position <= previous {
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
		})
	}
}
