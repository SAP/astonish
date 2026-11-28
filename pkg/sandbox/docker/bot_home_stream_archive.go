package docker

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/google/uuid"
)

var _ sandbox.BotHomeStreamArchiveBackend = (*DockerBackend)(nil)

// Run the callback exactly once and do not let a barrier swallow operation errors.
func archiveBarrier(ctx context.Context, b *sandbox.BotHomeBinding, barrier sandbox.BotHomeArchiveBarrier, op func(context.Context) error) error {
	if barrier == nil || validateBotHomeReference(b) != nil {
		return sandbox.ErrBotHomeUnsupported
	}
	var mu sync.Mutex
	closed := false
	calls := 0
	var operationErr error
	err := barrier(ctx, b.Clone(), func(c context.Context) error {
		mu.Lock()
		defer mu.Unlock()
		if closed {
			return errors.New("archive barrier callback escaped scope")
		}
		calls++
		if calls != 1 {
			operationErr = errors.Join(operationErr, errors.New("archive barrier repeated operation"))
			return operationErr
		}
		if c == nil {
			operationErr = errors.New("archive barrier context missing")
			return operationErr
		}
		if e := ctx.Err(); e != nil {
			operationErr = e
			return e
		}
		operationErr = errors.Join(op(c), c.Err())
		return operationErr
	})
	mu.Lock()
	defer mu.Unlock()
	closed = true
	if calls != 1 {
		return errors.Join(err, operationErr, errors.New("archive barrier must execute exactly once"))
	}
	return errors.Join(err, operationErr, ctx.Err())
}
func (db *DockerBackend) archiveDetached(ctx context.Context, b *sandbox.BotHomeBinding, own string) error {
	return db.archiveAttachments(ctx, b, own, "")
}

// archiveStoppedTarget is called only with archive admission held. It pins an
// explicitly bound session and its persisted recipe, not merely familiar labels.
// StartSession takes the same admission lock before resuming stopped compute.
func (db *DockerBackend) archiveStoppedTarget(ctx context.Context, b *sandbox.BotHomeBinding) (string, error) {
	value, ok := db.botFileSessions.Load(*b)
	if !ok {
		return "", nil
	}
	id := value.(string)
	raw, err := runDocker(ctx, db.cfg.ContainerRuntimePath, "inspect", "--format", "{{json .}}", containerName(id))
	if err != nil {
		if isDockerNotFoundError(err) {
			return "", nil
		}
		return "", err
	}
	meta, err := db.readSessionMetadata(id)
	if err != nil {
		return "", err
	}
	if meta == nil || !meta.BotHome.Equal(b) {
		return "", errors.New("archive session recipe mismatch")
	}
	var target struct {
		ID    string `json:"Id"`
		State struct {
			Status                      string
			Running, Paused, Restarting bool
		}
		Config struct{ Labels map[string]string }
	}
	if json.Unmarshal(raw, &target) != nil || target.ID == "" || target.State.Status != "exited" || target.State.Running || target.State.Paused || target.State.Restarting || target.Config.Labels["astonish.session_id"] != id {
		return "", errors.New("archive requires stopped trusted session")
	}
	if err := db.validateBotFileMount(ctx, id, b); err != nil {
		return "", err
	}
	return target.ID, nil
}

