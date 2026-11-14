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
	"github.com/google/uuid"
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

func (db *DockerBackend) workspace(ctx context.Context, b *sandbox.BotHomeBinding, q map[string]any, out any) (err error) {
	if err = validateBotHomeReference(b); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err = db.validateBotHome(ctx, b); err != nil {
		return err
	}
	volume, e := db.ensureWorkspaceVolume(ctx, b)
	if e != nil {
		return e
	}
	q["binding"] = b.Scope.Key()
	raw, e := json.Marshal(q)
	if e != nil {
		return e
	}
	name := "astonish-home-workspace-" + uuid.NewString()
	defer func() {
		c, stop := context.WithTimeout(context.Background(), 15*time.Second)
		defer stop()
		labels, e := runDocker(c, db.cfg.ContainerRuntimePath, "inspect", "--format", "{{json .Config.Labels}}", name)
		if e != nil {
			if !isDockerNotFoundError(e) {
				err = errors.Join(err, e)
			}
			return
		}
		var v map[string]string
		if json.Unmarshal(labels, &v) != nil || v[botHomeReaderLabel] != name || v[botHomeScopeLabel] != b.Scope.Key() {
			err = errors.Join(err, errors.New("workspace cleanup ownership mismatch"))
			return
		}
		_, e = runDocker(c, db.cfg.ContainerRuntimePath, "rm", "-fv", name)
		err = errors.Join(err, e)
	}()
	_, err = runDocker(ctx, db.cfg.ContainerRuntimePath, "container", "create", "--interactive", "--name="+name, "--label="+botHomeReaderLabel+"="+name, "--label="+botHomeScopeLabel+"="+b.Scope.Key(), "--pull=never", "--network=none", "--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges", "--pids-limit=16", "--memory=256m", "--cpus=0.5", "--user=0:0", "--log-driver=none", "--mount=type=volume,source="+volume+",target="+botHomeMount+",volume-nocopy", "--entrypoint=python3", BotWorkspaceHelperImage, "-I", "-c", botWorkspaceProgram)
	if err != nil {
		return err
	}
	if err = db.validateBotHome(ctx, b); err != nil {
		return err
	}
	mounts, e := runDocker(ctx, db.cfg.ContainerRuntimePath, "inspect", "--format", "{{json .Mounts}}", name)
	if e != nil {
		return e
	}
	var ms []struct {
		Type, Name, Destination string
		RW                      bool
	}
	if json.Unmarshal(mounts, &ms) != nil || len(ms) != 1 || ms[0].Type != "volume" || ms[0].Name != volume || ms[0].Destination != botHomeMount || !ms[0].RW {
		return errors.New("workspace mount mismatch")
	}
	cmd := exec.CommandContext(ctx, db.cfg.ContainerRuntimePath, "start", "--attach", "--interactive", name)
	input, approval, pipeErr := os.Pipe()
	if pipeErr != nil {
		return pipeErr
	}
	defer input.Close()
	defer approval.Close()
	cmd.Stdin = input
	go func() { _, _ = approval.Write(append(raw, '\n')) }()
	stdout := &artifactBuffer{remaining: 24 << 20}
	stderr := &artifactBuffer{remaining: 4096}
	control := &sandbox.WorkspaceCommitStream{Context: ctx, Output: stdout, Approve: approval, RunScript: func(input []byte) ([]byte, error) { return db.runWorkspaceScratch(ctx, b, input) }}
	cmd.Stdout, cmd.Stderr = control, stderr
	if e = cmd.Run(); e != nil {
		return fmt.Errorf("workspace: %w: %s", e, stderr.String())
	}
	code, e := runDocker(ctx, db.cfg.ContainerRuntimePath, "inspect", "--format", "{{.State.ExitCode}}", name)
	if e != nil {
		return e
	}
	if strings.TrimSpace(string(code)) != "0" {
		return fmt.Errorf("workspace confined operation failed: %s", stderr.String())
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
