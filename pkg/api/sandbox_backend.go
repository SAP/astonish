// Package api — backend-agnostic sandbox helpers.
//
// sandboxBackendForRequest builds a sandbox.Backend from the current
// request's effective AppConfig. It supports both Incus and K8s backends
// transparently, allowing handler code to use the Backend interface
// rather than calling Incus directly.
//
// The returned cleanup func MUST be deferred by the caller (it may be nil).
package api

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
)

// sandboxBackendForRequest constructs a sandbox.Backend appropriate for
// the deployment's sandbox.backend config. Returns the backend, a cleanup
// function (may be nil), and any construction error.
//
// Callers MUST defer cleanup when non-nil:
//
//	backend, cleanup, err := sandboxBackendForRequest(r)
//	if cleanup != nil { defer cleanup() }
func sandboxBackendForRequest(r *http.Request) (sandbox.Backend, func(), error) {
	appCfg := effectiveAppConfig(r)
	if appCfg == nil {
		return nil, nil, fmt.Errorf("sandbox unavailable: unable to load application config")
	}
	if !sandbox.IsSandboxEnabled(&appCfg.Sandbox) {
		return nil, nil, fmt.Errorf("sandbox is not enabled in configuration")
	}

	// In platform mode, use the PG-backed session registry for cross-replica
	// consistency. Returns nil in personal mode (single-node), causing
	// BackendFromAppConfigWithSessions to fall back to local file registry.
	sessRegistry := buildPGSessionRegistry(r.Context())
	b, cleanup, err := sandbox.BackendFromAppConfigWithSessions(appCfg, sessRegistry)
	if err != nil {
		return nil, nil, fmt.Errorf("sandbox unavailable: %w", err)
	}
	return b, cleanup, nil
}

// sandboxBackendForTeamTemplate constructs a sandbox.Backend whose session
// registry is backed by the team's PostgreSQL schema rather than the pod-
// local JSON file. This is critical for multi-replica API deployments: all
// replicas see the same session records, preventing the status-poll bug
// where a request lands on a replica that never saw the CreateSession call.
//
// Falls back to sandboxBackendForRequest if the platform pgstore is not
// available (personal mode) or if the tenant context cannot be resolved.
func sandboxBackendForTeamTemplate(r *http.Request) (sandbox.Backend, func(), error) {
	// Attempt to wire a pgstore-backed session registry for cross-replica
	// consistency. If unavailable, fall back to the default (local JSON)
	// via sandboxBackendForRequest.
	sessRegistry := buildPGSessionRegistry(r.Context())
	if sessRegistry == nil {
		return sandboxBackendForRequest(r)
	}

	appCfg := effectiveAppConfig(r)
	if appCfg == nil {
		return nil, nil, fmt.Errorf("sandbox unavailable: unable to load application config")
	}
	if !sandbox.IsSandboxEnabled(&appCfg.Sandbox) {
		return nil, nil, fmt.Errorf("sandbox is not enabled in configuration")
	}

	b, cleanup, err := sandbox.BackendFromAppConfigWithSessions(appCfg, sessRegistry)
	if err != nil {
		return nil, nil, fmt.Errorf("sandbox unavailable: %w", err)
	}
	return b, cleanup, nil
}

// buildPGSessionRegistry attempts to construct a DB-backed SessionRegistry
// for the current request's team. Returns nil if the platform backend does not
// support DB-backed sandbox sessions or tenant context is unavailable.
// For SQLite deployments (single-node), the caller falls back to the local
// file-based session registry which is adequate.
func buildPGSessionRegistry(ctx context.Context) *sandbox.SessionRegistry {
	backend := getPlatformBackend()
	if backend == nil {
		return nil
	}
	tc := store.TenantContextFrom(ctx)
	if tc == nil || tc.OrgSlug == "" || tc.TeamSlug == "" {
		return nil
	}

	// Check if the backend supports DB-backed sandbox sessions.
	provider, ok := backend.(store.SandboxSessionProvider)
	if !ok {
		return nil
	}
	sessStore := provider.SandboxSessionsForTeam(ctx, tc.OrgSlug, tc.TeamSlug)
	if sessStore == nil {
		return nil
	}
	return sandbox.NewSessionRegistryFromStore(sessStore)
}

// sandboxSessionRegistryForRequest returns the sandbox SessionRegistry that
// the caller is allowed to see, scoped to the request's tenant.
//
// In platform (multi-tenant / PostgreSQL) mode it returns the DB-backed
// registry for the caller's org/team via buildPGSessionRegistry, so lookups
// (List, ResolveSessionID, GetByContainerName, ...) only observe that team's
// sandbox sessions. In personal / single-tenant (SQLite) mode
// buildPGSessionRegistry returns nil and we fall back to the local
// file-based registry, which is adequate because there is only one tenant.
//
// SECURITY: sandbox container-management handlers MUST obtain their registry
// through this helper rather than calling sandbox.NewSessionRegistry()
// directly. The direct call yields the unscoped personal-mode registry, which
// on a platform deployment exposes every team's sandbox sessions and allows
// cross-tenant list/delete/expose/proxy by container id.
func sandboxSessionRegistryForRequest(r *http.Request) (*sandbox.SessionRegistry, error) {
	if reg := buildPGSessionRegistry(r.Context()); reg != nil {
		return reg, nil
	}
	// Personal / single-tenant deployments: no team schema to scope to.
	return sandbox.NewSessionRegistry()
}

