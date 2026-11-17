package store

import (
	"encoding/json"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/google/uuid"
	"strings"
	"testing"
	"time"
)

func TestBotTitleConfirmationContract(t *testing.T) {
	c := &BotTitleConfirmation{Version: BotTitleConfirmationVersion, State: BotTitleConfirmationNeedsQuestion, BotID: uuid.New(), RunID: uuid.New(), OwnerID: uuid.New(), OriginalTitle: "Research project", UserEventID: "event"}
	run := BotRun{ID: c.RunID, BotID: c.BotID, UserEventID: c.UserEventID, ExecutionMode: execution.BotExecutionModeChatInteractive}
	if !run.RequiresPhysicalHome() {
		t.Fatal("legacy changed")
	}
	run.TitleConfirmation = c
	if c.ValidateBinding(run, c.OwnerID) != nil || run.RequiresPhysicalHome() {
		t.Fatal("initial contract")
	}
	q := &BotInteraction{Kind: BotInteractionQuestion, QuestionID: "q", Payload: json.RawMessage(`{"questionId":"q","kind":"select","prompt":"Which project research?","options":[{"id":"a","label":"Compare designs"},{"id":"b","label":"Review implementation"},{"id":"c","label":"Summarize documentation"}],"allow_custom_answer":true}`)}
	now := time.Now().UTC()
	a := BotApproval{ID: uuid.New(), BotID: c.BotID, RunID: c.RunID, Status: ApprovalPending, Interaction: q, CreatedAt: now.Add(-time.Minute), ExpiresAt: now.Add(time.Minute)}
	waiting, err := c.WithQuestion(a)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := waiting.WithQuestion(a); err == nil {
		t.Fatal("repeated transition")
	}
	clone := waiting.Clone()
	clone.Question.Payload[0] = 'x'
	if waiting.Validate() != nil {
		t.Fatal("clone alias")
	}
	for _, answer := range []string{"a", "Focus on private project details"} {
		a.Status = ApprovalApproved
		a.DecidedBy = &c.OwnerID
		a.DecidedAt = &now
		a.Response = &BotInteractionResponse{QuestionID: "q", Answer: answer}
		confirmed, err := waiting.WithAnswer(a)
		if err != nil {
			t.Fatal(err)
		}
		want := answer
		if answer == "a" {
			want = "Compare designs"
		}
		if confirmed.ConfirmedAssignment != want || confirmed.OriginalTitle != c.OriginalTitle {
			t.Fatal("assignment")
		}
		run.TitleConfirmation = confirmed
		if !run.RequiresPhysicalHome() {
			t.Fatal("confirmed home")
		}
		if _, err := confirmed.WithAnswer(a); err == nil {
			t.Fatal("repeat confirmation")
		}
		confirmed.ConfirmedAssignment += "tamper"
		if confirmed.Validate() == nil {
			t.Fatal("digest tamper")
		}
	}
	for _, mutate := range []func(*BotApproval){func(a *BotApproval) { a.RunID = uuid.New() }, func(a *BotApproval) { u := uuid.New(); a.DecidedBy = &u }, func(a *BotApproval) { a.ExpiresAt = now }, func(a *BotApproval) { a.ID = uuid.New() }, func(a *BotApproval) { a.Response = &BotInteractionResponse{QuestionID: "foreign", Answer: "a"} }} {
		bad := a
		mutate(&bad)
		if _, err := waiting.WithAnswer(bad); err == nil {
			t.Fatal("foreign/stale decision accepted")
		}
	}
	for _, mutate := range []func(*BotTitleConfirmation){func(c *BotTitleConfirmation) { c.Version++ }, func(c *BotTitleConfirmation) { c.State = "unknown" }, func(c *BotTitleConfirmation) { c.AssignmentDigest = "answer" }, func(c *BotTitleConfirmation) { c.ApprovalID = uuid.New() }} {
		bad := c.Clone()
		mutate(bad)
		if bad.Validate() == nil {
			t.Fatal("invalid state")
		}
	}
	if waiting.ValidateBinding(run, uuid.New()) == nil {
		t.Fatal("foreign owner")
	}
	for _, raw := range []string{strings.Replace(string(q.Payload), `"allow_custom_answer":true`, `"allow_custom_answer":false`, 1), strings.Replace(string(q.Payload), `Review implementation`, `Compare designs`, 1), strings.Replace(string(q.Payload), `Review implementation`, ` `, 1), strings.Replace(string(q.Payload), `{"id":"c","label":"Summarize documentation"}`, `{"id":"a","label":"Summary"}`, 1)} {
		bad := *q
		bad.Payload = json.RawMessage(raw)
		if ValidateBotTitleQuestion(&bad) == nil {
			t.Fatal("invalid title options")
		}
	}
}

