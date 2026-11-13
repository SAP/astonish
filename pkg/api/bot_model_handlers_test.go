package api

import (
	"context"
	"encoding/json"
	"io"
	"maps"
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

// Exercises production routes, tenant middleware and scoped SQLite stores. No
// provider is constructed and the only HTTP traffic is to the local test server.
func TestBotHomeModelSAPCredentialsHTTP(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	base, db, err := entstore.NewPlatformServices(ctx, entstore.Config{DSN: filepath.Join(dir, "platform.db"), DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	org := &store.Organization{ID: uuid.NewString(), Slug: "sap-status", Name: "SAP status", Status: "active"}
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
	owner, viewer := uuid.NewString(), uuid.NewString()
	for _, id := range []string{owner, viewer} {
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
		c, err := execution.WithPrincipal(r.Context(), execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: id, OrgSlug: org.Slug, TeamSlug: team.Slug, Authenticated: true})
		if err != nil {
			http.Error(w, "fixture", http.StatusInternalServerError)
			return
		}
		c = context.WithValue(c, platformUserKey{}, &PlatformUser{ID: id, OrgSlug: org.Slug, TeamSlug: team.Slug, Role: "member"})
		c = store.WithTenantContext(c, &store.TenantContext{UserID: id, OrgSlug: org.Slug, TeamSlug: team.Slug})
		router.ServeHTTP(w, r.WithContext(c))
	}))
	t.Cleanup(server.Close)
	request := func(t *testing.T, method, path, actor, body string, status int) []byte {
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
			t.Fatalf("%s %s: status %d want %d: %s", method, path, res.StatusCode, status, data)
		}
		return data
	}
	complete := store.ProviderConfig{"type": "sap_ai_core", "client_id": "fixture-client", "client_secret": "fixture-secret", "auth_url": "https://auth.invalid", "base_url": "https://api.invalid"}
	const model = "anthropic--claude-4.6-sonnet"
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			var b store.Bot
			if err := json.Unmarshal(request(t, "POST", "/api/studio/bots?scope="+string(scope), owner, `{"name":"SAP status","idempotency_key":"sap-status","definition":{"charter":"Observe"}}`, 201), &b); err != nil {
				t.Fatal(err)
			}
			path := "/api/studio/sessions/" + b.HomeSessionID.String()
			for _, name := range []string{"sap_ai_core", "New AI Core Provider"} {
				t.Run(name, func(t *testing.T) {
					// A viewer's private default and token must not rescue incomplete
					// team configuration or replace its selected provider/model.
					if err := ods.ForUser(viewer).PersonalSettings().Save(ctx, &store.PersonalSettings{DefaultProvider: "private-provider", DefaultModel: "private-model"}); err != nil {
						t.Fatal(err)
					}
					if err := ods.ForUser(viewer).Credentials().SetSecret(ctx, name, "viewer-private-token"); err != nil {
						t.Fatal(err)
					}
					type credentialCase struct {
						name, field, value string
						want               bool
					}
					cases := []credentialCase{
						{name: "complete_without_resource_group", want: true},
						{name: "complete_with_resource_group", field: "resource_group", value: "default", want: true},
						{name: "padded_client_id", field: "client_id", value: " fixture-client ", want: true},
					}
					for _, field := range []string{"client_id", "client_secret", "auth_url", "base_url"} {
						cases = append(cases,
							credentialCase{name: "missing_" + field, field: field},
							credentialCase{name: "blank_" + field, field: field, value: " \t "})
					}
					for _, tc := range cases {
						t.Run(tc.name, func(t *testing.T) {
							cfg := maps.Clone(complete)
							if name == "sap_ai_core" {
								delete(cfg, "type")
							}
							if tc.field != "" {
								if tc.value == "" {
									delete(cfg, tc.field)
								} else {
									cfg[tc.field] = tc.value
								}
							}
							if err := ods.ForTeam(team.Slug).Settings().Save(ctx, &store.TeamSettings{Providers: map[string]store.ProviderConfig{name: cfg}, DefaultProvider: name, DefaultModel: model}); err != nil {
								t.Fatal(err)
							}
							pin, _ := json.Marshal(PatchSessionModelRequest{Provider: name, Model: model})
							check := func(raw []byte) {
								t.Helper()
								var status SessionModelStatusResponse
								if err := json.Unmarshal(raw, &status); err != nil {
									t.Fatal(err)
								}
								if status.CredentialsAvailable != tc.want || status.EffectiveProvider != name || status.EffectiveModel != model {
									t.Errorf("status %+v; want credentialsAvailable=%v provider=%q model=%q", status, tc.want, name, model)
								}
								for _, secret := range []string{"fixture-client", "fixture-secret", "viewer-private-token"} {
									if strings.Contains(string(raw), secret) {
										t.Error("status leaked credential")
									}
								}
							}
							check(request(t, "PATCH", path+"/model?scope="+string(scope), owner, string(pin), 200))
							check(request(t, "GET", path+"/model-status?scope="+string(scope), owner, "", 200))
							if scope == store.BotScopeTeam {
								check(request(t, "GET", path+"/model-status?scope=team", viewer, "", 200))
							}
						})
					}
				})
			}
			if scope == store.BotScopePersonal {
				request(t, "GET", path+"/model-status?scope=personal", viewer, "", 404)
			}
		})
	}
}