// teamTemplateSessionID returns the canonical session ID used by the
// team-template editor for a given team slug. This is a well-known,
// deterministic ID so the editor pod/container can be found across
// handler invocations without extra DB state.
func teamTemplateSessionID(teamSlug string) string {
	return "team-template-" + teamSlug
}

// ---------------------------------------------------------------------------
// Layer-chain resolution
// ---------------------------------------------------------------------------
//
// These helpers resolve the template DAG in the platform DB into concrete
// layer chains consumed by the K8s backend (SessionSpec.LayerChain →
// ASTONISH_LAYER_CHAIN env var → overlay entrypoint).
//
// Chain semantics:
//   - Oldest (bottom) first, youngest (top) last.
//   - The literal "@base" MUST appear exactly once as chain[0] — it
//     represents the seed-Job rootfs directory on the PVC.
//   - Subsequent entries are content-addressed SHA-256 deltas.
//
// Sentinel rule (§5.12 migration 005): the seed migration stores
// top_layer_id='@base' as a FK-satisfying sentinel meaning "no delta
// has been applied yet." Resolvers MUST filter this sentinel from CTE
// results so the chain never contains duplicate @base entries.
// ---------------------------------------------------------------------------

// baseTopLayerResolver is the narrow interface required by
// resolveBaseLayerChainWith. Satisfied by store.SandboxTemplateStore.
type baseTopLayerResolver interface {
	GetBaseTopLayerID(ctx context.Context) (string, error)
}

// resolveTemplateLayerChain resolves a template slug (e.g. "team-general")
// to an ordered layer chain (oldest-first, e.g. ["@base", "<sha256>"]).
//
// On K8s, the returned chain is passed as SessionSpec.LayerChain to the
// backend, which sets ASTONISH_LAYER_CHAIN in the pod env. Each element is
// a literal directory name under /mnt/astonish-layers/ on the PVC.
//
// Returns nil if:
//   - The platform backend is not available.
//   - The template doesn't exist in the DB (not yet saved).
//   - Resolution fails (broken DAG).
//
// Callers MUST tolerate nil and fall back gracefully (e.g. use @base).
func resolveTemplateLayerChain(ctx context.Context, templateSlug string) []string {
	backend := getPlatformBackend()
	if backend == nil {
		return nil
	}
	templates := backend.SandboxTemplates()
	if templates == nil {
		return nil
	}
	return resolveTemplateLayerChainWith(ctx, templates, templateSlug)
}

// resolveTemplateLayerChainWith is the testable core of
// resolveTemplateLayerChain. It accepts an explicit template store so
// tests can inject mocks without touching the package-level singleton.
func resolveTemplateLayerChainWith(ctx context.Context, templates store.SandboxTemplateStore, templateSlug string) []string {
	// Derive the lookup parameters from the slug convention.
	// Template slugs are "team-<teamSlug>"; scope=team, owner_id=<teamSlug>.
	teamSlug := strings.TrimPrefix(templateSlug, "team-")
	if teamSlug == templateSlug {
		// Not a team-prefixed template; might be a different scope.
		// For now, only team templates are supported.
		return nil
	}

	tpl, err := templates.GetBySlug(ctx, store.SandboxTemplateScopeTeam, teamSlug, templateSlug)
	if err != nil || tpl == nil {
		slog.Debug("template not found in platform DB, falling back to @base",
			"slug", templateSlug, "error", err)
		return nil
	}

	// Resolve the parent chain → ordered list of layer IDs.
	chain, err := templates.Resolve(ctx, tpl.ID)
	if err != nil || chain == nil || len(chain.LayerIDs) == 0 {
		slog.Warn("failed to resolve template layer chain",
			"template", tpl.ID, "slug", templateSlug, "error", err)
		return nil
	}

	// Filter the "@base" sentinel from the CTE result. The seed migration
	// (005) stores top_layer_id='@base' to satisfy the FK constraint; it
	// is NOT a real content-addressed delta. Without this filter, fresh-
	// install scenarios produce duplicate @base entries in the chain.
	filtered := make([]string, 0, len(chain.LayerIDs))
	for _, id := range chain.LayerIDs {
		if id != sandbox.BaseTemplateID {
			filtered = append(filtered, id)
		}
	}
	if len(filtered) == 0 {
		// All entries were sentinels — template has no real deltas.
		return nil
	}

	// Prepend the literal seed-layer (@base) so the overlay entrypoint
	// sees: lowerdir = @base : <configured-top> : ... : <team-layer>
	return append([]string{sandbox.BaseTemplateID}, filtered...)
}

