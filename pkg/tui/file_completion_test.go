package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseFileMentionInput(t *testing.T) {
	tests := []struct {
		name  string
		value string
		ok    bool
		query string
	}{
		{name: "empty mention", value: "read @", ok: true, query: ""},
		{name: "path query", value: "summarize @pkg/tui", ok: true, query: "pkg/tui"},
		{name: "not trailing token", value: "email me@example.com", ok: false},
		{name: "slash command unaffected", value: "/help", ok: false},
		{name: "multiline disabled", value: "hello\n@pkg", ok: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ok, query := parseFileMentionInput(tt.value)
			if ok != tt.ok || query != tt.query {
				t.Fatalf("parseFileMentionInput(%q) = (%v, %q), want (%v, %q)", tt.value, ok, query, tt.ok, tt.query)
			}
		})
	}
}

func TestReplaceActiveFileMention(t *testing.T) {
	got := replaceActiveFileMention("please read @pkg/tu", "pkg/tui/app.go")
	want := "please read @pkg/tui/app.go "
	if got != want {
		t.Fatalf("replaceActiveFileMention = %q, want %q", got, want)
	}
}

func TestListFileCandidatesSkipsIgnoredDirs(t *testing.T) {
	dir := t.TempDir()
	mustWrite := func(path, content string) {
		t.Helper()
		full := filepath.Join(dir, path)
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite("pkg/tui/app.go", "package tui")
	mustWrite("node_modules/pkg/index.js", "ignored")

	got := listFileCandidates(dir, "app")
	if len(got) != 1 || got[0].Path != "pkg/tui/app.go" {
		t.Fatalf("listFileCandidates = %#v", got)
	}
}

func TestExpandFileMentions(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "notes.md"), []byte("# Notes\nhello\n"), 0644); err != nil {
		t.Fatal(err)
	}

	got, err := expandFileMentions("summarize @notes.md", dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"summarize @notes.md", "<context from @file mentions>", "File: notes.md", "# Notes"} {
		if !strings.Contains(got, want) {
			t.Fatalf("expanded message missing %q:\n%s", want, got)
		}
	}
}

func TestReadMentionFileRejectsEscapes(t *testing.T) {
	_, err := readMentionFile(t.TempDir(), "../secret.txt")
	if err == nil {
		t.Fatal("expected escape path error")
	}
}
