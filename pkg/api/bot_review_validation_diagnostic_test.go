package api

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/provider/llmerror"
	"github.com/SAP/astonish/pkg/store"
)

func TestBotReviewValidationDiagnosticPayloadFree(t *testing.T) {
	_, req, _ := botChatTestRequest(t)
	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })
	for _, tc := range []struct {
		stage  botReviewValidationStage
		err    error
		reason string
	}{
		{botReviewContent, botContentProtocolFailure(botContentQuoteMismatch, "secret-item", "secret-ref", 2, 3), "quote_not_captured"},
		{botReviewContent, &botContentProtocolError{reason: "secret-reason"}, "rejected"},
		{botReviewDecode, &botReviewDecodeError{reason: botReviewInvalidJSON}, "invalid_json"},
		{botReviewDecode, &botReviewDecodeError{reason: botReviewInvalidSchema}, "invalid_schema"},
		{botReviewDecode, &botReviewDecodeError{reason: "secret-reason"}, "rejected"},
		{botReviewDecode, errors.New("secret-provider-response https://private.invalid"), "rejected"},
		{botReviewBoundary, context.Canceled, "canceled"},
		{botReviewIdentity, store.ErrBotConflict, "conflict"},
		{botReviewModel, llmerror.NewLLMError("secret-provider", 503, "secret-message", "https://private.invalid"), "provider_transient"},
		{botReviewModel, llmerror.NewLLMError("secret-provider", 400, "secret-message", "https://private.invalid"), "provider_rejected"},
		{botReviewModel, llmerror.NewInterruptedStream(llmerror.NewLLMError("secret-provider", 503, "secret-message", "https://private.invalid")), "provider_interrupted"},
		{botReviewModel, bot.ErrBudgetExceeded, "budget"},
	} {
		buf.Reset()
		botReviewValidationDiagnostic(req, tc.stage, 1, true, tc.err)
		log := buf.String()
		if strings.Contains(log, "secret") || strings.Contains(log, "private.invalid") || strings.Contains(log, "sha256") || !strings.Contains(log, `"reason":"`+tc.reason+`"`) || !strings.Contains(log, `"stage":"`+string(tc.stage)+`"`) || !strings.Contains(log, `"correction":true`) {
			t.Fatalf("unsafe/inexact diagnostic: %s", log)
		}
		if tc.reason == "provider_transient" && !strings.Contains(log, `"provider_status":503`) {
			t.Fatalf("typed HTTP status missing: %s", log)
		}
	}
}