// resolveBaseLayerChain returns the layer chain for sessions that run against
// the @base template when the admin has configured it via Configure Base
// Sandbox. The returned chain is ["@base", "<top_layer_id>"] where
// top_layer_id is the content-addressed delta produced by the last
// successful build.
//
// Returns nil if:
//   - The platform PGStore is not available (personal mode).
//   - @base has no top_layer_id (admin hasn't configured it yet; fresh install).
//   - @base's top_layer_id is the literal "@base" sentinel (fresh install).
//   - Any DB query error (fail-open: sessions still work with plain @base).
func resolveBaseLayerChain(ctx context.Context) []string {
	backend := getPlatformBackend()
	if backend == nil {
		return nil
	}
	tplStore := backend.SandboxTemplates()
	if tplStore == nil {
		return nil
	}
	return resolveBaseLayerChainWith(ctx, tplStore)
}

// resolveBaseLayerChainWith is the testable core of resolveBaseLayerChain.
func resolveBaseLayerChainWith(ctx context.Context, tplStore baseTopLayerResolver) []string {
	topLayerID, err := tplStore.GetBaseTopLayerID(ctx)
	if err != nil {
		slog.Debug("failed to resolve @base top_layer_id", "error", err)
		return nil
	}
	// Empty or literal sentinel → no configured delta exists.
	if topLayerID == "" || topLayerID == sandbox.BaseTemplateID {
		return nil
	}

	// Chain: seed-Job @base at the bottom, configured delta on top.
	return []string{sandbox.BaseTemplateID, topLayerID}
}

// resolveTemplateImage returns the per-template container image reference for
// backends that use per-template OCI images (e.g., OpenShell) instead of
// overlay layer chains.
//
// Returns "" if:
//   - The platform backend is not available (personal mode).
//   - The template doesn't exist in the DB.
//   - The template has no SandboxImage set (uses the layer chain approach).
//
// This function is separate from resolveTemplateLayerChain by design:
// both are called; the one that returns a non-empty value gets used,
// depending on the backend type.
func resolveTemplateImage(ctx context.Context, templateSlug string) string {
	backend := getPlatformBackend()
	if backend == nil {
		return ""
	}
	templates := backend.SandboxTemplates()
	if templates == nil {
		return ""
	}
	return resolveTemplateImageWith(ctx, templates, templateSlug)
}

// resolveTemplateImageWith is the testable core of resolveTemplateImage.
func resolveTemplateImageWith(ctx context.Context, templates store.SandboxTemplateStore, templateSlug string) string {
	// Derive the lookup parameters from the slug convention.
	teamSlug := strings.TrimPrefix(templateSlug, "team-")
	if teamSlug == templateSlug {
		return ""
	}

	tpl, err := templates.GetBySlug(ctx, store.SandboxTemplateScopeTeam, teamSlug, templateSlug)
	if err != nil || tpl == nil {
		return ""
	}

	if tpl.SandboxImage != nil && *tpl.SandboxImage != "" {
		return *tpl.SandboxImage
	}
	return ""
}

// resolveBaseImage returns the SandboxImage set on the @base template (if any).
// This is used when no team-specific template overrides the image. The admin
// can set a custom image on @base via the Platform Admin UI.
func resolveBaseImage(ctx context.Context) string {
	backend := getPlatformBackend()
	if backend == nil {
		return ""
	}
	templates := backend.SandboxTemplates()
	if templates == nil {
		return ""
	}
	return resolveBaseImageWith(ctx, templates)
}

// resolveBaseImageWith is the testable core of resolveBaseImage.
func resolveBaseImageWith(ctx context.Context, templates store.SandboxTemplateStore) string {
	// Look up the @base template (scope=global, slug="base", owner_id="").
	tpl, err := templates.GetBySlug(ctx, store.SandboxTemplateScopeGlobal, "", "base")
	if err != nil || tpl == nil {
		return ""
	}
	if tpl.SandboxImage != nil && *tpl.SandboxImage != "" {
		return *tpl.SandboxImage
	}
	return ""
}

// resolvePlatformDockerfileBody returns the Dockerfile body from the @base
// (platform) template. This is the platform admin's Layer 2 recipe that gets
// merged into every team build.
func resolvePlatformDockerfileBody(ctx context.Context) string {
	backend := getPlatformBackend()
	if backend == nil {
		return ""
	}
	templates := backend.SandboxTemplates()
	if templates == nil {
		return ""
	}
	return resolvePlatformDockerfileBodyWith(ctx, templates)
}

// resolvePlatformDockerfileBodyWith is the testable core of resolvePlatformDockerfileBody.
func resolvePlatformDockerfileBodyWith(ctx context.Context, templates store.SandboxTemplateStore) string {
	tpl, err := templates.GetBySlug(ctx, store.SandboxTemplateScopeGlobal, "", "base")
	if err != nil || tpl == nil {
		return ""
	}
	if tpl.DockerfileBody != nil {
		return *tpl.DockerfileBody
	}
	return ""
}
