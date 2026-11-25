package docker

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"strings"

	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

// Session lifecycle locks are acquired BEFORE this storage lock. Archive's
// caller barrier must quiesce workers before entering its callback; the archive
// never acquires session locks while holding the storage lock. The kernel lock
// is in the Docker volume, not on the client host, and is shared with all private
// workspace writers and the archive helper. Never unlink/replace this inode.
const botHomeAdmissionLabel = "astonish.bot_home_admission"
const botHomeAdmissionExecutable = "/usr/local/bin/astonish-host"

func (db *DockerBackend) lockBotHomeAdmission(ctx context.Context, b *sandbox.BotHomeBinding, provision bool) (*homeFileProcess, error) {
	return db.lockBotHomeAdmissionMode(ctx, b, provision, sandbox.BotHomeAdmissionWait)
}

func (db *DockerBackend) lockBotHomeArchiveAdmission(ctx context.Context, b *sandbox.BotHomeBinding, provision bool) (*homeFileProcess, error) {
	return db.lockBotHomeAdmissionMode(ctx, b, provision, sandbox.BotHomeAdmissionTry)
}

func (db *DockerBackend) lockBotHomeAdmissionMode(ctx context.Context, b *sandbox.BotHomeBinding, provision bool, mode sandbox.BotHomeAdmissionMode) (p *homeFileProcess, err error) {
	if b == nil {
		return nil, nil
	}
	if db.cfg.SandboxImage == "" || (mode != sandbox.BotHomeAdmissionWait && mode != sandbox.BotHomeAdmissionTry) {
		return nil, errors.New("compiled admission runtime image and mode required")
	}
	if err = validateBotHomeReference(b); err != nil {
		return nil, err
	}
	if err = db.validateBotHome(ctx, b); err != nil {
		return nil, err
	}
	volume := workspaceVolume(b)
	// Export must never create a missing private volume.
	if !provision {
		raw, inspectErr := runDocker(ctx, db.cfg.ContainerRuntimePath, "volume", "inspect", volume)
		if inspectErr != nil {
			return nil, inspectErr
		}
		var info []struct {
			Name, Driver    string
			Labels, Options map[string]string
		}
		if json.Unmarshal(raw, &info) != nil || len(info) != 1 || info[0].Name != volume || info[0].Driver != "local" || len(info[0].Options) != 0 || info[0].Labels[botHomeScopeLabel] != b.Scope.Key() || info[0].Labels[botHomeVersionLabel] != "workspace-1" {
			return nil, errors.New("admission private ownership mismatch")
		}
	} else {
		volume, err = db.ensureWorkspaceVolume(ctx, b)
		if err != nil {
			return nil, err
		}
	}
	// Cancellation interrupts acquisition, but an acquired lock belongs to the
	// operation until its deferred release. In particular, archive cleanup must
	// finish before a cancelled caller admits another attachment or writer.
	callerCtx := ctx
	ctx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	stopAcquisitionCancellation := context.AfterFunc(callerCtx, cancel)
	defer stopAcquisitionCancellation()
	p = &homeFileProcess{db: db, binding: b.Clone(), name: "astonish-home-admission-" + uuid.NewString(), cancel: cancel, stderr: &artifactBuffer{remaining: 4096}, done: make(chan struct{})}
	defer func() {
		if err != nil {
			err = errors.Join(err, callerCtx.Err(), p.Close())
			p = nil
		}
	}()
	args := []string{"container", "create", "--interactive", "--attach=stdin", "--attach=stdout", "--attach=stderr", "--name=" + p.name, "--label=" + botHomeReaderLabel + "=" + p.name, "--label=" + botHomeScopeLabel + "=" + b.Scope.Key(), "--label=" + botHomeAdmissionLabel + "=1", "--pull=never", "--network=none", "--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges", "--pids-limit=16", "--memory=64m", "--cpus=0.5", "--user=0:0", "--log-driver=none", "--mount=type=volume,source=" + volume + ",target=/private-files,volume-nocopy", "--entrypoint=" + botHomeAdmissionExecutable, db.cfg.SandboxImage, "node", "--internal-home-admission", string(mode)}
	if _, err = runDocker(ctx, db.cfg.ContainerRuntimePath, args...); err != nil {
		return p, err
	}
	if err = db.validateAdmissionHelper(ctx, b, p.name, false); err != nil {
		return p, err
	}
	p.cmd = exec.CommandContext(ctx, db.cfg.ContainerRuntimePath, "start", "--attach", "--interactive", p.name)
	p.input, err = p.cmd.StdinPipe()
	if err != nil {
		return p, err
	}
	p.output, err = p.cmd.StdoutPipe()
	if err != nil {
		return p, err
	}
	p.reader = bufio.NewReader(p.output)
	p.cmd.Stderr = p.stderr
	if err = p.cmd.Start(); err != nil {
		return p, err
	}
	go func(proc *homeFileProcess) {
		select {
		case <-ctx.Done():
			_ = proc.Close()
		case <-proc.done:
		}
	}(p)
	var ready struct{ Ready, Busy bool }
	if err = p.line(&ready); err != nil {
		return p, errors.Join(err, callerCtx.Err(), ctx.Err())
	}
	if ready.Busy && !ready.Ready && mode == sandbox.BotHomeAdmissionTry {
		// Join the normal helper exit and owned cleanup before dropping SQL's
		// fence; contention is not a context timeout or an attachment teardown.
		return p, errors.Join(store.ErrBotBusy, p.finish(false))
	}
	if !ready.Ready {
		return p, errors.New("admission lock not acquired")
	}
	if !stopAcquisitionCancellation() || callerCtx.Err() != nil {
		return p, callerCtx.Err()
	}
	return p, nil
}

