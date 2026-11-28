package api_test

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/SAP/astonish/pkg/store"
)

// titleStateCreate keeps public team creation denied while exercising the same
// atomic title admission on an existing trusted team fixture.
func titleStateCreate(t *testing.T, f *interactiveHTTPFixture, body string, status int) []byte {
	t.Helper()
	if f.scope != store.BotScopeTeam {
		return f.request("POST", "/api/studio/bots", body, status)
	}
	f.request("POST", "/api/studio/bots", body, 403)
	var input struct {
		Name string `json:"name"`
		Key  string `json:"idempotency_key"`
	}
	if err := json.Unmarshal([]byte(body), &input); err != nil {
		t.Fatal(err)
	}
	version, err := store.NewBotVersionForCreation(f.ctx, f.b.OwnerID)
	if err != nil {
		t.Fatal(err)
	}
	version.Charter = input.Name
	ctx := store.WithBotMessageAdmission(f.ctx, f.registry)
	receipt, err := f.repo.(store.BotTitleStartStore).CreateAndStart(ctx, store.BotCreateRequest{Name: input.Name, OwnerID: f.b.OwnerID, Version: version, IdempotencyKey: input.Key})
	if status == 409 {
		if !errors.Is(err, store.ErrBotConflict) {
			t.Fatalf("changed title replay: %v", err)
		}
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	message := &store.BotMessageReceipt{Run: receipt.Run, HomeSessionID: receipt.Bot.HomeSessionID, UserEventID: receipt.UserEventID, HistoryCursor: receipt.HistoryCursor}
	raw, err := json.Marshal(titleHTTPReceipt{Bot: receipt.Bot, InitialTurn: message})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
