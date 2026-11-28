package astonish

import "testing"

func TestPlatformRestoreCleanupArguments(t *testing.T) {
	for _, args := range [][]string{{"archive"}, {"archive", "--dry-run"}, {"archive", "--confirm", "--reset-target"}, {"archive", "--confirm", "--map-org", "old:new"}} {
		if _, err := parsePlatformRestoreCleanup(args); err == nil {
			t.Fatalf("accepted unsafe cleanup flags %v", args)
		}
	}
	if _, err := parsePlatformRestoreCleanup([]string{"archive", "--confirm"}); err != nil {
		t.Fatal(err)
	}
	if err := handlePlatformRestoreCommand([]string{"cleanup", "archive"}); err == nil {
		t.Fatal("cleanup dispatch did not require confirmation")
	}
}
