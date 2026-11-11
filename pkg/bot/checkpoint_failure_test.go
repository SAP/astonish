package bot

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/store"
)

func TestCheckpointFailureNativeIdentity(t *testing.T) {
	for _, cause := range []*CheckpointResponseError{
		{Reason: CheckpointResponseMultipleCalls, ToolCalls: 2},
		{Reason: CheckpointResponseTooLarge, ResponseBytes: 4194400, LimitBytes: 4194304},
	} {
		wrapped := fmt.Errorf("private wrapper secret: %w", cause)
		if !errors.Is(wrapped, store.ErrBotUnsupported) {
			t.Fatal("unsupported identity lost")
		}
		message, ok := CheckpointFailureMessage(wrapped)
		if !ok {
			t.Fatal("native error not recognized")
		}
		details := CheckpointFailureDetails(wrapped)
		if strings.Contains(message+details, "secret") {
			t.Fatal("wrapper leaked")
		}
		report := store.BotReport{Summary: message, Failure: &store.BotReportFailure{Kind: store.BotReportFailureExecutionFormat, TechnicalDetails: details}}
		raw, err := json.Marshal(report)
		if err != nil {
			t.Fatal(err)
		}
		var restored store.BotReport
		if err := json.Unmarshal(raw, &restored); err != nil {
			t.Fatal(err)
		}
		if restored.Failure.Kind != store.BotReportFailureExecutionFormat || restored.Failure.TechnicalDetails != details {
			t.Fatal("additive report metadata lost")
		}
		rendered := RenderReport(store.BotRun{Status: store.RunFailed, Report: restored})
		if rendered != EmployeeMessageFallback(store.RunFailed, restored, nil).Text+"\n" || !strings.Contains(rendered, "model response could not be processed") || strings.Contains(rendered, "###") || strings.Contains(rendered, details) {
			t.Fatal(rendered)
		}
		if _, ok := CheckpointFailureMessage(errors.New(cause.Error())); ok {
			t.Fatal("string lookalike classified")
		}
	}
	for _, err := range []error{nil, store.ErrBotUnsupported, fmt.Errorf("wrapped: %w", store.ErrBotUnsupported), &CheckpointResponseError{Reason: "unknown"}} {
		if _, ok := CheckpointFailureMessage(err); ok {
			t.Fatalf("unproven error recognized: %v", err)
		}
	}
}
