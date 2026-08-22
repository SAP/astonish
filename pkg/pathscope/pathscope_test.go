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

		// --- Filesystem commands: paths ARE extracted ---
		{"absolute root via ls", "ls /", []string{"/"}},
		{"absolute etc via cat", "cat /etc/passwd", []string{"/etc/passwd"}},
		{"home tilde via cat", "cat ~/Downloads/secret", []string{"~/Downloads/secret"}},
		{"bare tilde via ls", "ls ~", []string{"~"}},
		{"parent escape via cat", "cat ../secrets.txt", []string{"../secrets.txt"}},
		{"flags dropped", "ls -la /var/log", []string{"/var/log"}},
		{"long flags dropped", "head --lines=10 /etc/hosts", []string{"/etc/hosts"}},
		{"multiple distinct via cp", "cp /a /b", []string{"/a", "/b"}},
		{"dedupe via cp", "cp /a /a", []string{"/a"}},
		{"mid-token escape via cat", "cat foo/../../etc/passwd", []string{"foo/../../etc/passwd"}},
		{"quoted absolute via cat", `cat "/etc/passwd"`, []string{"/etc/passwd"}},
		{"single-quoted home via cat", "cat '~/x'", []string{"~/x"}},
		{"opt equals value in filesystem cmd", "tar --file=../y .", []string{"../y"}},

		// --- Non-filesystem commands: paths are NOT extracted ---
		{"git push with absolute ref", "git push origin /refs/heads/main", nil},
		{"curl with URL path", "curl https://example.com/api/path", nil},
		{"docker run with volume", "docker run -v /host:/container image", nil},
		{"go test with relative path", "go test ./pkg/tools -run TestFoo", nil},
		{"npm install absolute", "npm install /some/local/package", nil},
		{"echo with absolute path", "echo /etc/passwd", nil},
		{"python with import", "python -c 'import /something'", nil},
		{"grep with absolute path", "grep pattern /var/log/syslog", nil},
		{"git commit with slash in message", `git commit -m "fixes A / B"`, nil},
		{"in-tree bare names not flagged", "go build pkg/agent main.go", nil},
		{"dot-slash in-tree not flagged", "go run ./local.txt", nil},
		{"slash compounds in prose are not commands", "delegate_tasks Shoelace/Lit-based HTML/CSS/SVG APIs/add-ins documentation/specs", nil},
		{"glob prose is not a command path", "delegate_tasks inspect src/**/*.tsx", nil},

		// --- Pipelines: each segment analyzed independently ---
		{"pipe: cat is filesystem, base64 is not", "cat /etc/passwd | base64", []string{"/etc/passwd"}},
		{"and-chain: both cd and cat are filesystem", "cd /root && cat /root/x", []string{"/root", "/root/x"}},

		// --- Redirect targets: always treated as filesystem paths ---
		{"redirect target", "echo hi > /tmp/out.txt", []string{"/tmp/out.txt"}},
		{"redirect from non-fs command", "git log > /tmp/log.txt", []string{"/tmp/log.txt"}},
		{"input redirect", "sort < /etc/hosts", []string{"/etc/hosts"}},

		// --- Command prefixes (sudo, env, etc.) ---
		{"sudo wraps filesystem cmd", "sudo cat /etc/shadow", []string{"/etc/shadow"}},
		{"env prefix before filesystem cmd", "env FOO=bar cat /etc/hosts", []string{"/etc/hosts"}},
		{"sudo wraps non-fs cmd", "sudo docker run /image", nil},
		{"env assignment value with filesystem cmd", "OUT=/etc/x cp /a /b", []string{"/a", "/b"}},

		// --- Quoted literal data: NOT mis-flagged ---
		{"quoted commit message with slash", `git commit -m "fixes A / B"`, nil},
		{"quoted message parens tilde dotdot", `git commit -m "see notes (x) about ~/foo and ../bar"`, nil},
		{"quoted message with pipe and semicolon", `git commit -m "a | b ; c"`, nil},
		{"single-quoted literal slash", `echo 'a / b'`, nil},
		{"quoted flag-like value", `foo --msg "-- not a flag / path"`, nil},
		{"quoted message with equals", `git commit -m "x = y / z"`, nil},

		// --- Security: genuine paths in filesystem commands still flagged ---
		{"unquoted absolute among quoted text", `sh -c "echo hi" && cat /etc/passwd`, []string{"/etc/passwd"}},
		{"quoted absolute in filesystem cmd", `cat "/etc/shadow"`, []string{"/etc/shadow"}},
		{"quoted path with spaces in filesystem cmd", `cat "/var/log/system log"`, []string{"/var/log/system log"}},
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

func TestNormalizePathInRoot(t *testing.T) {
	root := t.TempDir()
	normalizedRoot := NormalizePath(root)

	// Relative path resolved against root stays within root.
	rel := "pkg/tools/internal.go"
	got := NormalizePathInRoot(rel, normalizedRoot)
	if !PathWithin(normalizedRoot, got) {
		t.Errorf("NormalizePathInRoot(%q, %q) = %q, expected inside root", rel, normalizedRoot, got)
	}
	expected := filepath.Join(normalizedRoot, rel)
	if got != expected {
		t.Errorf("NormalizePathInRoot(%q, %q) = %q, want %q", rel, normalizedRoot, got, expected)
	}

	// Relative escape resolves outside root.
	escape := "../outside.txt"
	got = NormalizePathInRoot(escape, normalizedRoot)
	if PathWithin(normalizedRoot, got) {
		t.Errorf("NormalizePathInRoot(%q, %q) = %q, expected OUTSIDE root", escape, normalizedRoot, got)
	}

	// Absolute path is resolved (with symlinks) regardless of root.
	abs := "/etc/passwd"
	got = NormalizePathInRoot(abs, normalizedRoot)
	// On macOS /etc → /private/etc, so just check it's still outside root.
	if PathWithin(normalizedRoot, got) {
		t.Errorf("NormalizePathInRoot(%q, %q) = %q, expected OUTSIDE root", abs, normalizedRoot, got)
	}
	if got == "" {
		t.Errorf("NormalizePathInRoot(%q, %q) should not return empty", abs, normalizedRoot)
	}

	// Empty root falls back to CWD-based resolution.
	got = NormalizePathInRoot(rel, "")
	if got == "" {
		t.Error("NormalizePathInRoot with empty root should not return empty")
	}

	// Empty path returns empty.
	if NormalizePathInRoot("", normalizedRoot) != "" {
		t.Error("empty path should normalize to empty")
	}

	// Home path expansion works.
	home, err := os.UserHomeDir()
	if err == nil {
		got = NormalizePathInRoot("~/x", normalizedRoot)
		want := filepath.Join(home, "x")
		if got != want {
			t.Errorf("NormalizePathInRoot(~/x, root) = %q, want %q", got, want)
		}
	}
}

func TestIsAlwaysAllowedPath(t *testing.T) {
	cases := []struct {
		name string
		path string
		want bool
	}{
		{"dev null absolute", "/dev/null", true},
		{"dev null with trailing dot segment", "/dev/./null", true},
		{"empty", "", false},
		{"project file", "/tmp/project/main.go", false},
		{"other dev device", "/dev/zero", false},
		{"home file", "~/secret.txt", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsAlwaysAllowedPath(tc.path); got != tc.want {
				t.Errorf("IsAlwaysAllowedPath(%q) = %v, want %v", tc.path, got, tc.want)
			}
		})
	}
}
