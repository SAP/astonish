package launcher

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/SAP/astonish/pkg/sandbox/sessioncreds"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/tools"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/adk/tool"
)

func TestBotMCPNodeRemote(t *testing.T) {
	tools.SetAllowPrivateNetworks(true)
	defer tools.SetAllowPrivateNetworks(false)
	for _, transport := range []string{"sse", "streamableHTTP"} {
		t.Run(transport, func(t *testing.T) {
			var authCalls, calls, requests atomic.Int32
			const token = "fresh-node-token-not-known-to-host-987654321"
			auth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				authCalls.Add(1)
				_, secret, _ := r.BasicAuth()
				if secret != productionTestSecret {
					t.Error("wrong refresh owner")
				}
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprintf(w, `{"access_token":%q,"token_type":"Bearer","expires_in":3600}`, token)
			}))
			defer auth.Close()
			schema := map[string]any{"type": "object", "properties": map[string]any{"mode": map[string]any{"type": "string", "enum": []any{"safe"}}}, "required": []any{"mode"}, "additionalProperties": false}
			server := sdk.NewServer(&sdk.Implementation{Name: "fixture", Version: "1"}, nil)
			server.AddTool(&sdk.Tool{Name: "read_owner", Description: "controlled", InputSchema: schema}, func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
				calls.Add(1)
				return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: token + ":" + productionTestSecret}}}, nil
			})
			get := func(*http.Request) *sdk.Server { return server }
			var handler http.Handler = sdk.NewSSEHandler(get, nil)
			if transport == "streamableHTTP" {
				handler = sdk.NewStreamableHTTPHandler(get, nil)
			}
			endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if r.Header.Get("Authorization") != "Bearer "+token || r.Header.Get("X-Owner") != "bot-owner" {
					t.Error("wrong MCP owner headers")
				}
				handler.ServeHTTP(w, r)
			}))
			defer endpoint.Close()
			f := newBotFactoryFixture(t)
			f.services.PersonalCredentials = sessioncreds.NewStore(map[string]*store.Credential{"owner": {Type: store.CredOAuthClientCreds, AuthURL: auth.URL, ClientID: "client", ClientSecret: productionTestSecret}})
			b := &productionCapabilityBackend{}
			client := &productionCapabilityClient{backend: b}
			pool := productionCapabilityPool{backend: b, client: client}
			servers := newMemMCPStore(store.MCPServer{Name: "controlled", Transport: transport, URL: endpoint.URL, Env: map[string]string{"Authorization": "Bearer {{CREDENTIAL:owner:token}}", "X-Owner": "bot-owner"}})
			discovery, discoveryCtx := productionFactoryTool(t, f, "mcp-discovery:controlled", func(ctx context.Context, id string) ([]tool.Tool, func(), error) {
				return BotMCPTools(ctx, pool, id, servers)
			})
			if client.calls != 0 || authCalls.Load() != 0 {
				t.Fatal("eager discovery")
			}
			discoveryCtx = approveMCPTestTool(t, discovery, discoveryCtx, map[string]any{})
			if _, err := discovery.Run(discoveryCtx, map[string]any{}); err != nil {
				t.Fatal(err)
			}
			target, ctx := productionFactoryTool(t, f, "mcp:controlled/read_owner", func(ctx context.Context, id string) ([]tool.Tool, func(), error) {
				return BotMCPTools(ctx, pool, id, servers)
			})
			ctx = approveMCPTestTool(t, target, ctx, map[string]any{"mode": "safe"})
			if client.calls != 1 || authCalls.Load() != 1 {
				t.Fatal("uncached discovery did not use node refresh")
			}
			if !reflect.DeepEqual(target.Declaration().ParametersJsonSchema, schema) {
				t.Fatalf("schema changed: %#v", target.Declaration())
			}
			out, err := target.Run(ctx, map[string]any{"mode": "safe"})
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(out)
			wire, _ := json.Marshal(client.args)
			if calls.Load() != 1 || authCalls.Load() != 2 || strings.Contains(string(raw), token) || strings.Contains(string(raw), productionTestSecret) || strings.Contains(string(wire), token) || strings.Contains(string(wire), productionTestSecret) || b.starts != 0 || b.owner != f.req.Run.SessionID.String() {
				t.Fatalf("MCP scope/refresh/redaction failure: %s", raw)
			}
			before := requests.Load()
			f.req.Bot.Status = store.BotPaused
			if _, err = target.Run(ctx, map[string]any{"mode": "safe"}); err == nil || requests.Load() != before {
				t.Fatal("revoked MCP dispatched")
			}
		})
	}
}

