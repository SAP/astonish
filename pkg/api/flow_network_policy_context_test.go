package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/sandbox/netpolicy"
	"github.com/SAP/astonish/pkg/store"
)

func TestFlowRuntimeNetworkPolicyContext_AttachesStoresAndGateway(t *testing.T) {
	platformStore := &stubNetworkPolicyStore{rules: []store.NetworkPolicyRule{{
		Host:   "platform.example.com",
		Port:   443,
		Action: store.NetworkPolicyAllow,
	}}}
	orgStore := &stubNetworkPolicyStore{rules: []store.NetworkPolicyRule{{
		Host:   "org.example.com",
		Port:   443,
		Action: store.NetworkPolicyAllow,
	}}}
	teamStore := &stubNetworkPolicyStore{rules: []store.NetworkPolicyRule{{
		Host:   "api.example.com",
		Port:   443,
		Action: store.NetworkPolicyAllow,
	}}}
	r := httptest.NewRequest(http.MethodPost, "/api/agents/test/run", nil)
	r = r.WithContext(store.WithServices(r.Context(), &store.Services{
		PlatformNetworkPolicies: platformStore,
		NetworkPolicies:         orgStore,
		TeamNetworkPolicies:     teamStore,
	}))

	appCfg := &config.AppConfig{}
	appCfg.Sandbox.OpenShell.GatewayAddr = "openshell.example:8443"

	ctx := withRuntimeNetworkPolicyContext(context.Background(), r, appCfg)
	nps := store.NetworkPolicyStoresFromContext(ctx)
	if nps == nil || nps.Platform != platformStore || nps.Org != orgStore || nps.Team != teamStore {
		t.Fatalf("expected all network policy stores on flow context, got %+v", nps)
	}
	gw := netpolicy.GatewayConfigFromContext(ctx)
	if gw == nil || gw.Addr != "openshell.example:8443" {
		t.Fatalf("expected OpenShell gateway config on flow context, got %+v", gw)
	}
}

// HandleChat (/api/chat) must use the same helpers as FlowRunHandler so classic
// Flows UI sandboxes inherit platform/org/team allows (e.g. **.cloud.sap).
func TestHandleChatRuntimeNetworkPolicyContext_AttachesStoresAndGateway(t *testing.T) {
	platformStore := &stubNetworkPolicyStore{rules: []store.NetworkPolicyRule{{
		Host:   "**.cloud.sap",
		Port:   443,
		Action: store.NetworkPolicyAllow,
	}}}
	orgStore := &stubNetworkPolicyStore{rules: []store.NetworkPolicyRule{{
		Host:   "org.example.com",
		Port:   443,
		Action: store.NetworkPolicyAllow,
	}}}
	teamStore := &stubNetworkPolicyStore{rules: []store.NetworkPolicyRule{{
		Host:   "kubernikus.qa-de-1.cloud.sap",
		Port:   443,
		Action: store.NetworkPolicyAllow,
	}}}
	r := httptest.NewRequest(http.MethodPost, "/api/chat", nil)
	r = r.WithContext(store.WithServices(r.Context(), &store.Services{
		PlatformNetworkPolicies: platformStore,
		NetworkPolicies:         orgStore,
		TeamNetworkPolicies:     teamStore,
	}))

	appCfg := &config.AppConfig{}
	appCfg.Sandbox.OpenShell.GatewayAddr = "openshell.example:8443"

	ctx := withRuntimeNetworkPolicyContext(context.Background(), r, appCfg)
	ctx = withRuntimeSandboxContext(ctx, r)
	nps := store.NetworkPolicyStoresFromContext(ctx)
	if nps == nil || nps.Platform != platformStore || nps.Org != orgStore || nps.Team != teamStore {
		t.Fatalf("expected all network policy stores on HandleChat context, got %+v", nps)
	}
	gw := netpolicy.GatewayConfigFromContext(ctx)
	if gw == nil || gw.Addr != "openshell.example:8443" {
		t.Fatalf("expected OpenShell gateway config on HandleChat context, got %+v", gw)
	}
}

