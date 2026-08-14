package agent

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRequiresToolAuthorization(t *testing.T) {
	tests := []struct {
		name     string
		tool     string
		planMode bool
		want     bool
	}{
		{"safe tool normal mode", "read_file", false, false},
		{"safe tool plan mode", "read_file", true, false},
		{"nav tool normal mode", "code_definition", false, false},
		{"mutating tool normal mode", "write_file", false, true},
		{"shell normal mode", "shell_command", false, true},
		{"delegate normal mode", "delegate_tasks", false, true},
		{"memory_save normal mode", "memory_save", false, true},
		// In plan mode the plan-mode gate handles blocking, not this path.
		{"mutating tool plan mode routes to plan gate", "write_file", true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := RequiresToolAuthorization(tt.tool, tt.planMode); got != tt.want {
				t.Errorf("RequiresToolAuthorization(%q, %v) = %v, want %v", tt.tool, tt.planMode, got, tt.want)
			}
		})
	}
}

func TestSessionAuthPolicy_ToolGrants(t *testing.T) {
	p := NewSessionAuthPolicy(t.TempDir())

	if p.ToolAuthorized("write_file") {
		t.Fatal("no grant yet: write_file should not be authorized")
	}

	// Allow-once is consumed on first use.
	p.GrantToolOnce("write_file")
	if !p.ToolAuthorized("write_file") {
		t.Fatal("after GrantToolOnce, write_file should be authorized once")
	}
	if p.ToolAuthorized("write_file") {
		t.Fatal("once grant should be consumed after a single use")
	}

	// Allow-all-this-iteration authorizes any tool repeatedly.
	p.GrantAllToolsThisIteration()
	if !p.ToolAuthorized("write_file") || !p.ToolAuthorized("shell_command") {
		t.Fatal("allow-all should authorize any tool")
	}
	if !p.ToolAuthorized("shell_command") {
		t.Fatal("allow-all should not be consumed by a single use")
	}
}

func TestSessionAuthPolicy_ResetForNewTurn(t *testing.T) {
	p := NewSessionAuthPolicy(t.TempDir())
	p.GrantAllToolsThisIteration()
	p.GrantToolOnce("edit_file")

	p.ResetForNewTurn()

	if p.ToolAuthorized("shell_command") {
		t.Error("allow-all should be cleared after ResetForNewTurn")
	}
	if p.ToolAuthorized("edit_file") {
		t.Error("once tool grant should be cleared after ResetForNewTurn")
	}
}

// TestSessionAuthPolicy_AllToolsSessionSurvivesReset asserts the session-scoped
// "Always Allow" grant (GrantAllToolsSession) keeps authorizing tools across
// turn boundaries — the user opted out of tool-execution prompts and must not
// be re-asked on the next message. This is the fix for "Always Allow still
// asks again".
func TestSessionAuthPolicy_AllToolsSessionSurvivesReset(t *testing.T) {
	p := NewSessionAuthPolicy(t.TempDir())
	p.GrantAllToolsSession()

	if !p.ToolAuthorized("shell_command") || !p.ToolAuthorized("write_file") {
		t.Fatal("session all-tools grant should authorize any tool")
	}

	p.ResetForNewTurn()

	if !p.ToolAuthorized("shell_command") {
		t.Error("session all-tools grant must survive ResetForNewTurn (no re-prompt on next turn)")
	}
	if !p.ToolAuthorized("delegate_tasks") {
		t.Error("session all-tools grant must cover every not-whitelisted tool after reset")
	}
}

func TestSessionAuthPolicy_ResetPreservesPathOnce(t *testing.T) {
	root := t.TempDir()
	p := NewSessionAuthPolicy(root)
	outside := filepath.Join(t.TempDir(), "other.txt")

	p.GrantPathOnce(outside)
	p.ResetForNewTurn() // a "once" path grant must survive into the resumed turn

	if !p.PathAllowed(outside) {
		t.Fatal("path-once grant should survive ResetForNewTurn (consumed on use)")
	}
	if p.PathAllowed(outside) {
		t.Fatal("path-once grant should be consumed after a single access")
	}
}

