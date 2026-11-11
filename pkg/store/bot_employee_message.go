package store

import (
	"crypto/sha256"
	"encoding/hex"
	"github.com/google/uuid"
	"strings"
)

// botEmployeeReviewAttestation binds a value snapshot, not mutable caller memory.
// It is deliberately nonserialized and uses no global authority or secret.
type botEmployeeReviewAttestation struct {
	message BotEmployeeMessage
}

// SealBotEmployeeModelReview is for the trusted employee composer only, after
// both owner-budgeted model calls and the boundary checks have succeeded. This
// is an in-process trust boundary, not protection against malicious Go code.
func SealBotEmployeeModelReview(m BotEmployeeMessage) BotEmployeeMessage {
	m.reviewAttestation = nil
	if m.Origin == BotEmployeeMessageModel {
		m.reviewAttestation = &botEmployeeReviewAttestation{message: m}
	}
	return m
}

// HasTrustedReviewAttestation is required at commit, including idempotent retry.
// Publication of already persisted data instead checks immutable integrity.
func (m BotEmployeeMessage) HasTrustedReviewAttestation() bool {
	attestation := m.reviewAttestation
	m.reviewAttestation = nil
	return attestation != nil && attestation.message == m
}

// BotWorkHasPendingControlCandidate identifies runtime receipt candidates. Never
// use this conservative classification to suppress mixed content or errors.
func BotWorkHasPendingControlCandidate(work *BotWorkState) bool {
	if work == nil || work.Candidate == nil {
		return false
	}
	for key := range work.Candidate.Bodies {
		if strings.HasPrefix(key, "pending-control:") {
			return true
		}
	}
	return false
}

// ValidateEmployeeMessageIntegrity checks immutable publication data without
// consulting mutable current work (an older sequence may precede resumption).
// Operation authority is checked separately against its durable receipt.
func (r BotReport) ValidateEmployeeMessageIntegrity(status RunStatus) error {
	m := r.EmployeeMessage
	if m == nil || strings.TrimSpace(m.Text) == "" || m.SourceDigest != r.EmployeeMessageSourceDigest(status) {
		return ErrBotInvalid
	}
	if m.Origin != BotEmployeeMessageOperation && (m.OperationID != uuid.Nil || m.OperationRevision != 0) {
		return ErrBotInvalid
	}
	if m.Origin != BotEmployeeMessageModel && (m.TextDigest != "" || m.ReviewedTextDigest != "" || m.ReviewedSourceDigest != "") {
		return ErrBotInvalid
	}
	switch m.Origin {
	case BotEmployeeMessageAssessed:
		if m.CandidateDigest == "" || m.Text != r.Summary || m.FailureKind != "" || m.Reason != "" || (status != RunSucceeded && status != RunPartial) {
			return ErrBotInvalid
		}
	case BotEmployeeMessageFallback:
		expected := NewBotEmployeeFallback(status, r, m.Reason)
		if *m != expected {
			return ErrBotInvalid
		}
	case BotEmployeeMessageModel:
		sum := sha256.Sum256([]byte(m.Text))
		digest := hex.EncodeToString(sum[:])
		original := r
		original.Acknowledgment = nil // the sole server-only post-review mutation
		facts := NewBotEmployeeFallback(status, r, m.Reason)
		if (status != RunFailed && status != RunPartial) || m.CandidateDigest != "" || m.TextDigest != digest || m.ReviewedTextDigest != digest || m.ReviewedSourceDigest != original.EmployeeMessageSourceDigest(status) || m.Reason != facts.Reason || m.FailureKind != facts.FailureKind || len(m.Text) > 1600 || strings.Contains(strings.ToLower(m.Text), "://") || strings.Contains(strings.ToLower(m.Text), "www.") || strings.ContainsAny(m.Text, "\x00\x1b") {
			return ErrBotInvalid
		}
	case BotEmployeeMessageOperation:
		if m.OperationID == uuid.Nil || m.OperationRevision < 1 || m.CandidateDigest != "" || m.FailureKind != "" || m.Reason != "" || m.Text != r.Summary {
			return ErrBotInvalid
		}
	default:
		return ErrBotInvalid
	}
	return nil
}

// BotEmployeeFailureReason is a server-selected reason, never provider prose.
type BotEmployeeFailureReason string

