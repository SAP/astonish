package astonish

import (
	"context"
	"testing"

	persistentsession "github.com/SAP/astonish/pkg/session"
	adksession "google.golang.org/adk/session"
)

func TestCodePermissionsDescendantPickerPTYAcceptance(t *testing.T) {
	f := newSidebarPTYFixture(t)
	store, err := persistentsession.NewFileStore(f.storePath)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.Create(context.Background(), &adksession.CreateRequest{
		AppName: "astonish_code", UserID: f.user,
		State: map[string]any{persistentsession.StateKeyParentID: f.sessionID},
	})
	if err != nil {
		t.Fatal(err)
	}
	term := f.start()
	defer term.close()
	term.wait("Enter send")
	term.send("/sessions\r")
	term.wait("Sessions", "enter open")
	term.send("\r")                             // root entry must reopen its active child, not the root ID
	term.wait("Message Astonish", "Enter send") // the active child has empty history
	for _, key := range []string{"\x10", "/permissions\r"} {
		term.send(key)
		term.wait("Permissions", "› Folders", "Ask outside project", "Ask when needed")
		term.send("\x1b[B")
		term.wait("› Tools", "Enter allows all tools for this session")
		term.send("\x1b[B")
		term.wait("› Plan access", "Choose full access when approving a plan")
		term.send("\x1b[A")
		term.wait("› Tools")
		term.send("\x1b[A")
		term.wait("› Folders")
		term.assertAbsent("Session changed")
		term.assertAbsent("Enter act")
		term.send("r")
		term.wait("› Folders", "esc close")
		term.send("\x1b")
		term.wait("Enter send")
	}
}
