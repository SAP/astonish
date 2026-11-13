package store

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/google/uuid"
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

// NewBotEmployeeAgentMessage binds communication to the exact captured final
// result. It establishes identity, not factual correctness or effect authority.
// Optional work/check metadata and legacy model review are not prerequisites.
// Pending controls do not invalidate independent answer content. The API must
// append their canonical queued acknowledgment before capturing the candidate;
// only the store's validated receipt may suppress a pure acknowledgment.
func NewBotEmployeeAgentMessage(status RunStatus, report BotReport, work *BotWorkState) (BotEmployeeMessage, error) {
	if work == nil || work.Protocol != BotWorkProtocolAgentLedV1 || work.Candidate == nil {
		return BotEmployeeMessage{}, ErrBotInvalid
	}
	m := BotEmployeeMessage{Origin: BotEmployeeMessageAgent, Text: work.Candidate.Text, CandidateDigest: work.Candidate.Digest, SourceDigest: report.EmployeeMessageSourceDigest(status)}
	report.EmployeeMessage = &m
	if err := report.ValidateEmployeeMessage(status, work); err != nil {
		return BotEmployeeMessage{}, err
	}
	return m, nil
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
	case BotEmployeeMessageAgent:
		if len(r.Unverified) != 0 || r.Work.Validate() != nil || r.Quality != nil || m.CandidateDigest != r.Work.CandidateDigest || m.CandidateDigest == "" || m.Text != r.Summary || m.FailureKind != "" || m.Reason != "" || !r.Work.allowsResult(status) {
			return ErrBotInvalid
		}
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
		original.Acknowledgment = nil // server-owned publication marker
		// Older and current composers review before CommitRun attaches the
		// server-owned skill receipt. Accept that exact pre-receipt snapshot,
		// never omit Skill from the durable SourceDigest checked above.
		if m.ReviewedSourceDigest != original.EmployeeMessageSourceDigest(status) && original.Skill != nil {
			original.Skill = nil
		}
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
	BotEmployeeReasonAgentUnverifiedEffects BotEmployeeFailureReason = "agent_effects_unverified"
	BotEmployeeReasonAgentNoResult          BotEmployeeFailureReason = "agent_result_unavailable"
	BotEmployeeReasonEmailTransport         BotEmployeeFailureReason = "required_email_transport_unavailable"
	BotEmployeeReasonMixedControl           BotEmployeeFailureReason = "independent_work_unverified"
	BotEmployeeReasonUnknown                BotEmployeeFailureReason = "operational_reason_unavailable"
	BotEmployeeReasonUnverified             BotEmployeeFailureReason = "result_not_verified"
	BotEmployeeReasonSetup                  BotEmployeeFailureReason = "setup_incomplete"
	BotEmployeeReasonBudget                 BotEmployeeFailureReason = "budget_prevented_work"
	BotEmployeeReasonCost                   BotEmployeeFailureReason = "model_cost_unknown"
	BotEmployeeReasonInput                  BotEmployeeFailureReason = "input_does_not_fit"
	BotEmployeeReasonOverrun                BotEmployeeFailureReason = "usage_exceeded_budget"
	BotEmployeeReasonFormat                 BotEmployeeFailureReason = "model_response_rejected"
	BotEmployeeReasonMultipleCalls          BotEmployeeFailureReason = "multiple_tool_calls_rejected"
	BotEmployeeReasonTooLarge               BotEmployeeFailureReason = "response_size_limit"
	BotEmployeeReasonInterrupted            BotEmployeeFailureReason = "interrupted"
	BotEmployeeReasonForbidden              BotEmployeeFailureReason = "authorization_denied"
	BotEmployeeReasonUnsupported            BotEmployeeFailureReason = "capability_unsupported"
	BotEmployeeReasonExpired                BotEmployeeFailureReason = "approval_or_retention_expired"
	BotEmployeeReasonNotFound               BotEmployeeFailureReason = "resource_not_found"
	BotEmployeeReasonInvalid                BotEmployeeFailureReason = "request_invalid"
	BotEmployeeReasonConflict               BotEmployeeFailureReason = "revision_or_lease_conflict"
	BotEmployeeReasonOwnerCancelled         BotEmployeeFailureReason = "owner_cancelled"
	BotEmployeeReasonSuperseded             BotEmployeeFailureReason = "schedule_superseded"
)

