package launcher

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/tools"
	"github.com/google/uuid"
	"google.golang.org/adk/tool"
)

func TestBotToolsRemainDeferredOutsideCode(t *testing.T) {
	botTools, err := tools.GetBotToolsForMode(false)
	if err != nil {
		t.Fatal(err)
	}
	groups := []*agent.ToolGroup{{Name: tools.BotToolGroup, Tools: botTools}}
	index := agent.NewLexicalToolIndex()
	if err := index.PrimeTools(context.Background(), nil, groups); err != nil {
		t.Fatal(err)
	}
	direct, err := providerToolsForMode(false, index, nil, groups)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range direct {
		for _, bot := range botTools {
			if tool.Name() == bot.Name() {
				t.Fatalf("%s became a direct provider tool", tool.Name())
			}
		}
	}
	for _, botTool := range botTools {
		entry := index.FirstPartyToolEntry(botTool.Name())
		if entry == nil || entry.Tool != botTool || entry.GroupName != tools.BotToolGroup {
			t.Fatalf("%s not registered in Bot group", botTool.Name())
		}
		matches, err := index.SearchHybrid(context.Background(), botTool.Name(), 10, 0)
		found := false
		for _, match := range matches {
			found = found || match.ToolName == botTool.Name() && match.GroupName == tools.BotToolGroup
		}
		if err != nil || !found {
			t.Fatalf("%s not discoverable: %v", botTool.Name(), err)
		}
	}
	bridge, err := agent.NewProgressiveToolBridge(index)
	if err != nil {
		t.Fatal(err)
	}
	ctx, err := execution.WithPrincipal(context.Background(), execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: uuid.NewString(), OrgSlug: "org", TeamSlug: "team", Authenticated: true})
	if err != nil {
		t.Fatal(err)
	}
	tc := botFactoryHandoffContext{base: ctx}
	described, err := bridge[0].(interface {
		Run(tool.Context, any) (map[string]any, error)
	}).Run(tc, map[string]any{"names": []any{"list_bots"}})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(described)
	if err != nil || !strings.Contains(string(raw), `"name":"list_bots"`) || !strings.Contains(string(raw), `"input_schema"`) {
		t.Fatalf("description=%s err=%v", raw, err)
	}
	// Discovery is not authority: real deferred execution still fails closed
	// when there is no trusted control-plane service in the request context.
	_, err = bridge[1].(interface {
		Run(tool.Context, any) (map[string]any, error)
	}).Run(tc, map[string]any{"name": "list_bots", "arguments": map[string]any{}})
	if !errors.Is(err, store.ErrBotUnsupported) {
		t.Fatalf("deferred control without service: %v", err)
	}
	code, err := tools.GetBotToolsForMode(true)
	if err != nil || len(code) != 0 {
		t.Fatal("Code exposed Bot controls")
	}
}
