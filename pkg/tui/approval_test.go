package tui

import "testing"

func TestPickYesNo(t *testing.T) {
	opts := []string{"Yes", "No"}
	if pickYes(opts) != "Yes" {
		t.Fatal(pickYes(opts))
	}
	if pickNo(opts) != "No" {
		t.Fatal(pickNo(opts))
	}
	if pickYes([]string{"Approve", "Deny"}) != "Approve" {
		t.Fatal("approve")
	}
	if pickNo([]string{"Approve", "Deny"}) != "Deny" {
		t.Fatal("deny")
	}
}

func TestRenderApprovalHintsFormatsKeysAndLabels(t *testing.T) {
	out := renderApprovalHints(DefaultTheme(), []approvalHint{{Keys: "enter/y", Label: "allow host"}, {Keys: "n/esc", Label: "deny"}})
	if plain := stripANSI(out); plain != "enter/y=allow host  ·  n/esc=deny" {
		t.Fatalf("plain hints = %q", plain)
	}
}
