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

// This intentionally exercises two actual compiled node processes, not a mock
// stream: authorization reenters the same workspace through another node.
// Namespace mutations remain exclusive; callbacks must not hold that lock.
func TestCompiledWorkspaceNodeDockerNestedCommitReads(t *testing.T) {
	binary := os.Getenv("ASTONISH_TEST_WORKSPACE_BINARY")
	if binary == "" {
		t.Skip("explicit freshly built Linux binary required")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	name := "astonish-workspace-nested-" + uuid.NewString()
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
	run("exec", name, "/bin/sh", "-ec", "mkdir -p "+botPrivateMount+" "+botHomeMount+"; chmod 0755 /usr/local/bin/astonish-host; ! command -v python3")
	if got := strings.TrimSpace(string(run("inspect", "--format", "{{.State.Running}}", name))); got != "true" {
		t.Fatalf("test container not running: %s", got)
	}
	db := &DockerBackend{cfg: Config{ContainerRuntimePath: "docker"}}
	call := func(c context.Context, q map[string]any, out any) error {
		q["binding"] = "nested-test-binding"
		raw, err := json.Marshal(q)
		if err != nil {
			return err
		}
		cmd := db.botCompiledWorkspaceCommand(c, name)
		input, approve, err := os.Pipe()
		if err != nil {
			return err
		}
		defer input.Close()
		defer approve.Close()
		cmd.Stdin = input
		var result, stderr bytes.Buffer
		control := &sandbox.WorkspaceCommitStream{Context: c, Output: &result, Approve: approve}
		cmd.Stdout, cmd.Stderr = control, &stderr
		written := make(chan error, 1)
		go func() { _, e := approve.Write(append(raw, '\n')); written <- e }()
		err = cmd.Run()
		_ = input.Close()
		_ = approve.Close()
		if writeErr := <-written; err == nil {
			err = writeErr
		}
		if err != nil {
			return fmt.Errorf("%w: %s (context: %v)", err, stderr.String(), c.Err())
		}
		if err = control.Finish(); err != nil {
			return err
		}
		if out != nil {
			return json.Unmarshal(result.Bytes(), out)
		}
		return nil
	}
	var receipt sandbox.BotHomeWorkspaceReceipt
	publish := sandbox.BotHomeWorkspacePublishRequest{PublicationID: "pub", TaskPath: "tasks/task", Files: []sandbox.BotHomeWorkspaceFile{{Path: "TASK.md", Data: []byte("committed")}}}
	if err := call(ctx, map[string]any{"op": "publish", "request": publish}, &receipt); err != nil {
		t.Fatal(err)
	}
	if err := call(ctx, map[string]any{"op": "write", "path": "tasks/task/state/value", "data": []byte("old")}, nil); err != nil {
		t.Fatal(err)
	}
	for _, op := range []string{"resolve", "snapshot", "read"} {
		t.Run(op, func(t *testing.T) {
			entered := false
			var nestedErr error
			guarded := sandbox.WithBotHomeWorkspaceCommitGuard(ctx, func(c context.Context) error {
				entered = true
				nested, stop := context.WithTimeout(c, 2*time.Second)
				defer stop()
				switch op {
				case "resolve":
					var got sandbox.BotHomeWorkspaceReceipt
					nestedErr = call(nested, map[string]any{"op": op, "path": "pub"}, &got)
					if nestedErr == nil && got.Digest != receipt.Digest {
						nestedErr = fmt.Errorf("receipt changed")
					}
				case "snapshot":
					var got sandbox.BotHomeWorkspaceSnapshot
					nestedErr = call(nested, map[string]any{"op": op, "path": "tasks/task", "limit": 1024}, &got)
					if nestedErr == nil && (len(got.Files) != 1 || string(got.Files[0].Data) != "committed") {
						nestedErr = fmt.Errorf("snapshot changed")
					}
				case "read":
					var got []byte
					nestedErr = call(nested, map[string]any{"op": op, "path": "tasks/task/state/value", "limit": 1024}, &got)
					if nestedErr == nil && string(got) != "old" {
						nestedErr = fmt.Errorf("mutable read returned staged bytes: %q", got)
					}
				}
				return nestedErr
			})
			// Same bytes on success leave every subtest independent. The guard
			// must see the previous committed state, never the staged inode.
			err := call(guarded, map[string]any{"op": "write", "path": "tasks/task/state/value", "data": []byte("old")}, nil)
			if !entered || nestedErr != nil || err != nil {
				t.Fatalf("nested %s during commit must finish: guard=%t nested=%v outer=%v", op, entered, nestedErr, err)
			}
		})
	}
	t.Run("concurrent-writer-preserved", func(t *testing.T) {
		entered := false
		guarded := sandbox.WithBotHomeWorkspaceCommitGuard(ctx, func(c context.Context) error {
			entered = true
			nested, stop := context.WithTimeout(ctx, 2*time.Second)
			defer stop()
			// Do not inherit the outer guard: this is a separate admitted writer.
			return call(nested, map[string]any{"op": "write", "path": "tasks/task/state/value", "data": []byte("winner")}, nil)
		})
		err := call(guarded, map[string]any{"op": "write", "path": "tasks/task/state/value", "data": []byte("stale-outer")}, nil)
		if !entered || err == nil || !strings.Contains(err.Error(), "workspace changed during host authorization") {
			t.Fatalf("outer stale write was not fenced: entered=%t err=%v", entered, err)
		}
		var got []byte
		if err := call(ctx, map[string]any{"op": "read", "path": "tasks/task/state/value", "limit": 1024}, &got); err != nil || string(got) != "winner" {
			t.Fatalf("concurrent writer lost: %q %v", got, err)
		}
	})
}
