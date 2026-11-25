package docker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/SAP/astonish/pkg/sandbox"
)

// BotWorkspaceHelperImage is a digest-pinned Python/SQLite userland, separate
// from the deliberately runtime-free overlay supervisor image. Provision it
// explicitly; operations never pull or install dependencies.
const BotWorkspaceHelperImage = "python@sha256:e48f59cdba69a3cbd9ed2cf0f348d90d45a8d350d98134cf1070bc106ee9ed90"

var _ sandbox.BotHomeWorkspaceBackend = (*DockerBackend)(nil)

// All path walks are descriptor relative, O_NOFOLLOW, and single-link regular
// files. This program is server-owned, never loaded from the retained Home.
var botWorkspaceProgram = `import os,sys,json,base64,hashlib,stat,fcntl,uuid
q=json.loads(sys.stdin.readline()); root=os.open('/mnt/astonish-bot-home',os.O_RDONLY|os.O_DIRECTORY|os.O_NOFOLLOW)
` + strings.Replace(sandbox.BotHomeWorkspaceProgram, "if op=='publish':", sandbox.BotKnowledgeWorkspaceProgram+"\nif op in ('knowledge-snapshot','knowledge-publish','knowledge-verify'): result=knowledgeoperation()\nelif op=='publish':", 1)

func (db *DockerBackend) botCompiledWorkspaceCommand(ctx context.Context, target string) *exec.Cmd {
	script := fmt.Sprintf("exec 8<%s; exec 9<%s; exec /usr/local/bin/astonish-host node --internal-home-workspace", botPrivateMount, botHomeMount)
	return db.botSessionExecCommand(ctx, target, []string{"exec", "--interactive", target, "/bin/sh", "-ec", script})
}

func (db *DockerBackend) workspace(ctx context.Context, b *sandbox.BotHomeBinding, q map[string]any, out any) (err error) {
	if err = validateBotHomeReference(b); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err = db.validateBotHome(ctx, b); err != nil {
		return err
	}
	id, e := db.botFileSession(ctx, b)
	if e != nil {
		return e
	}
	q["binding"] = b.Scope.Key()
	raw, e := json.Marshal(q)
	if e != nil {
		return e
	}
	program := strings.Replace(botWorkspaceProgram, "root=os.open('/mnt/astonish-bot-home',os.O_RDONLY|os.O_DIRECTORY|os.O_NOFOLLOW)", "root=os.dup(8)", 1)
	cmd := db.botSessionCommand(ctx, id, program)
	if sandbox.IsCompiledWorkspaceOperation(q["op"]) {
		target, resolveErr := db.resolveBotFileTarget(ctx, id, b)
		if resolveErr != nil {
			return resolveErr
		}
		cmd = db.botCompiledWorkspaceCommand(ctx, target)
	}
	input, approval, pipeErr := os.Pipe()
	if pipeErr != nil {
		return pipeErr
	}
	defer input.Close()
	defer approval.Close()
	cmd.Stdin = input
	requestWritten := make(chan error, 1)
	go func() { _, e := approval.Write(append(raw, '\n')); requestWritten <- e }()
	defer func() {
		_ = input.Close()
		_ = approval.Close()
		err = errors.Join(err, <-requestWritten)
	}()
	stdout := &artifactBuffer{remaining: 24 << 20}
	stderr := &artifactBuffer{remaining: 4096}
	control := &sandbox.WorkspaceCommitStream{Context: ctx, Output: stdout, Approve: approval, RunScript: func(input []byte) ([]byte, error) { return db.runWorkspaceScratch(ctx, b, input) }}
	cmd.Stdout, cmd.Stderr = control, stderr
	if e = cmd.Run(); e != nil {
		return fmt.Errorf("workspace: %w: %s", e, stderr.String())
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err = control.Finish(); err != nil {
		return err
	}
	if out != nil {
		return json.Unmarshal(stdout.Bytes(), out)
	}
	var response any
	return json.Unmarshal(stdout.Bytes(), &response)
}
func (db *DockerBackend) PublishBotHomeWorkspace(c context.Context, b *sandbox.BotHomeBinding, q sandbox.BotHomeWorkspacePublishRequest) (*sandbox.BotHomeWorkspaceReceipt, error) {
	if e := q.Validate(); e != nil {
		return nil, e
	}
	var r sandbox.BotHomeWorkspaceReceipt
	operation := func() error { return db.workspace(c, b, map[string]any{"op": "publish", "request": q}, &r) }
	var e error
	if q.BeforeCommit != nil {
		e = q.BeforeCommit(operation)
	} else {
		e = operation()
	}
	return &r, e
}
func (db *DockerBackend) ResolveBotHomeWorkspaceReceipt(c context.Context, b *sandbox.BotHomeBinding, ref string) (*sandbox.BotHomeWorkspaceReceipt, error) {
	var r sandbox.BotHomeWorkspaceReceipt
	e := db.workspace(c, b, map[string]any{"op": "resolve", "path": ref}, &r)
	return &r, e
}
func (db *DockerBackend) ReadBotHomeWorkspace(c context.Context, b *sandbox.BotHomeBinding, p string, n int64) ([]byte, error) {
	if sandbox.ValidateBotHomeArtifactPath(p) != nil || n < 1 || n > sandbox.MaxBotHomeWorkspaceStateBytes {
		return nil, errors.New("invalid workspace read")
	}
	var r []byte
	e := db.workspace(c, b, map[string]any{"op": "read", "path": p, "limit": n}, &r)
	return r, e
}
func (db *DockerBackend) WriteBotHomeWorkspace(c context.Context, b *sandbox.BotHomeBinding, f sandbox.BotHomeWorkspaceFile) error {
	if sandbox.ValidateBotHomeArtifactPath(f.Path) != nil || f.Executable || int64(len(f.Data)) > sandbox.MaxBotHomeWorkspaceAssetBytes {
		return errors.New("invalid workspace write")
	}
	operation := func() error {
		return db.workspace(c, b, map[string]any{"op": "write", "path": f.Path, "data": f.Data}, nil)
	}
	if f.BeforeCommit != nil {
		return f.BeforeCommit(operation)
	}
	return operation()
}
func (db *DockerBackend) ListBotHomeWorkspace(c context.Context, b *sandbox.BotHomeBinding, p, after string, n int) ([]sandbox.BotHomeWorkspaceEntry, string, error) {
	if sandbox.ValidateBotHomeArtifactPath(p) != nil || n < 1 || n > 100 {
		return nil, "", errors.New("invalid workspace list")
	}
	var r struct {
		Entries []sandbox.BotHomeWorkspaceEntry
		Next    string
	}
	e := db.workspace(c, b, map[string]any{"op": "list", "path": p, "after": after, "limit": n}, &r)
	return r.Entries, r.Next, e
}
func (db *DockerBackend) SnapshotBotHomeWorkspace(c context.Context, b *sandbox.BotHomeBinding, p string, n int64) (*sandbox.BotHomeWorkspaceSnapshot, error) {
	if sandbox.ValidateBotHomeArtifactPath(p) != nil || n < 1 || n > sandbox.MaxBotHomeWorkspaceBundleBytes {
		return nil, errors.New("invalid workspace snapshot")
	}
	var r sandbox.BotHomeWorkspaceSnapshot
	e := db.workspace(c, b, map[string]any{"op": "snapshot", "path": p, "limit": n}, &r)
	return &r, e
}
