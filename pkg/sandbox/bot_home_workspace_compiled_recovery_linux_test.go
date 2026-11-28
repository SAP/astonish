//go:build linux

package sandbox

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestCompiledWorkspaceCrashProcess(t *testing.T) {
	if os.Getenv("ASTONISH_WORKSPACE_CRASH_CHILD") != "1" {
		return
	}
	root, err := os.Open(os.Getenv("ASTONISH_WORKSPACE_CRASH_ROOT"))
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err = RunBotHomeWorkspace(root, os.Stdin, os.Stdout); err != nil {
		t.Fatal(err)
	}
}

func workspaceCrashStart(t *testing.T, root *os.File, q map[string]any) (*exec.Cmd, io.WriteCloser, *bufio.Reader) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestCompiledWorkspaceCrashProcess$")
	cmd.Env = append(os.Environ(), "ASTONISH_WORKSPACE_CRASH_CHILD=1", "ASTONISH_WORKSPACE_CRASH_ROOT="+root.Name())
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		in.Close()
		if cmd.ProcessState == nil {
			cmd.Process.Kill()
			cmd.Wait()
		}
	})
	q["binding"] = "scope"
	if err = json.NewEncoder(in).Encode(q); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(out)
	line, err := reader.ReadString('\n')
	if err != nil || line != "WORKSPACE_READY\n" {
		t.Fatalf("fence: %q %v", line, err)
	}
	return cmd, in, reader
}

func workspaceCrashCall(root *os.File, q map[string]any, suffix string) error {
	q["binding"] = "scope"
	raw, _ := json.Marshal(q)
	return RunBotHomeWorkspace(root, strings.NewReader(string(raw)+"\n"+suffix), io.Discard)
}

func TestCompiledWorkspaceCrashRecovery(t *testing.T) {
	for _, mode := range []string{"write", "publish"} {
		t.Run(mode, func(t *testing.T) {
			root := knowledgeCompiledRoot(t)
			q := map[string]any{"op": "write", "path": "tasks/task/state/value", "data": []byte("unapproved")}
			if mode == "publish" {
				q = map[string]any{"op": "publish", "request": BotHomeWorkspacePublishRequest{TaskPath: "tasks/task", PublicationID: "lost", Files: []BotHomeWorkspaceFile{{Path: "TASK.md", Data: []byte("unapproved")}}}}
			}
			cmd, _, _ := workspaceCrashStart(t, root, q)
			if err := cmd.Process.Kill(); err != nil {
				t.Fatal(err)
			}
			if err := cmd.Wait(); err == nil {
				t.Fatal("child not killed")
			}
			if err := workspaceCrashCall(root, map[string]any{"op": "write", "path": "tasks/other/state/value", "data": []byte("approved")}, "commit\n"); err != nil {
				t.Fatal(err)
			}
			var residues []string
			filepath.WalkDir(root.Name(), func(path string, e os.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if strings.HasPrefix(e.Name(), ".tmp-") || strings.HasPrefix(e.Name(), "stage-") || strings.HasPrefix(e.Name(), ".revision-stage-") {
					residues = append(residues, path)
				}
				return nil
			})
			if len(residues) != 0 {
				t.Fatalf("crash stages retained: %v", residues)
			}
			for _, path := range []string{"tasks/task/state/value", "tasks/task/.head", ".receipts/lost", "tasks/task/.revisions/lost"} {
				if _, err := os.Stat(filepath.Join(root.Name(), path)); !os.IsNotExist(err) {
					t.Fatalf("replayed %s: %v", path, err)
				}
			}
		})
	}
}

