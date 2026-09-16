package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/SAP/astonish/pkg/a2a"
	"github.com/SAP/astonish/pkg/store"
)

func collectTenantCapabilities(r *http.Request) a2a.CapabilitySet {
	capabilities := a2a.CapabilitySet{}
	services := store.FromRequest(r)
	mcpSources := effectiveMCPSourceNames(r, services)

	for _, toolInfo := range GetCachedToolsForRequest(r) {
		source := strings.TrimSpace(toolInfo.Source)
		if _, ok := mcpSources[strings.ToLower(source)]; !ok {
			source = "internal"
		}
		capabilities.Tools = append(capabilities.Tools, a2a.ToolCapability{
			Name:        toolInfo.Name,
			Description: toolInfo.Description,
			Source:      source,
		})
	}

	if services == nil {
		return capabilities
	}
	items, err := listA2AAgentsMerged(services)
	if err != nil {
		return capabilities
	}
	for _, item := range items {
		if !item.Enabled {
			continue
		}
		var skills []a2a.Skill
		if len(item.CachedSkills) > 0 {
			_ = json.Unmarshal(item.CachedSkills, &skills)
		}
		description := "Connected A2A agent"
		if len(skills) > 0 && strings.TrimSpace(skills[0].Description) != "" {
			description = skills[0].Description
		}
		capabilities.RemoteAgents = append(capabilities.RemoteAgents, a2a.RemoteAgentCapability{
			Name:        item.Name,
			Description: description,
			Skills:      skills,
		})
	}
	return capabilities
}

func tenantCardSkills(r *http.Request) ([]a2a.Skill, string) {
	capabilities := collectTenantCapabilities(r)
	return a2a.SkillsFromCapabilities(capabilities), a2a.SummarizeCapabilities(capabilities)
}

func effectiveMCPSourceNames(r *http.Request, services *store.Services) map[string]struct{} {
	names := make(map[string]struct{})
	if services == nil {
		return names
	}
	for _, serverStore := range []store.MCPServerStore{
		services.PlatformMCPServers,
		services.MCPServers,
		services.TeamMCPServers,
	} {
		if serverStore == nil {
			continue
		}
		servers, err := serverStore.List(r.Context())
		if err != nil {
			continue
		}
		for _, server := range servers {
			key := strings.ToLower(strings.TrimSpace(server.Name))
			if key == "" {
				continue
			}
			if server.IsEnabled() {
				names[key] = struct{}{}
			} else {
				delete(names, key)
			}
		}
	}
	return names
}
