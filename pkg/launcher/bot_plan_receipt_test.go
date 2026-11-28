package launcher

import (
	"context"
	"errors"
	"testing"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/store"
)

func TestBotWorkToolResultObservationReceipt(t *testing.T) {
	for _, cause := range []error{nil, store.ErrBotInvalid, store.ErrBotConflict, store.ErrBotForbidden, store.ErrBotUnsupported, store.ErrBotNotFound, context.Canceled, context.DeadlineExceeded, errors.New("uncertain service failure")} {
		name := "success"
		if cause != nil {
			name = cause.Error()
		}
		t.Run(name, func(t *testing.T) {
			f := newBotFactoryFixture(t)
			ctx := optionalKnowledgeContext{knowledgeCtx(f)}
			ctx.base, _ = bot.WithToolOutcomes(ctx.base)
			result, err := botWorkToolResult(ctx, map[string]any{"notes": "agent judgment"}, cause)
			receipt, captured := bot.TakeToolOutcome(ctx, ctx.FunctionCallID())
			terminal := errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) || (cause != nil && cause.Error() == "uncertain service failure")
			if terminal {
				if !errors.Is(err, cause) || captured {
					t.Fatalf("terminal error got receipt: err=%v receipt=%+v", err, receipt)
				}
				return
			}
			if err != nil || !captured || !receipt.Verified || len(receipt.Unchanged) != 1 || !bot.ValidObservation(receipt.Unchanged[0]) || receipt.Nonexecution != nil || len(receipt.Changed) != 0 {
				t.Fatalf("local attempt lacks observation receipt: err=%v receipt=%+v", err, receipt)
			}
			if receipt.Unchanged[0].ActorID != f.req.Bot.OwnerID {
				t.Fatal("wrong owner")
			}
			if cause != nil && result["status"] == "ok" {
				t.Fatal("failure became success")
			}
		})
	}
}
