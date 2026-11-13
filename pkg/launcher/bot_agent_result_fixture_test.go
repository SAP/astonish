package launcher

import (
	"context"
	"testing"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/store"
)

// Assert the runtime's exact persisted candidate binding, not just a public
// success flag or an invented legacy quality receipt.
func assertLauncherAgentResult(t *testing.T, repo store.BotStore, ctx context.Context, lease store.BotLease, result bot.RunResult) {
	t.Helper()
	work, err := repo.(store.BotWorkStore).LoadWork(ctx, lease)
	if err != nil {
		t.Fatal(err)
	}
	if work == nil || work.Protocol != store.BotWorkProtocolAgentLedV1 || work.Candidate == nil || work.Review != nil || result.Report.Quality != nil {
		t.Fatalf("expected agent-led candidate without legacy review: work=%+v report=%+v", work, result.Report)
	}
	if result.Report.EmployeeMessage == nil || result.Report.EmployeeMessage.Origin != store.BotEmployeeMessageAgent {
		t.Fatalf("missing agent-origin result: %+v", result.Report)
	}
	if err := store.ValidateBotWorkCompletion(work, store.BotRunCommit{Lease: lease, Status: result.Status, Report: result.Report}); err != nil {
		t.Fatalf("unbound agent result: %v", err)
	}
	if err := result.Report.ValidateEmployeeMessage(result.Status, work); err != nil {
		t.Fatalf("unbound employee message: %v", err)
	}
}
