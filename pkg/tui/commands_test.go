package tui

import (
	"testing"
)

func TestFilterSlashCommands_EmptyShowsAll(t *testing.T) {
	got := filterSlashCommands("")
	if len(got) != len(builtInSlashCommands) {
		t.Fatalf("got %d want %d", len(got), len(builtInSlashCommands))
	}
}

func TestFilterSlashCommands_Prefix(t *testing.T) {
	got := filterSlashCommands("se")
	// sessions, (maybe nothing else)
	if len(got) < 1 {
		t.Fatal("expected sessions")
	}
	found := false
	for _, c := range got {
		if c.Name == "sessions" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected sessions in %v", got)
	}
}

func TestFilterSlashCommands_Alias(t *testing.T) {
	got := filterSlashCommands("q")
	// exit has alias q; also nothing that starts with q alone except quit path
	found := false
	for _, c := range got {
		if c.Name == "exit" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected exit for q: %v", got)
	}
}

func TestParseSlashInput(t *testing.T) {
	ok, q := parseSlashInput("/hel")
	if !ok || q != "hel" {
		t.Fatalf("%v %q", ok, q)
	}
	ok, _ = parseSlashInput("hello")
	if ok {
		t.Fatal("not a slash")
	}
	ok, _ = parseSlashInput("/help\nmore")
	if ok {
		t.Fatal("multiline not slash complete")
	}
}