func TestBotTitleConfirmationProvenance(t *testing.T) {
	owner, run := uuid.New().String(), uuid.New().String()
	digest := BotTitleAssignmentDigest("assignment")
	p := BotMandateProvenance{InitiationKind: BotInitiationKindTitleConfirmation, Source: BotMandateSourceAuthenticatedUser, ActorID: owner, RunID: run, RequestID: "request", RecordedAt: time.Now(), UserEventID: "event", SourceDigest: digest, TitleConfirmation: &BotTitleConfirmationProvenance{BotID: uuid.New().String(), RunID: run, OwnerID: owner, UserEventID: "event", ApprovalID: uuid.New().String(), QuestionID: "q", AssignmentDigest: digest}}
	if p.Validate() != nil {
		t.Fatal("valid provenance")
	}
	record := BotMandateRecord{Provenance: p}
	clone := record.Clone()
	clone.Provenance.TitleConfirmation.OwnerID = uuid.New().String()
	if clone.Provenance.Validate() == nil || record.Provenance.Validate() != nil {
		t.Fatal("provenance tampering or clone")
	}
	p.SourceDigest = BotTitleAssignmentDigest("other")
	if p.Validate() == nil {
		t.Fatal("digest binding")
	}
}

func TestBotInteractionTextCustom(t *testing.T) {
	for _, raw := range []string{`{"questionId":"q","kind":"text","prompt":"Details"}`, `{"questionId":"q","kind":"select","prompt":"Pick","options":[{"id":"x"}],"allow_custom_answer":true}`} {
		i := &BotInteraction{Kind: BotInteractionQuestion, QuestionID: "q", Payload: json.RawMessage(raw)}
		if err := i.ValidateResponse(&BotInteractionResponse{QuestionID: "q", Answer: "custom answer"}); err != nil {
			t.Fatal(err)
		}
		for _, r := range []*BotInteractionResponse{{QuestionID: "q", Answer: "  "}, {QuestionID: "other", Answer: "valid"}} {
			if i.ValidateResponse(r) == nil {
				t.Fatal("accepted invalid response")
			}
		}
	}
}
func TestBotInteractionTypedAnswer(t *testing.T) {
	for _, kind := range []string{"yesno", "select"} {
		i := &BotInteraction{Kind: BotInteractionQuestion, QuestionID: "q", Payload: json.RawMessage(`{"questionId":"q","kind":"` + kind + `","prompt":"Choose","options":[{"id":"safe"}]}`)}
		answer := "no"
		if kind == "select" {
			answer = "safe"
		}
		if i.ValidateResponse(&BotInteractionResponse{QuestionID: "q", Answer: answer}) != nil {
			t.Fatal("valid typed answer rejected")
		}
		for _, r := range []*BotInteractionResponse{nil, {QuestionID: "other", Answer: answer}, {QuestionID: "q", Answer: "true"}, {QuestionID: "q", Answer: "unknown"}} {
			if i.ValidateResponse(r) == nil {
				t.Fatalf("invalid answer accepted: %+v", r)
			}
		}
	}
	i := &BotInteraction{Kind: BotInteractionQuestion, QuestionID: "q", Payload: json.RawMessage(`{"questionId":"q","kind":"select","prompt":"Choose","options":[{"id":"x"},{"id":"x"}]}`)}
	if i.Validate() == nil {
		t.Fatal("ambiguous choice accepted")
	}
}
