package api

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/store"
)

func TestBotChatFailurePreservesWrappedUncertainOutcome(t *testing.T) {
	for _, diagnostic := range []error{store.ErrBotConflict, context.Canceled, context.DeadlineExceeded} {
		t.Run(diagnostic.Error(), func(t *testing.T) {
			original := &bot.RunError{Class: bot.ErrorPermanent, Message: "Outcome requires reconciliation; never replay.", TerminalForAttempt: true, UncertainEffect: true, Cause: diagnostic}
			wrapped := fmt.Errorf("prepared recovery: %w", original)
			result, err := botChatFailure(bot.ErrorBlocked, wrapped)
			if result.Status != store.RunPartial || result.Report.EffectState != store.BotReportEffectsUncertain {
				t.Fatalf("uncertain outcome lost: status=%s effect=%s", result.Status, result.Report.EffectState)
			}
			failure := result.Failure
			if failure == nil || err != failure || failure == original || !failure.UncertainEffect || !failure.TerminalForAttempt || failure.Class != original.Class || failure.Message != original.Message {
				t.Fatalf("typed failure lost: result=%+v err=%v", failure, err)
			}
			if !errors.Is(err, wrapped) || !errors.Is(err, original) || !errors.Is(err, diagnostic) || original.Cause != diagnostic {
				t.Fatal("diagnostic wrapper lost or original error mutated")
			}
			if result.Report.Failure == nil || result.Report.Failure.TechnicalDetails != wrapped.Error() {
				t.Fatal("private report diagnostics lost")
			}
			if result.Report.EmployeeMessage == nil || result.Report.EmployeeMessage.SourceDigest != bot.EmployeeMessageSourceDigest(result.Status, result.Report) {
				t.Fatal("employee message not bound to final uncertain facts")
			}
		})
	}
}
