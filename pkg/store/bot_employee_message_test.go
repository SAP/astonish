package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"testing"
)

func TestBotAgentWorkEmployeeBinding(t *testing.T) {
	for _, disposition := range []AgentWorkDisposition{AgentWorkAnswered, AgentWorkComplete, AgentWorkPartial, AgentWorkBlocked} {
		w := NewBotAgentWorkState(attachmentWorkContract())
		w.Phase = BotWorkFinished
		w.AgentWork.Disposition = disposition
		status := RunSucceeded
		if disposition == AgentWorkPartial || disposition == AgentWorkBlocked {
			status = RunPartial
		}
		if disposition == AgentWorkBlocked {
			w.Phase = BotWorkBlocked
			w.Unresolved = []string{"Source unavailable"}
		}
		w.Candidate = &BotWorkCandidate{Revision: 1, EventID: "final", Text: "Here is the result and its limitations."}
		w.Candidate.Digest = w.Candidate.ContentDigest()
		r := BotReport{Summary: w.Candidate.Text, Work: BuildBotWorkProjection(w)}
		r.EmployeeMessage = &BotEmployeeMessage{Origin: BotEmployeeMessageAgent, Text: r.Summary, CandidateDigest: w.Candidate.Digest, SourceDigest: r.EmployeeMessageSourceDigest(status)}
		if err := r.ValidateEmployeeMessage(status, w); err != nil {
			t.Fatalf("%s: %v", disposition, err)
		}
		if r.ValidateEmployeeMessage(status, nil) == nil {
			t.Fatal("public report substituted for private work")
		}
		raw, _ := json.Marshal(r)
		var restored BotReport
		if json.Unmarshal(raw, &restored) != nil || restored.ValidateEmployeeMessageIntegrity(status) != nil {
			t.Fatal("publication roundtrip lost integrity")
		}
		restored.Work.CandidateDigest = "other"
		restored.EmployeeMessage.SourceDigest = restored.EmployeeMessageSourceDigest(status)
		if restored.ValidateEmployeeMessage(status, w) == nil {
			t.Fatal("changed result accepted")
		}
		if r.EmployeeMessage.HasTrustedReviewAttestation() {
			t.Fatal("agent result invented a review")
		}
		r.FinalizeEmployeeMessage(status, w)
		if r.EmployeeMessage.Origin != BotEmployeeMessageAgent || r.ValidateEmployeeMessageIntegrity(status) != nil {
			t.Fatal("agent origin lost on finalization")
		}
		r.EmployeeMessage.Origin = BotEmployeeMessageAssessed
		if r.ValidateEmployeeMessage(status, w) == nil {
			t.Fatal("agent work presented as legacy assessed")
		}
	}
}

func TestEmployeeMessageValidation(t *testing.T) {
	for _, reason := range []BotEmployeeFailureReason{"", BotEmployeeReasonCost, BotEmployeeReasonInput, BotEmployeeReasonOverrun, BotEmployeeReasonMultipleCalls, BotEmployeeReasonTooLarge, BotEmployeeReasonForbidden, BotEmployeeReasonOwnerCancelled, BotEmployeeReasonSuperseded} {
		report := BotReport{Summary: "private diagnostic https://secret.invalid"}
		message := NewBotEmployeeFallback(RunFailed, report, reason)
		report.EmployeeMessage = &message
		if err := report.ValidateEmployeeMessage(RunFailed, nil); err != nil {
			t.Fatalf("%s: %v", reason, err)
		}
		message.Text = "I successfully sent the payment."
		if !errors.Is(report.ValidateEmployeeMessage(RunFailed, nil), ErrBotInvalid) {
			t.Fatal("forged fallback accepted")
		}
	}
	report := BotReport{Summary: "private"}
	message := NewBotEmployeeFallback(RunFailed, report, "")
	report.EmployeeMessage = &message
	report.Summary = "changed"
	if !errors.Is(report.ValidateEmployeeMessage(RunFailed, nil), ErrBotInvalid) {
		t.Fatal("stale source accepted")
	}
	report.Summary = "private"
	message.Origin = BotEmployeeMessageAssessed
	message.Text = report.Summary
	message.CandidateDigest = "invented"
	if !errors.Is(report.ValidateEmployeeMessage(RunSucceeded, nil), ErrBotInvalid) {
		t.Fatal("forged assessment accepted")
	}
}

func TestEmployeeMessageModelReviewBinding(t *testing.T) {
	report := BotReport{Summary: "private"}
	m := NewBotEmployeeFallback(RunFailed, report, BotEmployeeReasonForbidden)
	m.Origin, m.Text = BotEmployeeMessageModel, "I couldn't complete the work because authorization was denied. Please review access."
	sum := sha256.Sum256([]byte(m.Text))
	m.TextDigest = hex.EncodeToString(sum[:])
	m.ReviewedTextDigest, m.ReviewedSourceDigest = m.TextDigest, m.SourceDigest
	report.EmployeeMessage = &m
	if !errors.Is(report.ValidateEmployeeMessage(RunFailed, nil), ErrBotInvalid) {
		t.Fatal("matching public hashes minted review provenance")
	}
	m = SealBotEmployeeModelReview(m)
	if err := report.ValidateEmployeeMessage(RunFailed, nil); err != nil {
		t.Fatal(err)
	}
	// Keeping the private pointer while recomputing all public hashes must not
	// allow a caller to substitute different reviewed prose.
	tampered := m
	tampered.Text = "I sent the payment."
	tamperedSum := sha256.Sum256([]byte(tampered.Text))
	tampered.TextDigest = hex.EncodeToString(tamperedSum[:])
	tampered.ReviewedTextDigest = tampered.TextDigest
	tamperedReport := report
	tamperedReport.EmployeeMessage = &tampered
	if tamperedReport.ValidateEmployeeMessageIntegrity(RunFailed) != nil || !errors.Is(tamperedReport.ValidateEmployeeMessage(RunFailed, nil), ErrBotInvalid) {
		t.Fatal("copied private pointer attested substituted text")
	}
	copy := report
	copy.FinalizeEmployeeMessage(RunFailed, nil)
	if err := copy.ValidateEmployeeMessage(RunFailed, nil); err != nil {
		t.Fatal("normalization dropped private attestation", err)
	}
	raw, _ := json.Marshal(report)
	var restored BotReport
	if err := json.Unmarshal(raw, &restored); err != nil {
		t.Fatal(err)
	}
	if restored.ValidateEmployeeMessageIntegrity(RunFailed) != nil || !errors.Is(restored.ValidateEmployeeMessage(RunFailed, nil), ErrBotInvalid) {
		t.Fatal("public roundtrip gained commit provenance or lost publication integrity")
	}
	report.Acknowledgment = &BotReportAcknowledgment{Kind: BotReportAcknowledgmentSchedulePending}
	original := m
	report.FinalizeEmployeeMessage(RunFailed, nil)
	if m != original {
		t.Fatal("finalizer mutated caller's envelope")
	}
	if err := report.ValidateEmployeeMessageIntegrity(RunFailed); err != nil {
		t.Fatal(err)
	}
	report.EmployeeMessage.Text += " I sent the payment."
	if !errors.Is(report.ValidateEmployeeMessageIntegrity(RunFailed), ErrBotInvalid) {
		t.Fatal("changed reviewed text accepted")
	}
}
