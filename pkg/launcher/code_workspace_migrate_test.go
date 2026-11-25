package launcher

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/SAP/astonish/pkg/gitutil"
	persistentsession "github.com/SAP/astonish/pkg/session"
	"github.com/SAP/astonish/pkg/tui/backend"
	adksession "google.golang.org/adk/session"
)

func TestCodeWorkspaceMoveExistingSession(t *testing.T) {
	dir := workspaceLaunchRepo(t)
	ctx := context.Background()
	b := newFileStoreBackend(t, t.TempDir(), codeUserIDForDir(dir))
	b.workingDir = dir
	created, err := b.fileStore.Create(ctx, &adksession.CreateRequest{AppName: codeAppName, UserID: b.userID})
	if err != nil {
		t.Fatal(err)
	}
	b.sessionID = created.Session.ID()
	b.checkpoints, _ = persistentsession.NewCheckpointStore(t.TempDir())
	manager, err := codeWorkspaceTestContext(ctx, dir, b)
	if err != nil {
		t.Fatal(err)
	}
	_ = manager
	oldID, oldUser := b.sessionID, b.effectiveUserID()
	oldFile := filepath.Join(dir, "move.txt")
	if err = os.WriteFile(oldFile, []byte("current work"), 0600); err != nil {
		t.Fatal(err)
	}
	if b.checkpoints != nil {
		b.checkpoints.BeginTurn(oldID, 1)
		if err = b.checkpoints.Capture(oldID, 1, oldFile); err != nil {
			t.Fatal(err)
		}
	}
	req, err := b.PrepareWorkspaceLaunch(ctx, backend.WorkspaceLaunchRequest{Mode: backend.WorkspaceIndependent, CopyMode: backend.WorkspaceCurrentWork, SessionID: oldID})
	if err != nil {
		t.Fatal(err)
	}
	if req.SessionID != oldID || req.WorkspaceID == "" || req.WorkingDir == dir {
		t.Fatalf("bad handoff %+v", req)
	}
	r, binding, err := b.workspace.manager.FindSession(ctx, b.workspace.project, oldID)
	if err != nil || r.ID != req.WorkspaceID || binding.UserID != oldUser {
		t.Fatalf("binding=%+v record=%+v err=%v", binding, r, err)
	}
	if got, err := os.ReadFile(filepath.Join(req.WorkingDir, "move.txt")); err != nil || string(got) != "current work" {
		t.Fatalf("copied=%q err=%v", got, err)
	}
	if b.checkpoints != nil && len(b.checkpoints.TurnsWithChanges(oldID)) != 0 {
		t.Fatal("old absolute checkpoints survived migration")
	}
}

// codeWorkspaceTestContext wires a directly-constructed test backend to the
// same manager context used by production launch without rebuilding its agent.
func codeWorkspaceTestContext(ctx context.Context, dir string, b *localAgentBackend) (*gitutil.CodeWorkspaceManager, error) {
	m, err := newLocalCodeWorkspaceManager()
	if err != nil {
		return nil, err
	}
	p, err := m.ResolveProject(ctx, dir)
	if err != nil {
		return nil, err
	}
	b.workspace = &codeWorkspaceContext{manager: m, project: p}
	return m, nil
}
