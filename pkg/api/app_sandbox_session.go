package api

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/sandbox/netpolicy"
	"github.com/SAP/astonish/pkg/sandbox/openshell"
	"github.com/SAP/astonish/pkg/store"
)

// ensureAppSandboxSession creates/ensures the per-user App sandbox session
// (shared by Apps MCP and Apps HTTP egress), waits until ready, PreSeeds
// Network Policy allows, and touches the idle tracker.
//
// Caller must invoke cleanup when done with the backend handle (session itself
// stays alive until idle timeout). The returned AppConfig is the one used for
// BackendFromAppConfig / PreSeed (reuse on HTTP transport retry).
var ensureAppSandboxSession = ensureAppSandboxSessionImpl

func ensureAppSandboxSessionImpl(ctx context.Context, r *http.Request, userID string) (sandbox.Backend, string, *config.AppConfig, func(), error) {
	appCfg, err := config.LoadAppConfig()
	if err != nil {
		return nil, "", nil, nil, fmt.Errorf("Apps HTTP requires a configured sandbox backend: %w", err)
	}
	if appCfg == nil || !sandbox.IsSandboxEnabled(&appCfg.Sandbox) {
		return nil, "", nil, nil, fmt.Errorf("Apps HTTP requires a configured sandbox backend")
	}

	backend, cleanup, err := sandbox.BackendFromAppConfig(appCfg)
	if err != nil {
		return nil, "", nil, nil, fmt.Errorf("sandbox unavailable for Apps: %w", err)
	}

	appMCPIdleTracker.StartIdleWatchdog(context.Background(), 10*time.Minute)

	syntheticSessionID := "app-mcp-" + userID
	templateName := sandbox.BaseTemplateID
	if r != nil {
		if svc := store.FromRequest(r); svc != nil && svc.Settings != nil {
			if settings, err := svc.Settings.Get(ctx); err == nil && settings != nil && settings.TemplateName != "" {
				templateName = settings.TemplateName
			}
		}
	}

	var layerChain []string
	if templateName != sandbox.BaseTemplateID {
		layerChain = resolveTemplateLayerChain(ctx, templateName)
	}
	if len(layerChain) == 0 {
		layerChain = resolveBaseLayerChain(ctx)
	}

	_, err = backend.CreateSession(ctx, sandbox.SessionSpec{
		SessionID:  syntheticSessionID,
		Type:       sandbox.SessionTypeChat,
		TemplateID: templateName,
		LayerChain: layerChain,
		UserID:     userID,
		Labels:     map[string]string{"purpose": "app-mcp"},
	})
	if err != nil {
		if cleanup != nil {
			cleanup()
		}
		return nil, "", nil, nil, fmt.Errorf("failed to ensure app sandbox session for user %q: %w", userID, err)
	}

	if err := backend.WaitForSessionReady(ctx, syntheticSessionID); err != nil {
		if cleanup != nil {
			cleanup()
		}
		return nil, "", nil, nil, fmt.Errorf("app sandbox session not ready for user %q: %w", userID, err)
	}

	seedCtx := withRuntimeNetworkPolicyContext(ctx, r, appCfg)
	netpolicy.EnsurePreSeedFromContext(seedCtx, syntheticSessionID)

	appMCPIdleTracker.touch(syntheticSessionID)
	return backend, syntheticSessionID, appCfg, cleanup, nil
}

// withRuntimeNetworkPolicyContext attaches DB Network Policy stores, OpenShell
// gateway config, and the request credential store so EnsurePreSeedFromContext
// can push allows into the sandbox and MCP env {{CREDENTIAL:...}} placeholders
// resolve during discovery/test.
func withRuntimeNetworkPolicyContext(ctx context.Context, r *http.Request, appCfg *config.AppConfig) context.Context {
	if r != nil {
		if svc := store.FromRequest(r); svc != nil {
			ctx = store.WithNetworkPolicyStores(ctx, &store.NetworkPolicyStores{
				Platform: svc.PlatformNetworkPolicies,
				Org:      svc.NetworkPolicies,
				Team:     svc.TeamNetworkPolicies,
			})
		}
		ctx = withRequestCredentialStore(ctx, r)
	}
	if appCfg != nil && appCfg.Sandbox.OpenShell.GatewayAddr != "" {
		ctx = netpolicy.WithGatewayConfig(ctx, &openshell.GRPCClientConfig{
			Addr: appCfg.Sandbox.OpenShell.GatewayAddr,
			TLS:  appCfg.Sandbox.OpenShell.OpenShellGatewayTLS(),
		})
	}
	return ctx
}

// detachedRuntimeNetworkPolicyContext carries only long-lived runtime values
// needed by background sandbox work. It deliberately does not inherit
// r.Context(), because async MCP discovery continues after the HTTP response.
func detachedRuntimeNetworkPolicyContext(r *http.Request, appCfg *config.AppConfig) context.Context {
	return withRuntimeNetworkPolicyContext(context.Background(), r, appCfg)
}

// --- App sandbox idle management (shared by MCP + HTTP) ---
//
// This tracker is also reused by the slides PDF export path
// (ExportSlidesPDFHandler) for its dedicated per-user "slides-pdf-<user>"
// sessions. destroyIdle reaps any tracked session id generically via
// backend.DestroySession — it makes no assumption about an "app-mcp-" prefix —
// so slides-pdf-* sessions are created, kept warm (touch), and reaped by the
// same watchdog. DestroySession is idempotent per the backend contract, so it
// is safe even for the Incus path which also has its own container idle
// management.
var appMCPIdleTracker = &appMCPTracker{
	lastActivity: make(map[string]time.Time),
}

type appMCPTracker struct {
	mu           sync.Mutex
	lastActivity map[string]time.Time
	started      bool
}

func (t *appMCPTracker) touch(sessionID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.lastActivity[sessionID] = time.Now()
}

func (t *appMCPTracker) StartIdleWatchdog(ctx context.Context, timeout time.Duration) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.started {
		return
	}
	t.started = true
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				t.destroyIdle(timeout)
			}
		}
	}()
}

func (t *appMCPTracker) destroyIdle(timeout time.Duration) {
	t.mu.Lock()
	now := time.Now()
	var expired []string
	for sid, lastUse := range t.lastActivity {
		if now.Sub(lastUse) > timeout {
			expired = append(expired, sid)
		}
	}
	for _, sid := range expired {
		delete(t.lastActivity, sid)
	}
	t.mu.Unlock()

	if len(expired) == 0 {
		return
	}

	appCfg, _ := config.LoadAppConfig()
	if appCfg == nil {
		slog.Warn("cannot destroy idle app sandbox sessions: app config not available")
		return
	}
	backend, cleanup, err := sandbox.BackendFromAppConfig(appCfg)
	if err != nil {
		slog.Warn("cannot destroy idle app sandbox sessions: backend unavailable", "error", err)
		return
	}
	if cleanup != nil {
		defer cleanup()
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	for _, sid := range expired {
		slog.Info("destroying idle app sandbox session", "sessionID", sid)
		netpolicy.ClearSessionSeeded(sid)
		if err := backend.DestroySession(ctx, sid); err != nil {
			slog.Warn("failed to destroy idle app sandbox session", "sessionID", sid, "error", err)
		}
	}
}
