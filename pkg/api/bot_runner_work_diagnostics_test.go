package api

import (
	"errors"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/store"
)

func TestBotWorkPreparationValidationDiagnostics(t *testing.T) {
	ctx, req, _ := botChatTestRequest(t)
	w := store.NewBotWorkState(store.BotWorkContract{BotID: req.Bot.ID, RunID: req.Run.ID, Version: req.Run.Version, Instructions: req.Instructions, Charter: req.Version.Charter})
	m := &formatRecoveryModel{t: t, texts: []string{`{"criteria":[{"id":"private-input","text":"private-input","source_ids":["private-input"]}]}`}}
	var usage store.BotUsage
	err := prepareBotWorkContract(ctx, req, m, w, &usage)
	if !errors.Is(err, store.ErrBotInvalid) || !strings.Contains(err.Error(), "preparation criteria rejected") || !strings.Contains(err.Error(), "criteria[0].source_ids[0]") || strings.Contains(err.Error(), "private-input") {
		t.Fatalf("unsafe or imprecise preparation error: %v", err)
	}
	if m.calls != 1 || usage.InputTokens != 1 || usage.ToolCalls != 0 {
		t.Fatalf("unexpected replay or usage: calls=%d usage=%+v", m.calls, usage)
	}
}
