package entstore

import (
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/store"
)

func TestBotContentHomeSelectionRetryDoesNotReadHistory(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			r, ctx, b, exact, backend := selectionFixture(t, scope)
			seedHomeSelectionCategories(t, r, ctx, b)
			staged := configurationRecords(t, r, ctx, b.ID)
			if err := r.selectHomeConfiguration(ctx, exact, backend); err != nil {
				t.Fatal(err)
			}
			// Make every body unreadable: an idempotent selection checks references,
			// not historical content. Actual consumers must still detect corruption.
			backend.corrupt = true
			if err := r.selectHomeConfiguration(ctx, exact, backend); err != nil {
				t.Fatalf("retry reread historical bytes: %v", err)
			}
			if _, err := r.GetVersion(ctx, b.ID, 1); err == nil {
				t.Fatal("consumer accepted corrupt selected content")
			}
			backend.corrupt = false
			// Even with an attestation, a staged reference must still be selected
			// and verified; it cannot take the metadata-only retry path.
			if err := r.db.Transaction(ctx, func(tx botDraftDB) error {
				row, err := tx.GetBot(ctx, b.ID)
				if err != nil {
					return err
				}
				return tx.(botHomeConfigurationDB).selectHomeConfigurationRecords(ctx, *row, staged[:1], nil)
			}); err != nil {
				t.Fatal(err)
			}
			if err := r.selectHomeConfiguration(ctx, exact, backend); err != nil {
				t.Fatal(err)
			}
			for _, row := range configurationRecords(t, r, ctx, b.ID) {
				_, ref, _, err := homeRecordEnvelope(row)
				if err != nil {
					t.Fatal(err)
				}
				if ref != nil && strings.HasPrefix(ref.Key, "staged-v1/") {
					t.Fatal("staged reference skipped")
				}
			}
			wrong := exact
			wrong.Ref = "wrong-home"
			if err := r.selectHomeConfiguration(ctx, wrong, backend); err == nil {
				t.Fatal("retry accepted wrong binding")
			}
			for key := range backend.objects {
				delete(backend.objects, key)
			}
			if err := r.selectHomeConfiguration(ctx, exact, backend); err != nil {
				t.Fatal("retry consumed missing historical content", err)
			}
			if _, err := r.GetVersion(ctx, b.ID, 1); err == nil {
				t.Fatal("consumer accepted missing selected content")
			}
		})
	}
}
