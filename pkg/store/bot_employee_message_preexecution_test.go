package store

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestEmployeeSetupFailureNoResult(t *testing.T) {
	r := BotReport{Failure: &BotReportFailure{Kind: BotReportFailureSetup, TechnicalDetails: "PRIVATE internal JSON"}}
	m := NewBotEmployeeFallback(RunFailed, r, "")
	if m.Reason != BotEmployeeReasonSetup || m.FailureKind != BotReportFailureSetup || !strings.Contains(m.Text, "No result was produced") || strings.Contains(m.Text, "confirm completion") || strings.Contains(m.Text, "PRIVATE") {
		t.Fatalf("misleading setup failure: %+v", m)
	}
	r.EmployeeMessage = &m
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var restored BotReport
	if err = json.Unmarshal(raw, &restored); err != nil {
		t.Fatal(err)
	}
	if err = restored.ValidateEmployeeMessageIntegrity(RunFailed); err != nil {
		t.Fatal(err)
	}
	if restored.EmployeeMessage.Text != m.Text {
		t.Fatal("publication changed after restore")
	}
}
