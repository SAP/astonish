package bot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
)

func TestBudgetFailureEvidence(t *testing.T) {
	for _, tc := range []struct {
		name                       string
		limit, used, input, output int64
	}{
		{"first", 71, 0, 80, 10}, {"equal", 71, 0, 71, 10}, {"zero", 0, 0, 1, 1}, {"history", 100000, 33018, 76824, 4096},
	} {
		t.Run(tc.name, func(t *testing.T) {
			limit := tc.limit
			b, err := RestoreRunBudget(store.BudgetSpec{MaxRuntimeSeconds: 60, MaxTokens: &limit}, time.Now(), store.BotUsage{InputTokens: tc.used})
			if err != nil {
				t.Fatal(err)
			}
			limit = 999999
			_, err = b.ReserveModel(context.Background(), execution.BudgetReservation{InputTokens: tc.input, OutputTokens: tc.output})
			wrapped := errors.Join(errors.New("unsafe wrapper"), fmt.Errorf("native: %w", err))
			var got *BudgetLimitError
			if !errors.As(wrapped, &got) || !errors.Is(wrapped, ErrBudgetExceeded) {
				t.Fatalf("lost type %v", wrapped)
			}
			if got.Limit != tc.limit || got.Used != tc.used || got.Remaining != max(0, tc.limit-tc.used) || *got.Requested != tc.input+tc.output || *got.InputEstimate != tc.input {
				t.Fatalf("wrong facts %+v", got)
			}
			if b.Usage().InputTokens != tc.used || b.Usage().OutputTokens != 0 {
				t.Fatal("denial charged")
			}
			if strings.Contains(BudgetFailureDetails(wrapped), "unsafe") {
				t.Fatal("wrapper leaked")
			}
		})
	}
}
func TestBudgetFailureHoldsAndSettlement(t *testing.T) {
	limit := int64(100)
	b, _ := NewRunBudget(store.BudgetSpec{MaxRuntimeSeconds: 60, MaxTokens: &limit}, time.Now())
	r, err := b.ReserveModel(context.Background(), execution.BudgetReservation{InputTokens: 10, OutputTokens: 30})
	if err != nil {
		t.Fatal(err)
	}
	_, err = b.ReserveModel(context.Background(), execution.BudgetReservation{InputTokens: 60, OutputTokens: 1})
	var got *BudgetLimitError
	if !errors.As(err, &got) || got.Used != 40 || got.Remaining != 60 {
		t.Fatalf("holds %+v %v", got, err)
	}
	err = b.SettleModel(context.Background(), r.ID, execution.BudgetUsage{InputTokens: 105, OutputTokens: 1})
	if !errors.As(err, &got) || got.Reason != BudgetSettlementOverrun || got.Remaining != 0 || got.Used != 106 {
		t.Fatalf("settle %+v %v", got, err)
	}
	if err = b.Check(context.Background()); !errors.As(err, &got) || got.Requested != nil {
		t.Fatalf("check %+v", got)
	}
}
func TestBudgetFailureQuotaAndReport(t *testing.T) {
	for _, dim := range []string{store.BotQuotaRunTokens, store.BotQuotaTotalTokens} {
		q := &store.BotMandateQuotaError{Dimension: dim, Used: 4, Requested: 8, Limit: 10}
		err := fmt.Errorf("secret wrapper: %w", q)
		message, ok := BudgetFailureMessage(err)
		scope := "run budget"
		if dim == store.BotQuotaTotalTokens {
			scope = "aggregate mandate allowance"
		}
		if !ok || !strings.Contains(message, scope) || !strings.Contains(message, "4 tokens") || !strings.Contains(message, "8 tokens") || !errors.Is(err, store.ErrBotForbidden) {
			t.Fatal(message)
		}
		report := store.BotReport{Summary: message, Failure: &store.BotReportFailure{Kind: store.BotReportFailureBudget, TechnicalDetails: BudgetFailureDetails(err)}}
		raw, _ := json.Marshal(report)
		var restored store.BotReport
		if err := json.Unmarshal(raw, &restored); err != nil {
			t.Fatal(err)
		}
		rendered := RenderDiagnosticReport(store.BotRun{Status: store.RunFailed, Report: restored})
		if strings.Count(rendered, message) != 1 || strings.Contains(rendered, "secret wrapper") || !strings.Contains(rendered, "### Technical details") {
			t.Fatal(rendered)
		}
		employeeMessage := RenderReport(store.BotRun{Status: store.RunFailed, Report: restored})
		if employeeMessage != strings.TrimSpace(message)+"\n" {
			t.Fatalf("employee message lost budget explanation or gained audit details: %s", employeeMessage)
		}
		for _, unwanted := range []string{"Run report", "Technical details", "secret wrapper", restored.Failure.TechnicalDetails} {
			if strings.Contains(employeeMessage, unwanted) {
				t.Fatalf("employee message leaked %q: %s", unwanted, employeeMessage)
			}
		}
	}
	if _, ok := BudgetFailureMessage(errors.New(ErrBudgetExceeded.Error())); ok {
		t.Fatal("string classified")
	}
	report := RenderDiagnosticReport(store.BotRun{Report: store.BotReport{Failure: &store.BotReportFailure{TechnicalDetails: "```\n# untrusted"}}})
	if strings.Contains(report, "\n# untrusted") || !strings.Contains(report, "    # untrusted") {
		t.Fatal(report)
	}
}
