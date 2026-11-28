package launcher

import (
	"context"
	"fmt"

	"github.com/SAP/astonish/pkg/tui/backend"
)

func (b *localAgentBackend) SetCodeSessionPermission(ctx context.Context, expected string, scope backend.CodePermissionScope, allow bool) (backend.CodePermissionsSnapshot, error) {
	if scope != backend.CodePermissionSessionAllFolders && scope != backend.CodePermissionSessionAllTools {
		return backend.CodePermissionsSnapshot{}, fmt.Errorf("unknown session permission %q", scope)
	}
	b.mu.Lock()
	id, override := b.sessionID, b.autoApprove
	b.mu.Unlock()
	if id != expected {
		return backend.CodePermissionsSnapshot{}, fmt.Errorf("Code session changed; reopen permissions")
	}
	if override || b.result == nil || b.result.ChatAgent == nil {
		return backend.CodePermissionsSnapshot{}, fmt.Errorf("session permissions cannot be changed with this backend or CLI override")
	}
	if id == "" {
		var err error
		id, _, err = b.ensureSession(ctx)
		if err != nil {
			return backend.CodePermissionsSnapshot{}, err
		}
	}
	if err := b.hydrateCodeFolders(ctx, id); err != nil {
		return backend.CodePermissionsSnapshot{}, err
	}
	b.mu.Lock()
	if b.sessionID != id {
		b.mu.Unlock()
		return backend.CodePermissionsSnapshot{}, fmt.Errorf("Code session changed; reopen permissions")
	}
	p := b.result.ChatAgent.GetOrCreateAuthPolicy(id)
	if scope == backend.CodePermissionSessionAllFolders {
		if err := p.SetAllFoldersForSession(allow); err != nil {
			b.mu.Unlock()
			return backend.CodePermissionsSnapshot{}, err
		}
	} else {
		p.SetAllToolsForSession(allow)
	}
	b.mu.Unlock()
	return b.CodePermissions(ctx)
}

func (b *lazyCodeBackend) SetCodeSessionPermission(ctx context.Context, id string, scope backend.CodePermissionScope, allow bool) (backend.CodePermissionsSnapshot, error) {
	if inner, ok := b.inner.(backend.CodeSessionPermissionsBackend); ok {
		return inner.SetCodeSessionPermission(ctx, id, scope, allow)
	}
	return backend.CodePermissionsSnapshot{}, fmt.Errorf("Code backend not opened")
}
