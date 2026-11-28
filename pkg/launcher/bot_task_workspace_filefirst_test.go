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

type fileFirstTaskBackend struct {
	*taskWorkspaceFake
	sandbox.BotHomeFilesBackend
	reads     []string
	homeReads []string
	body      string
}

func (f *fileFirstTaskBackend) ReadBotHomeWorkspace(c context.Context, h *sandbox.BotHomeBinding, p string, n int64) ([]byte, error) {
	f.reads = append(f.reads, p)
	return f.taskWorkspaceFake.ReadBotHomeWorkspace(c, h, p, n)
}
func (f *fileFirstTaskBackend) OpenBotHomeFile(_ context.Context, _ *sandbox.BotHomeBinding, p string, _ store.BotFileRange) (*store.BotFileStream, error) {
	f.homeReads = append(f.homeReads, p)
	return &store.BotFileStream{ReadCloser: io.NopCloser(strings.NewReader(f.body))}, nil
}
func TestBotTaskWorkspaceFileFirstReads(t *testing.T) {
	owner, id, task := uuid.New(), uuid.New(), uuid.New()
	ctx, _ := execution.WithPrincipal(t.Context(), execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: owner.String(), OrgSlug: "org", TeamSlug: "team", Authenticated: true})
	bots := homeManagerStore{b: store.Bot{ID: id, OwnerID: owner, Scope: store.BotScopeTeam, Home: store.BotHomeBinding{ID: uuid.New(), BotID: id, Scope: store.BotScopeTeam, Backend: "docker", StorageKey: "registered", Revision: 1}}}
	ref := store.BotTaskRef{TaskID: task, Revision: 1, Digest: strings.Repeat("a", 64)}
	file := sandbox.BotHomeWorkspaceFile{Path: "TASK.md", Data: []byte("retained definition")}
	other := sandbox.BotHomeWorkspaceFile{Path: "references/design.md", Data: []byte("large past design")}
	revision := store.BotTaskRevision{ID: uuid.New(), BotID: id, Ref: ref, Bundle: map[string][]byte{"TASK.md": []byte("stale duplicate")}}
	for _, f := range []sandbox.BotHomeWorkspaceFile{file, other} {
		revision.Manifest.Assets = append(revision.Manifest.Assets, store.BotTaskAsset{Path: f.Path, Digest: f.Digest(), SizeBytes: int64(len(f.Data))})
	}
	backend := &fileFirstTaskBackend{taskWorkspaceFake: &taskWorkspaceFake{snap: sandbox.BotHomeWorkspaceSnapshot{Files: []sandbox.BotHomeWorkspaceFile{file, other}}}, body: "actual ordinary home"}
	w := &BotTaskWorkspace{Backend: backend, Bots: bots, Tasks: taskWorkspaceRegistry{revision: revision}}
	loaded, err := w.Load(ctx, id, ref)
	if err != nil || string(loaded.Bundle["TASK.md"]) != string(file.Data) || len(loaded.Bundle) != 2 {
		t.Fatalf("retained hydration: %+v %v", loaded, err)
	}
	backend.reads = nil
	data, err := w.Read(ctx, id, "tasks/"+task.String()+"/TASK.md", 1024)
	if err != nil || string(data) != string(file.Data) || len(backend.reads) != 1 {
		t.Fatalf("explicit read fetched other assets: %q %v %v", data, err, backend.reads)
	}
	for _, root := range []string{"skills", "knowledge", "goals"} {
		data, err = w.Read(ctx, id, root+"/index.md", 1024)
		if err != nil || string(data) != backend.body {
			t.Fatalf("ordinary %s read: %q %v", root, data, err)
		}
	}
	if len(backend.homeReads) != 3 {
		t.Fatal("working files not read from ordinary Home")
	}
	for _, p := range []string{"knowledge/index.md", "skills/helper/SKILL.md"} {
		legacy := &BotTaskWorkspace{Backend: backend.taskWorkspaceFake, Bots: bots}
		if _, err = legacy.Read(ctx, id, p, 1024); !errors.Is(err, store.ErrBotUnsupported) {
			t.Fatalf("ordinary file fell back to private workspace: %v", err)
		}
	}
	for _, private := range []string{"receipts/private", "authority/private", ".receipts/private", "tasks/" + task.String() + "/.revisions/private/TASK.md", "tasks/" + task.String() + "/state/.authority"} {
		if _, err = w.Read(ctx, id, private, 1024); !errors.Is(err, store.ErrBotForbidden) {
			t.Fatalf("private path %q exposed: %v", private, err)
		}
	}
	if _, err = w.Read(ctx, id, "knowledge/.receipts/private", 1024); !errors.Is(err, store.ErrBotForbidden) {
		t.Fatal("private path exposed", err)
	}
	if _, err = w.Read(ctx, id, "knowledge/index.md", 1); !errors.Is(err, store.ErrBotInvalid) {
		t.Fatal("truncated read accepted", err)
	}
	if _, err = w.Load(ctx, uuid.New(), ref); err == nil {
		t.Fatal("cross bot accepted")
	}
	wrong := ref
	wrong.Revision++
	if _, err = w.Load(ctx, id, wrong); !errors.Is(err, store.ErrBotConflict) {
		t.Fatal("wrong revision accepted", err)
	}
	backend.snap.Files[0].Data = []byte("retained definItIon")
	if _, err = w.Load(ctx, id, ref); !errors.Is(err, store.ErrBotConflict) {
		t.Fatal("same-size digest mismatch accepted", err)
	}
	backend.snap.Files[0].Data = append(file.Data, '!')
	if _, err = w.Load(ctx, id, ref); !errors.Is(err, store.ErrBotConflict) {
		t.Fatal("size mismatch accepted", err)
	}
}
