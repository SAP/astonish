package astonish

import "testing"

func TestNodeInternalAdmissionArguments(t *testing.T) {
	for _, args := range [][]string{{"--internal-home-admission"}, {"--internal-home-admission", "unknown"}, {"--internal-home-admission", "wait", "extra"}} {
		if err := handleNodeCommand(args); err == nil {
			t.Fatalf("accepted %q", args)
		}
	}
}
