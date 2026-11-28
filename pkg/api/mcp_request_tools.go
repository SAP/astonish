package api

import (
	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/cache"
	"github.com/SAP/astonish/pkg/chatruntime"
	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
	"net/http"
)

func buildRequestMCPToolGroups(r *http.Request, pool sandbox.ToolNodePool, debug bool) map[string]*agent.ToolGroup {
	if r == nil {
		return nil
	}
	return chatruntime.BuildMCPToolGroups(r.Context(), store.FromRequest(r), loadMCPConfigForRequest(r), credentialResolverForRequest(r), pool, debug, chatruntime.FallbackAllowed)
}
func parseCachedToolsJSONBytes(data []byte) []cache.ToolEntry {
	entries, _ := chatruntime.ParseCachedToolsJSONBytes(data)
	return entries
}