func TestFolderContainment_InRoot(t *testing.T) {
	root := t.TempDir()
	p := NewSessionAuthPolicy(root)

	cases := []struct {
		name string
		path string
		want bool
	}{
		{"root itself", root, true},
		{"file in root", filepath.Join(root, "main.go"), true},
		{"nested subdir file", filepath.Join(root, "pkg", "agent", "x.go"), true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := p.PathAllowed(c.path); got != c.want {
				t.Errorf("PathAllowed(%q) = %v, want %v", c.path, got, c.want)
			}
		})
	}
}

func TestFolderContainment_OutsideRoot(t *testing.T) {
	root := t.TempDir()
	sibling := t.TempDir() // a different temp dir, guaranteed outside root
	p := NewSessionAuthPolicy(root)

	outside := filepath.Join(sibling, "secret.txt")
	if p.PathAllowed(outside) {
		t.Errorf("PathAllowed(%q) should be false (outside root)", outside)
	}

	// Parent-escape via ".." must not be allowed.
	escape := filepath.Join(root, "..", filepath.Base(sibling), "secret.txt")
	if p.PathAllowed(escape) {
		t.Errorf("PathAllowed(%q) should be false ('..' escape)", escape)
	}
}

func TestFolderContainment_SessionGrant(t *testing.T) {
	root := t.TempDir()
	sibling := t.TempDir()
	p := NewSessionAuthPolicy(root)

	outsideFile := filepath.Join(sibling, "notes", "a.txt")
	if p.PathAllowed(outsideFile) {
		t.Fatal("precondition: outside file should be denied before grant")
	}

	p.GrantPathForSession(sibling)
	if !p.PathAllowed(outsideFile) {
		t.Error("after session grant, files under the granted dir should be allowed")
	}
	// Session grant is not consumed.
	if !p.PathAllowed(filepath.Join(sibling, "other.txt")) {
		t.Error("session grant should persist across accesses")
	}
}

// TestFolderContainment_ExtraRoots verifies that directories passed as
// extraRoots to NewSessionAuthPolicy (e.g. Astonish's own state directory) are
// implicitly in-scope: their subtrees are allowed without any grant, and no
// grant is consumed on repeated access. This backs the fix that stops the agent
// from prompting when it writes session transcripts / PLAN.md outside the
// project root.
func TestFolderContainment_ExtraRoots(t *testing.T) {
	root := t.TempDir()
	stateDir := t.TempDir() // stands in for the astonish config/state dir
	p := NewSessionAuthPolicy(root, stateDir)

	planFile := filepath.Join(stateDir, "sessions", "code", "x.PLAN.md")
	if !p.PathAllowed(planFile) {
		t.Error("a file under an extra root should be allowed without a grant")
	}
	// Not consumed: allowed repeatedly.
	if !p.PathAllowed(planFile) {
		t.Error("extra-root allowance must persist (not a one-shot grant)")
	}
	// A truly unrelated outside path is still denied.
	unrelated := filepath.Join(t.TempDir(), "secret.txt")
	if p.PathAllowed(unrelated) {
		t.Error("paths outside root and all extra roots should still be denied")
	}
	// OutOfScopePaths must not flag paths under an extra root.
	if out := p.OutOfScopePaths(map[string]any{"path": planFile}); len(out) != 0 {
		t.Errorf("OutOfScopePaths should not flag extra-root path, got %v", out)
	}
}

func TestFolderContainment_SymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outsideDir := t.TempDir()
	outsideFile := filepath.Join(outsideDir, "target.txt")
	if err := os.WriteFile(outsideFile, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Create a symlink inside root that points outside root.
	link := filepath.Join(root, "escape")
	if err := os.Symlink(outsideDir, link); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}

	p := NewSessionAuthPolicy(root)
	viaLink := filepath.Join(link, "target.txt")
	if p.PathAllowed(viaLink) {
		t.Errorf("PathAllowed(%q) should be false: symlink escapes root", viaLink)
	}
}

