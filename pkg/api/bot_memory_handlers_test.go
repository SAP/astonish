package api

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
)

type botMemoryBoundaryStore struct {
	store.BotStore
	bot   *store.Bot
	err   error
	calls int
}

func (s *botMemoryBoundaryStore) Get(context.Context, store.BotID) (*store.Bot, error) {
	s.calls++
	return s.bot, s.err
}
func TestBotMemoryHTTPBoundary(t *testing.T) {
	actor := uuid.NewString()
	id := uuid.New()
	principal, err := execution.WithPrincipal(context.Background(), execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: actor, OrgSlug: "org", TeamSlug: "team", Authenticated: true})
	if err != nil {
		t.Fatal(err)
	}
	authenticated := store.WithTenantContext(principal, &store.TenantContext{UserID: actor, OrgSlug: "org", TeamSlug: "team"})
	for _, tc := range []struct {
		name          string
		ctx           context.Context
		query         string
		bot           *store.Bot
		err           error
		status, calls int
	}{
		{name: "anonymous", ctx: context.Background(), status: 401},
		{name: "missing tenant", ctx: principal, status: 403},
		{name: "invalid scope", ctx: authenticated, query: "?scope=org", status: 400},
		{name: "duplicate scope", ctx: authenticated, query: "?scope=personal&scope=team", status: 400},
		{name: "wrong identity", ctx: authenticated, bot: &store.Bot{ID: uuid.New(), Scope: store.BotScopePersonal}, status: 403, calls: 1},
		{name: "wrong scope", ctx: authenticated, bot: &store.Bot{ID: id, Scope: store.BotScopeTeam}, status: 403, calls: 1},
		{name: "not found", ctx: authenticated, status: 404, calls: 1},
		{name: "sanitized backend", ctx: authenticated, err: errors.New("database secret connection"), status: 500, calls: 1},
		{name: "no personal router fallback", ctx: authenticated, bot: &store.Bot{ID: id, Scope: store.BotScopePersonal}, status: 503, calls: 1},
		{name: "no team router fallback", ctx: authenticated, query: "?scope=team", bot: &store.Bot{ID: id, Scope: store.BotScopeTeam}, status: 503, calls: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, route := range []struct{ method, suffix string }{{"GET", ""}, {"POST", ""}, {"GET", "/search?q=x"}, {"GET", "/" + uuid.NewString()}, {"PUT", "/" + uuid.NewString()}, {"DELETE", "/" + uuid.NewString()}} {
				repo := &botMemoryBoundaryStore{bot: tc.bot, err: tc.err}
				// Generic memory must not rescue missing routed scope. An embedded nil
				// interface would panic if an unsafe fallback actually called it.
				svc := &store.Services{PersonalBots: repo, Bots: repo, Memory: &botBoundaryMemory{}}
				router := mux.NewRouter()
				registerBotRoutes(router)
				suffix := route.suffix
				query := tc.query
				if strings.Contains(suffix, "?") && query != "" {
					query = "&" + strings.TrimPrefix(query, "?")
				}
				req := httptest.NewRequest(route.method, "/api/studio/bots/"+id.String()+"/memory"+suffix+query, strings.NewReader(`{}`)).WithContext(store.WithServices(tc.ctx, svc))
				w := httptest.NewRecorder()
				router.ServeHTTP(w, req)
				if w.Code != tc.status || repo.calls != tc.calls {
					t.Fatalf("%s %s: status %d calls %d: %s", route.method, route.suffix, w.Code, repo.calls, w.Body.String())
				}
				if strings.Contains(w.Body.String(), "secret") {
					t.Fatal("internal error leaked")
				}
			}
		})
	}
}

type botBoundaryMemory struct{ store.MemoryStore }
