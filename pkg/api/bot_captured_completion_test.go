package api

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"google.golang.org/adk/model"
	"google.golang.org/adk/session"
	"iter"
	"os"
	"strings"
	"testing"
)

// This probe exercises the production review entry point before any paid call.
// It is deliberately a failing acceptance probe if the exact captured artifact
// cannot reach a reviewer; it never approves the design or changes its bytes.
type capturedReviewEntryProbe struct{ called bool }

func (*capturedReviewEntryProbe) Name() string { return "review-entry-probe" }
func (m *capturedReviewEntryProbe) GenerateContent(context.Context, *model.LLMRequest, bool) iter.Seq2[*model.LLMResponse, error] {
	m.called = true
	return func(yield func(*model.LLMResponse, error) bool) {
		yield(nil, fmt.Errorf("review-entry-only: no approval supplied"))
	}
}
func TestBotCapturedCompletionReviewEntry(t *testing.T) {
	path := os.Getenv("ASTONISH_CAPTURED_FINAL_EVENT")
	if path == "" {
		t.Skip("captured final event required")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var event session.Event
	if err = json.Unmarshal(raw, &event); err != nil {
		t.Fatal(err)
	}
	var text strings.Builder
	for _, part := range event.Content.Parts {
		if !part.Thought {
			text.WriteString(part.Text)
		}
	}
	body := text.String()
	if len(body) != 47138 || fmt.Sprintf("%x", sha256.Sum256([]byte(body))) != "726b6a77d1c633decaa611794bd83b9b7f1ce7f1cbdc407172484de5eaee7271" {
		t.Fatal("capture bytes changed")
	}
	ctx, req, _ := botChatTestRequest(t)
	w := &store.BotWorkState{Candidate: &store.BotWorkCandidate{Text: body}}
	m := &capturedReviewEntryProbe{}
	_, _, err = reviewBotEmployeeCandidate(ctx, req, m, w, &store.BotUsage{})
	t.Logf("CAPTURED_REVIEW_ENTRY bytes=%d nonempty_items=%d max_items=%d provider_called=%t error=%v", len(body), len(botContentInput(w).Items), botWorkContentMaxItems, m.called, err)
	if !m.called {
		t.Fatalf("exact captured completion cannot reach independent review: %v", err)
	}
}

func TestBotCapturedCompletionEligibility(t *testing.T) {
	path := os.Getenv("ASTONISH_CAPTURED_CONTENT")
	if path == "" {
		t.Skip("captured content required")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var objects []struct {
		Content struct {
			SessionID string         `json:"session_id"`
			Event     *session.Event `json:"event"`
		} `json:"content"`
	}
	if err = json.Unmarshal(raw, &objects); err != nil {
		t.Fatal(err)
	}
	ctx, req, _ := botChatTestRequest(t)
	ctx = execution.WithBotRunIdentity(ctx, req.Policy.Identity())
	cr := &ChatRunner{ctx: ctx}
	for _, o := range objects {
		if o.Content.SessionID == "99e9cc92-495d-48e4-8cd8-a1229fe9ec1e" {
			cr.observeCompletion(o.Content.Event)
		}
	}
	c := cr.Completion()
	work := &store.BotWorkState{Protocol: store.BotWorkProtocolAgentLedV1, AgentWork: &store.BotAgentWork{DeliveryIntent: store.BotDeliveryFileDeliverable, DeliverableFormat: store.BotDeliverableMarkdown}}
	req.Work = work
	report := store.BotReport{EffectState: store.BotReportEffectsReadOnly}
	// Unit boundary input only: physical harness owns runtime effect evidence.
	// Replayed JSON cannot recreate private dispatch receipts or prove read-only IO.
	result := bot.RunResult{Status: store.RunPartial, Report: report}
	t.Logf("captured final=%t id=%s chars=%d calls=%d responses=%d error=%v retained=%t", c.Final, c.FinalEventID, len(c.FinalText), c.Usage.ToolCalls, c.toolResponses, c.Err, botCanRetainReadOnlyFinal(c, req, report))
	var receipts []store.BotArtifact
	resources := &BotChatResources{PublishedFiles: func() []store.BotArtifact { return receipts }, RecoverFileDelivery: func(_ context.Context, text string) ([]store.BotArtifact, error) {
		ref := store.BotContentRef{Format: store.BotContentFormatVersion, Scope: store.BotContentScope{BotID: req.Bot.ID, OwnerID: req.Bot.OwnerID.String()}, Kind: store.BotContentArtifact, Key: "content-v1/artifact-1-captured", SHA256: fmt.Sprintf("%x", sha256.Sum256([]byte(text))), SizeBytes: int64(len(text)), Revision: 1}
		receipts = []store.BotArtifact{{ID: uuid.New(), HomeID: req.Bot.Home.ID, ContentRef: &ref, SHA256: ref.SHA256, SizeBytes: ref.SizeBytes}}
		return receipts, nil
	}}
	if err = recoverBotFileDelivery(ctx, req, resources, work, c, &result); err != nil {
		t.Fatal(err)
	}
	if result.Status != store.RunPartial || len(receipts) != 1 || !strings.Contains(result.Report.Summary, "Markdown") {
		t.Fatal("partial communication lost")
	}
}
