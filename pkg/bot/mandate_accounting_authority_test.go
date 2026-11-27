package bot

import (
	"context"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/store"
)

func TestMandateAccountingRetainsExpiryAndRevocation(t *testing.T) {
	for _, name := range []string{"expiry", "revocation"} {
		t.Run(name, func(t *testing.T) {
			mandate, runtime := mandateTestExecution(t)
			accounting, err := RestoreRunAccounting(store.BudgetSpec{}, time.Now(), store.BotUsage{ToolCalls: 1000, RuntimeNanos: int64(24 * time.Hour)})
			if err != nil {
				t.Fatal(err)
			}
			budget := &MandateBudget{SharedRunBudget: accounting, Mandate: mandate}
			if err := budget.ReserveTool(context.Background(), "filter_json"); err != nil {
				t.Fatal(err)
			}
			if name == "expiry" {
				runtime.m.ExpiresAt = time.Now().Add(-time.Second)
			} else {
				runtime.revoked = true
			}
			if budget.ReserveTool(context.Background(), "filter_json") == nil || runtime.tools != 1 {
				t.Fatal("accounting bypassed authority or charged denied dispatch")
			}
		})
	}
}
