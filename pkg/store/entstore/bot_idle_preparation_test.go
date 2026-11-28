package entstore

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
)

type idlePrepareSpy struct {
	*contentHomeBackend
	calls int
	inSQL bool
}

func (s *idlePrepareSpy) PrepareBotHomeSession(ctx context.Context, _ *sandbox.BotHomeBinding, _ string) error {
	s.calls++
	s.inSQL, _ = ctx.Value(botContentSQLTransactionKey{}).(bool)
	return nil
}
func TestBotHomeHydrationDoesNotPrepareCompute(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			h, backend, _ := contentHomeFixture(t, scope)
			spy := &idlePrepareSpy{contentHomeBackend: backend}
			id := h.stagedScope.BotID
			b := store.Bot{ID: id, OwnerID: uuid.MustParse(h.stagedScope.OwnerID), Scope: scope, HomeSessionID: uuid.New(), Home: store.BotHomeBinding{ID: uuid.New(), BotID: id, Scope: scope, Backend: "docker", StorageKey: h.binding.Ref, Revision: 1}}
			row := &botStoredRecord{Bot: b, Metadata: map[string]any{botContentHomeSelectionKey: botContentHomeSelection{Home: b.Home, Binding: h.binding}}}
			lookup := func(context.Context, store.BotID) (*botStoredRecord, error) { return row, nil }
			codec := &botContentCodec{Open: func(ctx context.Context, _ store.BotID) (store.BotContentStaging, error) {
				return openSelectedBotHome(ctx, h.staging, h.stagedScope, row, lookup, spy)
			}}
			if _, err := transactionBotContentCodec(codec).Open(t.Context(), id); err != nil {
				t.Fatal(err)
			}
			if spy.calls != 0 {
				t.Fatal("lifecycle invoked inside SQL")
			}
			if _, err := codec.Open(t.Context(), id); err != nil {
				t.Fatal(err)
			}
			if spy.calls != 0 || spy.inSQL {
				t.Fatalf("preparation calls=%d SQL=%t", spy.calls, spy.inSQL)
			}
			row.Bot.OwnerID = uuid.New()
			if _, err := codec.Open(t.Context(), id); err == nil {
				t.Fatal("cross-owner binding accepted")
			}
			if spy.calls != 0 {
				t.Fatal("denied binding started compute")
			}
		})
	}
}
