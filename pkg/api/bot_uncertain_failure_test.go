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
			if failure == nil || err != failure || failure == original || !failure.UncertainEffect || !failure.TerminalForAttempt || failure.Class != original.Class {
				t.Fatalf("typed failure lost: result=%+v err=%v", failure, err)
			}
			if failure.Message != "I couldn't confirm the outcome of the requested work." {
				t.Fatalf("unsafe public failure message: %q", failure.Message)
			}
			var preserved *bot.RunError
			if failure.Cause != wrapped || !errors.As(failure.Cause, &preserved) || preserved != original || !errors.Is(err, wrapped) || !errors.Is(err, original) || !errors.Is(err, diagnostic) {
				t.Fatal("diagnostic wrapper or original typed cause lost")
			}
			if original.Cause != diagnostic || original.Message != "Outcome requires reconciliation; never replay." || original.Class != bot.ErrorPermanent || !original.TerminalForAttempt || !original.UncertainEffect {
				t.Fatal("original error mutated")
			}
			if result.Report.Summary != "I couldn't complete the requested work." || result.Report.Failure == nil || result.Report.Failure.TechnicalDetails != "An operational failure stopped the requested work." {
				t.Fatalf("safe report evidence lost: %+v", result.Report)
			}
			if result.Report.EmployeeMessage == nil || result.Report.EmployeeMessage.SourceDigest != bot.EmployeeMessageSourceDigest(result.Status, result.Report) {
				t.Fatal("employee message not bound to final uncertain facts")
			}
		})
	}
}