func TestCompiledWorkspaceRecoveryPreservesRevisionsAndReads(t *testing.T) {
	root := knowledgeCompiledRoot(t)
	// Even exact historical stage-UUID publication IDs are committed data, not
	// reclaimable ownership. A missing receipt must never authorize deletion.
	id := "stage-01234567-89ab-4def-8123-456789abcdef"
	pub := BotHomeWorkspacePublishRequest{TaskPath: "tasks/task", PublicationID: id, Files: []BotHomeWorkspaceFile{{Path: "TASK.md", Data: []byte("immutable")}}}
	if err := workspaceCrashCall(root, map[string]any{"op": "publish", "request": pub}, "commit\ncommit\n"); err != nil {
		t.Fatal(err)
	}
	orphan := filepath.Join(root.Name(), "tasks/task/.revisions/unselected/TASK.md")
	if err := os.MkdirAll(filepath.Dir(orphan), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(orphan, []byte("uncertain"), 0600); err != nil {
		t.Fatal(err)
	}
	residue := filepath.Join(root.Name(), "tasks/task/state/.tmp-01234567-89ab-4def-8123-456789abcdef")
	os.MkdirAll(filepath.Dir(residue), 0700)
	os.WriteFile(residue, []byte("pending"), 0600)
	if err := workspaceCrashCall(root, map[string]any{"op": "resolve", "path": id}, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(residue); err != nil {
		t.Fatal("read reclaimed residue", err)
	}
	if err := workspaceCrashCall(root, map[string]any{"op": "write", "path": "tasks/other/state/value", "data": []byte("approved")}, "commit\n"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(residue); !os.IsNotExist(err) {
		t.Fatal("residue not removed", err)
	}
	for path, want := range map[string]string{filepath.Join(root.Name(), "tasks/task/.revisions", id, "TASK.md"): "immutable", orphan: "uncertain"} {
		got, err := os.ReadFile(path)
		if err != nil || string(got) != want {
			t.Fatalf("revision changed %q %v", got, err)
		}
	}
	if err := workspaceCrashCall(root, map[string]any{"op": "resolve", "path": id}, ""); err != nil {
		t.Fatal(err)
	}
}

func TestCompiledWorkspaceRecoveryRejectsSpecialStages(t *testing.T) {
	for _, kind := range []string{"symlink", "hardlink", "fifo", "directory", "revision-symlink"} {
		t.Run(kind, func(t *testing.T) {
			root := knowledgeCompiledRoot(t)
			victim := filepath.Join(root.Name(), "victim")
			os.WriteFile(victim, []byte("untouched"), 0600)
			stage := filepath.Join(root.Name(), ".tmp-01234567-89ab-4def-8123-456789abcdef")
			var err error
			switch kind {
			case "symlink":
				err = os.Symlink(victim, stage)
			case "hardlink":
				err = os.Link(victim, stage)
			case "fifo":
				err = unix.Mkfifo(stage, 0600)
			case "directory":
				err = os.Mkdir(stage, 0700)
			case "revision-symlink":
				stage = filepath.Join(root.Name(), "tasks/task/.revisions/.revision-stage-01234567-89ab-4def-8123-456789abcdef")
				err = os.MkdirAll(stage, 0700)
				if err == nil {
					err = os.Symlink(victim, filepath.Join(stage, "TASK.md"))
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = workspaceCrashCall(root, map[string]any{"op": "write", "path": "tasks/task/state/value", "data": []byte("denied")}, "commit\n"); err == nil {
				t.Fatal("unsafe stage accepted")
			}
			got, err := os.ReadFile(victim)
			if err != nil || string(got) != "untouched" {
				t.Fatalf("victim altered %q %v", got, err)
			}
			if _, err = os.Lstat(stage); err != nil {
				t.Fatal("unsafe stage removed", err)
			}
		})
	}
}

func TestCompiledWorkspaceRecoveryBeforeQuota(t *testing.T) {
	root := knowledgeCompiledRoot(t)
	stage := filepath.Join(root.Name(), ".tmp-01234567-89ab-4def-8123-456789abcdef")
	f, err := os.Create(stage)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.Truncate(512 << 20); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if err = workspaceCrashCall(root, map[string]any{"op": "write", "path": "tasks/task/state/value", "data": []byte("approved")}, "commit\n"); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(stage); !os.IsNotExist(err) {
		t.Fatal("quota residue remains", err)
	}
}

func TestCompiledWorkspaceRecoveryNestedWriter(t *testing.T) {
	root := knowledgeCompiledRoot(t)
	entered := false
	out := &workspaceExchangeWriter{hook: func(b []byte) {
		if string(b) != "WORKSPACE_READY\n" {
			return
		}
		entered = true
		stages, err := filepath.Glob(filepath.Join(root.Name(), "tasks/task/state/.tmp-*"))
		if err != nil || len(stages) != 1 {
			t.Fatalf("outer stage %v %v", stages, err)
		}
		if err = workspaceCrashCall(root, map[string]any{"op": "write", "path": "tasks/other/state/value", "data": []byte("winner")}, "commit\n"); err != nil {
			t.Fatal(err)
		}
		if _, err = os.Stat(stages[0]); err != nil {
			t.Fatal("nested writer reclaimed active stage", err)
		}
	}}
	q, _ := json.Marshal(map[string]any{"op": "write", "binding": "scope", "path": "tasks/task/state/value", "data": []byte("outer")})
	err := RunBotHomeWorkspace(root, strings.NewReader(string(q)+"\ncommit\n"), out)
	if !entered || err == nil || !strings.Contains(err.Error(), "workspace changed during host authorization") {
		t.Fatalf("stale outer accepted: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(root.Name(), "tasks/other/state/value"))
	if err != nil || string(got) != "winner" {
		t.Fatalf("nested writer lost %q %v", got, err)
	}
	if _, err = os.Stat(filepath.Join(root.Name(), "tasks/task/state/value")); !os.IsNotExist(err) {
		t.Fatalf("outer replayed: %v", err)
	}
}

func TestCompiledWorkspaceRecoveryPreservesActiveWriter(t *testing.T) {
	root := knowledgeCompiledRoot(t)
	cmd, in, out := workspaceCrashStart(t, root, map[string]any{"op": "write", "path": "tasks/task/state/value", "data": []byte("active")})
	before, err := os.ReadDir(filepath.Join(root.Name(), "tasks/task/state"))
	if err != nil || len(before) != 1 {
		t.Fatalf("active stage %v %v", before, err)
	}
	// Invalid independent mutation exercises recovery admission without changing the
	// optimistic data fence; it must not remove the live operation's stage.
	if err = workspaceCrashCall(root, map[string]any{"op": "write", "path": "forbidden", "data": []byte("other")}, ""); err == nil {
		t.Fatal("invalid write accepted")
	}
	got, err := os.ReadFile(filepath.Join(root.Name(), "tasks/task/state", before[0].Name()))
	if err != nil || !bytes.Equal(got, []byte("active")) {
		t.Fatalf("active stage lost %q %v", got, err)
	}
	io.WriteString(in, "commit\n")
	in.Close()
	io.Copy(io.Discard, out)
	if err = cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	got, err = os.ReadFile(filepath.Join(root.Name(), "tasks/task/state/value"))
	if err != nil || string(got) != "active" {
		t.Fatalf("active commit %q %v", got, err)
	}
}