func TestFolderContainment_HomeExpansion(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir")
	}
	// Root is home itself; ~/x should be inside.
	p := NewSessionAuthPolicy(home)
	if !p.PathAllowed("~/somefile.txt") {
		t.Error("~/somefile.txt should be inside root when root == home")
	}
}

func TestOutOfScopePaths(t *testing.T) {
	root := t.TempDir()
	sibling := t.TempDir()
	p := NewSessionAuthPolicy(root)

	args := map[string]any{
		"path":        filepath.Join(root, "in.go"),
		"working_dir": sibling, // outside
		"pattern":     "func",  // not a path arg
	}
	out := p.OutOfScopePaths(args)
	if len(out) != 1 {
		t.Fatalf("expected 1 out-of-scope path, got %d: %v", len(out), out)
	}
	if !pathWithin(sibling, out[0]) && out[0] != normalizePath(sibling) {
		t.Errorf("out-of-scope path %q should be the sibling working_dir", out[0])
	}

	// OutOfScopePaths must not consume grants.
	p.GrantPathOnce(sibling)
	if len(p.OutOfScopePaths(args)) != 0 {
		t.Error("after granting the path, it should not be reported out-of-scope")
	}
	if !p.PathAllowed(sibling) {
		t.Error("OutOfScopePaths must not consume the once grant")
	}
}

func TestOutOfScopePaths_SliceArg(t *testing.T) {
	root := t.TempDir()
	sibling := t.TempDir()
	p := NewSessionAuthPolicy(root)

	args := map[string]any{
		"paths": []any{
			filepath.Join(root, "ok.txt"),
			filepath.Join(sibling, "bad.txt"),
		},
	}
	out := p.OutOfScopePaths(args)
	if len(out) != 1 {
		t.Fatalf("expected 1 out-of-scope path from slice, got %d: %v", len(out), out)
	}
}

// TestOutOfScopePaths_CommandArg is the regression for the shell_command
// bypass: a command whose operands reference a location outside the project
// root must be flagged even though "command" is not a path arg.
func TestOutOfScopePaths_CommandArg(t *testing.T) {
	root := t.TempDir()
	p := NewSessionAuthPolicy(root)

	t.Run("absolute path outside root", func(t *testing.T) {
		out := p.OutOfScopePaths(map[string]any{"command": "cat /etc/passwd"})
		if len(out) != 1 {
			t.Fatalf("expected /etc/passwd flagged, got %v", out)
		}
	})

	t.Run("home path outside root", func(t *testing.T) {
		out := p.OutOfScopePaths(map[string]any{"command": "cat ~/Downloads/secret"})
		if len(out) != 1 {
			t.Fatalf("expected ~/Downloads/secret flagged, got %v", out)
		}
	})

	t.Run("parent escape", func(t *testing.T) {
		out := p.OutOfScopePaths(map[string]any{"command": "ls ../.."})
		if len(out) == 0 {
			t.Fatalf("expected parent escape flagged, got %v", out)
		}
	})

	t.Run("in-tree command not flagged", func(t *testing.T) {
		// A command that only references in-tree relative names must NOT prompt.
		out := p.OutOfScopePaths(map[string]any{
			"command":     "go test ./pkg/agent",
			"working_dir": root,
		})
		if len(out) != 0 {
			t.Fatalf("in-tree command should not be flagged, got %v", out)
		}
	})

	t.Run("in-root absolute path not flagged", func(t *testing.T) {
		out := p.OutOfScopePaths(map[string]any{
			"command": "cat " + filepath.Join(root, "main.go"),
		})
		if len(out) != 0 {
			t.Fatalf("path inside root should not be flagged, got %v", out)
		}
	})

	t.Run("session grant covers command path", func(t *testing.T) {
		sibling := t.TempDir()
		pp := NewSessionAuthPolicy(root)
		if len(pp.OutOfScopePaths(map[string]any{"command": "cat " + filepath.Join(sibling, "x")})) == 0 {
			t.Fatal("precondition: sibling path should be out of scope before grant")
		}
		pp.GrantPathForSession(sibling)
		if len(pp.OutOfScopePaths(map[string]any{"command": "cat " + filepath.Join(sibling, "x")})) != 0 {
			t.Fatal("after session grant, command path under granted dir should be allowed")
		}
	})
}

