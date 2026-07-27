package netpolicy

import (
	"context"

	"github.com/SAP/astonish/pkg/sandbox/openshell"
	"github.com/SAP/astonish/pkg/store"
)

// SessionBridge applies network policy to a headless session (scheduler, etc.).
type SessionBridge struct {
	GatewayCfg *openshell.GRPCClientConfig
	SessionID  string
	Stores     *store.NetworkPolicyStores
}

// OnToolResult auto-approves PolicyAllow denials. PreSeed is primarily done
// before the first tool Call (EnsurePreSeedFromContext); this path is a
// no-op when already seeded.
func (b *SessionBridge) OnToolResult(ctx context.Context, toolName string, resp map[string]any, fallbackURL string) {
	if b == nil || b.SessionID == "" {
		return
	}
	if !SessionIsSeeded(b.SessionID) {
		// Prefer gateway/stores from the bridge; also stash gateway on ctx
		// so EnsurePreSeedFromContext can find it if Stores were injected elsewhere.
		seedCtx := ctx
		if b.GatewayCfg != nil {
			seedCtx = WithGatewayConfig(seedCtx, b.GatewayCfg)
		}
		if b.Stores != nil {
			if err := PreSeedFromStores(seedCtx, b.GatewayCfg, b.SessionID, b.Stores); err == nil {
				MarkSessionSeeded(b.SessionID)
			}
		} else {
			EnsurePreSeedFromContext(seedCtx, b.SessionID)
		}
	}
	if resp == nil {
		return
	}

	ep := LoadFromStores(ctx, b.Stores)
	var denials []map[string]any
	switch {
	case toolName == "shell_command" && (LooksLikeNetworkDenial(resp) || LooksLikeShellNetworkFailure(resp)):
		stdout, _ := resp["stdout"].(string)
		denials = ExtractDenialsFromOutput(stdout)
		if len(denials) == 0 {
			denials = ExtractDenialsFromShellCommand(fallbackURL)
		}
	case IsNetworkTool(toolName) && LooksLikeNetworkToolDenial(resp):
		denials = ExtractDenialFromToolError(resp, fallbackURL)
	}
	if len(denials) == 0 {
		return
	}
	// Discard unknown — headless has no approval UI.
	_ = ApplyPolicyAllowDenials(ctx, b.GatewayCfg, b.SessionID, ep, denials)
}
