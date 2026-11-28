package launcher

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/store"
)

func TestBotTaskBundleInspection(t *testing.T) {
	bundle := map[string][]byte{"TASK.md": []byte("procedure"), "references/design.md": []byte("# Design"), "references/empty.txt": {}}
	for _, tc := range []struct {
		path string
		want string
		err  error
	}{
		{"", "procedure", nil},
		{"references/design.md", "# Design", nil},
		{"references/empty.txt", "", nil},
		{"references/missing.md", "", store.ErrBotNotFound},
		{"../secret", "", store.ErrBotInvalid},
		{"/etc/passwd", "", store.ErrBotInvalid},
		{"references/../../secret", "", store.ErrBotInvalid},
	} {
		t.Run(tc.path, func(t *testing.T) {
			result, err := botTaskBundleRead(store.BotTaskRef{}, bundle, tc.path)
			if !errors.Is(err, tc.err) {
				t.Fatalf("error = %v, want %v", err, tc.err)
			}
			if tc.err != nil {
				return
			}
			if result["content"] != tc.want || !reflect.DeepEqual(result["available_paths"], []string{"TASK.md", "references/design.md", "references/empty.txt"}) {
				t.Fatalf("unexpected inspection: %#v", result)
			}
		})
	}
}

func TestBotTaskInspectionExactReceipt(t *testing.T) {
	for _, path := range []string{"TASK.md", "references/missing.md", "../secret"} {
		t.Run(path, func(t *testing.T) {
			f, _, actions := retainedResultTaskTools(t)
			ctx := optionalKnowledgeContext{knowledgeCtx(f)}
			ctx.base, _ = bot.WithToolOutcomes(ctx.base)
			for _, action := range actions {
				if action.Name() != "bot_task_read" {
					continue
				}
				result, err := action.(botRunnableTool).Run(ctx, map[string]any{"path": path})
				if _, ok := bot.TakeToolOutcome(ctx, "foreign-call"); ok {
					t.Fatal("receipt crossed call identity")
				}
				outcome, ok := bot.TakeToolOutcome(ctx, ctx.FunctionCallID())
				if path == "TASK.md" {
					if err != nil || result["available_paths"] == nil || !ok || !outcome.Verified {
						t.Fatalf("read: %#v %v receipt=%+v", result, err, outcome)
					}
				} else if err == nil || ok {
					t.Fatalf("invalid/missing file acquired success receipt: %#v %v %+v", result, err, outcome)
				}
			}
		})
	}
}

func TestBotTaskDesignAssetRoundTrip(t *testing.T) {
	f, workspace, actions := retainedResultTaskTools(t)
	ctx := optionalKnowledgeContext{knowledgeCtx(f)}
	ctx.base, _ = bot.WithToolOutcomes(ctx.base)
	var asset, read botRunnableTool
	for _, action := range actions {
		switch action.Name() {
		case "bot_task_asset":
			asset = action.(botRunnableTool)
		case "bot_task_read":
			read = action.(botRunnableTool)
		}
	}
	content := "# Design\n\n```mermaid\nflowchart LR\n  Client --> Sidecar\n```\n"
	result, err := asset.Run(ctx, map[string]any{"path": "references/design.md", "content": content})
	if err != nil || result["task"] == nil || workspace.publications != 1 || workspace.scripts != 0 {
		t.Fatalf("asset: %#v %v publications=%d scripts=%d", result, err, workspace.publications, workspace.scripts)
	}
	if outcome, ok := bot.TakeToolOutcome(ctx, ctx.FunctionCallID()); !ok || !outcome.Verified || len(outcome.Artifacts) != 0 {
		t.Fatalf("asset receipt must attest scoped bytes, not a downloadable/rendered artifact: %+v %v", outcome, ok)
	}
	result, err = read.Run(ctx, map[string]any{"path": "references/design.md"})
	if err != nil || result["content"] != content {
		t.Fatalf("readback: %#v %v", result, err)
	}
}

func TestBotTaskAssetDiscoveryDescribesBoundedAlternative(t *testing.T) {
	_, _, actions := retainedResultTaskTools(t)
	for _, action := range actions {
		if action.Name() == "bot_task_asset" {
			for _, want := range []string{"Markdown", "Mermaid", "relative path", "does not render", "No host filesystem access"} {
				if !strings.Contains(action.Description(), want) {
					t.Fatalf("asset discovery missing %q: %s", want, action.Description())
				}
			}
		}
	}
}