func TestSessionAuthPolicy_NoRootDisablesScoping(t *testing.T) {
	p := NewSessionAuthPolicy("")
	if !p.PathAllowed("/etc/passwd") {
		t.Error("with no root, folder scoping should be disabled (all allowed)")
	}
	if len(p.OutOfScopePaths(map[string]any{"path": "/etc/passwd"})) != 0 {
		t.Error("with no root, nothing is out of scope")
	}
}

func TestApplyAuthorizationDecision_Tool(t *testing.T) {
	cases := []struct {
		name        string
		response    string
		wantGranted bool
		wantAll     bool // whether allow-all-this-iteration was set
	}{
		{"allow label", OptAllowToolOnce, true, false},
		{"always-allow label", OptAllowAllTools, true, true},
		{"allow lowercase", "allow", true, false},
		{"always allow lowercase", "always allow", true, true},
		{"numeric 1", "1", true, false},
		{"numeric 2", "2", true, true},
		{"y", "y", true, false},
		{"deny label", OptDeny, false, false},
		{"n", "n", false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := NewSessionAuthPolicy(t.TempDir())
			p.SetPending(&PendingAuthorization{Kind: "tool", Tool: "write_file"})
			d := p.ApplyAuthorizationDecision(c.response)
			if d == nil {
				t.Fatal("expected a decision")
			}
			if d.Granted != c.wantGranted {
				t.Errorf("Granted = %v, want %v", d.Granted, c.wantGranted)
			}
			if p.Pending() != nil {
				t.Error("pending should be cleared after decision")
			}
			if c.wantGranted {
				// A granted decision should authorize the tool.
				if !p.ToolAuthorized("write_file") {
					t.Error("write_file should be authorized after grant")
				}
				if c.wantAll {
					// allow-all persists across tools.
					if !p.ToolAuthorized("shell_command") {
						t.Error("allow-all should authorize other tools too")
					}
				}
			}
		})
	}
}

func TestApplyAuthorizationDecision_Folder(t *testing.T) {
	root := t.TempDir()
	sibling := t.TempDir()
	outside := filepath.Join(sibling, "x.txt")

	t.Run("allow once", func(t *testing.T) {
		p := NewSessionAuthPolicy(root)
		p.SetPending(&PendingAuthorization{Kind: "folder", Tool: "read_file", Paths: []string{normalizePath(outside)}})
		d := p.ApplyAuthorizationDecision(OptAllowPathOnce)
		if !d.Granted {
			t.Fatal("expected granted")
		}
		if !p.PathAllowed(outside) {
			t.Error("path should be allowed after once grant")
		}
		if p.PathAllowed(outside) {
			t.Error("once grant should be consumed")
		}
	})

	t.Run("allow for session", func(t *testing.T) {
		p := NewSessionAuthPolicy(root)
		p.SetPending(&PendingAuthorization{Kind: "folder", Tool: "read_file", Paths: []string{normalizePath(outside)}})
		d := p.ApplyAuthorizationDecision(OptAllowPathSession)
		if !d.Granted {
			t.Fatal("expected granted")
		}
		if !p.PathAllowed(outside) || !p.PathAllowed(filepath.Join(sibling, "y.txt")) {
			t.Error("session grant should allow the whole directory, repeatedly")
		}
	})

	t.Run("deny", func(t *testing.T) {
		p := NewSessionAuthPolicy(root)
		p.SetPending(&PendingAuthorization{Kind: "folder", Tool: "read_file", Paths: []string{normalizePath(outside)}})
		d := p.ApplyAuthorizationDecision(OptDeny)
		if d.Granted {
			t.Error("expected denied")
		}
		if p.PathAllowed(outside) {
			t.Error("denied path should remain out of scope")
		}
	})
}