const (
	BotEmployeeReasonEmailTransport BotEmployeeFailureReason = "required_email_transport_unavailable"
	BotEmployeeReasonMixedControl   BotEmployeeFailureReason = "independent_work_unverified"
	BotEmployeeReasonUnknown        BotEmployeeFailureReason = "operational_reason_unavailable"
	BotEmployeeReasonUnverified     BotEmployeeFailureReason = "result_not_verified"
	BotEmployeeReasonSetup          BotEmployeeFailureReason = "setup_incomplete"
	BotEmployeeReasonBudget         BotEmployeeFailureReason = "budget_prevented_work"
	BotEmployeeReasonCost           BotEmployeeFailureReason = "model_cost_unknown"
	BotEmployeeReasonInput          BotEmployeeFailureReason = "input_does_not_fit"
	BotEmployeeReasonOverrun        BotEmployeeFailureReason = "usage_exceeded_budget"
	BotEmployeeReasonFormat         BotEmployeeFailureReason = "model_response_rejected"
	BotEmployeeReasonMultipleCalls  BotEmployeeFailureReason = "multiple_tool_calls_rejected"
	BotEmployeeReasonTooLarge       BotEmployeeFailureReason = "response_size_limit"
	BotEmployeeReasonInterrupted    BotEmployeeFailureReason = "interrupted"
	BotEmployeeReasonForbidden      BotEmployeeFailureReason = "authorization_denied"
	BotEmployeeReasonUnsupported    BotEmployeeFailureReason = "capability_unsupported"
	BotEmployeeReasonExpired        BotEmployeeFailureReason = "approval_or_retention_expired"
	BotEmployeeReasonNotFound       BotEmployeeFailureReason = "resource_not_found"
	BotEmployeeReasonInvalid        BotEmployeeFailureReason = "request_invalid"
	BotEmployeeReasonConflict       BotEmployeeFailureReason = "revision_or_lease_conflict"
	BotEmployeeReasonOwnerCancelled BotEmployeeFailureReason = "owner_cancelled"
	BotEmployeeReasonSuperseded     BotEmployeeFailureReason = "schedule_superseded"
)

func EmployeeReasonDetails(reason BotEmployeeFailureReason) (string, string) {
	switch reason {
	case BotEmployeeReasonEmailTransport:
		return "The email delivery required by this request is blocked because the native owner email transport is unavailable.", "An administrator must enable/configure the email channel; Bot tools cannot do this."
	case BotEmployeeReasonMixedControl:
		return "I don't have a verified independent result to share from this turn.", "The control change is reported separately; this message does not confirm its application."
	case BotEmployeeReasonUnverified:
		return "The requested result has not been verified.", "Review the incomplete result before deciding how to continue."
	case BotEmployeeReasonSetup:
		return "Setup did not complete, so this run could not start.", "Resolve the setup problem before starting another run."
	case BotEmployeeReasonBudget:
		return "The available budget did not allow further work.", "Review the task and budget before starting another run."
	case BotEmployeeReasonCost:
		return "The model cost is unknown, so the cost-limited run could not continue safely.", "Configure model pricing or review the cost limit before starting another run."
	case BotEmployeeReasonInput:
		return "The next model request would not fit the remaining token budget.", "Shorten the task or review the token budget before starting another run."
	case BotEmployeeReasonOverrun:
		return "Reported model usage exceeded the token budget.", "Review the usage and token budget before starting another run."
	case BotEmployeeReasonFormat:
		return "The model response could not be processed.", "Review the response failure and any earlier work before starting another run."
	case BotEmployeeReasonMultipleCalls:
		return "The model returned multiple tool requests, but this run supports one tool request at a time. None of the requests from that response were executed.", "Review any earlier work before starting another run with serial tool requests."
	case BotEmployeeReasonTooLarge:
		return "The model response exceeded the supported size limit. None of the requests from that response were executed.", "Review any earlier work and reduce the response size before starting another run."
	case BotEmployeeReasonInterrupted:
		return "The run stopped before completion could be confirmed.", "Review any recorded work before deciding whether to resume or start another run."
	case BotEmployeeReasonForbidden:
		return "Authorization did not allow the operation.", "Review the required authorization before trying again."
	case BotEmployeeReasonUnsupported:
		return "The requested capability is not supported.", "Choose a supported capability or revise the request."
	case BotEmployeeReasonExpired:
		return "An approval or retention period expired.", "Review the expired approval or retained resource before trying again."
	case BotEmployeeReasonNotFound:
		return "A required resource could not be found.", "Check that the required resource is available before trying again."
	case BotEmployeeReasonInvalid:
		return "The request did not pass validation.", "Review and correct the request before trying again."
	case BotEmployeeReasonConflict:
		return "The operation encountered a revision or execution-lease conflict.", "Check the current run state before trying again."
	case BotEmployeeReasonOwnerCancelled:
		return "You cancelled the pending work.", "Review the request before starting another run."
	case BotEmployeeReasonSuperseded:
		return "This queued occurrence was cancelled because its schedule was replaced.", "Review the current schedule if you still need this work."
	default:
		return "An operational failure stopped the work; a safe, specific cause is not available.", "Review the run diagnostics before deciding how to continue."
	}
}

