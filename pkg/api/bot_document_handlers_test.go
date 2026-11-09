package api

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
)

func TestBotDocumentsHTTPBoundary(t *testing.T) {
	actor := uuid.NewString()
	c, err := execution.WithPrincipal(context.Background(), execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: actor, OrgSlug: "org", TeamSlug: "team", Authenticated: true})
	if err != nil {
		t.Fatal(err)
	}
	c = store.WithTenantContext(c, &store.TenantContext{UserID: actor, OrgSlug: "org", TeamSlug: "team"})
	for _, tc := range []struct {
		method, path, body string
		status             int
	}{
		{"GET", "?version=0", "", 400},
		{"GET", "?version=abc", "", 400},
		{"GET", "?version=1&version=2", "", 400},
		{"PUT", "/SOUL.md", `null`, 400},
		{"PUT", "/SOUL.md", `{"content":"x","confirmed":true,"expected_revision":0,"reason":"review"}`, 400},
		{"PUT", "/SOUL.md", `{"content":"x","confirmed":true,"expected_revision":1,"reason":" "}`, 400},
		{"PUT", "/SOUL.md", `{"content":"x","confirmed":false,"expected_revision":1,"reason":"review"}`, 403},
		{"PUT", "/SOUL.md", `{"content":"x","confirmed":true,"expected_revision":1,"reason":"review","version":100}`, 400},
		{"PUT", "/SOUL.md", `{"content":"x"} {}`, 400},
		{"PUT", "/unknown", `{"content":"x","confirmed":true,"expected_revision":1,"reason":"review"}`, 400},
	} {
		t.Run(tc.method+tc.path+tc.body, func(t *testing.T) {
			repo := &botMemoryBoundaryStore{}
			router := mux.NewRouter()
			registerBotRoutes(router)
			req := httptest.NewRequest(tc.method, "/api/studio/bots/"+uuid.NewString()+"/documents"+tc.path, strings.NewReader(tc.body)).WithContext(store.WithServices(c, &store.Services{PersonalBots: repo}))
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)
			if w.Code != tc.status || repo.calls != 0 {
				t.Fatalf("status %d calls %d: %s", w.Code, repo.calls, w.Body.String())
			}
		})
	}
}
