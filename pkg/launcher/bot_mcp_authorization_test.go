package launcher

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/google/uuid"
	"google.golang.org/adk/tool"

	"github.com/SAP/astonish/pkg/sandbox/sessioncreds"
	"github.com/SAP/astonish/pkg/store"
)

func TestBotMCPSecurityPreparationHasNoEffects(t *testing.T) {
	b := &productionCapabilityBackend{}
	client := &productionCapabilityClient{backend: b}
	ctx := store.WithCredentialStore(context.Background(), sessioncreds.NewStore(nil))
	ts, cleanup, err := BotMCPTools(ctx, productionCapabilityPool{backend: b, client: client}, "run", newMemMCPStore(store.MCPServer{Name: "unreviewed", Command: "must-not-start"}))
	defer cleanup()
	if client.calls != 0 || b.starts != 0 || len(b.vault) != 0 {
		t.Fatalf("unapproved discovery effects: calls=%d starts=%d vault=%d", client.calls, b.starts, len(b.vault))
	}
	if err != nil || len(ts) != 1 || ts[0].Name() != "mcp-discovery:unreviewed" {
		t.Fatalf("missing separately reviewed discovery: %v %v", ts, err)
	}
}

// Test guard models atomic exact-once grant consumption after live policy checks.
type mcpOnceGuard struct{ used bool }

