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

type botReconcileBoundaryStore struct {
	store.BotStore
	calls int
}

func (s *botReconcileBoundaryStore) Get(context.Context, store.BotID) (*store.Bot, error) {
	s.calls++
	return &store.Bot{Scope: store.BotScopePersonal}, nil
}

func TestBotReconcileHTTPBoundary(t *testing.T) {
	actor := uuid.NewString()
	principal, err := execution.WithPrincipal(context.Background(), execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: actor, OrgSlug: "org", TeamSlug: "team", Authenticated: true})
	if err != nil {
		t.Fatal(err)
	}
	authenticated := store.WithTenantContext(principal, &store.TenantContext{OrgSlug: "org", TeamSlug: "team", UserID: actor})
	for _, tc := range []struct {
		name, bot, correction, body string
		ctx                         context.Context
		status, calls               int
	}{
		{"anonymous", uuid.NewString(), uuid.NewString(), "", context.Background(), 401, 0},
		{"missing tenant", uuid.NewString(), uuid.NewString(), "", principal, 403, 0},
		{"invalid bot", "bad", uuid.NewString(), "", authenticated, 400, 0},
		{"invalid correction", uuid.NewString(), "bad", "", authenticated, 400, 0},
		{"nil correction", uuid.NewString(), uuid.Nil.String(), "", authenticated, 400, 0},
		{"replacement", uuid.NewString(), uuid.NewString(), `{"replacement":"forged"}`, authenticated, 400, 0},
		{"empty object", uuid.NewString(), uuid.NewString(), `{}`, authenticated, 400, 0},
		{"unsupported capability", uuid.NewString(), uuid.NewString(), "", authenticated, 503, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo := &botReconcileBoundaryStore{}
			router := mux.NewRouter()
			registerBotGoalRoutes(router)
			req := httptest.NewRequest(http.MethodPost, "/api/studio/bots/"+tc.bot+"/corrections/"+tc.correction+"/reconcile", strings.NewReader(tc.body)).WithContext(store.WithServices(tc.ctx, &store.Services{PersonalBots: repo}))
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)
			if w.Code != tc.status || repo.calls != tc.calls {
				t.Fatalf("status %d calls %d: %s", w.Code, repo.calls, w.Body.String())
			}
			if strings.Contains(w.Body.String(), "memory_applied") {
				t.Fatal("failure claimed memory applied")
			}
		})
	}
}
