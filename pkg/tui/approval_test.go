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
