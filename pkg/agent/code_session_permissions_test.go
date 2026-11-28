package agent

import "testing"

func TestSetAllToolsForSession(t *testing.T) {
	p := &SessionAuthPolicy{}
	p.SetAllToolsForSession(true)
	if !p.AllToolsAllowedForSession() {
		t.Fatal("not enabled")
	}
	p.SetAllToolsForSession(false)
	if p.AllToolsAllowedForSession() || p.CodeSessionFolders().AllFolders {
		t.Fatal("not disabled independently")
	}
}
