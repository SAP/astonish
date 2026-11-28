package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

// Disk-backed transport for the HTTP recovery contract. This intentionally does
// not claim Docker infrastructure evidence; public archive/SQL codecs are real,
// and destination files have no dependency on the source adapter or directory.
type skillRecoveryFiles struct {
	root string
	mu   sync.Mutex
	// Only this adapter's journaled destinations may be physically removed.
	targets map[string]sandbox.BotHomeBinding
}

var _ sandbox.BotHomeFilesBackend = (*skillRecoveryFiles)(nil)
var _ sandbox.BotHomeDeleteBackend = (*skillRecoveryFiles)(nil)

func (b *skillRecoveryFiles) CreateBotHomeRestoreTarget(ctx context.Context, s sandbox.BotHomeScope) (*sandbox.BotHomeBinding, error) {
	return b.CreateBotHomeRestoreTargetJournaled(ctx, s, func(*sandbox.BotHomeBinding) error { return nil })
}

func (b *skillRecoveryFiles) CreateBotHomeRestoreTargetJournaled(ctx context.Context, s sandbox.BotHomeScope, reserve func(*sandbox.BotHomeBinding) error) (*sandbox.BotHomeBinding, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	h := sandbox.BotHomeBinding{Scope: s, Backend: sandbox.BackendKindDocker, Ref: uuid.NewString()}
	if err := h.Validate(); err != nil {
		return nil, err
	}
	if reserve == nil {
		return nil, store.ErrBotInvalid
	}
	candidate := h
	if err := reserve(&candidate); err != nil {
		return nil, err
	}
	if candidate != h {
		return nil, store.ErrBotConflict
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.targets == nil {
		b.targets = make(map[string]sandbox.BotHomeBinding)
	}
	if err := os.Mkdir(filepath.Join(b.root, h.Ref), 0700); err != nil {
		return nil, err
	}
	b.targets[h.Ref] = h
	return &h, nil
}

func (b *skillRecoveryFiles) DeleteBotHome(ctx context.Context, h *sandbox.BotHomeBinding) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if h == nil || h.Validate() != nil || filepath.Base(h.Ref) != h.Ref {
		return store.ErrBotForbidden
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if target, ok := b.targets[h.Ref]; !ok || target != *h {
		return store.ErrBotForbidden
	}
	// Keep the ownership record so retrying exact cleanup is idempotent.
	return os.RemoveAll(filepath.Join(b.root, h.Ref))
}

// These unrelated ordinary-Home operations are deliberately unsupported, not
// promoted methods on a nil embedded interface that would panic if invoked.
func (*skillRecoveryFiles) ListBotHomeFiles(context.Context, *sandbox.BotHomeBinding, string, string, int) (*store.BotFilePage, error) {
	return nil, store.ErrBotUnsupported
}
func (*skillRecoveryFiles) StatBotHomeFile(context.Context, *sandbox.BotHomeBinding, string) (*store.BotFileEntry, error) {
	return nil, store.ErrBotUnsupported
}
func (*skillRecoveryFiles) OpenBotHomeFile(context.Context, *sandbox.BotHomeBinding, string, store.BotFileRange) (*store.BotFileStream, error) {
	return nil, store.ErrBotUnsupported
}
func (*skillRecoveryFiles) WriteBotHomeFile(context.Context, *sandbox.BotHomeBinding, store.BotFileWrite) (*store.BotFileEntry, error) {
	return nil, store.ErrBotUnsupported
}
func (*skillRecoveryFiles) PublishBotArtifactSnapshot(context.Context, *sandbox.BotHomeBinding, string, string, func(func() error) error) (*store.BotContentRef, error) {
	return nil, store.ErrBotUnsupported
}
func TestSkillRecoveryFilesJournaledAllocationAndExactCleanup(t *testing.T) {
	ctx := context.Background()
	b := &skillRecoveryFiles{root: t.TempDir()}
	scope := sandbox.BotHomeScope{OrgID: "recovery-org", TeamID: "recovery-team", UserID: uuid.NewString(), BotID: uuid.NewString()}
	failure := errors.New("journal write failed")
	var rejected sandbox.BotHomeBinding
	if _, err := b.CreateBotHomeRestoreTargetJournaled(ctx, scope, func(h *sandbox.BotHomeBinding) error {
		rejected = *h
		return failure
	}); !errors.Is(err, failure) {
		t.Fatalf("reservation failure: %v", err)
	}
	if _, err := os.Stat(filepath.Join(b.root, rejected.Ref)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed reservation allocated storage: %v", err)
	}
	if _, err := b.CreateBotHomeRestoreTargetJournaled(ctx, scope, nil); !errors.Is(err, store.ErrBotInvalid) {
		t.Fatalf("nil reservation accepted: %v", err)
	}
	var reserved sandbox.BotHomeBinding
	calls := 0
	home, err := b.CreateBotHomeRestoreTargetJournaled(ctx, scope, func(h *sandbox.BotHomeBinding) error {
		calls++
		reserved = *h
		if _, err := os.Stat(filepath.Join(b.root, h.Ref)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("allocation preceded reservation: %v", err)
		}
		return nil
	})
	if err != nil || home == nil || *home != reserved || calls != 1 || home.Ref == rejected.Ref {
		t.Fatalf("unstable journaled allocation: %+v calls=%d err=%v", home, calls, err)
	}
	other, err := b.CreateBotHomeRestoreTarget(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*sandbox.BotHomeBinding){
		func(h *sandbox.BotHomeBinding) { h.Scope.OrgID = "other-org" },
		func(h *sandbox.BotHomeBinding) { h.Scope.TeamID = "other-team" },
		func(h *sandbox.BotHomeBinding) { h.Scope.UserID = uuid.NewString() },
		func(h *sandbox.BotHomeBinding) { h.Scope.BotID = uuid.NewString() },
		func(h *sandbox.BotHomeBinding) { h.Ref = ".." },
		func(h *sandbox.BotHomeBinding) { h.Ref = uuid.NewString() },
		func(h *sandbox.BotHomeBinding) { h.Backend = sandbox.BackendKind("wrong-backend") },
	} {
		wrong := *home
		change(&wrong)
		if err := b.DeleteBotHome(ctx, &wrong); !errors.Is(err, store.ErrBotForbidden) {
			t.Fatalf("inexact cleanup accepted: %+v %v", wrong, err)
		}
	}
	if _, err := os.Stat(filepath.Join(b.root, home.Ref)); err != nil {
		t.Fatalf("inexact cleanup damaged target: %v", err)
	}
	for i := 0; i < 2; i++ {
		if err := b.DeleteBotHome(ctx, home); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(filepath.Join(b.root, home.Ref)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("cleanup left target: %v", err)
	}
	if _, err := os.Stat(filepath.Join(b.root, other.Ref)); err != nil {
		t.Fatalf("cleanup damaged other home: %v", err)
	}
}

func (b *skillRecoveryFiles) file(h *sandbox.BotHomeBinding, p string) (string, error) {
	if h == nil || h.Validate() != nil || filepath.Base(h.Ref) != h.Ref || sandbox.ValidateBotHomeArtifactPath(p) != nil {
		return "", store.ErrBotForbidden
	}
	return filepath.Join(b.root, h.Ref, p), nil
}
func (b *skillRecoveryFiles) PutBotContent(ctx context.Context, h *sandbox.BotHomeBinding, k store.BotContentKind, size int64, r io.Reader, rev int64, barrier func(func() error) error) (*store.BotContentRef, error) {
	ref := &store.BotContentRef{Format: store.BotContentFormatVersion, Scope: store.BotContentScope{OrgID: h.Scope.OrgID, TeamID: h.Scope.TeamID, OwnerID: h.Scope.UserID, BotID: uuid.MustParse(h.Scope.BotID)}, Kind: k, Revision: rev, SizeBytes: size, Key: fmt.Sprintf("content-v1/%s-%d-%s", k, rev, strings.ReplaceAll(uuid.NewString(), "-", ""))}
	p, e := b.file(h, ref.Key)
	if e != nil {
		return nil, e
	}
	e = barrier(func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
			return err
		}
		f, err := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		hash := sha256.New()
		n, err := io.Copy(io.MultiWriter(f, hash), r)
		err = errors.Join(err, f.Close())
		if err != nil {
			return err
		}
		if n != size {
			return store.ErrBotInvalid
		}
		ref.SHA256 = hex.EncodeToString(hash.Sum(nil))
		return nil
	})
	return ref, e
}
func (b *skillRecoveryFiles) OpenBotContent(ctx context.Context, h *sandbox.BotHomeBinding, ref store.BotContentRef, span store.BotFileRange) (*store.BotFileStream, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	want := store.BotContentScope{OrgID: h.Scope.OrgID, TeamID: h.Scope.TeamID, OwnerID: h.Scope.UserID, BotID: uuid.MustParse(h.Scope.BotID)}
	if ref.Scope != want || ref.Validate() != nil {
		return nil, store.ErrBotForbidden
	}
	p, e := b.file(h, ref.Key)
	if e != nil {
		return nil, e
	}
	f, e := os.Open(p)
	if e != nil {
		return nil, e
	}
	hash := sha256.New()
	n, e := io.Copy(hash, f)
	if e != nil || n != ref.SizeBytes || hex.EncodeToString(hash.Sum(nil)) != ref.SHA256 {
		_ = f.Close()
		return nil, store.ErrBotInvalid
	}
	if _, e = f.Seek(span.Offset, io.SeekStart); e != nil {
		_ = f.Close()
		return nil, e
	}
	return &store.BotFileStream{ReadCloser: f, Entry: store.BotFileEntry{Path: ref.Key, Kind: store.BotFileRegular, SizeBytes: n, SHA256: ref.SHA256}}, nil
}
func (b *skillRecoveryFiles) ExportBotHomeArchive(ctx context.Context, h *sandbox.BotHomeBinding, w io.Writer, barrier sandbox.BotHomeArchiveBarrier) (*sandbox.BotHomeArchiveManifest, error) {
	m := &sandbox.BotHomeArchiveManifest{Version: 1, Binding: h.Scope.Key()}
	e := barrier(ctx, h, func(ctx context.Context) error {
		root := filepath.Join(b.root, h.Ref)
		home, err := os.OpenRoot(root)
		if err != nil {
			return err
		}
		defer home.Close()
		return filepath.WalkDir(root, func(p string, d os.DirEntry, e error) error {
			if e != nil {
				return e
			}
			if d.IsDir() {
				if p != root {
					rel, _ := filepath.Rel(root, p)
					m.Entries = append(m.Entries, sandbox.BotHomeArchiveEntry{Root: sandbox.BotHomeArchivePrivate, Path: filepath.ToSlash(rel), Directory: true, Mode: 0700})
				}
				return nil
			}
			if !d.Type().IsRegular() {
				return store.ErrBotInvalid
			}
			rel, e := filepath.Rel(root, p)
			if e != nil {
				return e
			}
			rel = filepath.ToSlash(rel)
			f, e := home.Open(rel)
			if e != nil {
				return e
			}
			hash := sha256.New()
			n, e := io.Copy(io.MultiWriter(w, hash), f)
			e = errors.Join(e, f.Close())
			if e != nil {
				return e
			}
			kind := sandbox.BotHomeArchiveHome
			if strings.HasPrefix(rel, "content-v1/") {
				kind = sandbox.BotHomeArchivePrivate
			}
			m.Entries = append(m.Entries, sandbox.BotHomeArchiveEntry{Root: kind, Path: rel, Size: n, SHA256: hex.EncodeToString(hash.Sum(nil)), Mode: 0600})
			return ctx.Err()
		})
	})
	return m, e
}
func (b *skillRecoveryFiles) RestoreBotHomeArchive(ctx context.Context, h *sandbox.BotHomeBinding, m *sandbox.BotHomeArchiveManifest, r io.Reader, barrier sandbox.BotHomeArchiveBarrier) error {
	if m.Binding != h.Scope.Key() {
		return store.ErrBotForbidden
	}
	return barrier(ctx, h, func(ctx context.Context) error {
		for _, entry := range m.Entries {
			p, e := b.file(h, entry.Path)
			if e != nil {
				return e
			}
			if entry.Directory {
				if e = os.MkdirAll(p, 0700); e != nil {
					return e
				}
				continue
			}
			if e = os.MkdirAll(filepath.Dir(p), 0700); e != nil {
				return e
			}
			f, e := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			if e != nil {
				return e
			}
			hash := sha256.New()
			n, e := io.CopyN(io.MultiWriter(f, hash), r, entry.Size)
			e = errors.Join(e, f.Close())
			if e != nil {
				return e
			}
			if n != entry.Size || hex.EncodeToString(hash.Sum(nil)) != entry.SHA256 {
				return store.ErrBotInvalid
			}
		}
		return ctx.Err()
	})
}
