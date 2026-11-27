package api

import (
	"context"
	"encoding/json"
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

type reviewedHTTPFixture struct {
	ctx      context.Context
	bots     func(string) store.BotStore
	policy   *store.BotMandatePolicyResolver
	owner    string
	binding  store.BotChannelBinding
	request  func(string, string, any, int) []byte
	sessions func(string) store.SessionStore
	jobs     func(string) store.SchedulerStore
}

func newReviewedHTTPFixture(t *testing.T, roles ...string) reviewedHTTPFixture {
	role := "member"
	if len(roles) > 0 {
		role = roles[0]
	}
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	base, db, err := entstore.NewPlatformServices(ctx, entstore.Config{DSN: filepath.Join(dir, "platform.db"), DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	org := &store.Organization{ID: uuid.NewString(), Slug: "reviewed-http", Name: "Reviewed", Status: "active"}
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
	owner := uuid.NewString()
	if err := db.Users().Create(ctx, &store.User{ID: owner, Email: owner + "@example.test", DisplayName: "Owner", Status: "active"}); err != nil {
		t.Fatal(err)
	}
	if err := db.Organizations().AddMember(ctx, owner, org.ID, "member"); err != nil {
		t.Fatal(err)
	}
	if err := ods.Teams().AddMember(ctx, &store.TeamMembership{UserID: owner, TeamID: team.ID, Role: role}); err != nil {
		t.Fatal(err)
	}
	binding := store.BotChannelBinding{ID: uuid.New(), Channel: "telegram", ChatID: "123456"}
	if err := db.UserChannels().Link(ctx, &store.UserChannel{ID: binding.ID.String(), UserID: owner, ChannelType: binding.Channel, ExternalID: binding.ChatID, Enabled: true, Verified: true}); err != nil {
		t.Fatal(err)
	}
	var policy store.BotMandatePolicyResolver
	router := mux.NewRouter()
	RegisterRoutes(router, base, db, entstore.TenantMiddleware(db))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Identity is fixed on the server. Public actor headers must have no effect.
		c, err := execution.WithPrincipal(r.Context(), execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: owner, OrgSlug: org.Slug, TeamSlug: team.Slug, Authenticated: true})
		if err != nil {
			t.Fatal(err)
		}
		c = context.WithValue(c, platformUserKey{}, &PlatformUser{ID: owner, OrgSlug: org.Slug, TeamSlug: team.Slug, Role: role})
		c = store.WithTenantContext(c, &store.TenantContext{UserID: owner, OrgSlug: org.Slug, TeamSlug: team.Slug})
		if policy != nil {
			c = store.WithBotMandateAdmission(c, policy)
		}
		router.ServeHTTP(w, r.WithContext(c))
	}))
	t.Cleanup(server.Close)
	request := func(method, path string, body any, status int) []byte {
		t.Helper()
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		if raw, ok := body.(string); ok {
			data = []byte(raw)
		}
		req, err := http.NewRequest(method, server.URL+path, strings.NewReader(string(data)))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Actor-ID", uuid.NewString())
		req.Header.Set("X-User-ID", uuid.NewString())
		res, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		result, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatal(err)
		}
		if res.StatusCode != status {
			t.Fatalf("%s %s: %d want %d: %s", method, path, res.StatusCode, status, result)
		}
		return result
	}
	seedCtx, err := execution.WithPrincipal(ctx, execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: owner, OrgSlug: org.Slug, TeamSlug: team.Slug, Authenticated: true})
	if err != nil {
		t.Fatal(err)
	}
	seedCtx = store.WithTenantContext(seedCtx, &store.TenantContext{UserID: owner, OrgSlug: org.Slug, TeamSlug: team.Slug})
	return reviewedHTTPFixture{ctx: seedCtx, bots: func(scope string) store.BotStore {
		if scope == "team" {
			return ods.ForTeam(team.Slug).Bots()
		}
		return ods.ForUser(owner).Bots()
	}, policy: &policy, owner: owner, binding: binding, request: request, sessions: func(scope string) store.SessionStore {
		if scope == "team" {
			return ods.ForTeam(team.Slug).Sessions()
		}
		return ods.ForUser(owner).Sessions()
	}, jobs: func(scope string) store.SchedulerStore {
		if scope == "team" {
			return ods.ForTeam(team.Slug).ScheduledJobs()
		}
		return ods.ForUser(owner).ScheduledJobs()
	}}
}
func (f reviewedHTTPFixture) createExisting(t *testing.T, scope, body string) []byte {
	t.Helper()
	if scope != "team" {
		return f.request("POST", "/api/studio/bots?scope="+scope, body, 201)
	}
	var q botCreateDTO
	if err := json.Unmarshal([]byte(body), &q); err != nil {
		t.Fatal(err)
	}
	creationCtx := f.ctx
	if f.policy != nil && *f.policy != nil {
		creationCtx = store.WithBotMandateAdmission(creationCtx, *f.policy)
	}
	v, err := store.NewBotVersionForCreation(creationCtx, uuid.MustParse(f.owner))
	if err != nil {
		t.Fatal(err)
	}
	v.Charter = q.Definition.Charter
	if v.Charter == "" {
		v.Charter = "Take responsibility for the owner's goals and deliver useful, verified outcomes. Perform routine work within your authorized mandate and existing permissions without extra review. Escalate genuine ambiguity or actions outside that scope for clarification or authorization. A request or this charter does not itself grant permission for external side effects."
	}
	b, err := f.bots(scope).Create(f.ctx, store.BotCreateRequest{Name: q.Name, OwnerID: uuid.MustParse(f.owner), Version: v, IdempotencyKey: q.IdempotencyKey})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestBotReviewedConfigurationHTTP(t *testing.T) {
	f := newReviewedHTTPFixture(t)
	for _, scope := range []string{"personal", "team"} {
		t.Run(scope, func(t *testing.T) {
			suffix := "?scope=" + scope
			var b store.Bot
			if err := json.Unmarshal(f.createExisting(t, scope, `{"name":"Reviewed","idempotency_key":"config","definition":{"charter":"Observe"}}`), &b); err != nil {
				t.Fatal(err)
			}
			q := store.BotConfigurationRequest{ExpectedRevision: 1, ExpectedVersion: 1, Reviewed: true, Reason: "Owner reviewed grants", Autonomy: store.DefaultBotVersion().Autonomy, Budget: store.BudgetSpec{MaxRuntimeSeconds: 45}, Delivery: store.DeliveryPolicy{Notify: store.NotifyAlways, Bindings: []store.BotChannelBinding{f.binding}}}
			q.Autonomy.CanPublish = true
			q.Autonomy.CanContactPeople = true
			path := "/api/studio/bots/" + b.ID.String()
			f.request("PUT", path+"/configuration"+suffix, `{"actor_id":"forged"}`, 400)
			q.Reviewed = false
			f.request("PUT", path+"/configuration"+suffix, q, 403)
			q.Reviewed = true
			// Sensitive grants cannot be changed while an Active Bot may run.
			f.request("PUT", path+"/configuration"+suffix, q, 409)
			if err := json.Unmarshal(f.request("POST", path+"/pause"+suffix, map[string]any{"expected_revision": b.Revision, "reason": "Review sensitive policy"}, 200), &b); err != nil {
				t.Fatal(err)
			}
			q.ExpectedRevision = b.Revision
			var saved store.Bot
			if err := json.Unmarshal(f.request("PUT", path+"/configuration"+suffix, q, 200), &saved); err != nil {
				t.Fatal(err)
			}
			if saved.Status != store.BotPaused || saved.Revision != 3 || saved.Version != 2 || saved.OwnerID.String() != f.owner {
				t.Fatalf("configuration activated or changed owner: %+v", saved)
			}
			f.request("PUT", path+"/configuration"+suffix, q, 409)
			q.ExpectedRevision = saved.Revision
			f.request("PUT", path+"/configuration"+suffix, q, 409)
			var version store.BotVersion
			if err := json.Unmarshal(f.request("GET", path+"/versions/2"+suffix, nil, 200), &version); err != nil {
				t.Fatal(err)
			}
			if !version.Autonomy.CanPublish || version.Budget.MaxRuntimeSeconds != 45 || len(version.Delivery.Bindings) != 1 || version.CreatedBy.String() != f.owner {
				t.Fatalf("policy not stored: %+v", version)
			}
			f.request("POST", path+"/versions"+suffix, `{"expected_revision":3,"change_reason":"prose cannot change policy","definition":{"charter":"new","autonomy":{"can_publish":false}}}`, 400)
		})
	}
}

func TestBotReviewedProsePreservesPolicyHTTP(t *testing.T) {
	f := newReviewedHTTPFixture(t)
	for _, scope := range []string{"personal", "team"} {
		t.Run(scope, func(t *testing.T) {
			suffix := "?scope=" + scope
			var b store.Bot
			if err := json.Unmarshal(f.createExisting(t, scope, `{"name":"Policy","idempotency_key":"prose-policy","definition":{"charter":"Observe"}}`), &b); err != nil {
				t.Fatal(err)
			}
			path := "/api/studio/bots/" + b.ID.String()
			q := store.BotConfigurationRequest{ExpectedRevision: b.Revision, ExpectedVersion: b.Version, Reviewed: true, Reason: "Explicit grants", Autonomy: store.DefaultBotVersion().Autonomy, Budget: store.BudgetSpec{MaxRuntimeSeconds: 45}, Delivery: store.DeliveryPolicy{Notify: store.NotifyAlways, Bindings: []store.BotChannelBinding{f.binding}}}
			q.Autonomy.CanPublish = true
			f.request("PUT", path+"/configuration"+suffix, q, 409)
			if err := json.Unmarshal(f.request("POST", path+"/pause"+suffix, map[string]any{"expected_revision": b.Revision, "reason": "Review sensitive policy"}, 200), &b); err != nil {
				t.Fatal(err)
			}
			q.ExpectedRevision = b.Revision
			f.request("PUT", path+"/configuration"+suffix, q, 200)
			f.request("POST", path+"/versions"+suffix, `{"expected_revision":3,"change_reason":"Prose only","definition":{"charter":"Revised observation charter"}}`, 200)
			var v store.BotVersion
			if err := json.Unmarshal(f.request("GET", path+"/versions/3"+suffix, nil, 200), &v); err != nil {
				t.Fatal(err)
			}
			if v.Charter != "Revised observation charter" || !v.Autonomy.CanPublish || v.Budget.MaxRuntimeSeconds != 45 || len(v.Delivery.Bindings) != 1 || v.Delivery.Bindings[0].ID != q.Delivery.Bindings[0].ID {
				t.Fatalf("prose edit changed reviewed policy: %+v", v)
			}
			var saved store.Bot
			if err := json.Unmarshal(f.request("GET", path+suffix, nil, 200), &saved); err != nil {
				t.Fatal(err)
			}
			if saved.Status != store.BotPaused {
				t.Fatalf("prose edit activated Bot: %+v", saved)
			}
		})
	}
}
