package launcher

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

type filesManagerBackend struct {
	sandbox.BotHomeFilesBackend
	ensures, reads, writes int
	binding                sandbox.BotHomeBinding
	stream                 *filesManagerStream
	beforeStage            func()
}

func (b *filesManagerBackend) EnsureBotHome(context.Context, sandbox.BotHomeScope) (*sandbox.BotHomeBinding, error) {
	b.ensures++
	return nil, errors.New("must not allocate")
}
func (b *filesManagerBackend) ListBotHomeFiles(_ context.Context, h *sandbox.BotHomeBinding, _ string, _ string, limit int) (*store.BotFilePage, error) {
	b.reads++
	b.binding = *h
	if limit != store.BotFilePageDefault {
		return nil, errors.New("default page missing")
	}
	return &store.BotFilePage{}, nil
}
func (b *filesManagerBackend) StatBotHomeFile(_ context.Context, h *sandbox.BotHomeBinding, p string) (*store.BotFileEntry, error) {
	b.reads++
	b.binding = *h
	return &store.BotFileEntry{Path: p}, nil
}
func (b *filesManagerBackend) OpenBotHomeFile(_ context.Context, h *sandbox.BotHomeBinding, p string, _ store.BotFileRange) (*store.BotFileStream, error) {
	b.reads++
	b.binding = *h
	return &store.BotFileStream{ReadCloser: b.stream, Entry: store.BotFileEntry{Path: p}}, nil
}
func (b *filesManagerBackend) OpenBotContent(ctx context.Context, h *sandbox.BotHomeBinding, r store.BotContentRef, rr store.BotFileRange) (*store.BotFileStream, error) {
	return b.OpenBotHomeFile(ctx, h, r.Key, rr)
}
func (b *filesManagerBackend) WriteBotHomeFile(_ context.Context, _ *sandbox.BotHomeBinding, w store.BotFileWrite) (*store.BotFileEntry, error) {
	if b.beforeStage != nil {
		b.beforeStage()
	}
	err := w.BeforeCommit(func() error { b.writes++; return nil })
	return &store.BotFileEntry{Path: w.Path}, err
}

func (b *filesManagerBackend) PutBotContent(_ context.Context, h *sandbox.BotHomeBinding, kind store.BotContentKind, size int64, _ io.Reader, revision int64, fence func(func() error) error) (*store.BotContentRef, error) {
	if b.beforeStage != nil {
		b.beforeStage()
	}
	if err := fence(func() error { b.writes++; return nil }); err != nil {
		return nil, err
	}
	return &store.BotContentRef{Format: store.BotContentFormatVersion, Kind: kind, Scope: botHomeContentScope(h, uuid.MustParse(h.Scope.BotID)), Key: "objects/object", SHA256: strings.Repeat("a", 64), SizeBytes: size, Revision: revision}, nil
}
func (b *filesManagerBackend) PublishBotArtifactSnapshot(ctx context.Context, h *sandbox.BotHomeBinding, _, _ string, fence func(func() error) error) (*store.BotContentRef, error) {
	return b.PutBotContent(ctx, h, store.BotContentArtifact, 1, strings.NewReader("x"), 1, fence)
}

type filesManagerStream struct {
	io.Reader
	closed bool
}