func EmployeeReasonDetails(reason BotEmployeeFailureReason) (string, string) {
	switch reason {
	case BotEmployeeReasonAgentUnverifiedEffects:
		return "Some tool outcomes are not confirmed. This reply does not confirm that any requested external action, including delivery, completed.", "Check the destination or recorded outcome before retrying to avoid duplicate actions; no retry has been started."
	case BotEmployeeReasonAgentNoResult:
		return "No publishable final answer is available from this run.", "You can revise the request or start a new run; no retry has been started."
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
	case BotEmployeeReasonAgentUnverifiedEffects, BotEmployeeReasonAgentNoResult, BotEmployeeReasonEmailTransport, BotEmployeeReasonMixedControl, BotEmployeeReasonUnknown, BotEmployeeReasonUnverified, BotEmployeeReasonForbidden, BotEmployeeReasonUnsupported, BotEmployeeReasonExpired, BotEmployeeReasonNotFound, BotEmployeeReasonInvalid, BotEmployeeReasonConflict:
	default:
		reason = BotEmployeeReasonUnknown
	}
	explanation, action := EmployeeReasonDetails(reason)
	text := "I couldn't confirm completion of the requested work. " + explanation + " " + action
	if reason == BotEmployeeReasonSetup {
		text = "I couldn't start the requested work because internal preparation failed. No result was produced. Please try again; if this continues, ask an administrator to check the run diagnostics."
	}
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
	if report.Work != nil && report.Work.Protocol == BotWorkProtocolAgentLedV1 || reason == BotEmployeeReasonAgentNoResult {
		if reason == BotEmployeeReasonUnknown || reason == BotEmployeeReasonUnverified {
			reason = BotEmployeeReasonAgentNoResult
		}
		text, kind = agentEmployeeFallbackText(status, report, reason, kind)
	}
	return BotEmployeeMessage{Origin: BotEmployeeMessageFallback, Text: text, SourceDigest: report.EmployeeMessageSourceDigest(status), FailureKind: kind, Reason: reason}
}

// agentEmployeeFallbackText interpolates only closed stage names, counts, and
// typed artifact IDs, never diagnostics, observation prose, paths or provider URLs.
// Recorded observations are not claims that an external effect completed.
func agentEmployeeFallbackText(status RunStatus, report BotReport, reason BotEmployeeFailureReason, kind BotReportFailureKind) (string, BotReportFailureKind) {
	switch status {
	case RunQueued:
		return "I've queued this request; I haven't completed it yet.", ""
	case RunRunning:
		return "I'm working on this request; no final answer is available yet.", ""
	case RunWaitingApproval:
		return "I need your answer or authorization before I can continue. The pending action has not been confirmed as completed.", ""
	case RunSkipped:
		return "I skipped this run; I don't have a new result to share.", ""
	}
	if kind == "" {
		kind = BotReportFailureOperational
	}
	stage := "before a final answer was available"
	if report.Work != nil {
		switch report.Work.Phase {
		case BotWorkWorking:
			stage = "during execution"
		case BotWorkBlocked:
			stage = "at a blocked step"
		case BotWorkInterrupted:
			stage = "when execution was interrupted"
		case BotWorkFinished:
			stage = "while preparing the final answer for publication"
		}
	}
	explanation, action := EmployeeReasonDetails(reason)
	if reason == BotEmployeeReasonUnknown || reason == BotEmployeeReasonUnverified {
		explanation, action = EmployeeReasonDetails(BotEmployeeReasonAgentNoResult)
	}
	if reason == BotEmployeeReasonFormat {
		action = "Try a smaller request; if this persists, ask an administrator to check the model connection."
	}
	text := "I couldn't complete this request " + stage + ". " + explanation
	observations := 0
	for _, evidence := range append(append([]BotEvidence(nil), report.Changed...), report.Unchanged...) {
		if evidence.Kind == BotEvidenceObservation && evidence.ValidateCompletion() == nil {
			observations++
		}
	}
	if observations > 0 {
		text += fmt.Sprintf(" The run recorded %d tool observation(s); this does not confirm that all requested actions completed.", observations)
	}
	for _, artifact := range report.Artifacts {
		if artifact.ID != uuid.Nil && artifact.HomeID != uuid.Nil && skillDigestValid(artifact.SHA256) {
			text += " Recorded output reference: " + artifact.ID.String() + "."
		}
	}
	return text + " " + action, kind
}