func TestApplyAuthorizationDecision_NoPending(t *testing.T) {
	p := NewSessionAuthPolicy(t.TempDir())
	if d := p.ApplyAuthorizationDecision("Allow once"); d != nil {
		t.Error("expected nil decision when nothing is pending")
	}
}

// TestApplyAuthorizationDecision_FolderGrantSubsumesTool asserts the
// double-prompt collapse: when a folder-access grant is applied for a tool that
// ALSO requires tool-execution authorization (shell_command), the grant must
// implicitly authorize that tool for the immediate retry so the user is not
// prompted a second time on the same call. See the folder-grant branch of
// ApplyAuthorizationDecision.
func TestApplyAuthorizationDecision_FolderGrantSubsumesTool(t *testing.T) {
	root := t.TempDir()
	sibling := t.TempDir()
	outside := filepath.Join(sibling, "x.txt")

	// Sanity: shell_command is a not-whitelisted tool that needs tool auth.
	if !RequiresToolAuthorization("shell_command", false) {
		t.Fatal("precondition: shell_command should require tool authorization")
	}

	t.Run("once grant subsumes tool", func(t *testing.T) {
		p := NewSessionAuthPolicy(root)
		p.SetPending(&PendingAuthorization{Kind: "folder", Tool: "shell_command", Paths: []string{normalizePath(outside)}})
		d := p.ApplyAuthorizationDecision(OptAllowPathOnce)
		if !d.Granted {
			t.Fatal("expected granted")
		}
		if !p.PathAllowed(outside) {
			t.Error("path should be allowed after folder once grant")
		}
		if !p.ToolAuthorized("shell_command") {
			t.Error("folder grant should subsume the tool grant for the retry (no second prompt)")
		}
	})

	t.Run("session grant subsumes tool", func(t *testing.T) {
		p := NewSessionAuthPolicy(root)
		p.SetPending(&PendingAuthorization{Kind: "folder", Tool: "shell_command", Paths: []string{normalizePath(outside)}})
		d := p.ApplyAuthorizationDecision(OptAllowPathSession)
		if !d.Granted {
			t.Fatal("expected granted")
		}
		if !p.ToolAuthorized("shell_command") {
			t.Error("session folder grant should subsume the tool grant for the retry")
		}
	})

	t.Run("deny does not grant tool", func(t *testing.T) {
		p := NewSessionAuthPolicy(root)
		p.SetPending(&PendingAuthorization{Kind: "folder", Tool: "shell_command", Paths: []string{normalizePath(outside)}})
		d := p.ApplyAuthorizationDecision(OptDeny)
		if d.Granted {
			t.Fatal("expected denied")
		}
		if p.ToolAuthorized("shell_command") {
			t.Error("denied folder grant must not authorize the tool")
		}
	})

	t.Run("safe tool not granted", func(t *testing.T) {
		// read_file is whitelisted (safe); it never needs a tool grant, so the
		// subsume logic must be a no-op — it must not leave a spurious grant.
		p := NewSessionAuthPolicy(root)
		p.SetPending(&PendingAuthorization{Kind: "folder", Tool: "read_file", Paths: []string{normalizePath(outside)}})
		d := p.ApplyAuthorizationDecision(OptAllowPathOnce)
		if !d.Granted {
			t.Fatal("expected granted")
		}
		if p.ToolAuthorized("read_file") {
			t.Error("safe tool should not receive a tool-once grant")
		}
	})
}