func (s *filesManagerStream) Close() error { s.closed = true; return nil }
func filesManagerFixture(t *testing.T) (context.Context, *BotHomeManager, *filesManagerBackend, *homeManagerStore) {
	t.Helper()
	owner, id := uuid.New(), uuid.New()
	ctx, err := execution.WithPrincipal(t.Context(), execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: owner.String(), OrgSlug: "org", TeamSlug: "team", Authenticated: true})
	if err != nil {
		t.Fatal(err)
	}
	repo := &homeManagerStore{b: store.Bot{ID: id, OwnerID: owner, Scope: store.BotScopePersonal, Home: store.BotHomeBinding{ID: uuid.New(), BotID: id, Scope: store.BotScopePersonal, Backend: "docker", StorageKey: "registered", Revision: 1}}}
	backend := &filesManagerBackend{stream: &filesManagerStream{Reader: strings.NewReader("retained bytes")}}
	return ctx, &BotHomeManager{Bots: repo, Backend: backend, Scope: store.BotScopePersonal}, backend, repo
}
func TestBotHomeFilesAuthorizedTeamViewer(t *testing.T) {
	ctx, m, backend, repo := filesManagerFixture(t)
	repo.b.Scope, repo.b.Home.Scope, m.Scope = store.BotScopeTeam, store.BotScopeTeam, store.BotScopeTeam
	owner := repo.b.OwnerID
	ctx, err := execution.WithPrincipal(ctx, execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: uuid.NewString(), OrgSlug: "org", TeamSlug: "team", Authenticated: true})
	if err != nil {
		t.Fatal(err)
	}
	// Get is authorized by the scoped repository; the viewer is not the storage owner.
	if _, err = m.List(ctx, repo.b.ID, ".", "", 0); err != nil {
		t.Fatal(err)
	}
	if backend.binding.Scope.UserID != owner.String() {
		t.Fatal("viewer rebound storage ownership")
	}
	ref := store.BotContentRef{Format: store.BotContentFormatVersion, Scope: botHomeContentScope(&backend.binding, repo.b.ID), Kind: store.BotContentEvidence, Key: "objects/evidence", SHA256: strings.Repeat("a", 64), SizeBytes: 1, Revision: 1}
	if _, err := m.OpenContent(ctx, ref, store.BotFileRange{}); !errors.Is(err, store.ErrBotForbidden) {
		t.Fatal("viewer read private execution content", err)
	}
	m.AuthorizeFileCommit = func(_ context.Context, _ store.BotHomeBinding, commit func() error) error { return commit() }
	_, err = m.Write(ctx, repo.b.ID, store.BotFileWrite{Path: "x.md", Body: strings.NewReader("x"), SizeBytes: 1, CreateOnly: true})
	if !errors.Is(err, store.ErrBotForbidden) || backend.writes != 0 {
		t.Fatal("viewer gained write access", err)
	}
}

