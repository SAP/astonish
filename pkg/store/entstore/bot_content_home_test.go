package entstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

type contentHomeBackend struct {
	sandbox.BotHomeFilesBackend // No repository or provisioning capability.
	objects                     map[string][]byte
	corrupt                     bool
}

func (b *contentHomeBackend) PutBotContent(ctx context.Context, home *sandbox.BotHomeBinding, kind store.BotContentKind, size int64, body io.Reader, revision int64, guard func(func() error) error) (*store.BotContentRef, error) {
	data, err := io.ReadAll(body)
	if err != nil {
		return nil, err
	}
	hash := sha256.Sum256(data)
	ref := &store.BotContentRef{Format: store.BotContentFormatVersion, Scope: store.BotContentScope{OrgID: home.Scope.OrgID, TeamID: home.Scope.TeamID, OwnerID: home.Scope.UserID, BotID: uuid.MustParse(home.Scope.BotID)}, Kind: kind, Key: "content-v1/" + uuid.NewString(), SHA256: hex.EncodeToString(hash[:]), SizeBytes: size, Revision: revision}
	if err = guard(func() error { b.objects[ref.Key] = data; return nil }); err != nil {
		return nil, err
	}
	return ref, nil
}
func (b *contentHomeBackend) OpenBotContent(_ context.Context, _ *sandbox.BotHomeBinding, ref store.BotContentRef, _ store.BotFileRange) (*store.BotFileStream, error) {
	data, ok := b.objects[ref.Key]
	if !ok {
		return nil, os.ErrNotExist
	}
	if b.corrupt {
		data = []byte("corruption")
	}
	return &store.BotFileStream{ReadCloser: io.NopCloser(bytes.NewReader(data))}, nil
}
func contentHomeFixture(t *testing.T, scope store.BotScope) (*botHomeContent, *contentHomeBackend, store.BotContentRef) {
	t.Helper()
	id, owner := uuid.New(), uuid.New()
	staged := store.BotContentScope{OrgID: "org", TeamID: "team", OwnerID: owner.String(), BotID: id}
	if scope == store.BotScopePersonal {
		staged.TeamID = personalSchemaName(owner.String())
	}
	disk, err := newBotDiskContent(t.TempDir(), staged)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = disk.Close() })
	ref, err := disk.Put(t.Context(), store.BotContentDocument, 7, 3, strings.NewReader("abc"), contentHomeCommit)
	if err != nil {
		t.Fatal(err)
	}
	bot := store.Bot{ID: id, OwnerID: owner, Scope: scope, Home: store.BotHomeBinding{ID: uuid.New(), BotID: id, Scope: scope, Backend: "docker", StorageKey: "opaque-home", Revision: 1}}
	home := sandbox.BotHomeBinding{Scope: sandbox.BotHomeScope{OrgID: "org", TeamID: "team", UserID: owner.String(), BotID: id.String()}, Backend: sandbox.BackendKind("docker"), Ref: "opaque-home"}
	backend := &contentHomeBackend{objects: map[string][]byte{}}
	adapter, err := newBotHomeContent(disk, staged, bot, home, backend, contentHomeCommit)
	if err != nil {
		t.Fatal(err)
	}
	return adapter, backend, *ref
}
func contentHomeCommit(f func() error) error { return f() }
func contentHomeRead(t *testing.T, storage store.BotContentStaging, ref store.BotContentRef) string {
	t.Helper()
	stream, err := storage.Open(t.Context(), ref, store.BotFileRange{})
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(stream)
	if err = errors.Join(err, stream.Close()); err != nil {
		t.Fatal(err)
	}
	return string(data)
}
func TestBotContentHomeRoutingAndSelection(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			adapter, _, staged := contentHomeFixture(t, scope)
			if got := contentHomeRead(t, adapter.staging, staged); got != "abc" {
				t.Fatal(got)
			}
			next, err := adapter.bindStaged(t.Context(), staged, contentHomeCommit)
			if err != nil {
				t.Fatal(err)
			}
			// Only physical scope/key change. Logical bytes/digest/revision stay stable.
			if next.SHA256 != staged.SHA256 || next.Revision != staged.Revision || next.Kind != staged.Kind || next.SizeBytes != staged.SizeBytes {
				t.Fatal("logical identity changed")
			}
			if next.Scope.TeamID != "team" {
				t.Fatal("retained scope was fabricated", next.Scope)
			}
			if scope == store.BotScopePersonal && next.Scope == staged.Scope {
				t.Fatal("personal namespaces conflated")
			}
			for _, ref := range []store.BotContentRef{staged, *next} {
				if got := contentHomeRead(t, adapter, ref); got != "abc" {
					t.Fatal(got)
				}
			}
			later, err := adapter.Put(t.Context(), store.BotContentDocument, 8, 3, strings.NewReader("new"), contentHomeCommit)
			if err != nil || !strings.HasPrefix(later.Key, "content-v1/") || contentHomeRead(t, adapter, *later) != "new" {
				t.Fatal("post-selection write", later, err)
			}
		})
	}
}
func TestBotContentHomeMissingAndCrossScopeFailClosed(t *testing.T) {
	adapter, backend, ref := contentHomeFixture(t, store.BotScopePersonal)
	next, err := adapter.bindStaged(t.Context(), ref, contentHomeCommit)
	if err != nil {
		t.Fatal(err)
	}
	delete(backend.objects, next.Key)
	if _, err = adapter.Open(t.Context(), *next, store.BotFileRange{}); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("missing Home fell back to staging", err)
	}
	wrong := *next
	wrong.Scope = ref.Scope
	if _, err = adapter.Open(t.Context(), wrong, store.BotFileRange{}); !errors.Is(err, store.ErrBotForbidden) {
		t.Fatal("namespace conflation", err)
	}
	wrong = ref
	wrong.Scope.BotID = uuid.New()
	if _, err = adapter.Open(t.Context(), wrong, store.BotFileRange{}); !errors.Is(err, store.ErrBotForbidden) {
		t.Fatal("foreign staging", err)
	}
	if err = adapter.staging.Remove(t.Context(), ref, contentHomeCommit); err != nil {
		t.Fatal(err)
	}
	if _, err = adapter.bindStaged(t.Context(), ref, contentHomeCommit); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("missing stage", err)
	}
}
func TestBotContentHomeRollbackRetainsStaging(t *testing.T) {
	adapter, backend, ref := contentHomeFixture(t, store.BotScopeTeam)
	rollback := errors.New("transaction rollback")
	// A failed SQL selection can leave a verified orphan in Home, never remove
	// the previously selected staging ref. No SQL migration is simulated here.
	next, err := adapter.bindStaged(t.Context(), ref, contentHomeCommit)
	if err != nil || next == nil {
		t.Fatal(err)
	}
	adapter.guard = func(f func() error) error {
		if err := f(); err != nil {
			return err
		}
		return rollback
	}
	if _, err = adapter.bindStaged(t.Context(), ref, contentHomeCommit); !errors.Is(err, rollback) {
		t.Fatal(err)
	}
	if contentHomeRead(t, adapter.staging, ref) != "abc" {
		t.Fatal("staging lost")
	}
	backend.corrupt = true
	adapter.guard = contentHomeCommit
	if _, err = adapter.bindStaged(t.Context(), ref, contentHomeCommit); !errors.Is(err, store.ErrBotConflict) {
		t.Fatal("corrupt destination selected", err)
	}
	if contentHomeRead(t, adapter.staging, ref) != "abc" {
		t.Fatal("staging lost on corruption")
	}
}
func TestBotContentHomeTemplatesUnaffected(t *testing.T) {
	disk, err := newBotDiskContent(t.TempDir(), store.BotContentScope{OrgID: "org", TeamID: "team"})
	if err != nil {
		t.Fatal(err)
	}
	defer disk.Close()
	ref, err := disk.Put(t.Context(), store.BotContentTemplate, 1, 3, strings.NewReader("abc"), contentHomeCommit)
	if err != nil {
		t.Fatal(err)
	}
	adapter, _, _ := contentHomeFixture(t, store.BotScopeTeam)
	if _, err = adapter.bindStaged(t.Context(), *ref, contentHomeCommit); !errors.Is(err, store.ErrBotForbidden) {
		t.Fatal("template adopted", err)
	}
	if got := contentHomeRead(t, disk, *ref); got != "abc" {
		t.Fatal(got)
	}
}
func TestBotContentHomeRejectsUnverifiedOwnership(t *testing.T) {
	adapter, backend, _ := contentHomeFixture(t, store.BotScopePersonal)
	bot := store.Bot{ID: adapter.homeScope.BotID, OwnerID: uuid.MustParse(adapter.homeScope.OwnerID), Scope: store.BotScopePersonal}
	bot.Home = store.BotHomeBinding{ID: uuid.New(), BotID: bot.ID, Scope: bot.Scope, Backend: string(adapter.binding.Backend), StorageKey: adapter.binding.Ref, Revision: 1}
	for _, mode := range []string{"owner", "org", "home", "namespace", "guard"} {
		t.Run(mode, func(t *testing.T) {
			staged, binding, current, guard := adapter.stagedScope, adapter.binding, bot, contentHomeCommit
			switch mode {
			case "owner":
				binding.Scope.UserID = uuid.NewString()
			case "org":
				binding.Scope.OrgID = "foreign"
			case "home":
				current.Home.StorageKey = "different"
			case "namespace":
				staged.TeamID = binding.Scope.TeamID
			case "guard":
				guard = nil
			}
			if _, err := newBotHomeContent(adapter.staging, staged, current, binding, backend, guard); err == nil {
				t.Fatal("unverified scope accepted")
			}
		})
	}
}

func TestBotContentHomeGuardBounded(t *testing.T) {
	for _, mode := range []string{"skip", "twice", "suppressed", "late", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			calls := 0
			var saved func() error
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			guard := func(f func() error) error {
				switch mode {
				case "skip":
					return nil
				case "twice":
					_ = f()
					_ = f()
					return nil
				case "suppressed":
					_ = f()
					return nil
				case "late":
					saved = f
					return nil
				case "cancel":
					cancel()
					return f()
				}
				return nil
			}
			err := botContentCommitGuard(ctx, guard, func() error {
				calls++
				if mode == "suppressed" {
					return store.ErrBotConflict
				}
				return nil
			})
			if err == nil {
				t.Fatal("invalid guard accepted")
			}
			if saved != nil && saved() == nil {
				t.Fatal("escaped callback committed")
			}
			if calls > 1 || mode == "late" && calls != 0 || mode == "cancel" && calls != 0 {
				t.Fatal("unbounded commit", calls)
			}
		})
	}
}
