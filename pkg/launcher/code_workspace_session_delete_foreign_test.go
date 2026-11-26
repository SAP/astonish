package launcher

import (
	"context"
	persistentsession "github.com/SAP/astonish/pkg/session"
	adksession "google.golang.org/adk/session"
	"testing"
)

func TestCodeSessionDeletionRejectsForeignChildren(t *testing.T) {
	for _, foreignApp := range []bool{false, true} {
		t.Run(map[bool]string{false: "user", true: "app"}[foreignApp], func(t *testing.T) {
			b := newFileStoreBackend(t, t.TempDir(), "owner")
			id := seedSession(t, b, "Keep authorized parent")
			app, user := codeAppName, "foreign"
			if foreignApp {
				app, user = "other_app", "owner"
			}
			child, err := b.fileStore.Create(context.Background(), &adksession.CreateRequest{AppName: app, UserID: user, State: map[string]any{persistentsession.StateKeyParentID: id}})
			if err != nil {
				t.Fatal(err)
			}
			if err := b.DeleteSession(context.Background(), id); err == nil {
				t.Fatal("foreign child accepted")
			}
			for _, sid := range []string{id, child.Session.ID()} {
				if meta, err := b.fileStore.GetSessionMeta(sid); err != nil || meta == nil {
					t.Fatalf("ownership rejection deleted session: %s %v", sid, err)
				}
			}
		})
	}
}
