package api

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"syscall"

	"github.com/SAP/astonish/pkg/provider/llmerror"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/store"
)

// Public rendering preserves the safe owner-facing limitation; detailed failure
// stages belong only to private structured diagnostics.
type botReviewValidationReason string

const (
	botReviewRejected      botReviewValidationReason = "rejected"
	botReviewInvalidJSON   botReviewValidationReason = "invalid_json"
	botReviewInvalidSchema botReviewValidationReason = "invalid_schema"
)

type botReviewDecodeError struct{ reason botReviewValidationReason }

func (e *botReviewDecodeError) Error() string {
	if e.reason == botReviewInvalidJSON {
		return "malformed assessment: invalid JSON after format correction"
	}
	return "malformed assessment: invalid schema after format correction"
}

type botReviewValidationStage string

const (
	botReviewBoundary   botReviewValidationStage = "boundary"
	botReviewInput      botReviewValidationStage = "input"
	botReviewSchema     botReviewValidationStage = "response_contract"
	botReviewModel      botReviewValidationStage = "model_response"
	botReviewCompletion botReviewValidationStage = "completion"
	botReviewDecode     botReviewValidationStage = "strict_decode"
	botReviewIdentity   botReviewValidationStage = "candidate_identity_criteria"
	botReviewContent    botReviewValidationStage = "content_protocol"
	botReviewFeedback   botReviewValidationStage = "repair_feedback"
	botReviewFormatting botReviewValidationStage = "formatted_candidate"
)

// botReviewTransientModelError is intentionally narrower than the generic
// provider retry gate. Never infer replay safety from provider text or a bare
// Retryable bit, and preserve the global interrupted-stream no-replay boundary.
func botReviewTransientModelError(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, bot.ErrBudgetExceeded) || errors.Is(err, bot.ErrBudgetReservation) || errors.Is(err, bot.ErrBudgetUnknownCost) ||
		errors.Is(err, store.ErrBotForbidden) || errors.Is(err, store.ErrBotConflict) {
		return false
	}
	var interrupted *llmerror.InterruptedStreamError
	if errors.As(err, &interrupted) {
		return false
	}
	var provider *llmerror.LLMError
	if errors.As(err, &provider) {
		switch provider.StatusCode {
		case http.StatusTooManyRequests, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
			return llmerror.IsRetryable(err)
		default:
			return false
		}
	}
	var network net.Error
	return errors.Is(err, syscall.ECONNRESET) || (errors.As(err, &network) && network.Timeout())
}

// Return only server-owned reason codes, never provider messages/bodies or URLs.
func botReviewModelFailureReason(err error) string {
	var interrupted *llmerror.InterruptedStreamError
	if errors.As(err, &interrupted) {
		return "provider_interrupted"
	}
	if botReviewTransientModelError(err) {
		if llmerror.StatusCode(err) != 0 {
			return "provider_transient"
		}
		return "transport_transient"
	}
	if llmerror.StatusCode(err) != 0 {
		return "provider_rejected"
	}
	return string(botReviewRejected)
}

// Stage/reason codes are server constants. Never format an underlying error:
// provider errors and decoder fields can contain credentials or model payloads.
func botReviewValidationDiagnostic(req bot.RunRequest, stage botReviewValidationStage, attempt int, correction bool, err error) {
	if err == nil {
		return
	}
	reason := string(botReviewRejected)
	var decode *botReviewDecodeError
	var protocol *botContentProtocolError
	switch {
	case errors.Is(err, context.Canceled):
		reason = "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		reason = "deadline"
	case errors.Is(err, store.ErrBotConflict):
		reason = "conflict"
	case errors.Is(err, store.ErrBotForbidden):
		reason = "forbidden"
	case errors.Is(err, bot.ErrBudgetExceeded), errors.Is(err, bot.ErrBudgetReservation), errors.Is(err, bot.ErrBudgetUnknownCost):
		reason = "budget"
	case errors.As(err, &decode):
		if decode.reason == botReviewInvalidJSON || decode.reason == botReviewInvalidSchema {
			reason = string(decode.reason)
		}
	case errors.As(err, &protocol):
		// Do not trust even a privately constructed error's arbitrary reason value.
		switch protocol.reason {
		case botContentCoverage, botContentUnknownRef, botContentQuoteMismatch, botContentInvalidVerdict, botContentMissingExplanation:
			reason = string(protocol.reason)
		}
	default:
		if stage == botReviewModel {
			reason = botReviewModelFailureReason(err)
		}
	}
	slog.Warn("[bot-review-validation-v1]", "bot_id", req.Bot.ID, "run_id", req.Run.ID, "stage", stage, "reason", reason, "attempt", attempt, "correction", correction, "provider_status", llmerror.StatusCode(err))
}

func botReviewRecoverableError(err error) bool {
	if err == nil {
		return false
	}
	var interrupted *llmerror.InterruptedStreamError
	var decode *botReviewDecodeError
	var protocol *botContentProtocolError
	if botReviewTransientModelError(err) || errors.As(err, &interrupted) || errors.As(err, &decode) || errors.As(err, &protocol) {
		return true
	}
	msg := err.Error()
	return strings.HasPrefix(msg, "invalid ") ||
		strings.Contains(msg, "assessment") ||
		strings.Contains(msg, "repair") ||
		strings.Contains(msg, "revision") ||
		strings.Contains(msg, "formatted") ||
		strings.Contains(msg, "coverage limit")
}