// TestConsumePathGrants_OnceIsNotAlways is the regression test for the bug where
// choosing "Allow" (once) for an out-of-project path silently behaved like
// "Always Allow": the one-shot grant was created but never consumed, so later
// accesses to the same path were allowed without prompting again.
//
// Scenario reproduced:
//  1. list /tmp → user picks "Always Allow" (session grant) → subsequent /tmp allowed.
//  2. list /   → user picks "Allow" (once)  → this access allowed, grant consumed.
//  3. list /tmp → still allowed (session grant persists).
//  4. list /   → must prompt AGAIN (the once-grant was consumed in step 2).
func TestConsumePathGrants_OnceIsNotAlways(t *testing.T) {
	root := t.TempDir()
	p := NewSessionAuthPolicy(root)

	tmp := normalizePath("/tmp")
	slash := normalizePath("/")
	tmpArgs := map[string]any{"path": "/tmp"}
	slashArgs := map[string]any{"path": "/"}

	// 1. "Always Allow" /tmp → session grant.
	p.SetPending(&PendingAuthorization{Kind: "folder", Tool: "shell_command", Paths: []string{tmp}})
	if d := p.ApplyAuthorizationDecision(OptAllowPathSession); d == nil || !d.Granted {
		t.Fatal("expected /tmp session grant")
	}
	if len(p.OutOfScopePaths(tmpArgs)) != 0 {
		t.Fatal("/tmp should be allowed after session grant")
	}

	// 2. "Allow" / (once) → grant, then the execution-time consume.
	p.SetPending(&PendingAuthorization{Kind: "folder", Tool: "shell_command", Paths: []string{slash}})
	if d := p.ApplyAuthorizationDecision(OptAllowPathOnce); d == nil || !d.Granted {
		t.Fatal("expected / once grant")
	}
	// The gate sees it allowed on the retry and consumes the one-shot grant.
	if len(p.OutOfScopePaths(slashArgs)) != 0 {
		t.Fatal("/ should be allowed on the granted retry")
	}
	p.ConsumePathGrants(slashArgs)

	// 3. /tmp still allowed (session grant persists).
	if len(p.OutOfScopePaths(tmpArgs)) != 0 {
		t.Error("/tmp should still be allowed via the session grant")
	}

	// 4. / must prompt again — the once-grant is gone.
	if out := p.OutOfScopePaths(slashArgs); len(out) == 0 {
		t.Error("/ should be out-of-scope again after the once-grant was consumed")
	}
}

// TestConsumePathGrants_InScopeNoop verifies ConsumePathGrants never disturbs
// paths inside the project root or a session grant (there is no once-grant to
// consume for them) and is a no-op when no grant exists.
func TestConsumePathGrants_InScopeNoop(t *testing.T) {
	root := t.TempDir()
	p := NewSessionAuthPolicy(root)

	inRoot := map[string]any{"path": filepath.Join(root, "main.go")}
	// No panic / no state change for in-root paths.
	p.ConsumePathGrants(inRoot)
	if len(p.OutOfScopePaths(inRoot)) != 0 {
		t.Error("in-root path must remain allowed")
	}

	// A session-granted directory is unaffected by ConsumePathGrants.
	sibling := t.TempDir()
	p.SetPending(&PendingAuthorization{Kind: "folder", Tool: "read_file", Paths: []string{normalizePath(sibling)}})
	p.ApplyAuthorizationDecision(OptAllowPathSession)
	sibArgs := map[string]any{"path": filepath.Join(sibling, "a.txt")}
	p.ConsumePathGrants(sibArgs)
	if len(p.OutOfScopePaths(sibArgs)) != 0 {
		t.Error("session-granted directory must stay allowed after ConsumePathGrants")
	}
}

