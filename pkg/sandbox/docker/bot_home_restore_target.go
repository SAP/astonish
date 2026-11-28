package docker

import (
	"context"
	"strings"

	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/google/uuid"
)

// Restore generations retain logical scope while selecting new physical storage.
// No arbitrary volume alias is accepted: the scope hash and canonical UUID are
// required, in addition to the existing volume ownership/driver validation.
func validBotHomeVolume(b *sandbox.BotHomeBinding) bool {
	if b == nil {
		return false
	}
	base := botHomeVolume(b.Scope)
	if b.Ref == base {
		return true
	}
	suffix, ok := strings.CutPrefix(b.Ref, base+"-restore-")
	if !ok {
		return false
	}
	id, err := uuid.Parse(suffix)
	return err == nil && id != uuid.Nil && id.String() == suffix
}
func (db *DockerBackend) CreateBotHomeRestoreTarget(ctx context.Context, scope sandbox.BotHomeScope) (*sandbox.BotHomeBinding, error) {
	return db.CreateBotHomeRestoreTargetJournaled(ctx, scope, func(*sandbox.BotHomeBinding) error { return nil })
}

// CreateBotHomeRestoreTargetJournaled records the exact generation before Docker
// can allocate it, including ambiguous transport failures.
func (db *DockerBackend) CreateBotHomeRestoreTargetJournaled(ctx context.Context, scope sandbox.BotHomeScope, reserve func(*sandbox.BotHomeBinding) error) (*sandbox.BotHomeBinding, error) {
	if err := scope.Validate(); err != nil {
		return nil, err
	}
	b := &sandbox.BotHomeBinding{Scope: scope, Backend: sandbox.BackendKindDocker, Ref: botHomeVolume(scope) + "-restore-" + uuid.NewString()}
	if err := reserve(b); err != nil {
		return nil, err
	}
	// Unpredictable generation identity plus backend restore's empty-volume check
	// means this never adopts either source storage or an interrupted target.
	_, err := runDocker(ctx, db.cfg.ContainerRuntimePath, "volume", "create", "--driver=local", "--label="+botHomeScopeLabel+"="+scope.Key(), "--label="+botHomeVersionLabel+"=1", b.Ref)
	if err != nil {
		return nil, err
	}
	if err = db.validateBotHome(ctx, b); err != nil {
		return nil, err
	}
	return b, nil
}
