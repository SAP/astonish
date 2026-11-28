package docker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/google/uuid"
)

// Exercises the shipped CLI and unchanged host handshake, not the scratch
// interpreter itself. No session/template/container owned by a user is touched.
func TestCompiledScriptNodeDockerNoPython(t *testing.T) {
	binary := os.Getenv("ASTONISH_TEST_WORKSPACE_BINARY")
	if binary == "" {
		t.Skip("explicit freshly built Linux root CLI required")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	name := "astonish-script-node-" + uuid.NewString()
	run := func(args ...string) []byte {
		t.Helper()
		out, e := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
		if e != nil {
			t.Fatalf("docker %v: %s %v", args, out, e)
		}
		return out
	}
	run("create", "--pull=never", "--name="+name, "--network=none", "--entrypoint=/bin/sleep", "debian:bookworm-slim", "120")
	defer exec.Command("docker", "rm", "-f", name).Run()
	run("cp", binary, name+":/usr/local/bin/astonish-host")
	run("start", name)
	run("exec", name, "/bin/sh", "-ec", "mkdir -p "+botPrivateMount+" "+botHomeMount+"; ! command -v python3")
	db := &DockerBackend{cfg: Config{ContainerRuntimePath: "docker"}}
	script := sandbox.BotHomeWorkspaceFile{Path: "scripts/run.py", Data: []byte("print('scratch')"), Executable: true}
	calls := 0
	call := func(q map[string]any, out any) error {
		q["binding"] = "test-binding"
		raw, _ := json.Marshal(q)
		if !sandbox.IsCompiledWorkspaceOperation(q["op"]) {
			return fmt.Errorf("not compiled: %v", q["op"])
		}
		cmd := db.botCompiledWorkspaceCommand(ctx, name)
		input, approve, e := os.Pipe()
		if e != nil {
			return e
		}
		defer input.Close()
		defer approve.Close()
		cmd.Stdin = input
		var result, stderr bytes.Buffer
		control := &sandbox.WorkspaceCommitStream{Context: ctx, Output: &result, Approve: approve, RunScript: func(input []byte) ([]byte, error) {
			calls++
			var payload struct {
				Script sandbox.BotHomeWorkspaceFile   `json:"script"`
				Files  []sandbox.BotHomeWorkspaceFile `json:"files"`
			}
			if e := json.Unmarshal(input, &payload); e != nil {
				return nil, e
			}
			if payload.Script.Digest() != script.Digest() || len(payload.Files) != 1 || string(payload.Files[0].Data) != "old" {
				return nil, fmt.Errorf("unexpected scratch input")
			}
			return json.Marshal(map[string]any{"exit_code": 0, "stdout": "scratch", "stderr": "", "files": []sandbox.BotHomeWorkspaceFile{{Path: "result", Data: []byte("new\x00bytes")}}})
		}}
		cmd.Stdout = control
		cmd.Stderr = &stderr
		written := make(chan error, 1)
		go func() { _, e := approve.Write(append(raw, '\n')); written <- e }()
		e = cmd.Run()
		input.Close()
		approve.Close()
		werr := <-written
		if e != nil {
			return fmt.Errorf("%w: %s", e, stderr.String())
		}
		if werr != nil {
			return werr
		}
		if e = control.Finish(); e != nil {
			return e
		}
		if out != nil {
			return json.Unmarshal(result.Bytes(), out)
		}
		return nil
	}
	pub := uuid.NewString()
	if e := call(map[string]any{"op": "publish", "request": sandbox.BotHomeWorkspacePublishRequest{TaskPath: "tasks/task", PublicationID: pub, Files: []sandbox.BotHomeWorkspaceFile{script}}}, nil); e != nil {
		t.Fatal(e)
	}
	if e := call(map[string]any{"op": "write", "path": "tasks/task/state/old", "data": []byte("old")}, nil); e != nil {
		t.Fatal(e)
	}
	q := map[string]any{"op": "script", "path": "tasks/task", "publication": pub, "script": script.Path, "digest": script.Digest()}
	var result sandbox.BotHomeWorkspaceScriptResult
	if e := call(q, &result); e != nil {
		t.Fatal(e)
	}
	if result.Stdout != "scratch" || result.ExitCode != 0 || calls != 1 {
		t.Fatalf("result %+v calls %d", result, calls)
	}
	var data []byte
	if e := call(map[string]any{"op": "read", "path": "tasks/task/state/result", "limit": 100}, &data); e != nil || string(data) != "new\x00bytes" {
		t.Fatalf("bytes %q: %v", data, e)
	}
	run("exec", name, "/bin/sh", "-ec", "printf incomplete > "+botPrivateMount+"/.workspace-restore-incomplete")
	if e := call(q, nil); e == nil || !strings.Contains(e.Error(), "restore incomplete") {
		t.Fatalf("marked script accepted: %v", e)
	}
	if calls != 1 {
		t.Fatal("marked restore reached scratch")
	}
	t.Log("fresh root CLI without Python: script handshake, exact input/output bytes, atomic state selection and incomplete-restore rejection")
}
