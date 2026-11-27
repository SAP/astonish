package launcher

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	persistentsession "github.com/SAP/astonish/pkg/session"
)

// Pending roots remain visible in /sessions even after their last index vanished.
func (b *localAgentBackend) pendingDeletionMetas(ctx context.Context) ([]persistentsession.SessionMeta, error) {
	entries, err := os.ReadDir(filepath.Join(b.workspace.manager.Root(), b.workspace.project.ID))
	if err != nil {
		return nil, err
	}
	var metas []persistentsession.SessionMeta
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, "delete-session-") || !strings.HasSuffix(name, ".json") {
			continue
		}
		if !entry.Type().IsRegular() {
			slog.DebugContext(ctx, "Skipping unsafe session deletion intent", "name", name)
			continue
		}
		id := strings.TrimSuffix(strings.TrimPrefix(name, "delete-session-"), ".json")
		if !codeDeletionComponent.MatchString(id) {
			slog.DebugContext(ctx, "Skipping invalid session deletion intent ID", "name", name)
			continue
		}
		intent, err := b.readDeletionIntent(ctx, id)
		if err != nil {
			// Discovery is best-effort per journal: stale storage paths and
			// malformed intents cannot hide unrelated durable sessions. Explicit
			// deletion still validates the exact journal and fails closed.
			slog.DebugContext(ctx, "Skipping invalid session deletion intent", "name", name, "error", err)
			continue
		}
		if intent != nil {
			metas = append(metas, intent.Metas[0])
		}
	}
	return metas, nil
}
