package entstore

import (
	"context"
	"path/filepath"
	"sync"
	"testing"

	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

func TestTenantRouterConcurrentColdOpen(t *testing.T) {
	for _, reopen := range []bool{false, true} {
		for _, personal := range []bool{false, true} {
			name := "team"
			if personal {
				name = "personal"
			}
			if reopen {
				name += "-reopen"
			}
			t.Run(name, func(t *testing.T) {
				ctx := context.Background()
				dir := t.TempDir()
				_, backend, err := NewPlatformServices(ctx, Config{DSN: filepath.Join(dir, "platform.db"), DataDir: dir})
				if err != nil {
					t.Fatal(err)
				}
				defer func() { backend.Close() }()
				if err := backend.ProvisionOrg(ctx, uuid.NewString(), "concurrent"); err != nil {
					t.Fatal(err)
				}
				org, err := backend.ForOrg("concurrent")
				if err != nil {
					t.Fatal(err)
				}
				id := uuid.NewString()
				if reopen {
					if org.ForTeam("general") == nil || org.ForUser(id) == nil {
						t.Fatal("initial open failed")
					}
					if err := backend.Close(); err != nil {
						t.Fatal(err)
					}
					_, backend, err = NewPlatformServices(ctx, Config{DSN: filepath.Join(dir, "platform.db"), DataDir: dir})
					if err != nil {
						t.Fatal(err)
					}
					org, err = backend.ForOrg("concurrent")
					if err != nil {
						t.Fatal(err)
					}
				}
				const n = 24
				start := make(chan struct{})
				results := make(chan any, n)
				var wg sync.WaitGroup
				for i := 0; i < n; i++ {
					wg.Add(1)
					go func() {
						defer wg.Done()
						<-start
						if personal {
							p := org.ForUser(id)
							if p == nil {
								results <- nil
							} else {
								results <- p
							}
						} else {
							s := org.ForTeam("general")
							if s == nil {
								results <- nil
							} else {
								results <- s
							}
						}
					}()
				}
				close(start)
				wg.Wait()
				close(results)
				var first any
				for got := range results {
					if got == nil {
						t.Error("concurrent cold open returned unavailable store")
						continue
					}
					if first == nil {
						first = got
					}
					if got != first {
						t.Error("callers did not share cached store")
					}
				}
				// A separate scope must still resolve to a distinct database handle.
				if !personal {
					if other := org.ForTeam("other"); other == nil || other == first.(store.TeamDataStore) {
						t.Fatal("team isolation lost")
					}
				}
				if personal {
					if other := org.ForUser(uuid.NewString()); other == nil || other == first.(store.PersonalDataStore) {
						t.Fatal("personal isolation lost")
					}
				}
			})
		}
	}
}
