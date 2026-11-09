package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
)

type exactCancelHTTPStore struct {
	store.BotStore
	run   store.BotRun
	calls int
}

func (s *exactCancelHTTPStore) CancelRun(ctx context.Context, id store.BotID, rid store.RunID) (*store.BotRun, error) {
	p, ok := execution.PrincipalFromContext(ctx)
	if !ok || p.OrgSlug != "org" || p.TeamSlug != "team" {
		return nil, store.ErrBotForbidden
	}
	if id != s.run.BotID || rid != s.run.ID {
		return nil, store.ErrBotNotFound
	}
	s.calls++
	s.run.Status = store.RunInterrupted
	return &s.run, nil
}
func TestBotExactRunCancellationHTTP(t *testing.T) {
	actor := uuid.NewString()
	repo := &exactCancelHTTPStore{run: store.BotRun{ID: uuid.New(), BotID: uuid.New(), Status: store.RunRunning}}
	router := mux.NewRouter()
	registerBotRoutes(router)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		if r.Header.Get("X-Test-Auth") != "" {
			var err error
			ctx, err = execution.WithPrincipal(ctx, execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: actor, OrgSlug: "org", TeamSlug: "team", Authenticated: true})
			if err != nil {
				t.Error(err)
				return
			}
			org := "org"
			if r.Header.Get("X-Test-Wrong-Tenant") != "" {
				org = "other"
			}
			ctx = store.WithTenantContext(ctx, &store.TenantContext{UserID: actor, OrgSlug: org, TeamSlug: "team"})
		}
		router.ServeHTTP(w, r.WithContext(store.WithServices(ctx, &store.Services{Bots: repo})))
	}))
	defer server.Close()
	for _, tc := range []struct {
		name        string
		bot, run    uuid.UUID
		auth, wrong bool
		status      int
	}{
		{"anonymous", repo.run.BotID, repo.run.ID, false, false, 401},
		{"cross tenant", repo.run.BotID, repo.run.ID, true, true, 403},
		{"other bot", uuid.New(), repo.run.ID, true, false, 404},
		{"other run", repo.run.BotID, uuid.New(), true, false, 404},
		{"exact stop", repo.run.BotID, repo.run.ID, true, false, 202},
		{"duplicate stop", repo.run.BotID, repo.run.ID, true, false, 202},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodPost, server.URL+"/api/studio/bots/"+tc.bot.String()+"/runs/"+tc.run.String()+"/cancel?scope=team", nil)
			if err != nil {
				t.Fatal(err)
			}
			if tc.auth {
				req.Header.Set("X-Test-Auth", "yes")
			}
			if tc.wrong {
				req.Header.Set("X-Test-Wrong-Tenant", "yes")
			}
			resp, err := server.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			raw, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatal(err)
			}
			if resp.StatusCode != tc.status {
				t.Fatalf("status %d want %d: %s", resp.StatusCode, tc.status, raw)
			}
			if tc.status == 202 {
				var got store.BotRun
				if err = json.Unmarshal(raw, &got); err != nil {
					t.Fatal(err)
				}
				if got.ID != tc.run || got.BotID != tc.bot || got.Status != store.RunInterrupted {
					t.Fatalf("incorrect DTO %s", raw)
				}
			}
		})
	}
	if repo.calls != 2 {
		t.Fatalf("unauthorized request reached cancellation: %d", repo.calls)
	}
}
