package api

import (
	"context"
	"encoding/json"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBotExecutionIntentRoutes(t *testing.T) {
	actor := uuid.NewString()
	ctx, err := execution.WithPrincipal(context.Background(), execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: actor, OrgSlug: "org", TeamSlug: "team", Authenticated: true})
	if err != nil {
		t.Fatal(err)
	}
	ctx = store.WithTenantContext(ctx, &store.TenantContext{UserID: actor, OrgSlug: "org", TeamSlug: "team"})
	for _, tc := range []struct {
		route  string
		intent store.BotExecutionIntent
	}{{"test-run", store.BotIntentRegular}, {"conversation", store.BotIntentConversation}} {
		t.Run(tc.route, func(t *testing.T) {
			repo := &botAttachmentStore{}
			router := mux.NewRouter()
			registerBotRunRoutes(router)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				router.ServeHTTP(w, r.WithContext(store.WithServices(ctx, &store.Services{Bots: repo})))
			}))
			defer server.Close()
			response, err := server.Client().Post(server.URL+"/api/studio/bots/"+uuid.NewString()+"/"+tc.route+"?scope=team", "application/json", strings.NewReader(`{"version":1,"idempotency_key":"consent","instructions":"Reply once"}`))
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			if response.StatusCode != 202 {
				t.Fatalf("route status = %d, want 202", response.StatusCode)
			}
			if repo.request.Intent != tc.intent {
				t.Fatalf("server-selected intent = %q, want %q", repo.request.Intent, tc.intent)
			}
			// Regular is the default intent and is intentionally omitted on the
			// wire. Decoding must still recover the exact server-selected intent.
			raw, err := json.Marshal(repo.request)
			if err != nil {
				t.Fatal(err)
			}
			var decoded store.BotRunRequest
			if err := json.Unmarshal(raw, &decoded); err != nil {
				t.Fatal(err)
			}
			if decoded.Intent != tc.intent {
				t.Fatalf("serialized intent = %q, want %q: %s", decoded.Intent, tc.intent, raw)
			}
		})
	}
}
