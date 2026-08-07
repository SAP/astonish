package pathscope

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExtractCommandPaths(t *testing.T) {
	cases := []struct {
		name    string
		command string
		want    []string
	}{
		{"empty", "", nil},
		{"whitespace", "   ", nil},
		{"in-tree bare names not flagged", "ls pkg/agent main.go", nil},
		{"absolute root", "ls /", []string{"/"}},
		{"absolute etc", "cat /etc/passwd", []string{"/etc/passwd"}},
		{"home tilde", "cat ~/Downloads/secret", []string{"~/Downloads/secret"}},
		{"bare tilde", "ls ~", []string{"~"}},
		{"parent escape", "cat ../secrets.txt", []string{"../secrets.txt"}},
		{"dot-slash in-tree not flagged", "cat ./local.txt", nil},
		{"flags dropped", "ls -la /var/log", []string{"/var/log"}},
		{"long flags dropped", "grep --color /etc/hosts", []string{"/etc/hosts"}},
		{"pipe splits", "cat /etc/passwd | base64", []string{"/etc/passwd"}},
		{"redirect target", "echo hi > /tmp/out.txt", []string{"/tmp/out.txt"}},
		{"and-chain", "cd /root && cat /root/x", []string{"/root", "/root/x"}},
		{"quoted absolute", "cat \"/etc/passwd\"", []string{"/etc/passwd"}},
		{"single-quoted home", "cat '~/x'", []string{"~/x"}},
		{"env assignment value", "OUT=/etc/x cmd", []string{"/etc/x"}},
		{"opt equals value", "tool --file=../y", []string{"../y"}},
		{"mid-token escape", "cat foo/../../etc/passwd", []string{"foo/../../etc/passwd"}},
		{"dedupe", "diff /a /a", []string{"/a"}},
		{"multiple distinct", "cp /a /b", []string{"/a", "/b"}},

		// --- Quote-aware tokenization: quoted LITERAL DATA must NOT be
		// mis-flagged just because it contains /, ~, .., (), |, ; etc. This is
		// the regression suite for the false-positive folder-access prompts on
		// commands like `git commit -m "...A / B..."`.
		{"quoted commit message with slash", `git commit -m "fixes A / B"`, nil},
		{"quoted message parens tilde dotdot", `git commit -m "see notes (x) about ~/foo and ../bar"`, nil},
		{"quoted message with pipe and semicolon", `git commit -m "a | b ; c"`, nil},
		{"single-quoted literal slash", `echo 'a / b'`, nil},
		{"quoted flag-like value", `foo --msg "-- not a flag / path"`, nil},
		{"quoted message with equals", `git commit -m "x = y / z"`, nil},

		// --- Still flag genuine path operands, quoted or not (security must
		// not weaken).
		{"unquoted absolute among quoted text", `sh -c "echo hi" && cat /etc/passwd`, []string{"/etc/passwd"}},
		{"quoted absolute among quoted message", `git commit -m "note" && cat "/etc/shadow"`, []string{"/etc/shadow"}},
		{"quoted path with spaces", `cat "/var/log/system log"`, []string{"/var/log/system log"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ExtractCommandPaths(c.command)
			if len(got) != len(c.want) {
				t.Fatalf("ExtractCommandPaths(%q) = %v, want %v", c.command, got, c.want)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Errorf("ExtractCommandPaths(%q)[%d] = %q, want %q", c.command, i, got[i], c.want[i])
				}
			}
		})
	}
}

func TestPathWithin(t *testing.T) {
	root := "/home/user/project"
	cases := []struct {
		name      string
		candidate string
		want      bool
	}{
		{"root itself", root, true},
		{"file in root", filepath.Join(root, "main.go"), true},
		{"nested", filepath.Join(root, "pkg", "x.go"), true},
		{"sibling", "/home/user/other", false},
		{"parent", "/home/user", false},
		{"absolute elsewhere", "/etc/passwd", false},
		{"empty candidate", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := PathWithin(root, c.candidate); got != c.want {
				t.Errorf("PathWithin(%q, %q) = %v, want %v", root, c.candidate, got, c.want)
			}
		})
	}
	if PathWithin("", "/anything") {
		t.Error("empty root should never contain a path")
	}
}

func TestNormalizePath(t *testing.T) {
	root := t.TempDir()
	// A not-yet-existing file inside root should still normalize under root.
	nested := filepath.Join(root, "sub", "new.txt")
	got := NormalizePath(nested)
	if !PathWithin(NormalizePath(root), got) {
		t.Errorf("NormalizePath(%q) = %q, expected inside %q", nested, got, root)
	}
	if NormalizePath("") != "" {
		t.Error("empty path should normalize to empty")
	}
}

func TestExpandHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir")
	}
	if got := ExpandHome("~"); got != home {
		t.Errorf("ExpandHome(~) = %q, want %q", got, home)
	}
	if got := ExpandHome("~/x"); got != filepath.Join(home, "x") {
		t.Errorf("ExpandHome(~/x) = %q, want %q", got, filepath.Join(home, "x"))
	}
	if got := ExpandHome("/abs"); got != "/abs" {
		t.Errorf("ExpandHome(/abs) should be unchanged, got %q", got)
	}
}

func TestDirOf(t *testing.T) {
	root := t.TempDir()
	f := filepath.Join(root, "file.txt")
	if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := DirOf(f); got != NormalizePath(root) {
		t.Errorf("DirOf(file) = %q, want %q", got, NormalizePath(root))
	}
	if got := DirOf(root); got != NormalizePath(root) {
		t.Errorf("DirOf(dir) = %q, want %q (dir itself)", got, NormalizePath(root))
	}
}
