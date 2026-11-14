package api

import (
	"context"
	"errors"
	"log/slog"

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
	}
	slog.Warn("[bot-review-validation-v1]", "bot_id", req.Bot.ID, "run_id", req.Run.ID, "stage", stage, "reason", reason, "attempt", attempt, "correction", correction)
}