// An admission helper has no Home mount and executes only the fixed lock
// protocol. It is safe for it to wait on private storage during an archive.
// All other attachments, including stopped/unknown containers, remain denied.
func (db *DockerBackend) validateAdmissionHelper(ctx context.Context, b *sandbox.BotHomeBinding, name string, running bool) error {
	raw, err := runDocker(ctx, db.cfg.ContainerRuntimePath, "inspect", "--format", "{{json .}}", name)
	if err != nil {
		return err
	}
	var v struct {
		State  struct{ Status string }
		Config struct {
			Labels               map[string]string
			Image                string
			Entrypoint, Cmd      []string
			OpenStdin, StdinOnce bool
		}
		Mounts []struct {
			Type, Name, Destination string
			RW                      bool
		}
	}
	if json.Unmarshal(raw, &v) != nil {
		return errors.New("invalid admission helper")
	}
	if running && v.State.Status != "running" && v.State.Status != "created" {
		return errors.New("orphaned admission helper still attached")
	}
	c := v.Config
	if c.Labels[botHomeAdmissionLabel] != "1" || c.Labels[botHomeScopeLabel] != b.Scope.Key() || c.Labels[botHomeReaderLabel] != name || db.cfg.SandboxImage == "" || c.Image != db.cfg.SandboxImage || !c.OpenStdin || !c.StdinOnce || len(c.Entrypoint) != 1 || c.Entrypoint[0] != botHomeAdmissionExecutable || len(c.Cmd) != 3 || c.Cmd[0] != "node" || c.Cmd[1] != "--internal-home-admission" || (sandbox.BotHomeAdmissionMode(c.Cmd[2]) != sandbox.BotHomeAdmissionWait && sandbox.BotHomeAdmissionMode(c.Cmd[2]) != sandbox.BotHomeAdmissionTry) || len(v.Mounts) != 1 {
		return errors.New("admission helper identity mismatch")
	}
	m := v.Mounts[0]
	if m.Type != "volume" || m.Name != workspaceVolume(b) || m.Destination != "/private-files" || !m.RW {
		return errors.New("admission helper mount mismatch")
	}
	return nil
}

// A dead client may leave an archive helper behind after its lock holder exits.
// Refuse admission until explicit owned cleanup; never force-detach that helper.
func (db *DockerBackend) rejectHomeArchiveAttachment(ctx context.Context, b *sandbox.BotHomeBinding) error {
	raw, err := runDocker(ctx, db.cfg.ContainerRuntimePath, "ps", "-a", "--format={{.Names}}", "--filter=volume="+b.Ref)
	if err != nil {
		return err
	}
	for _, name := range strings.Fields(string(raw)) {
		if strings.HasPrefix(name, "astonish-home-archive-") {
			return errors.New("Home archive helper still attached")
		}
	}
	return nil
}

func releaseBotHomeAdmission(p *homeFileProcess) error {
	if p == nil {
		return nil
	}
	if _, err := p.input.Write([]byte("release\n")); err != nil {
		return errors.Join(err, p.Close())
	}
	return p.finish(false)
}
