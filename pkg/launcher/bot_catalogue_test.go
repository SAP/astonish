package launcher

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/toolconfirmation"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/tools"
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

type sourceAccessCatalogueClient struct {
	catalogueClient
	response json.RawMessage
	err      error
}

func (c *sourceAccessCatalogueClient) Call(id, _ string, _ map[string]any) (json.RawMessage, error) {
	c.calls++
	c.session = id
	return c.response, c.err
}

func TestBotCatalogueSourceAccessResponseDigest(t *testing.T) {
	for _, response := range []string{
		`{"status":200,"final_url":"https://example.test/final","content":"Copyright | Privacy policy","truncated":true}`,
		`{"status":429,"final_url":"https://example.test/final","content":"Rate limited"}`,
		`{"error":"fetch failed: denied"}`,
	} {
		c := &sourceAccessCatalogueClient{response: json.RawMessage(response)}
		catalogue, err := BotCatalogueTools(c, "source-session")
		if err != nil {
			t.Fatal(err)
		}
		var fetch botRunnableTool
		for _, entry := range catalogue {
			if entry.Name() == "web_fetch" {
				fetch = entry.(botRunnableTool)
			}
		}
		if fetch == nil {
			t.Fatal("missing built-in web_fetch")
		}
		base := execution.WithBotRunIdentity(context.Background(), execution.BotRunIdentity{BotID: uuid.New(), RunID: uuid.New(), ActorID: uuid.NewString()})
		base, _ = bot.WithToolOutcomes(base)
		ctx := proposalTestContext{botFactoryToolContext: botFactoryToolContext{base: base}, sid: "source-session"}
		result, err := fetch.Run(ctx, map[string]any{"url": "https://example.test/original"})
		if err != nil {
			t.Fatal(err)
		}
		outcome, ok := bot.TakeToolOutcome(ctx, ctx.FunctionCallID())
		if !ok || len(outcome.SourceAccess) != 1 {
			t.Fatalf("missing source access: %+v", outcome)
		}
		raw, err := json.Marshal(result)
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(raw)
		if outcome.SourceAccess[0].BodyDigest != hex.EncodeToString(sum[:]) || c.calls != 1 || c.session != ctx.SessionID() {
			t.Fatalf("capture mismatch or repeated call: %+v client=%+v", outcome.SourceAccess[0], c)
		}
	}
}

func TestBotAgentSourceAccessTransportError(t *testing.T) {
	failure := errors.New("transport unavailable")
	client := &sourceAccessCatalogueClient{err: failure}
	catalogue, err := BotCatalogueTools(client, "source-session")
	if err != nil {
		t.Fatal(err)
	}
	base := execution.WithBotRunIdentity(context.Background(), execution.BotRunIdentity{BotID: uuid.New(), RunID: uuid.New(), ActorID: uuid.NewString()})
	base, _ = bot.WithToolOutcomes(base)
	ctx := proposalTestContext{botFactoryToolContext: botFactoryToolContext{base: base}, sid: "source-session"}
	for _, target := range catalogue {
		if target.Name() != "web_fetch" {
			continue
		}
		if _, err := target.(botRunnableTool).Run(ctx, map[string]any{"url": "https://example.test/unavailable"}); !errors.Is(err, failure) {
			t.Fatal(err)
		}
		outcome, ok := bot.TakeToolOutcome(ctx, ctx.FunctionCallID())
		if !ok || outcome.Verified || len(outcome.SourceAccess) != 1 || outcome.SourceAccess[0].Status != store.BotSourceAccessFailed || client.calls != 1 {
			t.Fatalf("failed access lost or certified: %+v", outcome)
		}
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
