package docker

import (
	"context"
	"github.com/SAP/astonish/pkg/sandbox"
)

var _ sandbox.BotHomeWorkspaceArchiveBackend = (*DockerBackend)(nil)

func (db *DockerBackend) RestoreBotHomeWorkspace(c context.Context, b *sandbox.BotHomeBinding, snapshot *sandbox.BotHomeWorkspaceSnapshot) error {
	if err := sandbox.ValidateLegacyWorkspaceSnapshot(snapshot); err != nil {
		return err
	}
	if b == nil || b.Validate() != nil {
		return sandbox.ErrBotHomeUnsupported
	}
	return db.workspace(c, b, map[string]any{"op": "restore", "snapshot": snapshot}, nil)
}