// FinalizeEmployeeMessage is called only after validation of private work and
// supplied communication. It copies rather than mutating a caller's envelope.
func (r *BotReport) FinalizeEmployeeMessage(status RunStatus, work *BotWorkState) {
	if (work != nil && work.Protocol == BotWorkProtocolAgentLedV1 || r.Work != nil && r.Work.Protocol == BotWorkProtocolAgentLedV1) && (r.EmployeeMessage == nil || r.EmployeeMessage.Origin != BotEmployeeMessageOperation) {
		m := r.EmployeeMessage
		if m == nil {
			if agent, err := NewBotEmployeeAgentMessage(status, *r, work); err == nil {
				r.EmployeeMessage = &agent
				return
			}
		} else if m.Origin == BotEmployeeMessageAgent {
			copy := *m
			original := *r
			// These receipts are attached only by the trusted committer, after
			// validation. Never silently rebind changed result/work/evidence.
			original.Skill, original.Acknowledgment = nil, nil
			if m.SourceDigest == r.EmployeeMessageSourceDigest(status) || m.SourceDigest == original.EmployeeMessageSourceDigest(status) {
				copy.SourceDigest = r.EmployeeMessageSourceDigest(status)
				candidate := *r
				candidate.EmployeeMessage = &copy
				if candidate.ValidateEmployeeMessage(status, work) == nil {
					r.EmployeeMessage = &copy
					return
				}
			}
		}
		reason := BotEmployeeFailureReason("")
		if m != nil && m.Origin == BotEmployeeMessageFallback {
			reason = m.Reason
		}
		if len(r.Unverified) != 0 && (reason == "" || reason == BotEmployeeReasonAgentNoResult || reason == BotEmployeeReasonUnknown || reason == BotEmployeeReasonUnverified) {
			reason = BotEmployeeReasonAgentUnverifiedEffects
		}
		fallback := NewBotEmployeeFallback(status, *r, reason)
		r.EmployeeMessage = &fallback
		return
	}
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
		// A receipt may be additive; changed semantic facts (including quality
		// revision changes) are not a review of the finalized report. Publish a
		// safe explanation rather than persisting an unpublishable envelope.
		if r.ValidateEmployeeMessageIntegrity(status) != nil {
			fallback := NewBotEmployeeFallback(status, *r, "")
			r.EmployeeMessage = &fallback
		}
		return
	}
	m := NewBotEmployeeFallback(status, *r, "")
	if work != nil && work.Validate() == nil && work.Candidate != nil && work.Candidate.Text == r.Summary && ((status == RunSucceeded && work.Phase == BotWorkAccepted) || (status == RunPartial && work.VerifiedBlockedResponse() != nil)) {
		m = BotEmployeeMessage{Origin: BotEmployeeMessageAssessed, Text: work.Candidate.Text, SourceDigest: r.EmployeeMessageSourceDigest(status), CandidateDigest: work.Candidate.Digest}
	}
	r.EmployeeMessage = &m
}
