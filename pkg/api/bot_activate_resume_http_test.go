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
	"testing"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/entstore"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
)

// Real HTTP, production routes, tenant middleware and SQLite. Only identity and
// activation readiness are controlled; no worker or model runs in this test.
func TestBotActivateResumeHTTP(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	base, db, err := entstore.NewPlatformServices(ctx, entstore.Config{DSN: filepath.Join(dir, "platform.db"), DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	org := &store.Organization{ID: uuid.NewString(), Slug: "phase2-http", Name: "Phase Two", Status: "active"}
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
	owner, reader := uuid.NewString(), uuid.NewString()
	for _, id := range []string{owner, reader} {
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
	request := func(t *testing.T, method, path, actor, body string, status int) []byte {
		t.Helper()
		req, err := http.NewRequest(method, server.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("X-Test-Actor", actor)
		req.Header.Set("Content-Type", "application/json")
		resp, err := server.Client().Do(req)
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

	db.SetBotActivationReadiness(func(context.Context, store.Bot, store.BotVersion) error { return nil })
	for _, scope := range []string{"personal", "team"} {
		t.Run(scope, func(t *testing.T) {
			suffix := "?scope=" + scope
			var b store.Bot
			decode := func(raw []byte) {
				t.Helper()
				if err := json.Unmarshal(raw, &b); err != nil {
					t.Fatal(err)
				}
			}
			decode(request(t, "POST", "/api/studio/bots"+suffix, owner, fmt.Sprintf(`{"name":"Explicit lifecycle","idempotency_key":%q,"definition":{"charter":"Observe only"}}`, uuid.NewString()), 201))
			path := "/api/studio/bots/" + b.ID.String()
			lifecycle := func(action string, rev int64, actor string, status int) []byte {
				t.Helper()
				return request(t, "POST", path+"/"+action+suffix, actor, fmt.Sprintf(`{"expected_revision":%d,"reason":"Explicit reviewed lifecycle"}`, rev), status)
			}
			for _, from := range []store.BotStatus{store.BotDraft, store.BotPaused, store.BotStopped} {
				t.Logf("explicit lifecycle from %s", from)
				func() {
					if from == store.BotPaused {
						decode(lifecycle("pause", b.Revision, owner, 200))
					}
					if from == store.BotStopped {
						decode(lifecycle("stop", b.Revision, owner, 200))
					}
					if b.Status != from {
						t.Fatalf("state=%s want %s", b.Status, from)
					}
					rev := b.Revision
					versionBeforeResume := b.Version
					lifecycle("activate", rev, "", 401)
					denied := 403
					if scope == "personal" {
						denied = 404
					}
					lifecycle("activate", rev, reader, denied)
					wrong := "personal"
					if scope == "personal" {
						wrong = "team"
					}
					request(t, "POST", path+"/activate?scope="+wrong, owner, fmt.Sprintf(`{"expected_revision":%d,"reason":"Explicit"}`, rev), 404)
					if rev > 1 {
						lifecycle("activate", rev-1, owner, 409)
					}
					decode(lifecycle("activate", rev, owner, 200))
					if b.Status != store.BotActive || b.Revision != rev+1 || b.Version != versionBeforeResume {
						t.Fatalf("activation=%+v", b)
					}
					// Replaying stale consent or re-activating an active Bot never adds an event.
					lifecycle("activate", rev, owner, 409)
					lifecycle("activate", b.Revision, owner, 409)
					decode(request(t, "GET", path+suffix, owner, "", 200))
					if b.Status != store.BotActive || b.Revision != rev+1 || b.Version != versionBeforeResume {
						t.Fatalf("duplicate mutated state: %+v", b)
					}
					if from == store.BotDraft {
						tid := uuid.New()
						decode(request(t, "PUT", path+"/triggers/"+tid.String()+suffix, owner, fmt.Sprintf(`{"expected_revision":%d,"reason":"Reviewed cron","definition":{"kind":"cron","cron":"* * * * *","timezone":"UTC","instructions":"Observe only"}}`, b.Revision), 200))
						decode(request(t, "POST", path+"/triggers/"+tid.String()+"/enable"+suffix, owner, fmt.Sprintf(`{"expected_revision":%d,"reason":"Explicit recurrence"}`, b.Revision), 200))
					} else {
						var version store.BotVersion
						if err := json.Unmarshal(request(t, "GET", fmt.Sprintf("%s/versions/%d%s", path, b.Version, suffix), owner, "", 200), &version); err != nil {
							t.Fatal(err)
						}
						if len(version.Triggers) != 1 || !version.Triggers[0].Enabled {
							t.Fatal("resume changed configured recurrence")
						}
						var job store.ScheduledJob
						if err := json.Unmarshal(request(t, "GET", "/api/scheduler/jobs/"+version.Triggers[0].ScheduleID.String()+suffix, owner, "", 200), &job); err != nil {
							t.Fatal(err)
						}
						if job.Enabled || job.NextRun != nil || job.Payload.BotVersion != b.Version {
							t.Fatalf("resume re-enabled recurrence: %+v", job)
						}
					}
				}()
			}
			decode(lifecycle("pause", b.Revision, owner, 200))
			db.SetBotActivationReadiness(nil)
			lifecycle("activate", b.Revision, owner, 503)
			decode(request(t, "GET", path+suffix, owner, "", 200))
			if b.Status != store.BotPaused {
				t.Fatal("readiness failure resumed Bot")
			}
			db.SetBotActivationReadiness(func(context.Context, store.Bot, store.BotVersion) error { return nil })
			// A concurrent explicit stop during readiness invalidates the reviewed
			// revision. The final transaction must not overwrite it with active.
			reviewedRevision := b.Revision
			db.SetBotActivationReadiness(func(context.Context, store.Bot, store.BotVersion) error {
				decode(lifecycle("stop", reviewedRevision, owner, 200))
				return nil
			})
			lifecycle("activate", reviewedRevision, owner, 409)
			decode(request(t, "GET", path+suffix, owner, "", 200))
			if b.Status != store.BotStopped || b.Revision != reviewedRevision+1 {
				t.Fatalf("concurrent stop overwritten: %+v", b)
			}
			db.SetBotActivationReadiness(func(context.Context, store.Bot, store.BotVersion) error { return nil })
		})
	}
}
