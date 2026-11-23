package launcher

import (
	"context"
	"github.com/SAP/astonish/pkg/tui/backend"
	"testing"
)

func TestCodeSessionPermissionSwitches(t *testing.T) {
	ctx := context.Background()
	dir, work := t.TempDir(), t.TempDir()
	b, _ := codePlanAcceptanceBackend(t, dir, work, "")
	b.NewSession()
	s, err := b.SetCodeSessionPermission(ctx, "", backend.CodePermissionSessionAllFolders, true)
	if err != nil || s.SessionID == "" || !s.SessionAllFolders || s.AllToolsSession {
		t.Fatalf("new folder switch: %+v %v", s, err)
	}
	id := s.SessionID
	s, err = b.SetCodeSessionPermission(ctx, id, backend.CodePermissionSessionAllTools, true)
	if err != nil || !s.AllToolsSession || !s.SessionAllFolders {
		t.Fatalf("tools switch: %+v %v", s, err)
	}
	s, err = b.SetCodeSessionPermission(ctx, id, backend.CodePermissionSessionAllTools, false)
	if err != nil || s.AllToolsSession || !s.SessionAllFolders {
		t.Fatalf("tools revoke: %+v %v", s, err)
	}
	if _, err = b.SetCodeSessionPermission(ctx, "wrong", backend.CodePermissionSessionAllFolders, false); err == nil {
		t.Fatal("stale setter accepted")
	}
	reopened, _ := codePlanAcceptanceBackend(t, dir, work, id)
	s, err = reopened.CodePermissions(ctx)
	if err != nil || !s.SessionAllFolders || s.AllToolsSession {
		t.Fatalf("restore: %+v %v", s, err)
	}
	s, err = reopened.SetCodeSessionPermission(ctx, id, backend.CodePermissionSessionAllFolders, false)
	if err != nil || s.SessionAllFolders {
		t.Fatalf("folder revoke: %+v %v", s, err)
	}
}
