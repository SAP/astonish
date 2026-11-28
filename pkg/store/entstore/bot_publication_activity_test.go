package entstore

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/store"
)

func TestBotPublicationActivity(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			r, ctx, b, exact, backend := selectionFixture(t, scope)
			if err := r.selectHomeConfiguration(ctx, exact, backend); err != nil {
				t.Fatal(err)
			}
			var reader interface {
				ListBotPublicationActivity(context.Context, store.BotID, int64, int) ([]store.BotActivity, error)
			}
			switch d := r.db.(type) {
			case personalBotDB:
				reader = &personalBotStore{client: d.client, content: d.content, auth: r.auth}
			case teamBotDB:
				reader = &teamBotStore{client: d.client, content: d.content, auth: r.auth}
			}
			for i := int64(2); i <= 6; i++ {
				kind := store.BotActivityLifecycle
				if i == 3 || i == 6 {
					kind = store.BotActivityReport
				}
				if err := r.db.InsertActivity(ctx, store.BotActivity{BotID: b.ID, Sequence: i, Kind: kind, Payload: json.RawMessage(`{"sentinel":true}`), CreatedAt: time.Now()}, "fixture-"+time.Now().String()); err != nil {
					t.Fatal(err)
				}
			}
			// Activities are database-inline, so deleting workspace objects does not
			// affect reads; generic history still returns all rows.
			all, err := r.ListActivity(ctx, b.ID, 0, 200)
			if err != nil || len(all) == 0 {
				t.Fatalf("generic history read failed: %v", err)
			}
			first, err := reader.ListBotPublicationActivity(ctx, b.ID, 0, 1)
			if err != nil || len(first) != 1 || first[0].Sequence != 3 {
				t.Fatalf("first=%+v err=%v", first, err)
			}
			next, err := reader.ListBotPublicationActivity(ctx, b.ID, first[0].Sequence, 1)
			if err != nil || len(next) != 1 || next[0].Sequence != 6 {
				t.Fatalf("next=%+v err=%v", next, err)
			}
			if _, err := reader.ListBotPublicationActivity(ctx, b.ID, -1, 1); !errors.Is(err, store.ErrBotInvalid) {
				t.Fatal(err)
			}
			if _, err := reader.ListBotPublicationActivity(context.Background(), b.ID, 0, 1); !errors.Is(err, store.ErrBotForbidden) {
				t.Fatal("unauthenticated read", err)
			}
		})
	}
}