// This subprocess represents the node's stdio child, not a host MCP callback.
func TestBotMCPNodeStdioFixture(t *testing.T) {
	if os.Getenv("ASTONISH_MCP_FIXTURE") != "1" {
		return
	}
	server := sdk.NewServer(&sdk.Implementation{Name: "stdio-fixture", Version: "1"}, nil)
	server.AddTool(&sdk.Tool{Name: "read_owner", InputSchema: map[string]any{"type": "object", "properties": map[string]any{}}}, func(context.Context, *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: os.Getenv("TOKEN")}}}, nil
	})
	if err := server.Run(context.Background(), &sdk.StdioTransport{}); err != nil {
		os.Exit(2)
	}
	os.Exit(0)
}

func TestBotMCPNodeStdio(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	f := newBotFactoryFixture(t)
	f.services.PersonalCredentials = sessioncreds.NewStore(map[string]*store.Credential{"owner": {Type: store.CredBearer, Token: productionTestSecret}})
	b := &productionCapabilityBackend{}
	client := &productionCapabilityClient{backend: b}
	pool := productionCapabilityPool{backend: b, client: client}
	servers := newMemMCPStore(store.MCPServer{Name: "controlled", Command: exe, Args: []string{"-test.run=^TestBotMCPNodeStdioFixture$"}, Env: map[string]string{"ASTONISH_MCP_FIXTURE": "1", "TOKEN": "{{CREDENTIAL:owner:token}}"}})
	discovery, discoveryCtx := productionFactoryTool(t, f, "mcp-discovery:controlled", func(ctx context.Context, id string) ([]tool.Tool, func(), error) {
		return BotMCPTools(ctx, pool, id, servers)
	})
	discoveryCtx = approveMCPTestTool(t, discovery, discoveryCtx, map[string]any{})
	if _, err := discovery.Run(discoveryCtx, map[string]any{}); err != nil {
		t.Fatal(err)
	}
	target, ctx := productionFactoryTool(t, f, "mcp:controlled/read_owner", func(ctx context.Context, id string) ([]tool.Tool, func(), error) {
		return BotMCPTools(ctx, pool, id, servers)
	})
	ctx = approveMCPTestTool(t, target, ctx, map[string]any{})
	rows, err := servers.List(context.Background())
	if err != nil || len(rows) != 1 {
		t.Fatalf("server configuration: %v", err)
	}
	original := rows[0]
	changed := original
	changed.Command = "unapproved-replacement"
	if err := servers.Save(context.Background(), &changed); err != nil {
		t.Fatal(err)
	}
	if _, err := target.Run(ctx, map[string]any{}); err == nil || client.calls != 1 {
		t.Fatalf("approved call dispatched changed target: %v calls=%d", err, client.calls)
	}
	if err := servers.Save(context.Background(), &original); err != nil {
		t.Fatal(err)
	}
	out, err := target.Run(ctx, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(out)
	if client.calls != 2 || strings.Contains(string(raw), productionTestSecret) {
		t.Fatalf("stdio discovery/call failed %s", raw)
	}
}

func TestBotMCPNodeCachedAndDisabled(t *testing.T) {
	b := &productionCapabilityBackend{}
	client := &productionCapabilityClient{backend: b}
	pool := productionCapabilityPool{backend: b, client: client}
	ctx := store.WithCredentialStore(context.Background(), sessioncreds.NewStore(nil))
	cached := store.MCPServer{Name: "controlled", Command: "must-not-start", CachedTools: json.RawMessage(`[{"name":"read_owner","inputSchema":{"type":"object","properties":{}}}]`)}
	ts, cleanup, err := BotMCPTools(ctx, pool, "run", newMemMCPStore(cached))
	defer cleanup()
	if err != nil || len(ts) != 1 || client.calls != 0 {
		t.Fatalf("cached discovery started node: %v", err)
	}
	disabled := false
	override := cached
	override.Enabled = &disabled
	ts, cleanup, err = BotMCPTools(ctx, pool, "run", newMemMCPStore(cached), newMemMCPStore(override))
	defer cleanup()
	if err != nil || len(ts) != 0 || client.calls != 0 {
		t.Fatal("disabled owner override ignored")
	}
}

func TestBotMCPNodeRequiresOwner(t *testing.T) {
	b := &productionCapabilityBackend{}
	client := &productionCapabilityClient{backend: b}
	_, cleanup, err := BotMCPTools(context.Background(), productionCapabilityPool{backend: b, client: client}, "run", newMemMCPStore(store.MCPServer{Name: "uncached", Command: "must-not-start"}))
	defer cleanup()
	if err == nil || client.calls != 0 || b.starts != 0 {
		t.Fatal("ownerless discovery dispatched")
	}
}
