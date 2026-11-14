package api

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/store"
)

func TestBotEmployeeResearchDiagnosticPayloadFree(t *testing.T) {
	var output bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&output, nil)))
	defer slog.SetDefault(previous)
	_, req, _ := botChatTestRequest(t)
	acquired := BotEmployeeResearchResult{Result: bot.RunResult{Status: store.RunPartial, Report: store.BotReport{Summary: "PRIVATE answer", Unverified: []string{"PRIVATE receipt"}, EffectState: store.BotReportEffectsUncertain}}}
	botEmployeeResearchDiagnostic(req, "completion_gate", acquired, errors.New("PRIVATE provider error"), context.Canceled)
	text := output.String()
	for _, want := range []string{"bot-research-continuation-v1", `"stage":"completion_gate"`, `"attempt_error":"other"`, `"boundary_error":"canceled"`, `"unverified_count":1`, `"summary_present":true`, req.Run.ID.String()} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing diagnostic %q: %s", want, text)
		}
	}
	if strings.Contains(text, "PRIVATE") {
		t.Fatal("diagnostic leaked payload")
	}
}
