package launcher

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"maps"
	"slices"
	"strings"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/tool"
)

// Snapshot the symbolic configuration, never the resolved credential vault.
func botMCPConfig(s store.MCPServer) config.MCPServerConfig {
	enabled := s.IsEnabled()
	transport := s.Transport
	if transport == "" {
		transport = "stdio"
	}
	return config.MCPServerConfig{Command: s.Command, Args: slices.Clone(s.Args), Env: maps.Clone(s.Env), Transport: transport, URL: s.URL, Enabled: &enabled}
}
func botMCPFingerprint(cfg config.MCPServerConfig) string {
	raw, _ := json.Marshal(cfg)
	sum := sha256.Sum256(raw)
	return "mcp-config-v1:" + hex.EncodeToString(sum[:])
}
func (t *botMCPTool) currentStore(ctx context.Context) (store.MCPServerStore, error) {
	var selected store.MCPServerStore
	var current *store.MCPServer
	for _, s := range t.stores {
		if s == nil {
			continue
		}
		rows, err := s.List(ctx)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			if row.Name == t.serverName {
				copy := row
				current = &copy
				selected = s
			}
		}
	}
	if current == nil || !current.IsEnabled() || botMCPFingerprint(botMCPConfig(*current)) != botMCPFingerprint(t.config) {
		return nil, store.ErrBotConflict
	}
	return selected, nil
}

// WithBotMCPClassifier binds executable adapters before factory contexts are
// captured. Missing adapters fail closed, and model arguments cannot choose a
// target or promote MCP annotations to effect authority.
func WithBotMCPClassifier(base agent.BotActionClassifier, tools []tool.Tool) agent.BotActionClassifier {
	targets := map[string]*botMCPTool{}
	for _, t := range tools {
		if m, ok := t.(*botMCPTool); ok {
			targets[m.Name()] = m
		}
	}
	return botMCPClassifier{base: base, targets: targets}
}

type botMCPClassifier struct {
	base    agent.BotActionClassifier
	targets map[string]*botMCPTool
}

func (c botMCPClassifier) DescribeAction(ctx context.Context, name string, args json.RawMessage) (execution.ActionDescriptor, error) {
	if strings.HasPrefix(name, "mcp:") || strings.HasPrefix(name, "mcp-discovery:") {
		t := c.targets[name]
		if t == nil {
			return execution.ActionDescriptor{}, store.ErrBotForbidden
		}
		if _, err := t.currentStore(ctx); err != nil {
			return execution.ActionDescriptor{}, err
		}
		return execution.ActionDescriptor{Tool: name, Arguments: args, Effect: execution.EffectUnknown, TargetFingerprint: botMCPFingerprint(t.config)}, nil
	}
	if c.base == nil {
		return execution.ActionDescriptor{}, store.ErrBotForbidden
	}
	return c.base.DescribeAction(ctx, name, args)
}

// The bound factory installs this private handoff only after lease, exact policy
// authorization and tool budget reservation. It is never a public argument.
type botMCPAuthorizedKey struct{}
