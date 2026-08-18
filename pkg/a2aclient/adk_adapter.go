package a2aclient

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/tool"
	"google.golang.org/genai"
)

// adkA2ATool wraps an A2ATool to implement the ADK tool.Tool interface.
// This allows A2A agent skills to be registered in the ChatAgent's tool
// groups and discovered via the ToolIndex (dynamic injection).
type adkA2ATool struct {
	inner *A2ATool
}

// Name returns the tool name (implements tool.Tool).
func (t *adkA2ATool) Name() string {
	return t.inner.Name()
}

// Description returns the tool description (implements tool.Tool).
func (t *adkA2ATool) Description() string {
	return t.inner.Description()
}

// Declaration returns the JSON schema for the tool parameters (implements tool.Tool).
func (t *adkA2ATool) Declaration() *genai.FunctionDeclaration {
	return &genai.FunctionDeclaration{
		Name:        t.inner.Name(),
		Description: t.inner.Description(),
		Parameters: &genai.Schema{
			Type: "OBJECT",
			Properties: map[string]*genai.Schema{
				"message": {
					Type:        "STRING",
					Description: "The message to send to the remote A2A agent",
				},
				"context_id": {
					Type:        "STRING",
					Description: "Optional conversation context ID for multi-turn interactions with this agent",
				},
			},
			Required: []string{"message"},
		},
	}
}

// Run executes the tool (implements tool.Tool).
func (t *adkA2ATool) Run(ctx tool.Context, args any) (map[string]any, error) {
	argsMap, ok := args.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("a2a tool %q: expected map[string]any args, got %T", t.inner.Name(), args)
	}
	return t.inner.Run(ctx, argsMap)
}

// IsLongRunning returns true because A2A calls are remote network requests
// that may take significant time (implements tool.Tool).
func (t *adkA2ATool) IsLongRunning() bool {
	return true
}

// ToADKTool wraps an A2ATool into an ADK-compatible tool.Tool.
func ToADKTool(t *A2ATool) tool.Tool {
	return &adkA2ATool{inner: t}
}

// GetA2ATools loads A2A agent configuration, initializes the manager,
// fetches agent cards, and returns ADK-compatible tools for all enabled agents.
//
// In platform mode, it reads from context-injected stores (platform → org → team cascade).
// In personal mode, it reads from the local config file.
//
// The returned tools are meant to be registered as a tool group (not on the main
// thread) so they are discoverable via search_tools / ToolIndex without bloating
// the system prompt.
func GetA2ATools(ctx context.Context, platformMode bool) []tool.Tool {
	cfg, err := LoadA2AAgentConfig(ctx, platformMode)
	if err != nil {
		slog.Warn("failed to load A2A agent config", "error", err)
		return nil
	}
	if cfg == nil || len(cfg.Agents) == 0 {
		return nil
	}

	mgr := NewManager(cfg)
	if err := mgr.Initialize(ctx); err != nil {
		slog.Warn("failed to initialize A2A client manager", "error", err)
		return nil
	}

	var tools []tool.Tool
	for name := range cfg.Agents {
		card, err := mgr.GetAgentCard(name)
		if err != nil {
			// Agent card not fetched (offline, etc.) — skip
			slog.Debug("skipping A2A agent (no card)", "agent", name, "error", err)
			continue
		}

		client, err := mgr.GetClient(name)
		if err != nil {
			continue
		}

		a2aTools := GenerateTools(name, card, client)
		for _, t := range a2aTools {
			tools = append(tools, ToADKTool(t))
		}
	}

	if len(tools) > 0 {
		slog.Info("loaded A2A agent tools", "count", len(tools))
	}
	return tools
}

// GetA2AToolsFromStores loads A2A tools using explicit store references
// (for per-request context injection in platform mode).
// This is called from the chat runner's per-request setup, not at startup.
func GetA2AToolsFromStores(ctx context.Context, stores *store.A2AAgentStores) []tool.Tool {
	if stores == nil {
		return nil
	}

	// Build a merged config from the stores (same cascade as loadA2AAgentConfigPlatform)
	merged := make(map[string]A2AAgentConfig)

	// Platform tier
	if stores.Platform != nil {
		agents, err := stores.Platform.List(ctx)
		if err == nil {
			for _, a := range agents {
				if !a.IsEnabled() {
					continue
				}
				merged[a.Name] = storeAgentToConfig(a)
			}
		}
	}

	// Org tier (overrides platform)
	if stores.Org != nil {
		agents, err := stores.Org.List(ctx)
		if err == nil {
			for _, a := range agents {
				if !a.IsEnabled() {
					continue
				}
				merged[a.Name] = storeAgentToConfig(a)
			}
		}
	}

	// Team tier (overrides org)
	if stores.Team != nil {
		agents, err := stores.Team.List(ctx)
		if err == nil {
			for _, a := range agents {
				if !a.IsEnabled() {
					continue
				}
				merged[a.Name] = storeAgentToConfig(a)
			}
		}
	}

	if len(merged) == 0 {
		return nil
	}

	cfg := &A2AClientConfig{Agents: merged}
	mgr := NewManager(cfg)
	if err := mgr.Initialize(ctx); err != nil {
		slog.Warn("failed to initialize A2A client manager from stores", "error", err)
		return nil
	}

	var tools []tool.Tool
	for name := range merged {
		card, err := mgr.GetAgentCard(name)
		if err != nil {
			continue
		}
		client, err := mgr.GetClient(name)
		if err != nil {
			continue
		}
		a2aTools := GenerateTools(name, card, client)
		for _, t := range a2aTools {
			tools = append(tools, ToADKTool(t))
		}
	}

	return tools
}