// TestOutOfScopePaths_ShellCommandFalsePositives verifies that non-filesystem
// commands (git, curl, docker, go, npm, etc.) do NOT trigger out-of-scope
// detection, even when their arguments contain path-shaped tokens.
func TestOutOfScopePaths_ShellCommandFalsePositives(t *testing.T) {
	root := t.TempDir()
	p := NewSessionAuthPolicy(root)

	// Commands that should NOT produce out-of-scope paths (not filesystem commands).
	noFlag := []struct {
		name string
		args map[string]any
	}{
		{"git commit with slash", map[string]any{"command": `git commit -m "fix A / B"`}},
		{"go test with path", map[string]any{"command": "go test ./pkg/tools -run TestFoo"}},
		{"curl with URL", map[string]any{"command": "curl https://api.example.com/v1/resource"}},
		{"docker build with context", map[string]any{"command": "docker build -t myimage /path/to/ctx"}},
		{"npm install", map[string]any{"command": "npm install @scope/package"}},
		{"echo with path", map[string]any{"command": "echo 'path is /etc/passwd'"}},
		{"python with import", map[string]any{"command": "python -c 'import /something'"}},
		{"git push with ref", map[string]any{"command": "git push origin /refs/heads/main"}},
	}
	for _, tc := range noFlag {
		t.Run("no_flag/"+tc.name, func(t *testing.T) {
			if paths := p.OutOfScopePaths(tc.args); len(paths) != 0 {
				t.Errorf("OutOfScopePaths(%v) = %v, want empty (non-filesystem command)", tc.args, paths)
			}
		})
	}

	// Commands that SHOULD produce out-of-scope paths (filesystem commands).
	flag := []struct {
		name string
		args map[string]any
	}{
		{"cat /etc/passwd", map[string]any{"command": "cat /etc/passwd"}},
		{"cp /etc/hosts", map[string]any{"command": "cp /etc/hosts ./local"}},
		{"rm -rf /tmp/important", map[string]any{"command": "rm -rf /tmp/important"}},
		{"sudo cat /etc/shadow", map[string]any{"command": "sudo cat /etc/shadow"}},
		{"ls ~/Downloads", map[string]any{"command": "ls ~/Downloads"}},
	}
	for _, tc := range flag {
		t.Run("flag/"+tc.name, func(t *testing.T) {
			if paths := p.OutOfScopePaths(tc.args); len(paths) == 0 {
				t.Errorf("OutOfScopePaths(%v) = empty, want non-empty (filesystem command with external path)", tc.args)
			}
		})
	}
}

// TestOutOfScopePaths_RelativePathsInProject verifies that relative paths
// within the project (like "pkg/tools/internal.go") do NOT trigger out-of-scope
// detection when the policy root is set correctly.
func TestOutOfScopePaths_RelativePathsInProject(t *testing.T) {
	root := t.TempDir()
	p := NewSessionAuthPolicy(root)

	// Relative paths that should resolve inside the project root.
	inScope := []struct {
		name string
		args map[string]any
	}{
		{"relative with slashes", map[string]any{"path": "pkg/tools/internal.go"}},
		{"relative tsx", map[string]any{"path": "src/components/App.tsx"}},
		{"bare filename", map[string]any{"path": "main.go"}},
		{"dot-slash relative", map[string]any{"path": "./README.md"}},
	}
	for _, tc := range inScope {
		t.Run("in_scope/"+tc.name, func(t *testing.T) {
			if paths := p.OutOfScopePaths(tc.args); len(paths) != 0 {
				t.Errorf("OutOfScopePaths(%v) = %v, want empty (relative path inside project)", tc.args, paths)
			}
		})
	}

	// Paths that should be flagged as out-of-scope.
	outScope := []struct {
		name string
		args map[string]any
	}{
		{"absolute /etc/passwd", map[string]any{"path": "/etc/passwd"}},
		{"relative escape", map[string]any{"path": "../outside.txt"}},
		{"home path", map[string]any{"path": "~/Downloads/secret.txt"}},
	}
	for _, tc := range outScope {
		t.Run("out_scope/"+tc.name, func(t *testing.T) {
			if paths := p.OutOfScopePaths(tc.args); len(paths) == 0 {
				t.Errorf("OutOfScopePaths(%v) = empty, want non-empty (path outside project)", tc.args)
			}
		})
	}
}
