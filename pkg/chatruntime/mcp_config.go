package chatruntime

import (
	"context"
	"fmt"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/store"
)

// LoadMCPConfig preserves the legacy context-only API. Strict callers must use
// LoadMCPConfigChecked to receive resolution errors.
func LoadMCPConfig(ctx context.Context, svc *store.Services, app *config.AppConfig, fallback FallbackPolicy) *config.MCPConfig {
	cfg, _ := LoadMCPConfigChecked(ctx, svc, app, fallback)
	return cfg
}
func LoadMCPConfigChecked(ctx context.Context, svc *store.Services, app *config.AppConfig, fallback FallbackPolicy) (*config.MCPConfig, error) {
	cfg := &config.MCPConfig{MCPServers: map[string]config.MCPServerConfig{}}
	if svc != nil && (svc.Mode == store.ModePlatform || fallback == FallbackForbidden) {
		if fallback == FallbackAllowed {
			for name, s := range config.FileMCPServers() {
				cfg.MCPServers[name] = s
			}
		}
		for i, tier := range []store.MCPServerStore{svc.PlatformMCPServers, svc.MCPServers, svc.TeamMCPServers} {
			if tier == nil {
				continue
			}
			servers, err := tier.List(ctx)
			if err != nil {
				if fallback == FallbackForbidden {
					return nil, fmt.Errorf("MCP config tier %d: %w", i, err)
				}
				continue
			}
			for _, s := range servers {
				cfg.MCPServers[s.Name] = config.MCPServerConfig{Command: s.Command, Args: s.Args, Env: s.Env, Transport: s.Transport, URL: s.URL, Enabled: s.Enabled}
			}
		}
		if fallback == FallbackAllowed {
			config.MergeStandardServersWithConfig(cfg, app)
		}
		return cfg, nil
	}
	if fallback == FallbackForbidden {
		return cfg, nil
	}
	loaded, err := config.LoadMCPConfig()
	if err != nil {
		return cfg, nil
	}
	return loaded, nil
}
