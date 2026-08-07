package api

import (
	"sync"

	"github.com/SAP/astonish/pkg/config"
)

// localProvidersMu guards the process-wide snapshot of providers configured in
// the local config.yaml. In platform mode, the daemon loads config.yaml at
// startup and publishes its provider instances here so they can be surfaced —
// read-only — alongside the database-backed providers in the effective view.
//
// Provider RUNTIME resolution in platform mode still comes exclusively from the
// database cascade (see request_helpers.go). This snapshot is display-only: it
// lets operators see the config.yaml defaults (useful for Kubernetes deploys)
// without those entries becoming runtime-selectable or writable.
//
// Because every pod loads the same config.yaml, each config.yaml provider is
// keyed by its instance name and therefore appears exactly once — the map keys
// naturally dedupe identical configuration across pods.
var (
	localProvidersMu sync.RWMutex
	localProviders   map[string]config.ProviderConfig
)

// SetLocalProviders publishes the config.yaml provider instances so the
// effective-providers view can surface them read-only. Called once at daemon
// bootstrap. A nil or empty map clears the snapshot.
func SetLocalProviders(providers map[string]config.ProviderConfig) {
	localProvidersMu.Lock()
	defer localProvidersMu.Unlock()
	if len(providers) == 0 {
		localProviders = nil
		return
	}
	// Copy so later mutations of the caller's map don't leak in.
	cp := make(map[string]config.ProviderConfig, len(providers))
	for name, pCfg := range providers {
		inner := make(config.ProviderConfig, len(pCfg))
		for k, v := range pCfg {
			inner[k] = v
		}
		cp[name] = inner
	}
	localProviders = cp
}

// getLocalProviders returns a snapshot copy of the config.yaml providers.
func getLocalProviders() map[string]config.ProviderConfig {
	localProvidersMu.RLock()
	defer localProvidersMu.RUnlock()
	if len(localProviders) == 0 {
		return nil
	}
	cp := make(map[string]config.ProviderConfig, len(localProviders))
	for name, pCfg := range localProviders {
		cp[name] = pCfg
	}
	return cp
}
