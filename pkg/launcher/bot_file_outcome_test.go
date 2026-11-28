package launcher

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"iter"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/api"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/config"
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

// fileOutcomeFiles injects missing/tampered service receipts after an actual write.
// The adapter must independently read back bytes and reject these entries.
type fileOutcomeFiles struct {
	store.BotFiles
	strip, tamper bool
}

func (f fileOutcomeFiles) Write(ctx context.Context, id store.BotID, w store.BotFileWrite) (*store.BotFileEntry, error) {
	entry, err := f.BotFiles.Write(ctx, id, w)
	if err == nil && (f.strip || f.tamper) {
		copy := *entry
		copy.SHA256 = ""
		if f.tamper {
			copy.SHA256 = fmt.Sprintf("%x", sha256.Sum256([]byte("different bytes")))
		}
		entry = &copy
	}
	return entry, err
}

type fileOutcomeModel struct {
	name          string
	args          map[string]any
	calls         int
	toolResponses string
}

func (*fileOutcomeModel) Name() string { return "file-outcome" }
func (m *fileOutcomeModel) GenerateContent(_ context.Context, req *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		m.calls++
		for _, content := range req.Contents {
			for _, part := range content.Parts {
				if part.FunctionResponse != nil {
					m.toolResponses += fmt.Sprint(part.FunctionResponse.Response)
				}
			}
		}
		content := genai.NewContentFromText("Observed file operation.", "model")
		if m.calls == 1 {
			name, args := m.name, m.args
			if req.Tools[name] == nil && req.Tools["execute_tool"] != nil {
				name, args = "execute_tool", map[string]any{"name": name, "arguments": args}
			}
			content = &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{ID: "file-call", Name: name, Args: args}}}}
		}
		yield(&model.LLMResponse{Content: content, TurnComplete: true}, nil)
	}
}

