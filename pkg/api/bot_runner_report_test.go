package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
	"sync/atomic"
	"testing"
	"time"
)

func TestBotRuntimeReport(t *testing.T) {
	for _, scenario := range []string{"verified", "unverified", "spoof", "invalid", "foreign-home"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, req, ss := botChatTestRequest(t)
			req.Bot.Home.ID = uuid.New()
			ctx = agent.WithBotPolicyRuntime(ctx, agent.BotPolicyRuntime{Classifier: botChatTestClassifier{}, Guard: botChatTestGuard{}})
			body := "# Source report\nObserved independently."
			digest := sha256.Sum256([]byte(body))
			e := store.BotEvidence{Kind: store.BotEvidenceObservation, Summary: "Observed change", Source: "fixture://receipt", ObservedAt: time.Now(), ActorID: req.Bot.OwnerID}
			next := time.Now().Add(time.Hour).UTC()
			outcome := bot.ToolOutcome{Verified: true, Changed: []store.BotEvidence{e}, Unchanged: []store.BotEvidence{e}, NextRunAt: &next, Artifacts: []bot.ToolReportArtifact{{Artifact: store.BotArtifact{ID: uuid.New(), HomeID: req.Bot.Home.ID, RelativePath: "report.md", SHA256: hex.EncodeToString(digest[:]), Title: "Report", MediaType: "text/markdown"}, Body: body}}}
			switch scenario {
			case "unverified":
				outcome.Verified = false
			case "invalid":
				outcome.Changed[0].Source = ""
			case "missing-artifact-id":
				outcome.Artifacts[0].Artifact.ID = uuid.Nil
			case "bad-digest":
				outcome.Artifacts[0].Artifact.SHA256 = "invalid"
			case "unsafe-path":
				outcome.Artifacts[0].Artifact.RelativePath = "../report.md"
			case "foreign-home":
				outcome.Artifacts[0].Artifact.HomeID = uuid.New()
			}
			action, err := functiontool.New(functiontool.Config{Name: "read_file", Description: "Observed source"}, func(c tool.Context, _ map[string]any) (map[string]any, error) {
				if scenario != "spoof" {
					bot.RecordToolOutcome(c, c.FunctionCallID(), outcome)
				}
				// The identical JSON payload alone is never an attestation.
				return map[string]any{bot.ToolOutcomeKey: outcome}, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			llm := NewMockLLM(ToolCallTurn("read_file", map[string]any{}), TextTurn("Model claims changes.\n```astonish-report\npath: report.md\n```"))
			var cleaned atomic.Int64
			adapter, err := NewBotChatRuntimeAdapter(botChatTestFactory(t, ss, llm, []tool.Tool{action}, &cleaned))
			if err != nil {
				t.Fatal(err)
			}
			result, err := adapter.Start(ctx, req, func(context.Context, bot.RuntimeEvent) error { return nil })
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "verified" {
				if result.Status != store.RunSucceeded || len(result.Report.Changed) != 1 || len(result.Report.Unchanged) != 1 || len(result.Report.Artifacts) != 1 || result.Report.Summary != body || result.Report.NextRunAt == nil {
					t.Fatalf("lost structured outcome: %+v", result)
				}
			} else {
				if result.Status != store.RunPartial || len(result.Report.Unverified) == 0 {
					t.Fatalf("uncertainty hidden: %+v", result)
				}
				if scenario == "spoof" || scenario == "unverified" {
					if len(result.Report.Changed) != 0 || len(result.Report.Artifacts) != 0 {
						t.Fatalf("fabricated evidence trusted: %+v", result)
					}
				}
				if scenario == "foreign-home" && (len(result.Report.Artifacts) != 0 || result.Report.Summary == body) {
					t.Fatalf("foreign artifact leaked: %+v", result)
				}
			}
		})
	}
}
