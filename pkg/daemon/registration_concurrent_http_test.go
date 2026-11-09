package daemon

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/SAP/astonish/pkg/api"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/store/entstore"
	"github.com/gorilla/mux"
)

// Independent HTTP servers and connection pools model competing daemon instances.
func TestRegistrationConcurrentHTTP(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	cfg := entstore.Config{DSN: filepath.Join(dir, "platform.db"), DataDir: dir}
	_, first, err := entstore.NewPlatformServices(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	_, second, err := entstore.NewPlatformServices(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	disabled := false
	var servers []*httptest.Server
	for _, db := range []*entstore.Store{first, second} {
		pa := api.NewPlatformAuth(config.PlatformAuthConfig{Mode: "builtin", JWTSecret: "registration-concurrency-test", AllowRegistration: &disabled, DefaultOrgSlug: "bootstrap"}, db, config.StorageConfig{Backend: "sqlite"})
		r := mux.NewRouter()
		api.RegisterPlatformAuthRoutes(r, pa)
		s := httptest.NewServer(api.PlatformAuthMiddleware(pa, r))
		defer s.Close()
		servers = append(servers, s)
	}
	start := make(chan struct{})
	results := make(chan int, 2)
	var wg sync.WaitGroup
	for i, s := range servers {
		wg.Add(1)
		go func(i int, s *httptest.Server) {
			defer wg.Done()
			<-start
			body := fmt.Sprintf(`{"email":"first%d@example.test","password":"Registration-pass-123!"}`, i)
			resp, e := s.Client().Post(s.URL+"/api/auth/register", "application/json", strings.NewReader(body))
			if e != nil {
				results <- 0
				return
			}
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			results <- resp.StatusCode
		}(i, s)
	}
	close(start)
	wg.Wait()
	close(results)
	successes := 0
	for status := range results {
		if status == http.StatusOK {
			successes++
		} else if status != http.StatusConflict && status != http.StatusForbidden {
			t.Errorf("registration status %d", status)
		}
	}
	if successes != 1 {
		t.Fatalf("successful registrations=%d", successes)
	}
	users, err := first.Users().List(ctx)
	if err != nil || len(users) != 1 {
		t.Fatalf("users: count=%d err=%v", len(users), err)
	}
	orgs, err := first.Organizations().List(ctx)
	if err != nil || len(orgs) != 1 {
		t.Fatalf("orgs: count=%d err=%v", len(orgs), err)
	}
	data, err := first.ForOrg(orgs[0].Slug)
	if err != nil {
		t.Fatal(err)
	}
	teams, err := data.Teams().GetUserTeams(ctx, users[0].ID)
	if err != nil || len(teams) != 1 {
		t.Fatalf("memberships=%v err=%v", teams, err)
	}
	if users[0].PlatformRole != "superadmin" {
		t.Fatal("first user is not superadmin")
	}
}
