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

func TestCompiledWorkspaceCommand(t *testing.T) {
	db := &DockerBackend{cfg: Config{ContainerRuntimePath: "docker"}}
	args := strings.Join(db.botCompiledWorkspaceCommand(t.Context(), "physical-id").Args, " ")
	if !strings.Contains(args, "exec --interactive physical-id") || !strings.Contains(args, "/usr/local/bin/astonish-host node --internal-home-workspace") || strings.Contains(args, "python") || strings.Contains(args, "chroot") {
		t.Fatal(args)
	}
}

// Physical command/CLI proof, without a template or a user-owned session. This
// test intentionally does not certify session provisioning or idle recovery.
func TestCompiledWorkspaceNodeDocker(t *testing.T) {
	binary := os.Getenv("ASTONISH_TEST_WORKSPACE_BINARY")
	if binary == "" {
		t.Skip("explicit freshly built Linux binary required")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	name := "astonish-workspace-node-" + uuid.NewString()
	run := func(args ...string) []byte {
		t.Helper()
		out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("docker %v: %s %v", args, out, err)
		}
		return out
	}
	run("container", "create", "--name="+name, "--network=none", "debian:bookworm-slim", "/bin/sleep", "120")
	defer exec.Command("docker", "container", "rm", "-f", name).Run()
	run("cp", binary, name+":/usr/local/bin/astonish-host")
	run("container", "start", name)
	run("exec", name, "/bin/sh", "-ec", "mkdir -p "+botPrivateMount+" "+botHomeMount+"; ! command -v python3")
	db := &DockerBackend{cfg: Config{ContainerRuntimePath: "docker"}}
	call := func(q map[string]any, out any) error {
		q["binding"] = "test-binding"
		raw, _ := json.Marshal(q)
		cmd := db.botCompiledWorkspaceCommand(ctx, name)
		input, approve, pipeErr := os.Pipe()
		if pipeErr != nil {
			return pipeErr
		}
		defer input.Close()
		defer approve.Close()
		cmd.Stdin = input
		var result, stderr bytes.Buffer
		control := &sandbox.WorkspaceCommitStream{Context: ctx, Output: &result, Approve: approve}
		cmd.Stdout = control
		cmd.Stderr = &stderr
		go func() { _, _ = approve.Write(append(raw, '\n')) }()
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("%w: %s", err, stderr.String())
		}
		if err := control.Finish(); err != nil {
			return err
		}
		if out != nil {
			return json.Unmarshal(result.Bytes(), out)
		}
		return nil
	}
	req := sandbox.BotHomeWorkspacePublishRequest{PublicationID: "pub", TaskPath: "tasks/task", Files: []sandbox.BotHomeWorkspaceFile{{Path: "TASK.md", Data: []byte("compiled bytes")}}}
	var receipt sandbox.BotHomeWorkspaceReceipt
	if err := call(map[string]any{"op": "publish", "request": req}, &receipt); err != nil {
		t.Fatal(err)
	}
	var data []byte
	if err := call(map[string]any{"op": "read", "path": "tasks/task/TASK.md", "limit": 100}, &data); err != nil || string(data) != "compiled bytes" {
		t.Fatalf("%q %v", data, err)
	}
	if err := call(map[string]any{"op": "resolve", "path": "pub"}, &receipt); err != nil {
		t.Fatal(err)
	}
	if err := call(map[string]any{"op": "write", "path": "tasks/task/state/value", "data": []byte("state")}, nil); err != nil {
		t.Fatal(err)
	}
	var snap sandbox.BotHomeWorkspaceSnapshot
	if err := call(map[string]any{"op": "snapshot", "path": "workspace", "limit": 1 << 20}, &snap); err != nil || len(snap.Files) != 4 {
		t.Fatalf("snapshot %d %v", len(snap.Files), err)
	}
	t.Log("compiled node publishes, resolves, reads, writes and snapshots in disposable container without Python")
}
