package daemon

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/api"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/entstore"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
)

// No fixture user, organization, team or membership is inserted: all identity
// and tenant provisioning must come from the public registration handler.
func TestRegistrationBootstrapHTTP(t *testing.T) {
	for _, failProvision := range []bool{false, true} {
		t.Run(map[bool]string{false: "concurrent-worker", true: "failed-provision-retry"}[failProvision], func(t *testing.T) {
			ctx := context.Background()
			dir := t.TempDir()
			svc, db, err := entstore.NewPlatformServices(ctx, entstore.Config{DSN: filepath.Join(dir, "platform.db"), DataDir: dir})
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			cfg := config.PlatformAuthConfig{Mode: "builtin", JWTSecret: "registration-http-test-secret", DefaultOrgSlug: "bootstrap", DefaultOrgName: "Bootstrap"}
			pa := api.NewPlatformAuth(cfg, db, config.StorageConfig{Backend: "sqlite"})
			router := mux.NewRouter()
			api.RegisterPlatformAuthRoutes(router, pa)
			// Use actual auth and tenant middleware, not a manufactured principal.
			router.HandleFunc("/api/studio/bots", api.BotCreateHandler).Methods("POST")
			svc.BotMandatePolicy = newProductionBotMandatePolicy(db, &config.AppConfig{})
			handler := api.PlatformAuthMiddleware(pa, entstore.TenantMiddleware(db)(router))
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				handler.ServeHTTP(w, r.WithContext(store.WithServices(r.Context(), svc)))
			}))
			defer server.Close()
			jar, _ := cookiejar.New(nil)
			client := server.Client()
			client.Jar = jar
			call := func(method, path, body string, want int) []byte {
				t.Helper()
				r, _ := http.NewRequest(method, server.URL+path, strings.NewReader(body))
				r.Header.Set("Content-Type", "application/json")
				r.Header.Set("X-Requested-With", "XMLHttpRequest")
				resp, e := client.Do(r)
				if e != nil {
					t.Fatal(e)
				}
				defer resp.Body.Close()
				b, _ := io.ReadAll(resp.Body)
				if resp.StatusCode != want {
					t.Fatalf("%s %s: %d %s", method, path, resp.StatusCode, b)
				}
				return b
			}
			initialized := func() bool {
				var s struct {
					Initialized bool `json:"initialized"`
				}
				json.Unmarshal(call("GET", "/api/auth/setup-status", "", 200), &s)
				return s.Initialized
			}
			if initialized() {
				t.Fatal("empty store initialized")
			}
			worker := &BotWorker{config: BotWorkerConfig{Backend: db, WorkerID: "bootstrap-poll"}}
			stop := make(chan struct{})
			var wg sync.WaitGroup
			wg.Add(1)
			go func() {
				defer wg.Done()
				for {
					select {
					case <-stop:
						return
					default:
						_ = worker.Poll(ctx)
						time.Sleep(time.Millisecond)
					}
				}
			}()
			defer func() {
				if stop != nil {
					close(stop)
					wg.Wait()
				}
			}()
			body := `{"email":"first@example.test","password":"Registration-pass-123!"}`
			if failProvision {
				// Force a real filesystem provisioning error without touching DB rows.
				blocked := filepath.Join(dir, "orgs", "bootstrap", "teams", "general.db")
				if err := os.MkdirAll(filepath.Dir(blocked), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(blocked, []byte("blocked"), 0600); err != nil {
					t.Fatal(err)
				}
				call("POST", "/api/auth/register", body, 500)
				if initialized() {
					t.Fatal("failed provisioning published initialized=true")
				}
				call("POST", "/api/auth/login", body, http.StatusUnauthorized)
				users, e := db.Users().List(ctx)
				if e != nil || len(users) != 0 {
					t.Fatalf("failed setup published users: %v %v", users, e)
				}
				if err := os.Remove(blocked); err != nil {
					t.Fatal(err)
				}
			}
			call("POST", "/api/auth/register", body, 200)
			if !initialized() {
				t.Fatal("successful registration not initialized")
			}
			call("POST", "/api/auth/login", body, 200)
			org, err := db.ForOrg("bootstrap")
			if err != nil {
				t.Fatal(err)
			}
			u, err := db.Users().GetByEmail(ctx, "first@example.test")
			if err != nil || u == nil {
				t.Fatalf("user: %v", err)
			}
			teams, err := org.Teams().GetUserTeams(ctx, u.ID)
			if err != nil || len(teams) != 1 {
				t.Fatalf("required team membership missing: %v %v", teams, err)
			}
			// Production mandate policy must accept current membership, not just JWT role.
			_, err = newProductionBotMandatePolicy(db, &config.AppConfig{})(ctx, store.BotMandateRef{ID: uuid.New(), BotID: uuid.New(), Revision: 1, OwnerID: u.ID, OrgSlug: "bootstrap", TeamSlug: "general"})
			if err != nil {
				t.Fatalf("production admission membership: %v", err)
			}
			call("POST", "/api/studio/bots?scope=team", `{"name":"Weather","idempotency_key":"bootstrap-create"}`, 201)
			close(stop)
			wg.Wait()
			stop = nil
			call("POST", "/api/studio/bots", `{"name":"Weather assistant","idempotency_key":"bootstrap-title","start_from_title":true}`, 201)
		})
	}
}