func TestBotSharedWriteReceiptValidation(t *testing.T) {
	content := "written bytes"
	digest := fmt.Sprintf("%x", sha256.Sum256([]byte(content)))
	for _, tc := range []struct {
		name   string
		args   any
		result map[string]any
		valid  bool
	}{
		{name: "changed", args: map[string]any{"path": "/output", "content": content}, result: map[string]any{"path": "/output", "content_changed": true, "readback_sha256": digest}, valid: true},
		{name: "unchanged", args: map[string]any{"path": "/output", "content": content}, result: map[string]any{"path": "/output", "content_changed": false, "readback_sha256": digest}, valid: true},
		{name: "missing", args: map[string]any{"path": "/output", "content": content}},
		{name: "wrong_bytes", args: map[string]any{"path": "/output", "content": content}, result: map[string]any{"path": "/output", "content_changed": true, "readback_sha256": strings.Repeat("0", 64)}},
		{name: "missing_baseline", args: map[string]any{"path": "/output", "content": content}, result: map[string]any{"path": "/output", "readback_sha256": digest}},
		{name: "forged_outcome", args: map[string]any{"path": "/output", "content": content}, result: map[string]any{"astonish_bot_outcome": map[string]any{"Verified": true}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateBotSharedWriteReceipt(tc.args, tc.result)
			if tc.valid {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), store.ErrBotConflict.Error()) {
				t.Fatalf("unverified receipt accepted: %v", err)
			}
		})
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
			path := filepath.Join(f.files.root, "outputs", "receipt.md")
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			body := "# Independently observed\n"
			if tc.existing {
				if err := os.WriteFile(path, []byte(body), 0600); err != nil {
					t.Fatal(err)
				}
			}
			args := map[string]any{"path": path}
			if name == "write_file" {
				args["path"] = path
				args["content"] = body
				if tc.existing {
					args["expected_sha256"] = fmt.Sprintf("%x", sha256.Sum256([]byte(body)))
				} else {
					args["create_only"] = true
				}
				f.services.BotFilesFactory = func(context.Context, *store.Services, store.BotScope) (store.BotFiles, error) {
					return fileOutcomeFiles{BotFiles: f.files, strip: tc.stripReceipt, tamper: tc.tamperReceipt}, nil
				}
			}
			policy, err := execution.NewRunPolicy(execution.RunPolicySpec{Identity: f.req.Policy.Identity(), Level: execution.AutonomySupervised, AllowedTools: []string{name}})
			if err != nil {
				t.Fatal(err)
			}
			if name == "write_file" {
				// The shared file implementation requires exact approval. Bind it
				// only to this fixture's path and complete argument payload.
				raw, err := json.Marshal(args)
				if err != nil {
					t.Fatal(err)
				}
				digest, err := (execution.ActionDescriptor{Tool: name, Arguments: raw, Effect: execution.EffectUnknown}).Digest()
				if err != nil {
					t.Fatal(err)
				}
				policy, err = policy.WithGrant(execution.ScopedGrant{ID: uuid.New(), Identity: policy.Identity(), ActionDigest: digest, ExpiresAt: time.Now().Add(time.Minute)})
				if err != nil {
					t.Fatal(err)
				}
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
			policyRuntime := agent.BotPolicyRuntime{Guard: botFactoryTestGuard{}, Classifier: botFactoryTestClassifier{}}
			if name == "write_file" {
				policyRuntime.Guard = &mcpOnceGuard{}
			}
			f.cfg.Prepare = func(context.Context, bot.RunRequest, *BotChatScope) (*BotChatSafeResources, error) {
				return &BotChatSafeResources{ChatConfig: &config.AppConfig{}, Catalog: &ChatCatalogResources{Pool: &interactiveOutcomePool{node: node}}, Cleanup: func() {}, WorkspaceDir: filepath.Dir(path), Tools: selected, PolicyRuntime: policyRuntime, VerifyIsolation: func(context.Context) error { return nil }}, nil
			}
			factory, err := NewBotChatAgentFactory(f.cfg)
			if err != nil {
				t.Fatal(err)
			}
			adapter, err := api.NewBotChatRuntimeAdapter(outcomeWorkerFactory(factory))
			if err != nil {
				t.Fatal(err)
			}
			var artifacts []bot.RuntimeEvent
			writesBefore := f.files.writes
			result, err := adapter.Start(f.ctx, f.req, func(_ context.Context, event bot.RuntimeEvent) error {
				if event.Type == "artifact" {
					artifacts = append(artifacts, event)
				}
				return nil
			})
			if name == "load_file" && !tc.stripReceipt && !tc.tamperReceipt && len(artifacts) != 1 {
				t.Fatalf("successful load must emit one artifact: %d", len(artifacts))
			}
			invalidWriteReceipt := name == "write_file" && (tc.stripReceipt || tc.tamperReceipt)
			if invalidWriteReceipt {
				if err == nil || result.Status != store.RunFailed || result.Report.Failure == nil || !strings.Contains(llm.toolResponses, store.ErrBotConflict.Error()) {
					t.Fatalf("invalid retained receipt did not fail closed: %v result=%+v responses=%s", err, result, llm.toolResponses)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if tc.stripReceipt || tc.tamperReceipt {
				wantStatus := store.RunPartial
				if invalidWriteReceipt {
					wantStatus = store.RunFailed
				}
				if result.Status != wantStatus || len(result.Report.Unverified) != 1 || len(result.Report.Changed)+len(result.Report.Unchanged) != 0 {
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
			wantNode, wantWrites := 1, 0
			// The shared Chat node executes ordinary writes, not BotFiles.
			if node.calls != wantNode || f.files.writes-writesBefore != wantWrites || result.Usage.ToolCalls != 1 {
				t.Fatalf("one action expanded: node=%d usage=%+v", node.calls, result.Usage)
			}
			got, err := os.ReadFile(path)
			if err != nil || string(got) != body {
				t.Fatalf("readback %q: %v", got, err)
			}
		})
	}
}
