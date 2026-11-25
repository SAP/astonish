package entstore

import (
	"encoding/json"
	personalent "github.com/SAP/astonish/ent/personal"
	teament "github.com/SAP/astonish/ent/team"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"strings"
	"testing"
	"time"
)

func TestBotTitleConfirmationEnvelope(t *testing.T) {
	c := &store.BotTitleConfirmation{Version: store.BotTitleConfirmationVersion, State: store.BotTitleConfirmationNeedsQuestion, BotID: uuid.New(), RunID: uuid.New(), OwnerID: uuid.New(), OriginalTitle: "Private original title", UserEventID: "event"}
	now := time.Now().UTC()
	q := &store.BotInteraction{Kind: store.BotInteractionQuestion, QuestionID: "q", Payload: json.RawMessage(`{"questionId":"q","kind":"select","prompt":"Which task?","options":[{"id":"a","label":"Design"},{"id":"b","label":"Implement"},{"id":"c","label":"Review"}],"allow_custom_answer":true}`)}
	a := store.BotApproval{ID: uuid.New(), BotID: c.BotID, RunID: c.RunID, Status: store.ApprovalPending, Interaction: q, ExpiresAt: now.Add(time.Hour)}
	waiting, err := c.WithQuestion(a)
	if err != nil {
		t.Fatal(err)
	}
	a.Status = store.ApprovalApproved
	a.DecidedBy = &c.OwnerID
	a.DecidedAt = &now
	a.Response = &store.BotInteractionResponse{QuestionID: "q", Answer: "a"}
	confirmed, err := waiting.WithAnswer(a)
	if err != nil {
		t.Fatal(err)
	}
	for _, state := range []*store.BotTitleConfirmation{nil, c, waiting, confirmed} {
		original := botStoredRun{Run: store.BotRun{ID: c.RunID, BotID: c.BotID, UserEventID: c.UserEventID, TitleConfirmation: state, RuntimeAccountingRecorded: true, HistoryCursor: 9007199254740993}, Instructions: c.OriginalTitle, RequestHash: "immutable"}
		b, err := json.Marshal(original)
		if err != nil {
			t.Fatal(err)
		}
		var got botStoredRun
		if err = json.Unmarshal(b, &got); err != nil {
			t.Fatal(err)
		}
		if !got.Run.RuntimeAccountingRecorded || got.Run.HistoryCursor != original.Run.HistoryCursor || got.RequestHash != original.RequestHash || got.Instructions != original.Instructions {
			t.Fatal("envelope loss")
		}
		if state != nil && (got.Run.TitleConfirmation == nil || got.Run.TitleConfirmation.State != state.State) {
			t.Fatal("confirmation lost")
		}
		public, _ := json.Marshal(got.Run)
		if strings.Contains(string(public), "Private original") || strings.Contains(string(public), "runtime_accounting_recorded") || strings.Contains(string(public), "title_confirmation") {
			t.Fatal("private leak")
		}
	}
	var old botStoredRun
	if err := json.Unmarshal([]byte(`{"Run":{"id":"`+c.RunID.String()+`"},"RequestHash":"legacy"}`), &old); err != nil || old.Run.TitleConfirmation != nil || old.Run.RuntimeAccountingRecorded {
		t.Fatal("legacy changed", err)
	}
	bad := botStoredRun{Run: store.BotRun{ID: uuid.New(), BotID: c.BotID, UserEventID: c.UserEventID, TitleConfirmation: c}, Instructions: c.OriginalTitle}
	data, _ := json.Marshal(bad)
	if json.Unmarshal(data, &old) == nil {
		t.Fatal("cross-run accepted")
	}
}

func TestBotCallerCeilingRoundTrip(t *testing.T) {
	run := store.BotRun{ID: uuid.New(), BotID: uuid.New(), SessionID: uuid.New(), Version: 1, ExecutionMode: execution.BotExecutionModeChatInteractive, CallerCeiling: &execution.BotCallerCeiling{Principal: execution.Principal{Subject: "private-caller", Scopes: []string{"tool:execute"}}, ExpiresAt: time.Now().UTC()}}
	codec := newBotContentCodec(t.TempDir(), store.BotContentScope{OwnerID: uuid.NewString()})
	data, err := codec.encodeRun(t.Context(), botStoredRun{Run: run})
	if err != nil {
		t.Fatal(err)
	}
	p, err := decodePersonalBotRun(t.Context(), codec, &personalent.BotRun{ID: run.ID, BotID: run.BotID, Version: 1, SessionID: run.SessionID.String(), Data: data})
	if err != nil {
		t.Fatal(err)
	}
	team, err := decodeTeamBotRun(t.Context(), codec, &teament.BotRun{ID: run.ID, BotID: run.BotID, Version: 1, SessionID: run.SessionID.String(), Data: data})
	if err != nil {
		t.Fatal(err)
	}
	for _, saved := range []*botStoredRun{p, team} {
		if saved.Run.CallerCeiling == nil || saved.Run.CallerCeiling.Principal.Subject != "private-caller" {
			t.Fatal("ceiling lost")
		}
		b, err := json.Marshal(saved.Run)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), "private-caller") || strings.Contains(string(b), "CallerCeiling") {
			t.Fatal("public auth metadata")
		}
	}
}
func TestBotCallerCeilingUnknownModeRejected(t *testing.T) {
	run := store.BotRun{ID: uuid.New(), BotID: uuid.New(), SessionID: uuid.New(), Version: 1, ExecutionMode: "future"}
	codec := newBotContentCodec(t.TempDir(), store.BotContentScope{OwnerID: uuid.NewString()})
	data, err := codec.encodeRun(t.Context(), botStoredRun{Run: run})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodePersonalBotRun(t.Context(), codec, &personalent.BotRun{ID: run.ID, BotID: run.BotID, Version: 1, SessionID: run.SessionID.String(), Data: data}); err == nil {
		t.Fatal("personal accepted unknown mode")
	}
	if _, err := decodeTeamBotRun(t.Context(), codec, &teament.BotRun{ID: run.ID, BotID: run.BotID, Version: 1, SessionID: run.SessionID.String(), Data: data}); err == nil {
		t.Fatal("team accepted unknown mode")
	}
}

func TestBotCallerCeilingLegacyControlOnly(t *testing.T) {
	for _, intent := range []store.BotExecutionIntent{store.BotIntentDraftConversation, store.BotIntentConversation} {
		if !(store.BotRun{Intent: intent}).ControlOnly() {
			t.Fatal("legacy widened")
		}
	}
	if !(store.BotRun{ExecutionMode: "future"}).ControlOnly() {
		t.Fatal("unknown mode not fail closed")
	}
}
