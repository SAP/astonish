package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
)

func TestBotDeletedAccess(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			owner, id, sid := uuid.New(), uuid.New(), uuid.New()
			repo := &botSessionFixtureStore{b: store.Bot{ID: id, OwnerID: owner, Scope: scope, HomeSessionID: sid, PermanentDelete: true, Status: store.BotDeleted}}
			ctx, err := execution.WithPrincipal(context.Background(), execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: owner.String(), OrgSlug: "org", TeamSlug: "team", Authenticated: true})
			if err != nil {
				t.Fatal(err)
			}
			ctx = store.WithTenantContext(ctx, &store.TenantContext{UserID: owner.String(), OrgSlug: "org", TeamSlug: "team"})
			svc := &store.Services{PersonalBots: repo, Bots: repo}
			ctx = store.WithServices(ctx, svc)
			router := mux.NewRouter()
			registerBotRoutes(router)
			for _, suffix := range []string{"", "/snapshot", "/versions/1", "/runs", "/activity", "/sessions/" + sid.String() + "/events", "/sessions/" + sid.String() + "/stream"} {
				req := httptest.NewRequest(http.MethodGet, "/api/studio/bots/"+id.String()+suffix+"?scope="+string(scope), nil).WithContext(ctx)
				w := httptest.NewRecorder()
				router.ServeHTTP(w, req)
				if w.Code != http.StatusGone || !strings.Contains(w.Body.String(), "deletion_pending") {
					t.Fatalf("%s: %d %s", suffix, w.Code, w.Body.String())
				}
				if strings.Contains(w.Body.String(), sid.String()) {
					t.Fatal("pending response exposed Home")
				}
			}
			// A separate status route remains observable without exposing retained data.
			statusRequest := httptest.NewRequest(http.MethodGet, "/api/studio/bots/"+id.String()+"/permanent-delete?scope="+string(scope), nil).WithContext(ctx)
			statusResponse := httptest.NewRecorder()
			router.ServeHTTP(statusResponse, statusRequest)
			if statusResponse.Code != http.StatusAccepted || !strings.Contains(statusResponse.Body.String(), botDeletionPending) || strings.Contains(statusResponse.Body.String(), sid.String()) {
				t.Fatalf("deletion status: %d %s", statusResponse.Code, statusResponse.Body.String())
			}
			repo.denied = true
			denied := httptest.NewRecorder()
			router.ServeHTTP(denied, statusRequest)
			if denied.Code != http.StatusForbidden || strings.Contains(denied.Body.String(), botDeletionPending) {
				t.Fatalf("unauthorized status leaked: %d %s", denied.Code, denied.Body.String())
			}
			repo.denied = false
			meta := &store.SessionMeta{ID: sid.String(), UserID: owner.String(), BotID: id.String(), Purpose: store.SessionPurposeBotHome, Retention: store.SessionRetentionBot}
			req := httptest.NewRequest(http.MethodGet, "/", nil).WithContext(ctx)
			if botSessionReadAllowed(req, svc, meta, scope) {
				t.Fatal("generic session alias exposed deleted content")
			}
			// Worker access remains available; soft deletion remains reversible/readable.
			if b, err := repo.Get(ctx, id); err != nil || !b.PermanentDelete {
				t.Fatal("lost tombstone")
			}
			repo.b.PermanentDelete = false
			if !botSessionReadAllowed(req, svc, meta, scope) {
				t.Fatal("soft deleted Home became inaccessible")
			}
		})
	}
}
