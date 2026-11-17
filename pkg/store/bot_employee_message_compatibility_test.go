package store

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func employeeFallbackObservationReport() BotReport {
	return BotReport{
		Work:    &BotWorkProjection{Protocol: BotWorkProtocolAgentLedV1, Phase: BotWorkWorking},
		Changed: []BotEvidence{{Kind: BotEvidenceObservation, Summary: "private discovery", Source: "private source", ObservedAt: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), ActorID: uuid.MustParse("11111111-1111-1111-1111-111111111111")}},
	}
}

// Literal wording pins the persisted contract independently of the formatter.
func employeeFallbackLegacyText(count int) string {
	return fmt.Sprintf("I couldn't complete this request during execution. No publishable final answer is available from this run. The run recorded %d tool observation(s); this does not confirm that all requested actions completed. You can revise the request or start a new run; no retry has been started.", count)
}

func TestEmployeeFallbackLegacyRoundTrip(t *testing.T) {
	for _, count := range []int{1, 2} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			r := employeeFallbackObservationReport()
			if count == 2 {
				r.Unchanged = append(r.Unchanged, r.Changed[0])
			}
			r.EmployeeMessage = &BotEmployeeMessage{Origin: BotEmployeeMessageFallback, Text: employeeFallbackLegacyText(count), SourceDigest: r.EmployeeMessageSourceDigest(RunFailed), FailureKind: BotReportFailureOperational, Reason: BotEmployeeReasonAgentNoResult}
			encoded, err := json.Marshal(r)
			if err != nil {
				t.Fatal(err)
			}
			var saved BotReport
			if err := json.Unmarshal(encoded, &saved); err != nil {
				t.Fatal(err)
			}
			if err := saved.ValidateEmployeeMessageIntegrity(RunFailed); err != nil {
				t.Fatalf("saved canonical legacy fallback rejected: %v", err)
			}
			if saved.EmployeeMessage.Text != employeeFallbackLegacyText(count) {
				t.Fatal("validation rewrote historical text")
			}
		})
	}
}

func TestEmployeeFallbackCurrentObservationWording(t *testing.T) {
	for _, count := range []int{0, 1, 2} {
		for _, valid := range []bool{false, true} {
			t.Run(fmt.Sprintf("count_%d_valid_%t", count, valid), func(t *testing.T) {
				r := employeeFallbackObservationReport()
				evidence := r.Changed[0]
				if !valid {
					evidence.ActorID = uuid.Nil
				}
				r.Changed = nil
				for i := 0; i < count; i++ {
					r.Unchanged = append(r.Unchanged, evidence)
				}
				m := NewBotEmployeeFallback(RunFailed, r, "")
				if strings.Contains(m.Text, "tool observation(s)") || strings.Contains(m.Text, "private") {
					t.Fatalf("misleading count or private facts: %s", m.Text)
				}
				if got, want := strings.Contains(m.Text, "some observations; this does not confirm that all requested actions completed"), valid && count > 0; got != want {
					t.Fatalf("observation warning = %t, want %t: %s", got, want, m.Text)
				}
				r.EmployeeMessage = &m
				if err := r.ValidateEmployeeMessageIntegrity(RunFailed); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

func TestEmployeeFallbackLegacyRejectsTampering(t *testing.T) {
	for _, mutation := range []string{"count", "suffix", "prefix", "warning", "kind", "reason", "candidate", "source", "status", "evidence", "evidence_rebound", "invalid_observation_rebound", "non_agent_rebound", "text_digest", "operation"} {
		t.Run(mutation, func(t *testing.T) {
			r := employeeFallbackObservationReport()
			m := BotEmployeeMessage{Origin: BotEmployeeMessageFallback, Text: employeeFallbackLegacyText(1), SourceDigest: r.EmployeeMessageSourceDigest(RunFailed), FailureKind: BotReportFailureOperational, Reason: BotEmployeeReasonAgentNoResult}
			status := RunFailed
			switch mutation {
			case "count":
				m.Text = employeeFallbackLegacyText(15)
			case "suffix":
				m.Text += " Delivery succeeded."
			case "prefix":
				m.Text = "Delivery succeeded. " + m.Text
			case "warning":
				m.Text = strings.Replace(m.Text, "does not confirm", "confirms", 1)
			case "kind":
				m.FailureKind = BotReportFailureSetup
			case "reason":
				m.Reason = BotEmployeeReasonSetup
			case "candidate":
				m.CandidateDigest = strings.Repeat("a", 64)
			case "source":
				m.SourceDigest = strings.Repeat("a", 64)
			case "status":
				status = RunPartial
			case "evidence", "evidence_rebound":
				r.Changed = nil
				if mutation == "evidence_rebound" {
					m.SourceDigest = r.EmployeeMessageSourceDigest(status)
				}
			case "invalid_observation_rebound":
				r.Changed[0].ActorID = uuid.Nil
				m.SourceDigest = r.EmployeeMessageSourceDigest(status)
			case "non_agent_rebound":
				r.Work = nil
				m.Reason = BotEmployeeReasonUnknown
				m.SourceDigest = r.EmployeeMessageSourceDigest(status)
			case "text_digest":
				m.TextDigest = strings.Repeat("a", 64)
			case "operation":
				m.OperationID = uuid.New()
			}
			r.EmployeeMessage = &m
			if err := r.ValidateEmployeeMessageIntegrity(status); err == nil {
				t.Fatal("altered legacy fallback accepted")
			}
		})
	}
}
