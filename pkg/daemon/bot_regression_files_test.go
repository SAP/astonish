package daemon

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

// regressionHomeBackend uses temporary retained files for HTTP workflow fixtures.
// It does not claim physical sandbox/remount coverage.
type regressionHomeBackend struct{ fileFirstHTTPBackend }

func newRegressionHomeBackend(t *testing.T) *regressionHomeBackend {
	t.Helper()
	return &regressionHomeBackend{fileFirstHTTPBackend{setupHomeFilesBackend: setupHomeFilesBackend{documentsRoot: t.TempDir()}}}
}

type regressionCancellationBackend struct {
	*regressionHomeBackend
	receipt *cancellationReceiptBackend
}

func (b *regressionCancellationBackend) VerifySessionDestroyed(ctx context.Context, id string) error {
	return b.receipt.VerifySessionDestroyed(ctx, id)
}

func testRetainedContentBackend(t *testing.T, db store.PlatformBackend) *regressionHomeBackend {
	t.Helper()
	backend := newRegressionHomeBackend(t)
	installer, ok := db.(interface {
		InstallBotContentHomeBackend(sandbox.BotHomeFilesBackend) error
	})
	if !ok {
		t.Fatal("fixture store lacks retained content installer")
	}
	if err := installer.InstallBotContentHomeBackend(backend); err != nil {
		t.Fatal(err)
	}
	return backend
}

func (s *regressionHomeBackend) PutBotContent(ctx context.Context, h *sandbox.BotHomeBinding, kind store.BotContentKind, size int64, body io.Reader, revision int64, barrier func(func() error) error) (*store.BotContentRef, error) {
	if barrier == nil {
		return nil, store.ErrBotForbidden
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(body)
	if err != nil {
		return nil, err
	}
	if int64(len(data)) != size {
		return nil, store.ErrBotInvalid
	}
	ref := &store.BotContentRef{Format: store.BotContentFormatVersion, Scope: store.BotContentScope{OrgID: h.Scope.OrgID, TeamID: h.Scope.TeamID, OwnerID: h.Scope.UserID, BotID: uuid.MustParse(h.Scope.BotID)}, Kind: kind, Key: "content-v1/" + uuid.NewString(), SHA256: fmt.Sprintf("%x", sha256.Sum256(data)), SizeBytes: size, Revision: revision}
	if err := ref.Validate(); err != nil {
		return nil, err
	}
	err = barrier(func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		path := filepath.Join(s.documentsRoot, h.Scope.BotID, ".snapshots", ref.Key)
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return err
		}
		return os.WriteFile(path, data, 0600)
	})
	if err != nil {
		return nil, err
	}
	return ref, nil
}