func TestWithRuntimeSandboxContextAttachesTeamTemplateLayerAndImage(t *testing.T) {
	prevTemplateLayer := resolveRuntimeTemplateLayerChain
	prevTemplateImage := resolveRuntimeTemplateImage
	prevBaseLayer := resolveRuntimeBaseLayerChain
	prevBaseImage := resolveRuntimeBaseImage
	t.Cleanup(func() {
		resolveRuntimeTemplateLayerChain = prevTemplateLayer
		resolveRuntimeTemplateImage = prevTemplateImage
		resolveRuntimeBaseLayerChain = prevBaseLayer
		resolveRuntimeBaseImage = prevBaseImage
	})
	resolveRuntimeTemplateLayerChain = func(context.Context, string) []string {
		return []string{"@base", "cloakbrowser"}
	}
	resolveRuntimeTemplateImage = func(context.Context, string) string {
		return "ghcr.io/sap/browser-sandbox:test"
	}
	resolveRuntimeBaseLayerChain = func(context.Context) []string { return []string{"@base"} }
	resolveRuntimeBaseImage = func(context.Context) string { return "ghcr.io/sap/base-sandbox:test" }

	ctx := store.WithServices(context.Background(), &store.Services{
		Settings: &mockTeamSettingsStore{settings: &store.TeamSettings{TemplateName: "browser-enabled"}},
	})
	ctx = WithRuntimeSandboxContext(ctx)

	if got := store.SandboxTemplateFromContext(ctx); got != "browser-enabled" {
		t.Fatalf("expected team template, got %q", got)
	}
	if got := store.SandboxLayerChainFromContext(ctx); len(got) != 2 || got[1] != "cloakbrowser" {
		t.Fatalf("expected browser-enabled layer chain, got %v", got)
	}
	if got := store.SandboxImageFromContext(ctx); got != "ghcr.io/sap/browser-sandbox:test" {
		t.Fatalf("expected browser-enabled image, got %q", got)
	}
}

func TestFlowRuntimeSandboxContext_AttachesTeamTemplateLayerAndImage(t *testing.T) {
	prevTemplateLayer := resolveRuntimeTemplateLayerChain
	prevTemplateImage := resolveRuntimeTemplateImage
	prevBaseLayer := resolveRuntimeBaseLayerChain
	prevBaseImage := resolveRuntimeBaseImage
	t.Cleanup(func() {
		resolveRuntimeTemplateLayerChain = prevTemplateLayer
		resolveRuntimeTemplateImage = prevTemplateImage
		resolveRuntimeBaseLayerChain = prevBaseLayer
		resolveRuntimeBaseImage = prevBaseImage
	})
	resolveRuntimeTemplateLayerChain = func(context.Context, string) []string { return []string{"@base", "team-layer"} }
	resolveRuntimeTemplateImage = func(context.Context, string) string { return "ghcr.io/sap/team-sandbox:test" }
	resolveRuntimeBaseLayerChain = func(context.Context) []string { return []string{"@base", "base-layer"} }
	resolveRuntimeBaseImage = func(context.Context) string { return "ghcr.io/sap/base-sandbox:test" }

	r := httptest.NewRequest(http.MethodPost, "/api/agents/test/run", nil)
	r = r.WithContext(store.WithServices(r.Context(), &store.Services{
		Settings: &mockTeamSettingsStore{settings: &store.TeamSettings{TemplateName: "team-general"}},
	}))

	ctx := withRuntimeSandboxContext(context.Background(), r)
	if got := store.SandboxTemplateFromContext(ctx); got != "team-general" {
		t.Fatalf("expected team template, got %q", got)
	}
	if got := store.SandboxLayerChainFromContext(ctx); len(got) != 2 || got[1] != "team-layer" {
		t.Fatalf("expected team layer chain, got %v", got)
	}
	if got := store.SandboxImageFromContext(ctx); got != "ghcr.io/sap/team-sandbox:test" {
		t.Fatalf("expected team image, got %q", got)
	}
}
