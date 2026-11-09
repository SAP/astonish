package launcher

import (
	"context"
	"encoding/json"
	"iter"
	"os"
	"path/filepath"
	"testing"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/api"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/tools"
	"google.golang.org/adk/model"
	"google.golang.org/adk/tool"
	"google.golang.org/genai"
)

// Transport fixture executes the actual sandbox-node dispatcher, not a tool
// stub that manufactures outcome metadata. No live daemon or container is used.
type fileOutcomeNode struct {
	calls         int
	stripReceipt  bool
	tamperReceipt bool
}

func (*fileOutcomeNode) BindSession(string)       {}
func (*fileOutcomeNode) EnsureReady(string) error { return nil }
func (n *fileOutcomeNode) Call(_ string, name string, args map[string]any) (json.RawMessage, error) {
	n.calls++
	result, err := tools.ExecuteTool(context.Background(), name, args, "")
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(result)
	if err != nil || (!n.stripReceipt && !n.tamperReceipt) {
		return raw, err
	}
	var legacy map[string]any
	if err := json.Unmarshal(raw, &legacy); err != nil {
		return nil, err
	}
	delete(legacy, "readback_sha256")
	delete(legacy, "inspected_sha256")
	if n.tamperReceipt {
		legacy["readback_sha256"] = "invalid"
		legacy["inspected_sha256"] = "invalid"
	}
	legacy["astonish_bot_outcome"] = map[string]any{"Verified": true}
	return json.Marshal(legacy)
}

type fileOutcomeModel struct {
	name  string
	args  map[string]any
	calls int
}

func (*fileOutcomeModel) Name() string { return "file-outcome" }
func (m *fileOutcomeModel) GenerateContent(context.Context, *model.LLMRequest, bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		m.calls++
		content := genai.NewContentFromText("Observed file operation.", "model")
		if m.calls == 1 {
			content = &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{ID: "file-call", Name: m.name, Args: m.args}}}}
		}
		yield(&model.LLMResponse{Content: content, TurnComplete: true}, nil)
	}
}

func TestBotBuiltinFileOutcomesThroughRunner(t *testing.T) {
	for _, tc := range []struct {
		label, name   string
		existing      bool
		stripReceipt  bool
		tamperReceipt bool
	}{
		{label: "create", name: "write_file"},
		{label: "identical_write", name: "write_file", existing: true},
		{label: "load", name: "load_file", existing: true},
		{label: "tampered_write", name: "write_file", tamperReceipt: true},
		{label: "tampered_load", name: "load_file", existing: true, tamperReceipt: true},
		{label: "old_node_write", name: "write_file", stripReceipt: true},
		{label: "old_node_load", name: "load_file", existing: true, stripReceipt: true},
	} {
		name := tc.name
		t.Run(tc.label, func(t *testing.T) {
			f := newBotFactoryFixture(t)
			path := filepath.Join(t.TempDir(), "receipt.md")
			body := "# Independently observed\n"
			if tc.existing {
				if err := os.WriteFile(path, []byte(body), 0600); err != nil {
					t.Fatal(err)
				}
			}
			args := map[string]any{"path": path}
			if name == "write_file" {
				args["content"] = body
			}
			policy, err := execution.NewRunPolicy(execution.RunPolicySpec{Identity: f.req.Policy.Identity(), Level: execution.AutonomySupervised, AllowedTools: []string{name}})
			if err != nil {
				t.Fatal(err)
			}
			f.req.Policy = policy
			node := &fileOutcomeNode{stripReceipt: tc.stripReceipt, tamperReceipt: tc.tamperReceipt}
			catalogue, err := BotCatalogueTools(node, f.req.Run.SessionID.String())
			if err != nil {
				t.Fatal(err)
			}
			var selected []tool.Tool
			for _, entry := range catalogue {
				if entry.Name() == name {
					selected = append(selected, entry)
				}
			}
			llm := &fileOutcomeModel{name: name, args: args}
			f.cfg.WrapModel = func(_ context.Context, _ model.LLM, _ execution.RunBudget) (model.LLM, error) { return llm, nil }
			f.cfg.Prepare = func(context.Context, bot.RunRequest, *BotChatScope) (*BotChatSafeResources, error) {
				return &BotChatSafeResources{Cleanup: func() {}, WorkspaceDir: filepath.Dir(path), Tools: selected, PolicyRuntime: agent.BotPolicyRuntime{Guard: botFactoryTestGuard{}, Classifier: botFactoryTestClassifier{}}, VerifyIsolation: func(context.Context) error { return nil }}, nil
			}
			factory, err := NewBotChatAgentFactory(f.cfg)
			if err != nil {
				t.Fatal(err)
			}
			adapter, err := api.NewBotChatRuntimeAdapter(factory)
			if err != nil {
				t.Fatal(err)
			}
			var artifacts []bot.RuntimeEvent
			result, err := adapter.Start(f.ctx, f.req, func(_ context.Context, event bot.RuntimeEvent) error {
				if event.Type == "artifact" {
					artifacts = append(artifacts, event)
				}
				return nil
			})
			if name == "load_file" && !tc.stripReceipt && !tc.tamperReceipt && len(artifacts) != 1 {
				t.Fatalf("successful load must emit one artifact: %d", len(artifacts))
			}
			if err != nil {
				t.Fatal(err)
			}
			if tc.stripReceipt || tc.tamperReceipt {
				if result.Status != store.RunPartial || len(result.Report.Unverified) != 1 || len(result.Report.Changed)+len(result.Report.Unchanged) != 0 {
					t.Fatalf("missing receipt upgraded: %+v", result)
				}
			} else {
				if result.Status != store.RunSucceeded || len(result.Report.Unverified) != 0 {
					t.Fatalf("actual built-in outcome lost: %+v", result)
				}
				changed, unchanged := 0, 1
				if name == "write_file" && !tc.existing {
					changed, unchanged = 1, 0
				}
				if len(result.Report.Changed) != changed || len(result.Report.Unchanged) != unchanged {
					t.Fatalf("incorrect change classification: %+v", result.Report)
				}
			}
			if node.calls != 1 || result.Usage.ToolCalls != 1 {
				t.Fatalf("one action expanded: node=%d usage=%+v", node.calls, result.Usage)
			}
			got, err := os.ReadFile(path)
			if err != nil || string(got) != body {
				t.Fatalf("readback %q: %v", got, err)
			}
		})
	}
}
