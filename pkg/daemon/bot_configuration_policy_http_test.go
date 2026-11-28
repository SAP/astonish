package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/api"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/entstore"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
)

// Exercise the production policy resolver over a real HTTP socket and the real
// single-connection SQLite router, without running a worker, sandbox or model.
func TestBotConfigurationProductionPolicyHTTP(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			dir := t.TempDir()
			_, db, err := entstore.NewPlatformServices(ctx, entstore.Config{DSN: filepath.Join(dir, "platform.db"), DataDir: dir})
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			org := &store.Organization{ID: uuid.NewString(), Slug: "configuration", Name: "Configuration", Status: "active"}
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
			team := &store.Team{ID: uuid.NewString(), Slug: "team", Name: "Team", SchemaName: "team_team"}
			if err := ods.Teams().CreateTeam(ctx, team); err != nil {
				t.Fatal(err)
			}
			owner := uuid.New()
			if err := db.Users().Create(ctx, &store.User{ID: owner.String(), Email: owner.String() + "@test.invalid", DisplayName: "Owner", Status: "active"}); err != nil {
				t.Fatal(err)
			}
			if err := db.Organizations().AddMember(ctx, owner.String(), org.ID, "member"); err != nil {
				t.Fatal(err)
			}
			if err := ods.Teams().AddMember(ctx, &store.TeamMembership{UserID: owner.String(), TeamID: team.ID, Role: "member"}); err != nil {
				t.Fatal(err)
			}
			scoped := ods.ForTeam(team.Slug)
			if err := scoped.Settings().Save(ctx, &store.TeamSettings{WebSearchTool: "tavily:tavily_search"}); err != nil {
				t.Fatal(err)
			}
			if err := scoped.MCPServers().Save(ctx, &store.MCPServer{Name: "tavily", Command: "test-only", Env: map[string]string{"API_KEY": "{{CREDENTIAL:test-only:value}}"}}); err != nil {
				t.Fatal(err)
			}
			if err := scoped.NetworkPolicies().Save(ctx, &store.NetworkPolicyRule{Host: "policy.test", Action: store.NetworkPolicyAllow}); err != nil {
				t.Fatal(err)
			}
			repo := scoped.Bots()
			if scope == store.BotScopePersonal {
				repo = ods.ForUser(owner.String()).Bots()
			}
			auth, err := execution.WithPrincipal(ctx, execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: owner.String(), OrgSlug: org.Slug, TeamSlug: team.Slug, Authenticated: true})
			if err != nil {
				t.Fatal(err)
			}
			v := store.DefaultBotVersion()
			v.Charter = "Reviewed configuration"
			b, err := repo.Create(auth, store.BotCreateRequest{Name: "Policy", OwnerID: owner, Version: v, IdempotencyKey: "draft"})
			if err != nil {
				t.Fatal(err)
			}
			b, err = repo.Pause(auth, store.BotLifecycleRequest{BotID: b.ID, ActorID: owner, ExpectedRevision: b.Revision, Reason: "Review sensitive configuration"})
			if err != nil {
				t.Fatal(err)
			}
			production := newProductionBotMandatePolicy(db, &config.AppConfig{})
			policy := func(c context.Context, ref store.BotMandateRef) (store.BotMandatePolicy, error) {
				p, err := production(c, ref)
				if err == nil && p.PublicResearch == nil {
					return store.BotMandatePolicy{}, store.ErrBotUnsupported
				}
				return p, err
			}
			svc := &store.Services{Bots: repo, BotMandatePolicy: policy}
			if scope == store.BotScopePersonal {
				svc.PersonalBots = repo
			}
			auth = store.WithServices(store.WithTenantContext(auth, &store.TenantContext{UserID: owner.String(), OrgSlug: org.Slug, TeamSlug: team.Slug}), svc)
			router := mux.NewRouter()
			router.HandleFunc("/bots/{botID}/configuration", api.BotConfigurationHandler).Methods(http.MethodPut)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requestCtx, cancel := context.WithTimeout(auth, 3*time.Second)
				defer cancel()
				router.ServeHTTP(w, r.WithContext(requestCtx))
			}))
			defer server.Close()
			client := server.Client()
			client.Timeout = 5 * time.Second
			q := store.BotConfigurationRequest{ExpectedRevision: b.Revision, ExpectedVersion: b.Version, Reviewed: true, Reason: "Reviewed", Autonomy: v.Autonomy, Budget: v.Budget, Delivery: v.Delivery}
			q.Budget.RuntimePolicy = execution.NewBotProgressRuntimePolicy()
			request := func(want int) []byte {
				t.Helper()
				raw, err := json.Marshal(q)
				if err != nil {
					t.Fatal(err)
				}
				req, err := http.NewRequestWithContext(ctx, http.MethodPut, server.URL+"/bots/"+b.ID.String()+"/configuration?scope="+string(scope), bytes.NewReader(raw))
				if err != nil {
					t.Fatal(err)
				}
				req.Header.Set("Content-Type", "application/json")
				response, err := client.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				defer response.Body.Close()
				body, err := io.ReadAll(response.Body)
				if err != nil {
					t.Fatal(err)
				}
				if response.StatusCode != want {
					t.Fatalf("configuration HTTP %d want %d: %s", response.StatusCode, want, body)
				}
				return body
			}
			raw := request(http.StatusOK)
			var saved store.Bot
			if err := json.Unmarshal(raw, &saved); err != nil {
				t.Fatal(err)
			}
			if saved.Version != b.Version+1 || saved.Status != store.BotPaused {
				t.Fatalf("configuration activated or failed to version: %+v", saved)
			}
			q.ExpectedRevision, q.ExpectedVersion = saved.Revision, saved.Version
			// Resolve current membership again on the next request; successful policy
			// resolution must never become a cached authorization for this owner.
			if err := ods.Teams().RemoveMember(ctx, owner.String(), team.ID); err != nil {
				t.Fatal(err)
			}
			request(http.StatusForbidden)
			if err := ods.Teams().AddMember(ctx, &store.TeamMembership{UserID: owner.String(), TeamID: team.ID, Role: "member"}); err != nil {
				t.Fatal(err)
			}
			after, err := repo.Get(auth, b.ID)
			if err != nil || after.Version != saved.Version || after.Revision != saved.Revision {
				t.Fatalf("denial wrote configuration: %+v %v", after, err)
			}
		})
	}
}
