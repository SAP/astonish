//go:build integration

package entstore

import (
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/store"
)

// Uses the live PostgreSQL router and owned databases, not the SQLite fixture.
func TestIntegrationPostgresBotPublicationWaitingThenFinal(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			f := newPostgresBotServicesFixture(t, scope, 100, 100)
			now := time.Now().UTC()
			testPublicationWaitingThenFinal(t, f.repo, f.owner, f.worker, f.run, f.lease, &now)
		})
	}
}