// NewBotEmployeeFallback regenerates the exact public text from closed typed
// facts. An empty reason derives the safe reason from report/status alone.
func NewBotEmployeeFallback(status RunStatus, report BotReport, reason BotEmployeeFailureReason) BotEmployeeMessage {
	kind := BotReportFailureOperational
	if reason == "" {
		reason = BotEmployeeReasonUnknown
		if status == RunPartial {
			reason = BotEmployeeReasonUnverified
		}
		if report.Failure != nil {
			switch report.Failure.Kind {
			case BotReportFailureSetup:
				reason = BotEmployeeReasonSetup
			case BotReportFailureBudget:
				reason = BotEmployeeReasonBudget
			case BotReportFailureExecutionFormat:
				reason = BotEmployeeReasonFormat
			case BotReportFailureInterrupted:
				reason = BotEmployeeReasonInterrupted
			}
		}
		if status == RunCancelled || status == RunInterrupted {
			reason = BotEmployeeReasonInterrupted
		}
	}
	switch reason {
	case BotEmployeeReasonSetup:
		kind = BotReportFailureSetup
	case BotEmployeeReasonBudget, BotEmployeeReasonCost, BotEmployeeReasonInput, BotEmployeeReasonOverrun:
		kind = BotReportFailureBudget
	case BotEmployeeReasonFormat, BotEmployeeReasonMultipleCalls, BotEmployeeReasonTooLarge:
		kind = BotReportFailureExecutionFormat
	case BotEmployeeReasonInterrupted, BotEmployeeReasonOwnerCancelled, BotEmployeeReasonSuperseded:
		kind = BotReportFailureInterrupted
	case BotEmployeeReasonEmailTransport, BotEmployeeReasonMixedControl, BotEmployeeReasonUnknown, BotEmployeeReasonUnverified, BotEmployeeReasonForbidden, BotEmployeeReasonUnsupported, BotEmployeeReasonExpired, BotEmployeeReasonNotFound, BotEmployeeReasonInvalid, BotEmployeeReasonConflict:
	default:
		reason = BotEmployeeReasonUnknown
	}
	explanation, action := EmployeeReasonDetails(reason)
	text := "I couldn't confirm completion of the requested work. " + explanation + " " + action
	if reason == BotEmployeeReasonUnknown && report.Failure == nil {
		switch status {
		case RunQueued:
			text, kind = "I've queued this request; I haven't completed it yet.", ""
		case RunRunning:
			text, kind = "I'm working on this request; I don't have a completed result yet.", ""
		case RunWaitingApproval:
			text, kind = "I need your answer or authorization before I can continue.", ""
		case RunSucceeded:
			text, kind = "I've finished this run, but I don't have an assessed result to share.", ""
		case RunSkipped:
			text, kind = "I skipped this run; I don't have a new result to share.", ""
		}
	}
	return BotEmployeeMessage{Origin: BotEmployeeMessageFallback, Text: text, SourceDigest: report.EmployeeMessageSourceDigest(status), FailureKind: kind, Reason: reason}
}

// FinalizeEmployeeMessage is called only after validation of private work and
// supplied communication. It copies rather than mutating a caller's envelope.
func (r *BotReport) FinalizeEmployeeMessage(status RunStatus, work *BotWorkState) {
	// Suppression authority remains the independently validated marker. When a
	// pending candidate is mixed or erroneous, keep a visible safe explanation
	// instead of promoting the raw UUID-bearing runtime acknowledgment.
	if r.Acknowledgment == nil && BotWorkHasPendingControlCandidate(work) && (r.EmployeeMessage == nil || r.EmployeeMessage.Origin == BotEmployeeMessageAssessed) {
		reason := BotEmployeeFailureReason("")
		if r.Failure == nil {
			reason = BotEmployeeReasonMixedControl
		}
		m := NewBotEmployeeFallback(status, *r, reason)
		r.EmployeeMessage = &m
		return
	}
	if r.EmployeeMessage != nil {
		copy := *r.EmployeeMessage
		copy.SourceDigest = r.EmployeeMessageSourceDigest(status)
		r.EmployeeMessage = &copy
		return
	}
	m := NewBotEmployeeFallback(status, *r, "")
	if work != nil && work.Validate() == nil && work.Candidate != nil && work.Candidate.Text == r.Summary && ((status == RunSucceeded && work.Phase == BotWorkAccepted) || (status == RunPartial && work.VerifiedBlockedResponse() != nil)) {
		m = BotEmployeeMessage{Origin: BotEmployeeMessageAssessed, Text: work.Candidate.Text, SourceDigest: r.EmployeeMessageSourceDigest(status), CandidateDigest: work.Candidate.Digest}
	}
	r.EmployeeMessage = &m
}