func (g *mcpOnceGuard) Authorize(ctx context.Context, a execution.ActionDescriptor) (execution.PolicyDecision, error) {
	d := execution.EvaluateRunPolicy(ctx, a, time.Now())
	if g.used || d.Kind != execution.PolicyConsumeGrant {
		return d, store.ErrBotForbidden
	}
	g.used = true
	d.Kind = execution.PolicyAllow
	return d, nil
}
func approveMCPTestTool(t *testing.T, target botRunnableTool, ctx tool.Context, args map[string]any) tool.Context {
	t.Helper()
	bound := target.(*botBoundTool)
	raw, _ := json.Marshal(args)
	runtime := agent.BotPolicyRuntimeFromContext(bound.binding.owner)
	a, err := runtime.Classifier.DescribeAction(bound.binding.owner, target.Name(), raw)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := a.Digest()
	if err != nil {
		t.Fatal(err)
	}
	policy, err := bound.binding.req.Policy.WithGrant(execution.ScopedGrant{ID: uuid.New(), Identity: bound.binding.req.Policy.Identity(), ActionDigest: digest, ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	runtime.Guard = &mcpOnceGuard{}
	bound.binding.owner = agent.WithBotPolicyRuntime(bound.binding.owner, runtime)
	return botFactoryHandoffContext{base: execution.WithRunPolicy(ctx, policy), session: ctx.SessionID()}
}

func TestBotMCPSecurityDiscoveryFences(t *testing.T) {
	for _, mode := range []string{"no-permission", "review-required", "revoked", "budget", "target-changed", "approved-once"} {
		t.Run(mode, func(t *testing.T) {
			f := newBotFactoryFixture(t)
			b := &productionCapabilityBackend{}
			client := &productionCapabilityClient{backend: b}
			exe, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			s := store.MCPServer{Name: "controlled", Command: exe, Args: []string{"-test.run=^TestBotMCPNodeStdioFixture$"}, Env: map[string]string{"ASTONISH_MCP_FIXTURE": "1"}}
			servers := newMemMCPStore(s)
			target, ctx := productionFactoryTool(t, f, "mcp-discovery:controlled", func(ctx context.Context, id string) ([]tool.Tool, func(), error) {
				return BotMCPTools(ctx, productionCapabilityPool{backend: b, client: client}, id, servers)
			})
			if client.calls != 0 || len(b.vault) != 0 {
				t.Fatal("preparation effects")
			}
			if mode != "review-required" && mode != "no-permission" {
				ctx = approveMCPTestTool(t, target, ctx, map[string]any{})
			}
			switch mode {
			case "no-permission":
				p, _ := execution.NewRunPolicy(execution.RunPolicySpec{Identity: f.req.Policy.Identity(), Level: execution.AutonomyScoped, AllowedTools: []string{"read_file"}})
				ctx = botFactoryHandoffContext{base: execution.WithRunPolicy(ctx, p), session: ctx.SessionID()}
			case "revoked":
				f.req.Bot.Status = store.BotPaused
			case "budget":
				target.(*botBoundTool).binding.req.Budget = &mcpDeniedBudget{}
			case "target-changed":
				s.Command = "replacement"
				if err := servers.Save(context.Background(), &s); err != nil {
					t.Fatal(err)
				}
			}
			_, err = target.Run(ctx, map[string]any{})
			if mode == "approved-once" {
				if err != nil || client.calls != 1 {
					t.Fatalf("approved discovery: %v calls=%d", err, client.calls)
				}
				if _, err = target.Run(ctx, map[string]any{}); err == nil || client.calls != 1 {
					t.Fatal("approval replay")
				}
				rows, _ := servers.List(context.Background())
				if len(rows[0].CachedTools) == 0 {
					t.Fatal("discovery not cached")
				}
			} else {
				if err == nil || client.calls != 0 || len(b.vault) != 0 {
					t.Fatalf("blocked action dispatched: %v calls=%d", err, client.calls)
				}
				if mode == "review-required" {
					var p *agent.BotPolicyError
					if !errors.As(err, &p) || !p.ApprovalRequired() {
						t.Fatalf("missing explicit approval: %v", err)
					}
				}
			}
		})
	}
}

type mcpDeniedBudget struct{ botFactoryTestBudget }

func (*mcpDeniedBudget) ReserveTool(context.Context, string) error { return store.ErrBotForbidden }
func TestBotMCPTargetFingerprintSymbolicConfig(t *testing.T) {
	original := store.MCPServer{Name: "same", Command: "server", Args: []string{"--mode=read"}, URL: "https://old.invalid/mcp", Env: map[string]string{"TOKEN": "{{CREDENTIAL:owner:token}}"}}
	baseline := botMCPFingerprint(botMCPConfig(original))
	mutations := map[string]func(*store.MCPServer){
		"command":              func(s *store.MCPServer) { s.Command = "replacement" },
		"url":                  func(s *store.MCPServer) { s.URL = "https://replacement.invalid/mcp" },
		"transport":            func(s *store.MCPServer) { s.Transport = "sse" },
		"arguments":            func(s *store.MCPServer) { s.Args = []string{"--mode=write"} },
		"credential-reference": func(s *store.MCPServer) { s.Env = map[string]string{"TOKEN": "{{CREDENTIAL:other:token}}"} },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			s := original
			mutate(&s)
			if botMCPFingerprint(botMCPConfig(s)) == baseline {
				t.Fatal("target change unbound")
			}
		})
	}
	snapshot := botMCPConfig(original)
	original.Env["TOKEN"] = "changed"
	original.Args[0] = "changed"
	if botMCPFingerprint(snapshot) != baseline {
		t.Fatal("snapshot mutable through caller configuration")
	}
	raw, _ := json.Marshal(execution.ActionDescriptor{Tool: "mcp:same/call", Arguments: json.RawMessage(`{}`), Effect: execution.EffectUnknown, TargetFingerprint: baseline})
	if strings.Contains(string(raw), "CREDENTIAL") || strings.Contains(string(raw), "TOKEN") {
		t.Fatalf("configuration leaked into approval: %s", raw)
	}
}
func TestBotMCPTargetDescriptorChanges(t *testing.T) {
	ctx := store.WithCredentialStore(context.Background(), sessioncreds.NewStore(nil))
	b := &productionCapabilityBackend{}
	client := &productionCapabilityClient{backend: b}
	server := store.MCPServer{Name: "target", Command: "old", CachedTools: json.RawMessage(`[{"name":"call","inputSchema":{"type":"object","properties":{}}}]`)}
	var digests []string
	for _, cmd := range []string{"old", "new"} {
		server.Command = cmd
		ts, cleanup, err := BotMCPTools(ctx, productionCapabilityPool{backend: b, client: client}, "run", newMemMCPStore(server))
		if err != nil {
			t.Fatal(err)
		}
		cleanup()
		// Catalogue classification must be supplied from trusted executable adapters.
		classifier := WithBotMCPClassifier(BotCatalogueClassifier{}, ts)
		_ = ts
		a, err := classifier.DescribeAction(ctx, "mcp:target/call", json.RawMessage(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		digest, err := a.Digest()
		if err != nil {
			t.Fatal(err)
		}
		if a.TargetFingerprint != botMCPFingerprint(botMCPConfig(server)) {
			t.Fatal("descriptor not bound to trusted server configuration")
		}
		forged, err := classifier.DescribeAction(ctx, "mcp:target/call", json.RawMessage(`{"target_fingerprint":"model-chosen","command":"model-command"}`))
		if err != nil || forged.TargetFingerprint != a.TargetFingerprint || forged.Effect != execution.EffectUnknown {
			t.Fatalf("model arguments changed trusted authority: %+v %v", forged, err)
		}
		if _, err := classifier.DescribeAction(ctx, "mcp:missing/call", json.RawMessage(`{}`)); err == nil {
			t.Fatal("unbound MCP alias accepted")
		}
		digests = append(digests, digest)
	}
	if digests[0] == digests[1] {
		t.Fatal("same alias/arguments retain approval after target replacement")
	}
}
