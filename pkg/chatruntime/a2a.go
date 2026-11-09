package chatruntime

import (
	"context"
	"fmt"
	"github.com/SAP/astonish/pkg/a2aclient"
	"github.com/SAP/astonish/pkg/credentials"
	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/tool"
	"time"
)

func strictA2ATools(ctx context.Context, s *store.Services, resolver credentials.CredentialResolver) ([]tool.Tool, error) {
	merged := map[string]a2aclient.A2AAgentConfig{}
	for _, tier := range []store.A2AAgentStore{s.PlatformA2AAgents, s.A2AAgents, s.TeamA2AAgents} {
		if tier == nil {
			continue
		}
		agents, err := tier.List(ctx)
		if err != nil {
			return nil, fmt.Errorf("scoped A2A config: %w", err)
		}
		for _, a := range agents {
			// A disabled higher-tier entry must suppress the inherited agent.
			if !a.IsEnabled() {
				delete(merged, a.Name)
				continue
			}
			timeout := 30 * time.Second
			if a.Timeout != "" {
				timeout, err = time.ParseDuration(a.Timeout)
				if err != nil {
					return nil, fmt.Errorf("A2A %q timeout: %w", a.Name, err)
				}
			}
			merged[a.Name] = a2aclient.A2AAgentConfig{Name: a.Name, URL: a.URL, CredentialName: a.CredentialName, AuthType: a.AuthType, Enabled: a.Enabled, Headers: a.Headers, Timeout: timeout}
		}
	}
	if len(merged) == 0 {
		return nil, nil
	}
	mgr := a2aclient.NewManager(&a2aclient.A2AClientConfig{Agents: merged})
	mgr.SetCredentialResolver(resolver)
	if err := mgr.Initialize(ctx); err != nil {
		return nil, fmt.Errorf("scoped A2A initialization: %w", err)
	}
	var tools []tool.Tool
	for name := range merged {
		card, err := mgr.GetAgentCard(name)
		if err != nil {
			return nil, fmt.Errorf("A2A %q card: %w", name, err)
		}
		client, err := mgr.GetClient(name)
		if err != nil {
			return nil, fmt.Errorf("A2A %q client: %w", name, err)
		}
		for _, t := range a2aclient.GenerateTools(name, card, client) {
			tools = append(tools, a2aclient.ToADKTool(t))
		}
	}
	return tools, nil
}
