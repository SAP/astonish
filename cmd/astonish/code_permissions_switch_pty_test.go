package astonish

import "testing"

func TestCodePermissionsEnterSwitchPTYAcceptance(t *testing.T) {
	f := newSidebarPTYFixture(t)
	term := f.start()
	defer term.close()
	term.wait("Enter send")
	term.send("/new\r")
	term.wait("Message Astonish", "Enter send")
	term.send("/permissions\r")
	term.wait("› Folders", "Ask outside project")
	term.send("\r")
	term.wait("All folders · session")
	term.send("\x1b[B")
	term.wait("› Tools", "Ask when needed")
	term.send("\r")
	term.wait("All tools · session")
	term.send("\r")
	term.wait("› Tools", "Ask when needed")
	term.send("\x1b[A\r")
	term.wait("› Folders", "Ask outside project")
	term.send("\x1b")
	term.wait("Folders ○", "Tools ○", "Enter send")
}
