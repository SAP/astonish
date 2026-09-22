// DestroySessionEverywhere — backend-agnostic session destruction helper.
//
// This replaces the Incus-only TryDestroySessionContainer by routing through
// the Backend interface (DestroySession). Works for both Incus and K8s backends.
// Idempotent: absent sessions succeed without error.
//
// Also provides PruneOrphansForBackend — a backend-neutral orphan reaper that
// replaces the Incus-only PruneOrphans for the K8s path.

package sandbox

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/SAP/astonish/pkg/config"
)

// DestroySessionEverywhere builds the configured backend from appCfg and
// destroys the session identified by sessionID. It is designed for best-effort
// call sites (session-delete handlers, daemon cleanup cycles, CLI commands).
//
// sessRegistry may be nil; when non-nil, the backend uses it for session
// lookup (critical in platform mode where sessions are stored in PostgreSQL
// rather than the pod-local JSON file). Callers in platform mode should pass
// a PG-backed registry built from the request's tenant context.
//
// Errors are returned for actionable failures (unreachable backend, context
// cancelled). A nil error means the session is gone (or never existed).
//
// Timeout: if ctx has no deadline, a 30-second deadline is imposed.
func DestroySessionEverywhere(ctx context.Context, appCfg *config.AppConfig, sessionID string, sessRegistry *SessionRegistry) error {
	if appCfg == nil {
		return fmt.Errorf("sandbox: DestroySessionEverywhere: nil app config")
	}
	if sessionID == "" {
		return nil // nothing to destroy
	}

	b, cleanup, err := BackendFromAppConfigWithSessions(appCfg, sessRegistry)
	if err != nil {
		return fmt.Errorf("sandbox: DestroySessionEverywhere: backend init: %w", err)
	}
	if cleanup != nil {
		defer cleanup()
	}

	// Impose a deadline if none exists.
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
	}

	return b.DestroySession(ctx, sessionID)
}

// TryDestroySession is a fire-and-forget wrapper around
// DestroySessionEverywhere that logs warnings on failure. Suitable for
// best-effort cleanup sites where the caller cannot propagate errors.
//
// sessRegistry may be nil (falls back to local file registry for personal mode).
func TryDestroySession(appCfg *config.AppConfig, sessionID string, sessRegistry *SessionRegistry) {
	if appCfg == nil || sessionID == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := DestroySessionEverywhere(ctx, appCfg, sessionID, sessRegistry); err != nil {
		slog.Warn("best-effort session destroy failed",
			"component", "sandbox",
			"session", sessionID,
			"error", err)
	}
}

// PruneOrphansForBackend is a backend-agnostic orphan reaper. It compares
// sandbox sessions tracked in the registry against the set of
// existingSessionIDs (which come from the chat/session store) and destroys
// any session that is:
//   - NOT in existingSessionIDs (the chat session has been deleted)
//   - NOT pinned (pinned sessions are exempt from automatic cleanup)
//
// It also performs a secondary pass using Backend.ListSessions to find pods/
// containers that exist in the backend tier but are absent from the registry
// (e.g., crash before registration). These are destroyed if older than 1h.
//
// Returns the number of sessions pruned and the first error encountered (if
// any; non-fatal errors are logged and skipped).
func PruneOrphansForBackend(ctx context.Context, b Backend, registry *SessionRegistry, existingSessionIDs map[string]bool) (int, error) {
	return PruneOrphansForBackendFiltered(ctx, b, registry, existingSessionIDs, nil)
}

// PruneOrphansForBackendFiltered is PruneOrphansForBackend with an optional
// team filter on the unregistered pass. A nil filter keeps the daemon reaper's
// current behavior. HTTP prune must pass the caller's org and team so a
// platform admin cannot delete another tenant's containers by accident.
func PruneOrphansForBackendFiltered(ctx context.Context, b Backend, registry *SessionRegistry, existingSessionIDs map[string]bool, filter *SessionFilter) (int, error) {
	entries := registry.List()
	pruned := 0

	for _, entry := range entries {
		if ctx.Err() != nil {
			return pruned, ctx.Err()
		}
		if existingSessionIDs[entry.SessionID] {
			continue // session still exists
		}
		if entry.Pinned {
			continue // manually created, exempt from cleanup
		}

		slog.Info("pruning orphaned sandbox session",
			"component", "sandbox",
			"session", safeShortID(entry.SessionID, 16),
			"backend", string(b.Kind()))

		if err := b.DestroySession(ctx, entry.SessionID); err != nil {
			slog.Warn("failed to destroy orphan session",
				"component", "sandbox",
				"session", entry.SessionID,
				"error", err)
			continue
		}
		pruned++
	}

	// Secondary pass: find backend-tier sessions not in the registry.
	// These can occur if a crash happened between pod creation and registry
	// write. For K8s this queries pods by label; for Incus it's handled by
	// the legacy PruneOrphans function (ListSessionContainers).
	listFilter := SessionFilter{}
	if filter != nil {
		listFilter = *filter
	}
	allSessions, err := b.ListSessions(ctx, listFilter)
	if err != nil {
		// Non-fatal: we already pruned registered orphans above.
		slog.Warn("PruneOrphansForBackend: ListSessions failed",
			"component", "sandbox",
			"error", err)
		return pruned, nil
	}

	registeredIDs := make(map[string]bool, len(entries))
	for _, e := range entries {
		registeredIDs[e.SessionID] = true
	}

	for _, s := range allSessions {
		if ctx.Err() != nil {
			return pruned, ctx.Err()
		}
		if registeredIDs[s.SessionID] {
			continue // already handled above
		}
		if existingSessionIDs[s.SessionID] {
			continue // session still alive in chat store
		}
		// Only prune unregistered sessions older than 1 hour to avoid
		// racing with in-flight creation.
		if time.Since(s.CreatedAt) < time.Hour {
			continue
		}

		slog.Info("pruning unregistered sandbox session",
			"component", "sandbox",
			"session", safeShortID(s.SessionID, 16),
			"backend", string(b.Kind()),
			"age", time.Since(s.CreatedAt).Round(time.Minute))

		if err := b.DestroySession(ctx, s.SessionID); err != nil {
			slog.Warn("failed to destroy unregistered session",
				"component", "sandbox",
				"session", s.SessionID,
				"error", err)
			continue
		}
		pruned++
	}

	return pruned, nil
}
