package api

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

func TestBotFileDeliveryRecoveryGate(t *testing.T) {
	for _, mode := range []string{"saved", "unsupported", "undeclared", "chat", "binary", "missing-receipt", "partial", "uncertain", "cancelled", "blocked", "event-missing", "empty", "error", "unbound", "foreign-owner", "retained-read-feedback"} {
		t.Run(mode, func(t *testing.T) {
			ctx, req, _ := botChatTestRequest(t)
			work := &store.BotWorkState{AgentWork: &store.BotAgentWork{DeliveryIntent: store.BotDeliveryFileDeliverable, DeliverableFormat: store.BotDeliverableMarkdown}}
			text := strings.Repeat("Full supported text.\n", 2000)
			c := ChatCompletion{Final: true, FinalEventID: "final", FinalText: text}
			result := bot.RunResult{Status: store.RunSucceeded, Report: store.BotReport{Summary: text, EffectState: store.BotReportEffectsReadOnly}}
			calls := 0
			var trusted []store.BotArtifact
			resources := &BotChatResources{PublishedFiles: func() []store.BotArtifact { return trusted }, RecoverFileDelivery: func(_ context.Context, source string) ([]store.BotArtifact, error) {
				calls++
				if source != text {
					t.Fatal("clipped source")
				}
				if mode == "unsupported" {
					return nil, store.ErrBotUnsupported
				}
				if mode == "missing-receipt" {
					return nil, nil
				}
				ref := store.BotContentRef{Format: store.BotContentFormatVersion, Scope: store.BotContentScope{BotID: req.Bot.ID, OwnerID: req.Bot.OwnerID.String()}, Kind: store.BotContentArtifact, Key: "content-v1/artifact-1-receipt", SHA256: fmt.Sprintf("%x", sha256.Sum256([]byte(source))), SizeBytes: int64(len(source)), Revision: 1}
				if mode == "foreign-owner" {
					ref.Scope.OwnerID = uuid.NewString()
				}
				trusted = []store.BotArtifact{{ID: uuid.New(), HomeID: req.Bot.Home.ID, ContentRef: &ref, SHA256: ref.SHA256, SizeBytes: ref.SizeBytes}}
				return trusted, nil
			}}
			switch mode {
			case "retained-read-feedback":
				req.Work = work
				work.Protocol = store.BotWorkProtocolAgentLedV1
				c.Err = &botToolFeedbackError{cause: fmt.Errorf("missing read directory"), recoverable: true}
				c.Usage.ToolCalls, c.toolResponses = 1, 1
				result.Status = store.RunPartial
			case "blocked":
				c.Blocked = true
			case "event-missing":
				c.FinalEventID = ""
			case "empty":
				c.FinalText = ""
			case "error":
				c.Err = fmt.Errorf("unsafe provider error")
			case "unbound":
				resources.PublishedFiles = func() []store.BotArtifact { return nil }
			case "undeclared":
				work.AgentWork.DeliveryIntent = ""
				work.AgentWork.DeliverableFormat = ""
			case "chat":
				work.AgentWork.DeliveryIntent = store.BotDeliveryChatOnly
			case "binary":
				work.AgentWork.DeliverableFormat = store.BotDeliverableBinary
			case "partial":
				c.Final = false
			case "uncertain":
				result.Report.EffectState = store.BotReportEffectsUncertain
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			err := recoverBotFileDelivery(ctx, req, resources, work, c, &result)
			if mode == "unsupported" && (!errors.Is(err, store.ErrBotUnsupported) || calls != 1 || len(result.Report.Artifacts) != 0) {
				t.Fatalf("unsupported required delivery was masked: err=%v calls=%d", err, calls)
			}
			if mode == "saved" || mode == "retained-read-feedback" {
				if err != nil || calls != 1 || result.Report.Summary == text || len(result.Report.Artifacts) != 1 {
					t.Fatalf("save: %v %+v calls=%d", err, result, calls)
				}
			} else if mode == "chat" || mode == "undeclared" {
				if err != nil || calls != 0 || result.Report.Summary != text {
					t.Fatal("changed ordinary conversation")
				}
			} else {
				if err == nil {
					t.Fatal("missing file accepted")
				}
				if mode != "unsupported" && mode != "missing-receipt" && mode != "unbound" && mode != "foreign-owner" && calls != 0 {
					t.Fatal("unsafe recovery invoked")
				}
			}
		})
	}
}

func TestBotFileDeliverySummaryNotExpanded(t *testing.T) {
	_, req, _ := botChatTestRequest(t)
	req.Work = &store.BotWorkState{Protocol: store.BotWorkProtocolAgentLedV1}
	c := ChatCompletion{FinalText: "Saved [report](outputs/report.md).", reportBodies: map[string]string{"outputs/report.md": "FULL LONG REPORT"}, Report: store.BotReport{Artifacts: []store.BotArtifact{{HomeID: req.Bot.Home.ID, RelativePath: "outputs/report.md"}}}}
	if report := botCompletionReport(c, req); report.Summary != c.FinalText {
		t.Fatalf("expanded: %s", report.Summary)
	}
}