// A pinned stopped attachment is permitted only for export under admission.
// Restore and standalone detached checks keep the stricter empty-target rule.
func (db *DockerBackend) archiveAttachments(ctx context.Context, b *sandbox.BotHomeBinding, own, stopped string) error {
	for _, volume := range []string{b.Ref, workspaceVolume(b)} {
		raw, e := runDocker(ctx, db.cfg.ContainerRuntimePath, "ps", "-a", "--format={{.Names}}", "--filter=volume="+volume)
		if e != nil {
			return e
		}
		for _, name := range strings.Fields(string(raw)) {
			if name != own {
				if volume == workspaceVolume(b) && strings.HasPrefix(name, "astonish-home-admission-") && db.validateAdmissionHelper(ctx, b, name, true) == nil {
					continue
				}
				if stopped != "" {
					if bound, ok := db.botFileSessions.Load(*b); ok && name == containerName(bound.(string)) {
						current, err := db.archiveStoppedTarget(ctx, b)
						if err != nil || current != stopped {
							return errors.Join(err, errors.New("archive stopped target changed"))
						}
						continue
					}
				}
				return errors.New("archive requires detached Home and private storage")
			}
		}
	}
	return nil
}
func (db *DockerBackend) startHomeArchive(ctx context.Context, b *sandbox.BotHomeBinding, q map[string]any, stopped string) (p *homeFileProcess, err error) {
	if err = validateBotHomeReference(b); err != nil {
		return nil, err
	}
	if strings.TrimSpace(db.cfg.SandboxImage) == "" {
		return nil, errors.New("compiled archive runtime image required")
	}
	if err = db.validateBotHome(ctx, b); err != nil {
		return nil, err
	}
	if err = db.archiveAttachments(ctx, b, "", stopped); err != nil {
		return nil, err
	}
	volume := workspaceVolume(b)
	// Export never provisions missing private storage. Restore may explicitly
	// provision its private half only after validating the owned Home.
	if q["op"] == "restore" {
		if _, err = db.ensureWorkspaceVolume(ctx, b); err != nil {
			return nil, err
		}
	}
	raw, err := runDocker(ctx, db.cfg.ContainerRuntimePath, "volume", "inspect", volume)
	if err != nil {
		return nil, err
	}
	var info []struct {
		Name, Driver    string
		Labels, Options map[string]string
	}
	if json.Unmarshal(raw, &info) != nil || len(info) != 1 || info[0].Name != volume || info[0].Driver != "local" || len(info[0].Options) != 0 || info[0].Labels[botHomeScopeLabel] != b.Scope.Key() || info[0].Labels[botHomeVersionLabel] != "workspace-1" {
		return nil, errors.New("archive private ownership mismatch")
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	p = &homeFileProcess{db: db, binding: b.Clone(), name: "astonish-home-archive-" + uuid.NewString(), cancel: cancel, stderr: &artifactBuffer{remaining: 4096}, done: make(chan struct{})}
	defer func() {
		if err != nil {
			err = errors.Join(err, p.finish(true))
			p = nil
		}
	}()
	restoring := q["op"] == "restore"
	homeMount := "--mount=type=volume,source=" + b.Ref + ",target=/home-files,volume-nocopy"
	if !restoring {
		homeMount += ",readonly"
	}
	args := []string{"container", "create", "--interactive", "--name=" + p.name, "--label=" + botHomeReaderLabel + "=" + p.name, "--label=" + botHomeScopeLabel + "=" + b.Scope.Key(), "--pull=never", "--network=none", "--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges", "--pids-limit=16", "--memory=256m", "--cpus=0.5", "--user=0:0", "--log-driver=none", homeMount, "--mount=type=volume,source=" + volume + ",target=/private-files,volume-nocopy", "--entrypoint=/bin/sh", db.cfg.SandboxImage, "-ec", "exec 8</private-files; exec 9</home-files; exec /usr/local/bin/astonish-host node --internal-home-archive admission-held"}
	if _, err = runDocker(ctx, db.cfg.ContainerRuntimePath, args...); err != nil {
		return p, err
	}
	raw, err = runDocker(ctx, db.cfg.ContainerRuntimePath, "inspect", "--format", "{{json .Mounts}}", p.name)
	if err != nil {
		return p, err
	}
	var mounts []struct {
		Type, Name, Destination string
		RW                      bool
	}
	if json.Unmarshal(raw, &mounts) != nil || len(mounts) != 2 {
		return p, errors.New("archive helper mount mismatch")
	}
	seen := map[string]bool{}
	for _, m := range mounts {
		if seen[m.Destination] || m.Type != "volume" {
			return p, errors.New("archive helper duplicate/type mount")
		}
		seen[m.Destination] = true
		if m.Destination == "/home-files" && m.Name == b.Ref && m.RW == restoring {
			continue
		}
		if m.Destination == "/private-files" && m.Name == volume && m.RW {
			continue
		}
		return p, errors.New("archive helper mount ownership mismatch")
	}
	if err = db.archiveAttachments(ctx, b, p.name, stopped); err != nil {
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
	q["binding"] = b.Scope.Key()
	if err = json.NewEncoder(p.input).Encode(q); err != nil {
		return p, err
	}
	go func(proc *homeFileProcess) {
		select {
		case <-ctx.Done():
			_ = proc.finish(true)
		case <-proc.done:
		}
	}(p)
	return p, nil
}
func archiveManifestLine(p *homeFileProcess, m *sandbox.BotHomeArchiveManifest) error {
	var line []byte
	for {
		v, prefix, e := p.reader.ReadLine()
		if e != nil {
			return e
		}
		if len(line)+len(v) > sandbox.MaxBotHomeArchiveManifestBytes {
			return errors.New("archive manifest bound")
		}
		line = append(line, v...)
		if !prefix {
			break
		}
	}
	if e := json.Unmarshal(line, m); e != nil {
		return e
	}
	return m.Validate()
}
func archiveCopy(dst io.Writer, src io.Reader, m *sandbox.BotHomeArchiveManifest) error {
	for _, entry := range m.Entries {
		if entry.Directory {
			continue
		}
		hash := sha256.New()
		if _, e := io.CopyN(io.MultiWriter(dst, hash), src, entry.Size); e != nil {
			return e
		}
		if hex.EncodeToString(hash.Sum(nil)) != entry.SHA256 {
			return errors.New("archive file digest mismatch")
		}
	}
	return nil
}
func (db *DockerBackend) ExportBotHomeArchive(ctx context.Context, b *sandbox.BotHomeBinding, out io.Writer, barrier sandbox.BotHomeArchiveBarrier) (*sandbox.BotHomeArchiveManifest, error) {
	if out == nil {
		return nil, errors.New("archive output required")
	}
	var m sandbox.BotHomeArchiveManifest
	err := archiveBarrier(ctx, b, barrier, func(c context.Context) (err error) {
		admission, err := db.lockBotHomeArchiveAdmission(c, b, false)
		if err != nil {
			return err
		}
		defer func() { err = errors.Join(err, releaseBotHomeAdmission(admission)) }()
		stopped, err := db.archiveStoppedTarget(c, b)
		if err != nil {
			return err
		}
		p, err := db.startHomeArchive(c, b, map[string]any{"op": "export"}, stopped)
		if err != nil {
			return err
		}
		defer func() { err = errors.Join(err, p.Close()) }()
		if err = archiveManifestLine(p, &m); err != nil {
			return err
		}
		if m.Binding != b.Scope.Key() {
			return errors.New("archive source binding mismatch")
		}
		if err = db.archiveAttachments(c, b, p.name, stopped); err != nil {
			return err
		}
		if _, err = io.WriteString(p.input, "commit\n"); err != nil {
			return err
		}
		if err = archiveCopy(out, p.reader, &m); err != nil {
			return err
		}
		if _, err = p.reader.ReadByte(); err != io.EOF {
			return errors.New("archive trailing bytes or stream error")
		}
		if err = p.finish(false); err != nil {
			return err
		}
		return db.archiveAttachments(c, b, "", stopped)
	})
	if err != nil {
		return nil, err
	}
	return &m, nil
}
func (db *DockerBackend) RestoreBotHomeArchive(ctx context.Context, b *sandbox.BotHomeBinding, m *sandbox.BotHomeArchiveManifest, in io.Reader, barrier sandbox.BotHomeArchiveBarrier) error {
	if err := m.Validate(); err != nil {
		return err
	}
	if in == nil || b == nil || (b.Scope.Key() == m.Binding && (!validBotHomeVolume(b) || b.Ref == botHomeVolume(b.Scope))) {
		return errors.New("archive requires fresh destination and input")
	}
	// Clone the caller-owned metadata before crossing the guarded boundary.
	raw, _ := json.Marshal(m)
	var manifest sandbox.BotHomeArchiveManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return err
	}
	return archiveBarrier(ctx, b, barrier, func(c context.Context) (err error) {
		admission, err := db.lockBotHomeArchiveAdmission(c, b, true)
		if err != nil {
			return err
		}
		defer func() { err = errors.Join(err, releaseBotHomeAdmission(admission)) }()
		p, err := db.startHomeArchive(c, b, map[string]any{"op": "restore", "manifest": &manifest}, "")
		if err != nil {
			return err
		}
		defer func() { err = errors.Join(err, p.Close()) }()
		var ready struct{ Ready bool }
		if err = p.line(&ready); err != nil {
			return err
		}
		if !ready.Ready {
			return errors.New("archive target not ready")
		}
		if err = archiveCopy(p.input, in, &manifest); err != nil {
			return err
		}
		var extra [1]byte
		if n, e := io.ReadFull(in, extra[:]); n != 0 || e != io.EOF {
			return errors.New("archive trailing input")
		}
		var staged struct{ Commit bool }
		if err = p.line(&staged); err != nil {
			return err
		}
		if !staged.Commit {
			return errors.New("archive stage not verified")
		}
		if err = c.Err(); err != nil {
			return err
		}
		if err = db.archiveDetached(c, b, p.name); err != nil {
			return err
		}
		if _, err = io.WriteString(p.input, "commit\n"); err != nil {
			return err
		}
		var done struct{ Restored bool }
		if err = p.line(&done); err != nil {
			return err
		}
		if !done.Restored {
			return fmt.Errorf("archive restore not acknowledged")
		}
		if err = p.finish(false); err != nil {
			return err
		}
		return db.archiveDetached(c, b, "")
	})
}