func TestBotHomeFilesReadExistingWithoutAllocation(t *testing.T) {
	ctx, m, b, repo := filesManagerFixture(t)
	if _, err := m.List(ctx, repo.b.ID, ".", "", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Stat(ctx, repo.b.ID, "report.md"); err != nil {
		t.Fatal(err)
	}
	stream, err := m.Open(ctx, repo.b.ID, "report.md", store.BotFileRange{})
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(stream)
	if err != nil || string(data) != "retained bytes" {
		t.Fatalf("%q %v", data, err)
	}
	if err := stream.Close(); err != nil || !b.stream.closed {
		t.Fatal("backend closer lost")
	}
	if b.ensures != 0 || b.reads != 3 || b.binding.Ref != repo.b.Home.StorageKey || b.binding.Scope.UserID != repo.b.OwnerID.String() {
		t.Fatalf("unexpected binding/IO: %+v", b)
	}
}
func TestBotHomeFilesAuthorization(t *testing.T) {
	for _, mode := range []string{"no-principal", "foreign-owner", "wrong-scope", "unset-scope", "missing-home", "mismatched-home", "wrong-id", "unsupported"} {
		t.Run(mode, func(t *testing.T) {
			ctx, m, b, repo := filesManagerFixture(t)
			id := repo.b.ID
			switch mode {
			case "no-principal":
				ctx = t.Context()
			case "foreign-owner":
				repo.b.OwnerID = uuid.New()
			case "wrong-scope":
				m.Scope = store.BotScopeTeam
			case "unset-scope":
				m.Scope = ""
			case "missing-home":
				repo.b.Home.ID = uuid.Nil
			case "mismatched-home":
				repo.b.Home.BotID = uuid.New()
			case "wrong-id":
				id = uuid.New()
			case "unsupported":
				m.Backend = homeManagerBackend{}
			}
			if _, err := m.List(ctx, id, ".", "", 0); err == nil {
				t.Fatal("unauthorized read")
			}
			if b.reads != 0 || b.ensures != 0 {
				t.Fatal("unauthorized backend call")
			}
		})
	}
}
func TestBotHomeFilesCommitFence(t *testing.T) {
	for _, mode := range []string{"allowed", "missing-authority", "revoked", "cancelled", "rebound", "caller-denied"} {
		t.Run(mode, func(t *testing.T) {
			ctx, m, b, repo := filesManagerFixture(t)
			ctx, cancel := context.WithCancel(ctx)
			defer cancel()
			m.AuthorizeFileCommit = func(_ context.Context, h store.BotHomeBinding, commit func() error) error {
				if h != repo.b.Home {
					return store.ErrBotConflict
				}
				if mode == "revoked" {
					return store.ErrBotConflict
				}
				return commit()
			}
			w := store.BotFileWrite{Path: "MEMORY.md", SizeBytes: 1, Body: strings.NewReader("x"), CreateOnly: true, BeforeCommit: func(commit func() error) error { return commit() }}
			switch mode {
			case "missing-authority":
				m.AuthorizeFileCommit = nil
			case "cancelled":
				b.beforeStage = cancel
			case "rebound":
				b.beforeStage = func() { repo.b.Home.Revision++ }
			case "caller-denied":
				w.BeforeCommit = func(func() error) error { return store.ErrBotForbidden }
			}
			_, err := m.Write(ctx, repo.b.ID, w)
			if (err == nil) != (mode == "allowed") {
				t.Fatalf("write %v", err)
			}
			if (b.writes == 1) != (mode == "allowed") {
				t.Fatalf("writes %d", b.writes)
			}
			if b.ensures != 0 {
				t.Fatal("allocated")
			}
		})
	}
}
func TestBotHomeFilesContentWritesRequireAuthority(t *testing.T) {
	ctx, m, b, repo := filesManagerFixture(t)
	operations := []func() error{
		func() error {
			_, err := m.PutContent(ctx, repo.b.ID, store.BotContentDocument, 1, strings.NewReader("x"), 1, nil)
			return err
		},
		func() error { _, err := m.PublishSnapshot(ctx, repo.b.ID, "report.md", "report.md", nil); return err },
	}
	for _, op := range operations {
		if err := op(); !errors.Is(err, store.ErrBotForbidden) {
			t.Fatalf("unguarded: %v", err)
		}
	}
	if b.writes != 0 {
		t.Fatal("unguarded write")
	}
	m.AuthorizeFileCommit = func(_ context.Context, _ store.BotHomeBinding, commit func() error) error { return commit() }
	for _, op := range operations {
		if err := op(); err != nil {
			t.Fatal(err)
		}
	}
	if b.writes != 2 || b.ensures != 0 {
		t.Fatal("unexpected writes/allocation")
	}
}

func TestBotHomeFilesContentScope(t *testing.T) {
	ctx, m, b, repo := filesManagerFixture(t)
	ref := store.BotContentRef{Format: store.BotContentFormatVersion, Kind: store.BotContentDocument, Scope: store.BotContentScope{OrgID: "org", TeamID: "team", OwnerID: repo.b.OwnerID.String(), BotID: repo.b.ID}, Key: "objects/document", SHA256: strings.Repeat("a", 64), SizeBytes: 14, Revision: 1}
	stream, err := m.OpenContent(ctx, ref, store.BotFileRange{})
	if err != nil {
		t.Fatal(err)
	}
	stream.Close()
	ref.Scope.TeamID = "foreign"
	if _, err := m.OpenContent(ctx, ref, store.BotFileRange{}); !errors.Is(err, store.ErrBotForbidden) {
		t.Fatalf("cross-scope %v", err)
	}
	if b.reads != 1 || b.ensures != 0 {
		t.Fatal("cross-scope backend call")
	}
}
