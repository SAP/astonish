package launcher

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/sandbox/sessioncreds"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/tools"
	"google.golang.org/adk/tool"
)

const productionTestSecret = "owner-only-secret-123456789"

type productionCapabilityBackend struct {
	sandbox.Backend
	vault  []byte
	owner  string
	starts int
	env    map[string]string
}

func (b *productionCapabilityBackend) PushFile(_ context.Context, id, path string, r io.Reader, mode os.FileMode) error {
	if path != sessioncreds.VaultPath || mode != 0600 {
		return fmt.Errorf("invalid vault push")
	}
	b.owner = id
	var err error
	b.vault, err = io.ReadAll(r)
	return err
}
func (b *productionCapabilityBackend) Kind() sandbox.BackendKind { return sandbox.BackendKindDocker }
func (b *productionCapabilityBackend) ExecStreaming(_ context.Context, id string, spec sandbox.ExecStreamSpec) (sandbox.ExecStream, error) {
	b.owner = id
	b.starts++
	b.env = spec.Env
	client, server := net.Pipe()
	go func() {
		defer server.Close()
		dec, enc := json.NewDecoder(server), json.NewEncoder(server)
		for {
			var msg struct {
				ID     json.RawMessage `json:"id"`
				Method string          `json:"method"`
			}
			if dec.Decode(&msg) != nil {
				return
			}
			if len(msg.ID) == 0 {
				continue
			}
			var result any
			switch msg.Method {
			case "initialize":
				result = map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]any{"name": "fixture", "version": "1"}}
			case "tools/list":
				result = map[string]any{"tools": []any{map[string]any{"name": "read_owner", "description": "controlled", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{}}}}}
			case "tools/call":
				result = map[string]any{"content": []any{map[string]any{"type": "text", "text": id + ":" + productionTestSecret}}}
			default:
				result = map[string]any{}
			}
			if enc.Encode(map[string]any{"jsonrpc": "2.0", "id": msg.ID, "result": result}) != nil {
				return
			}
		}
	}()
	return &productionCapabilityStream{client}, nil
}

type productionCapabilityStream struct{ net.Conn }

func (*productionCapabilityStream) Resize(int, int) error { return nil }
func (*productionCapabilityStream) Wait() (int, error)    { return 0, nil }

type productionCapabilityClient struct {
	catalogueClient
	backend *productionCapabilityBackend
	args    map[string]any
	calls   int
}

func (c *productionCapabilityClient) Call(id, name string, args map[string]any) (json.RawMessage, error) {
	c.calls++
	c.args = args
	if id != c.backend.owner {
		return nil, fmt.Errorf("wrong session")
	}
	cs, err := sessioncreds.Parse(c.backend.vault)
	if err != nil {
		return nil, err
	}
	result, err := tools.ExecuteTool(store.WithCredentialStore(context.Background(), cs), name, args, "")
	if err != nil {
		return nil, err
	}
	return json.Marshal(result)
}

type productionCapabilityPool struct {
	sandbox.ToolNodePool
	backend sandbox.Backend
	client  sandbox.ToolNodeClient
}

func (p productionCapabilityPool) GetBackend() sandbox.Backend               { return p.backend }
func (p productionCapabilityPool) GetOrCreate(string) sandbox.ToolNodeClient { return p.client }
func (p productionCapabilityPool) SetSessionScope(string, string, string)    {}
func productionFactoryTool(t *testing.T, f *botFactoryFixture, name string, prepare func(context.Context, string) ([]tool.Tool, func(), error), additionalAllowed ...string) (botRunnableTool, tool.Context) {
	t.Helper()
	policy, err := execution.NewRunPolicy(execution.RunPolicySpec{Identity: f.req.Policy.Identity(), Level: execution.AutonomySupervised, AllowedTools: append([]string{name}, additionalAllowed...)})
	if err != nil {
		t.Fatal(err)
	}
	f.req.Policy = policy
	f.cfg.Prepare = func(ctx context.Context, r bot.RunRequest, _ *BotChatScope) (*BotChatSafeResources, error) {
		ts, cleanup, err := prepare(ctx, r.Run.SessionID.String())
		return &BotChatSafeResources{Tools: ts, WorkspaceDir: "/workspace", PolicyRuntime: agent.BotPolicyRuntime{Guard: botFactoryTestGuard{}, Classifier: botFactoryTestClassifier{}}, VerifyIsolation: func(context.Context) error { return nil }, Cleanup: cleanup}, err
	}
	r, err := f.construct(t)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(r.Cleanup)
	ctx := execution.WithRunPolicy(execution.WithBotRunIdentity(f.ctx, policy.Identity()), policy)
	ctx = agent.WithBotPolicyRuntime(ctx, agent.BotPolicyRuntime{Guard: botFactoryTestGuard{}, Classifier: botFactoryTestClassifier{}})
	for _, candidate := range r.Agent.Tools {
		if candidate.Name() == name {
			return candidate.(botRunnableTool), botFactoryHandoffContext{base: ctx, session: "viewer-child", functionCallID: "production-" + name}
		}
	}
	t.Fatal("missing capability")
	return nil, nil
}
func TestBotProductionHTTPFactory(t *testing.T) {
	// This models the node network namespace; production does not change this global.
	tools.SetAllowPrivateNetworks(true)
	defer tools.SetAllowPrivateNetworks(false)
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Authorization") != "Bearer "+productionTestSecret {
			t.Error("wrong owner credential")
		}
		fmt.Fprint(w, productionTestSecret)
	}))
	defer srv.Close()
	f := newBotFactoryFixture(t)
	f.services.PersonalCredentials = sessioncreds.NewStore(map[string]*store.Credential{"owner": {Type: store.CredBearer, Token: productionTestSecret}})
	b := &productionCapabilityBackend{}
	client := &productionCapabilityClient{backend: b}
	target, ctx := productionFactoryTool(t, f, "http_request", func(_ context.Context, id string) ([]tool.Tool, func(), error) {
		h, e := BotHTTPTool(b, client, id)
		return []tool.Tool{h}, func() {}, e
	})
	out, err := target.Run(ctx, map[string]any{"url": srv.URL, "credential": "owner"})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(out)
	if calls != 1 || strings.Contains(string(raw), productionTestSecret) || b.owner != f.req.Run.SessionID.String() {
		t.Fatalf("scope/redaction failure: calls=%d output=%s", calls, raw)
	}
	out, err = target.Run(ctx, map[string]any{"url": srv.URL, "headers": map[string]any{"Authorization": "Bearer {{CREDENTIAL:owner:token}}"}})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ = json.Marshal(out)
	wire, _ := json.Marshal(client.args)
	if calls != 2 || strings.Contains(string(raw), productionTestSecret) || strings.Contains(string(wire), productionTestSecret) || !strings.Contains(string(wire), "{{CREDENTIAL:owner:token}}") {
		t.Fatalf("placeholder was not resolved only in node: calls=%d output=%s", calls, raw)
	}
	f.req.Bot.Status = store.BotPaused
	if _, err = target.Run(ctx, map[string]any{"url": srv.URL, "credential": "owner"}); err == nil || calls != 2 {
		t.Fatal("revoked HTTP dispatched")
	}
}
