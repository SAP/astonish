//go:build integration

package entstore

import (
	"database/sql"
	"testing"

	"github.com/SAP/astonish/pkg/store"
)

// A durable reopen must retire tenant pools too, not merely the platform pool.
func TestIntegrationPostgresBotFixtureReopenClosesPools(t *testing.T) {
	requireDraftActivationPostgres(t)
	eachSpendScope(t, func(t *testing.T, scope store.BotScope) {
		f := newPostgresBotServicesFixture(t, scope, 100, 100)
		for range 3 {
			old := f.platform
			pools := []*sql.DB{old.platformDB}
			old.orgClients.Range(func(_, value any) bool {
				org := value.(*orgDataStore)
				pools = append(pools, org.db)
				for _, team := range org.teams {
					pools = append(pools, team.db)
				}
				for _, personal := range org.users {
					pools = append(pools, personal.db)
				}
				return true
			})
			if len(pools) < 3 {
				t.Fatalf("missing routed pools: %d", len(pools))
			}
			f.reopenReviewed(t)
			for i, db := range pools {
				if n := db.Stats().OpenConnections; n != 0 {
					t.Errorf("retired pool %d retains %d connections after reopen", i, n)
				}
				if err := db.PingContext(f.owner); err == nil {
					t.Errorf("retired pool %d still accepts queries after reopen", i)
				}
			}
			if _, err := f.repo.Get(f.owner, f.run.BotID); err != nil {
				t.Fatalf("new routed repository unavailable: %v", err)
			}
		}
	})
}
