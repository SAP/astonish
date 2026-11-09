package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/entstore"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
)

// Real HTTP + scoped SQLite; only authentication is controlled by this fixture.
func TestBotUnfinishedIntakeHTTPAcceptance(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	cfg := entstore.Config{DSN: filepath.Join(dir, "platform.db"), DataDir: dir}
	base, db, err := entstore.NewPlatformServices(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	org := &store.Organization{ID: uuid.NewString(), Slug: "intake-http", Name: "Intake", Status: "active"}
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
	team := &store.Team{ID: uuid.NewString(), Slug: "team-a", Name: "Team", SchemaName: "team_team-a"}
	if err := ods.Teams().CreateTeam(ctx, team); err != nil {
		t.Fatal(err)
	}
	owner, other := uuid.NewString(), uuid.NewString()
	for _, id := range []string{owner, other} {
		if err := db.Users().Create(ctx, &store.User{ID: id, Email: id + "@example.test", DisplayName: "Tester", Status: "active"}); err != nil {
			t.Fatal(err)
		}
		if err := db.Organizations().AddMember(ctx, id, org.ID, "member"); err != nil {
			t.Fatal(err)
		}
		if err := ods.Teams().AddMember(ctx, &store.TeamMembership{UserID: id, TeamID: team.ID, Role: "admin"}); err != nil {
			t.Fatal(err)
		}
	}
	start := func() *httptest.Server {
		router := mux.NewRouter()
		RegisterRoutes(router, base, db, entstore.TenantMiddleware(db))
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			c := r.Context()
			id := r.Header.Get("X-Test-Actor")
			if id != "" {
				var err error
				c, err = execution.WithPrincipal(c, execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: id, OrgSlug: org.Slug, TeamSlug: team.Slug, Authenticated: true})
				if err != nil {
					t.Error(err)
					http.Error(w, "fixture", 500)
					return
				}
				c = context.WithValue(c, platformUserKey{}, &PlatformUser{ID: id, OrgSlug: org.Slug, TeamSlug: team.Slug, Role: "admin"})
				c = store.WithTenantContext(c, &store.TenantContext{UserID: id, OrgSlug: org.Slug, TeamSlug: team.Slug})
			}
			router.ServeHTTP(w, r.WithContext(c))
		}))
	}
	server := start()
	defer func() { server.Close() }()
	request := func(method, path, actor, body string) (int, []byte) {
		q, err := http.NewRequest(method, server.URL+path, strings.NewReader(body))
		if err != nil {
			t.Error(err)
			return 0, nil
		}
		q.Header.Set("X-Test-Actor", actor)
		q.Header.Set("Content-Type", "application/json")
		response, err := server.Client().Do(q)
		if err != nil {
			t.Error(err)
			return 0, nil
		}
		defer response.Body.Close()
		if strings.Contains(path, "/bots/intake/") && response.Header.Get("Cache-Control") != "no-store" {
			t.Errorf("missing no-store for %s: %d", path, response.StatusCode)
		}
		data, err := io.ReadAll(response.Body)
		if err != nil {
			t.Error(err)
		}
		return response.StatusCode, data
	}
	check := func(method, path, actor, body string, want int) []byte {
		t.Helper()
		status, data := request(method, path, actor, body)
		if status != want {
			t.Fatalf("%s %s: %d want %d: %s", method, path, status, want, data)
		}
		return data
	}
	put := func(rev int, key, name string) string {
		return fmt.Sprintf(`{"expected_revision":%d,"idempotency_key":%q,"content":{"mode":"blank","name":%q}}`, rev, key, name)
	}
	check("GET", "/api/studio/bots/intake/not-a-uuid", owner, "", 400)
	check("GET", "/api/studio/bots/intake/"+uuid.NewString()+"?scope=org", owner, "", 400)
	check("GET", "/api/studio/bots/intake/"+uuid.NewString()+"?scope=team&scope=personal", owner, "", 400)
	ids := map[string]string{"personal": uuid.NewString(), "team": uuid.NewString()}
	for scope, id := range ids {
		path := "/api/studio/bots/intake/" + id + "?scope=" + scope
		check("GET", path, "", "", 401)
		check("GET", path, owner, "", 404)
		for _, body := range []string{`null`, `{} {}`, `{"owner_id":"forged"}`, `{"content":{"mode":"blank","runtime":true}}`, put(0, "big", strings.Repeat("x", store.BotIntakeMaxBytes))} {
			check("PUT", path, owner, body, 400)
		}
		first := check("PUT", path, owner, put(0, "first", "Unfinished"), 200)
		retry := check("PUT", path, owner, put(0, "first", "Unfinished"), 200)
		if string(first) != string(retry) {
			t.Fatal("replay changed response")
		}
		check("PUT", path, owner, put(0, "first", "Changed"), 409)
		check("GET", path, other, "", 404)
		check("PUT", path, other, put(1, "attack", "stolen"), 409)
		check("DELETE", path, other, `{"expected_revision":1,"idempotency_key":"attack"}`, 404)
	}
	// Race unique creation and save-vs-discard through production routed pools.
	for scope := range ids {
		path := "/api/studio/bots/intake/" + uuid.NewString() + "?scope=" + scope
		runPair := func(methodA, bodyA, methodB, bodyB string) map[int]int {
			statuses := make(chan int, 2)
			var wg sync.WaitGroup
			for _, q := range []struct{ method, body string }{{methodA, bodyA}, {methodB, bodyB}} {
				wg.Add(1)
				go func(method, body string) {
					defer wg.Done()
					status, _ := request(method, path, owner, body)
					statuses <- status
				}(q.method, q.body)
			}
			wg.Wait()
			close(statuses)
			counts := map[int]int{}
			for status := range statuses {
				counts[status]++
			}
			return counts
		}
		if counts := runPair("PUT", put(0, "concurrent-create", "draft"), "PUT", put(0, "concurrent-create", "draft")); counts[200] != 2 {
			t.Fatalf("unique insert retry race: %v", counts)
		}
		counts := runPair("PUT", put(1, "concurrent-save", "edited"), "DELETE", `{"expected_revision":1,"idempotency_key":"concurrent-discard"}`)
		if counts[409] != 1 || counts[200]+counts[204] != 1 {
			t.Fatalf("save/discard CAS race: %v", counts)
		}
		if counts[200] == 1 {
			check("DELETE", path, owner, `{"expected_revision":2,"idempotency_key":"cleanup"}`, 204)
		}
		check("GET", path, owner, "", 404)
		check("PUT", path, owner, put(0, "concurrent-create", "draft"), 409)
	}
	server.Close()
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	base, db, err = entstore.NewPlatformServices(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	server = start()
	for scope, id := range ids {
		path := "/api/studio/bots/intake/" + id + "?scope=" + scope
		var saved store.BotUnfinishedIntake
		if err := json.Unmarshal(check("GET", path, owner, "", 200), &saved); err != nil {
			t.Fatal(err)
		}
		if saved.Content == nil || saved.Content.Name != "Unfinished" || saved.Revision != 1 || saved.Scope != store.BotScope(scope) {
			t.Fatalf("reopen lost intake: %+v", saved)
		}
		var wg sync.WaitGroup
		statuses := make(chan int, 2)
		for _, key := range []string{"race-a", "race-b"} {
			wg.Add(1)
			go func(key string) {
				defer wg.Done()
				status, _ := request("PUT", path, owner, put(1, key, key))
				statuses <- status
			}(key)
		}
		wg.Wait()
		close(statuses)
		counts := map[int]int{}
		for status := range statuses {
			counts[status]++
		}
		if counts[200] != 1 || counts[409] != 1 {
			t.Fatalf("CAS race: %v", counts)
		}
		check("PUT", path, owner, put(1, "stale", "stale"), 409)
		check("DELETE", path, owner, `{"expected_revision":1,"idempotency_key":"stale-delete"}`, 409)
		check("DELETE", path, owner, `{"expected_revision":2,"idempotency_key":"discard"}`, 204)
		check("DELETE", path, owner, `{"expected_revision":2,"idempotency_key":"discard"}`, 204)
		check("DELETE", path, owner, `{"expected_revision":2,"idempotency_key":"changed-discard"}`, 409)
		check("GET", path, owner, "", 404)
		check("PUT", path, owner, put(0, "first", "Unfinished"), 409)
		check("PUT", path, owner, put(3, "resurrect", "Forbidden"), 409)
		if body := check("GET", "/api/studio/bots?scope="+scope, owner, "", 200); strings.TrimSpace(string(body)) != `{"bots":[]}` {
			t.Fatalf("intake created Bot: %s", body)
		}
	}
	server.Close()
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	base, db, err = entstore.NewPlatformServices(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	server = start()
	for scope, id := range ids {
		path := "/api/studio/bots/intake/" + id + "?scope=" + scope
		check("GET", path, owner, "", 404)
		check("PUT", path, owner, put(0, "first", "Unfinished"), 409)
		check("DELETE", path, owner, `{"expected_revision":2,"idempotency_key":"discard"}`, 204)
	}

	// Existing JWT/principal and cached scope cannot outlive revoked org membership.
	if err := db.Organizations().RemoveMember(ctx, owner, org.ID); err != nil {
		t.Fatal(err)
	}
	for scope, id := range ids {
		path := "/api/studio/bots/intake/" + id + "?scope=" + scope
		check("GET", path, owner, "", 403)
		check("PUT", path, owner, put(0, "revoked", "no"), 403)
		check("DELETE", path, owner, `{"expected_revision":2,"idempotency_key":"discard"}`, 403)
	}
}
