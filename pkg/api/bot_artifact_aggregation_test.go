package api

import (
	"context"
	"reflect"
	"testing"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
)

func TestMergeBotArtifactsExactReceipt(t *testing.T) {
	ref := store.BotContentRef{Key: "snapshots/report", Revision: 1}
	artifact := store.BotArtifact{ID: uuid.New(), HomeID: uuid.New(), RelativePath: "outputs/report.md", ContentRef: &ref, SHA256: "digest", Title: "Report", SizeBytes: 42}
	copyRef := ref
	identical := artifact
	identical.ContentRef = &copyRef
	for name, change := range map[string]func(*store.BotArtifact){
		"revision": func(a *store.BotArtifact) { r := *a.ContentRef; r.Revision++; a.ContentRef = &r },
		"digest":   func(a *store.BotArtifact) { a.SHA256 = "different" },
		"title":    func(a *store.BotArtifact) { a.Title = "different" },
		"size":     func(a *store.BotArtifact) { a.SizeBytes++ },
		"home":     func(a *store.BotArtifact) { a.HomeID = uuid.New() },
		"path":     func(a *store.BotArtifact) { a.RelativePath = "outputs/other.md" },
		"identity": func(a *store.BotArtifact) { a.ID = uuid.New() },
		"purpose":  func(a *store.BotArtifact) { a.Purpose = "different" },
		"memory":   func(a *store.BotArtifact) { a.MemorySummary = "different" },
		"media":    func(a *store.BotArtifact) { a.MediaType = "text/plain" },
	} {
		t.Run(name, func(t *testing.T) {
			conflict := artifact
			change(&conflict)
			got := mergeBotArtifacts([]store.BotArtifact{artifact}, []store.BotArtifact{identical, conflict, identical})
			if !reflect.DeepEqual(got, []store.BotArtifact{artifact, conflict}) {
				t.Fatalf("identical receipt duplicated or conflict discarded: %+v", got)
			}
			completion := ChatCompletion{Report: store.BotReport{Artifacts: []store.BotArtifact{artifact}}}
			mergeBotEmployeeCompletion(&completion, ChatCompletion{Report: store.BotReport{Artifacts: []store.BotArtifact{identical, conflict}}})
			if !reflect.DeepEqual(completion.Report.Artifacts, got) {
				t.Fatalf("worker completion lost exact receipt semantics: %+v", completion.Report.Artifacts)
			}
		})
	}
	if got := mergeBotArtifacts(nil, []store.BotArtifact{artifact, identical}); len(got) != 1 {
		t.Fatalf("duplicates within source retained: %+v", got)
	}
}

func TestBotChatAttemptPublishedFilesExactReceipt(t *testing.T) {
	ctx, req, ss := botChatTestRequest(t)
	ctx = execution.WithBotRunIdentity(ctx, req.Policy.Identity())
	ctx = execution.WithRunPolicy(ctx, req.Policy)
	ctx, _ = bot.WithToolOutcomes(ctx)
	ctx = agent.WithBotPolicyRuntime(ctx, agent.BotPolicyRuntime{Classifier: botChatTestClassifier{}, Guard: botChatTestGuard{}})
	artifact := store.BotArtifact{ID: uuid.New(), HomeID: uuid.New(), RelativePath: "outputs/report.md", Title: "Report"}
	read, err := functiontool.New(functiontool.Config{Name: "read_file", Description: "Read assigned report"}, func(c tool.Context, _ map[string]any) (map[string]any, error) {
		bot.RecordToolOutcome(c, c.FunctionCallID(), bot.ToolOutcome{Verified: true, Artifacts: []bot.ToolReportArtifact{{Artifact: artifact, Body: "report"}}})
		return map[string]any{"content": "report"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	llm := NewMockLLM(ToolCallTurn("read_file", map[string]any{"path": "report.md"}), TextTurn("Report retained."))
	resources := &BotChatResources{Agent: agent.NewChatAgent(llm, []tool.Tool{read}, nil, ss, &agent.SystemPromptBuilder{}, false, false), SessionService: ss, AppName: studioChatAppName, PublishedFiles: func() []store.BotArtifact { return []store.BotArtifact{artifact} }}
	var sequence int64
	var completion ChatCompletion
	_, err = runBotChatAttempt(ctx, req, resources, func(context.Context, bot.RuntimeEvent) error { return nil }, nil, nil, nil, false, &sequence, &completion)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(completion.Report.Artifacts, []store.BotArtifact{artifact}) {
		t.Fatalf("tool and run-local publication receipt duplicated: %+v", completion.Report.Artifacts)
	}
}
