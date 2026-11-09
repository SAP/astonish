package launcher

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/tools"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/toolconfirmation"
	"testing"
)

type catalogueContext struct{ botFactoryToolContext }

func (catalogueContext) ToolConfirmation() *toolconfirmation.ToolConfirmation { return nil }

type catalogueClient struct {
	calls   int
	session string
}

func (*catalogueClient) BindSession(string)       {}
func (*catalogueClient) EnsureReady(string) error { return nil }
func (c *catalogueClient) Call(id, n string, a map[string]any) (json.RawMessage, error) {
	c.calls++
	c.session = id
	return json.RawMessage(`{"result":"ok"}`), nil
}
func TestBotCatalogueScopesAndRejectsUnsupported(t *testing.T) {
	c := &catalogueClient{}
	all, err := BotCatalogueTools(c, "owner-run")
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]tool.Tool{}
	for _, entry := range all {
		byName[entry.Name()] = entry
	}
	for _, d := range tools.GetAllFlowToolDeclarations() {
		if byName[d.Name] == nil {
			t.Errorf("missing %s", d.Name)
		}
	}
	ctx := catalogueContext{botFactoryToolContext{base: context.Background()}}
	for name, args := range map[string]map[string]any{
		"read_file":     {"path": "/workspace/a"},
		"filter_json":   {"json_data": "{}", "fields_to_extract": []any{}},
		"shell_command": {"command": "printf controlled"},
		"web_fetch":     {"url": "https://example.invalid"},
		"grep_search":   {"pattern": "controlled"},
	} {
		if _, err := byName[name].(botRunnableTool).Run(ctx, args); err != nil {
			t.Fatal(err)
		}
	}
	if c.calls != 5 || c.session != "owner-run" {
		t.Fatal("not pinned to owner sandbox")
	}
	for _, name := range []string{"distill_flow", "run_flow", "http_request", "browser_navigate"} {
		if _, err := byName[name].(botRunnableTool).Run(ctx, map[string]any{}); !errors.Is(err, store.ErrBotUnsupported) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if c.calls != 5 {
		t.Fatal("unsupported effect reached sandbox")
	}
}
func TestBotCatalogueClassifierConservative(t *testing.T) {
	for _, name := range []string{"filter_json", "git_diff_add_line_numbers", "read_file", "http_request", "shell_command", "mcp:mail/filter_json", "distill_flow"} {
		a, err := (BotCatalogueClassifier{}).DescribeAction(context.Background(), name, json.RawMessage(`{"auto_approve":true,"method":"GET"}`))
		if err != nil {
			t.Fatal(err)
		}
		read := name == "filter_json" || name == "git_diff_add_line_numbers"
		if a.ConstraintsVerified != read || (a.Effect == execution.EffectRead) != read {
			t.Fatalf("unsafe classification: %+v", a)
		}
	}
	if _, err := BotCatalogueTools(nil, ""); !errors.Is(err, store.ErrBotUnsupported) {
		t.Fatal(err)
	}
}
