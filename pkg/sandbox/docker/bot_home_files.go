package docker

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

var _ sandbox.BotHomeFilesBackend = (*DockerBackend)(nil)

// botFileAccessSession selects existing compute only when it is already running.
// No binding (cold restore) or absent compute uses owned storage, not a launch.
// Binding/recipe/inspection errors are terminal, never fallback signals.
func (db *DockerBackend) botFileAccessSession(ctx context.Context, b *sandbox.BotHomeBinding) (string, error) {
	value, ok := db.botFileSessions.Load(*b)
	if !ok {
		return "", nil
	}
	id := value.(string)
	meta, err := db.readSessionMetadata(id)
	if err != nil {
		return "", err
	}
	if meta != nil && !meta.BotHome.Equal(b) {
		return "", errors.New("sandbox/docker: Bot session Home mismatch")
	}
	state, err := db.containerState(ctx, containerName(id))
	if err != nil {
		return "", err
	}
	if state == sandbox.SessionStateGone || state == sandbox.SessionStateStopped {
		return "", nil
	}
	if state != sandbox.SessionStateRunning || meta == nil {
		return "", errors.New("sandbox/docker: unavailable authorized file session")
	}
	return id, nil
}

// existingHomeFiles is an operation-owned inert storage attachment, not a Bot
// session or a backend pool. Each borrow runs one compiled process and joins it.
type existingHomeFiles struct {
	db              *DockerBackend
	binding         *sandbox.BotHomeBinding
	name, target    string
	private, mutate bool
}

func (h *existingHomeFiles) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if h.target == "" {
		// No physical identity was established during startup. Retain the fresh
		// ownership check for this ambiguous-create cleanup only.
		return h.db.finishBotHomeHelper(ctx, h.name, h.binding.Scope.Key(), false)
	}
	// Names can be reused between validation and release. Cancellation owns the
	// original attachment, never a same-name replacement (even with its labels).
	raw, err := runDocker(ctx, h.db.cfg.ContainerRuntimePath, "inspect", "--format", "{{json .}}", h.target)
	if err != nil {
		if isDockerNotFoundError(err) {
			return nil
		}
		return err
	}
	var c struct {
		ID     string `json:"Id"`
		Config struct{ Labels map[string]string }
	}
	if json.Unmarshal(raw, &c) != nil || c.ID != h.target || c.Config.Labels[botHomeReaderLabel] != h.name || c.Config.Labels[botHomeScopeLabel] != h.binding.Scope.Key() {
		return errors.New("existing Home attachment cleanup identity mismatch")
	}
	_, err = runDocker(ctx, h.db.cfg.ContainerRuntimePath, "rm", "-fv", h.target)
	return err
}

func (h *existingHomeFiles) validate(ctx context.Context, running bool) error {
	if err := h.db.validateBotHome(ctx, h.binding); err != nil {
		return err
	}
	if h.private {
		if err := h.db.validateExistingPrivate(ctx, h.binding); err != nil {
			return err
		}
	}
	raw, err := runDocker(ctx, h.db.cfg.ContainerRuntimePath, "inspect", "--format", "{{json .}}", h.name)
	if err != nil {
		return err
	}
	var c struct {
		ID     string `json:"Id"`
		State  struct{ Running bool }
		Config struct{ Labels map[string]string }
		Mounts []struct {
			Type, Name, Destination string
			RW                      bool
		}
	}
	if json.Unmarshal(raw, &c) != nil || c.ID == "" || c.State.Running != running || c.Config.Labels[botHomeReaderLabel] != h.name || c.Config.Labels[botHomeScopeLabel] != h.binding.Scope.Key() || (h.target != "" && h.target != c.ID) {
		return errors.New("existing Home attachment identity mismatch")
	}
	want := 2 // Home plus private volume, or an empty read-only private root.
	if len(c.Mounts) != want {
		return errors.New("existing Home attachment mounts mismatch")
	}
	seen := map[string]bool{}
	for _, m := range c.Mounts {
		if seen[m.Destination] {
			return errors.New("duplicate Home attachment mount")
		}
		seen[m.Destination] = true
		switch m.Destination {
		case botHomeMount:
			if m.Type != "volume" || m.Name != h.binding.Ref || m.RW != h.mutate {
				return errors.New("existing Home mount mismatch")
			}
		case botPrivateMount:
			if h.private {
				if m.Type != "volume" || m.Name != workspaceVolume(h.binding) || m.RW != h.mutate {
					return errors.New("existing private mount mismatch")
				}
			} else if m.Type != "tmpfs" || m.RW {
				return errors.New("empty private mount mismatch")
			}
		default:
			return errors.New("unexpected Home attachment mount")
		}
	}
	h.target = c.ID
	return h.db.rejectHomeArchiveAttachment(ctx, h.binding)
}

func (db *DockerBackend) validateExistingPrivate(ctx context.Context, b *sandbox.BotHomeBinding) error {
	raw, err := runDocker(ctx, db.cfg.ContainerRuntimePath, "volume", "inspect", workspaceVolume(b))
	if err != nil {
		return err
	}
	var info []struct {
		Name, Driver    string
		Labels, Options map[string]string
	}
	if json.Unmarshal(raw, &info) != nil || len(info) != 1 || info[0].Name != workspaceVolume(b) || info[0].Driver != "local" || len(info[0].Options) != 0 || info[0].Labels[botHomeScopeLabel] != b.Scope.Key() || info[0].Labels[botHomeVersionLabel] != "workspace-1" {
		return errors.New("existing private ownership mismatch")
	}
	return nil
}

func (db *DockerBackend) borrowExistingHomeFiles(ctx context.Context, b *sandbox.BotHomeBinding, q map[string]any) (context.Context, string, func(bool) error, error) {
	private := q["private"] == true || q["op"] == "put" || q["op"] == "snapshot"
	mutate := q["op"] == "write" || q["op"] == "put" || q["op"] == "snapshot"
	key := struct {
		DB              *DockerBackend
		Binding         sandbox.BotHomeBinding
		Private, Mutate bool
	}{db, *b, private, mutate}
	create := func(c context.Context) (resource sandbox.BotHomeFileResource, err error) {
		if db.cfg.SandboxImage == "" {
			return nil, errors.New("compiled files runtime image required")
		}
		if private {
			if mutate {
				_, err = db.ensureWorkspaceVolume(c, b)
			} else {
				err = db.validateExistingPrivate(c, b)
			}
			if err != nil {
				return nil, err
			}
		}
		// Serialize mutating attachment admission with archive when private
		// storage exists. Home-only reads never allocate that storage.
		var admission *homeFileProcess
		if e := db.validateExistingPrivate(c, b); e == nil {
			// Read-only attachments cannot alter capture or restore and must not
			// wait on a writer's lock from inside its authorization callback.
			if mutate {
				admission, err = db.lockBotHomeArchiveAdmission(c, b, false)
				if err != nil {
					return nil, err
				}
			}
		} else if !isDockerNotFoundError(e) && !strings.Contains(strings.ToLower(e.Error()), "no such volume") {
			return nil, e
		}
		defer func() {
			err = errors.Join(err, releaseBotHomeAdmission(admission))
			if err != nil && resource != nil {
				err = errors.Join(err, resource.Close())
				resource = nil
			}
		}()
		if err = db.rejectHomeArchiveAttachment(c, b); err != nil {
			return nil, err
		}
		h := &existingHomeFiles{db: db, binding: b.Clone(), name: "astonish-home-files-" + uuid.NewString(), private: private, mutate: mutate}
		resource = h
		mount := "--mount=type=volume,source=" + b.Ref + ",target=" + botHomeMount + ",volume-nocopy"
		if !mutate {
			mount += ",readonly"
		}
		args := []string{"container", "create", "--name=" + h.name, "--label=" + botHomeReaderLabel + "=" + h.name, "--label=" + botHomeScopeLabel + "=" + b.Scope.Key(), "--pull=never", "--network=none", "--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges", "--pids-limit=64", "--memory=256m", "--cpus=0.5", "--user=0:0", "--log-driver=none", mount}
		if private {
			mount = "--mount=type=volume,source=" + workspaceVolume(b) + ",target=" + botPrivateMount + ",volume-nocopy"
			if !mutate {
				mount += ",readonly"
			}
		} else {
			mount = "--mount=type=tmpfs,destination=" + botPrivateMount + ",readonly,tmpfs-size=4096"
		}
		args = append(args, mount, "--entrypoint=/bin/sleep", db.cfg.SandboxImage, "infinity")
		if _, err = runDocker(c, db.cfg.ContainerRuntimePath, args...); err != nil {
			return resource, err
		}
		if err = h.validate(c, false); err != nil {
			return resource, err
		}
		if _, err = runDocker(c, db.cfg.ContainerRuntimePath, "start", h.target); err != nil {
			return resource, err
		}
		if err = h.validate(c, true); err != nil {
			return resource, err
		}
		return resource, nil
	}
	operation := sandbox.BotHomeFileOperationFromContext(ctx)
	if operation == nil {
		var end func() error
		ctx, end = sandbox.WithBotHomeFileOperation(ctx)
		operation = sandbox.BotHomeFileOperationFromContext(ctx)
		c, resource, release, err := operation.Borrow(ctx, key, create)
		if err != nil {
			return ctx, "", nil, errors.Join(err, end())
		}
		h := resource.(*existingHomeFiles)
		return c, h.target, func(healthy bool) error { return errors.Join(release(healthy), end()) }, nil
	}
	c, resource, release, err := operation.Borrow(ctx, key, create)
	if err != nil {
		return ctx, "", nil, err
	}
	h := resource.(*existingHomeFiles)
	if err = h.validate(c, true); err != nil {
		return ctx, "", nil, errors.Join(err, release(false))
	}
	return c, h.target, release, nil
}

// homeFileProcess owns one compiled exec or an archive/admission helper.
type homeFileProcess struct {
	sessionOwned bool
	db           *DockerBackend
	binding      *sandbox.BotHomeBinding
	name         string
	cmd          *exec.Cmd
	input        io.WriteCloser
	output       io.ReadCloser
	reader       *bufio.Reader
	cancel       context.CancelFunc
	stderr       *artifactBuffer
	once         sync.Once
	done         chan struct{}
	release      func(bool) error
	ctx          context.Context
	err          error
}

func (p *homeFileProcess) finish(cancel bool) error {
	// Cancellation must happen before once.Do: a concurrent EOF waiter may
	// already own the join while the Docker attachment is still running.
	if cancel {
		p.cancel()
		if p.output != nil {
			_ = p.output.Close()
		}
	}
	p.once.Do(func() {
		defer close(p.done)
		if p.input != nil {
			_ = p.input.Close()
		}
		var err error
		started := p.cmd != nil && p.cmd.Process != nil
		if started {
			// Wait also joins exec's stderr copier before String is called.
			err = p.cmd.Wait()
			if err != nil && p.stderr.String() != "" {
				err = fmt.Errorf("file helper: %w: %s", err, p.stderr.String())
			}
		}
		if p.output != nil {
			_ = p.output.Close()
		}
		if p.sessionOwned {
			p.err = err
			p.cancel()
			return
		}
		if p.release != nil {
			// Exec status is authoritative; the inert container is still running.
			p.err = errors.Join(err, p.release(err == nil && !cancel && p.ctx.Err() == nil))
			p.cancel()
			return
		}
		p.cancel()
		c, stop := context.WithTimeout(context.Background(), 15*time.Second)
		defer stop()
		p.err = errors.Join(err, p.db.finishBotHomeHelper(c, p.name, p.binding.Scope.Key(), started))
	})
	return p.err
}
func (p *homeFileProcess) Read(b []byte) (int, error) {
	n, e := p.reader.Read(b)
	if e != nil {
		if done := p.finish(e != io.EOF); done != nil {
			return n, done
		}
	}
	return n, e
}
func (p *homeFileProcess) Close() error { return p.finish(true) }
func (p *homeFileProcess) line(out any) error {
	// Listing metadata is bounded by 500 canonical paths, never file bodies.
	var line []byte
	for {
		part, e := p.reader.ReadSlice('\n')
		line = append(line, part...)
		if len(line) > 4<<20 {
			return errors.New("file metadata limit")
		}
		if e == bufio.ErrBufferFull {
			continue
		}
		if e != nil {
			return e
		}
		break
	}
	return sandbox.DecodeBotHomeFileLine(line, out)
}
func (db *DockerBackend) startHomeFiles(ctx context.Context, b *sandbox.BotHomeBinding, q map[string]any) (p *homeFileProcess, err error) {
	// Reject aliases and malformed metadata before inspecting or provisioning
	// storage, not merely inside a newly launched privileged helper.
	op, _ := q["op"].(string)
	switch op {
	case "list", "stat", "open", "write", "snapshot":
		path, ok := q["path"].(string)
		if !ok || (op != "list" || path != ".") && sandbox.ValidateBotHomeArtifactPath(path) != nil {
			return nil, store.ErrBotInvalid
		}
	case "put":
	default:
		return nil, store.ErrBotInvalid
	}
	if after, ok := q["after"]; ok && after != "" {
		path, ok := after.(string)
		if !ok || sandbox.ValidateBotHomeArtifactPath(path) != nil {
			return nil, store.ErrBotInvalid
		}
	}
	if err = validateBotHomeReference(b); err != nil {
		return nil, err
	}
	if err = db.validateBotHome(ctx, b); err != nil {
		return nil, err
	}
	// Select storage-only attachment before dispatch, never as an exec retry.
	id, e := db.botFileAccessSession(ctx, b)
	if e != nil {
		return nil, e
	}
	var target string
	var release func(bool) error
	if id != "" {
		target, e = db.resolveBotFileTarget(ctx, id, b)
	} else {
		ctx, target, release, e = db.borrowExistingHomeFiles(ctx, b, q)
	}
	if e != nil {
		return nil, e
	}
	ctx, cancel := context.WithCancel(ctx)
	p = &homeFileProcess{db: db, binding: b.Clone(), name: target, cancel: cancel, stderr: &artifactBuffer{remaining: 4096}, done: make(chan struct{}), sessionOwned: id != "", release: release, ctx: ctx}
	defer func() {
		if err != nil {
			err = errors.Join(err, p.finish(true))
			p = nil
		}
	}()
	p.cmd = db.botCompiledFilesCommand(ctx, target)
	if id == "" {
		// An attachment is exclusively borrowed; cancelling it removes the owned
		// container after joining this exec. No writable /tmp or PID markers needed.
		p.cmd = exec.CommandContext(ctx, db.cfg.ContainerRuntimePath, "exec", "--interactive", target, "/bin/sh", "-ec", fmt.Sprintf("exec 8<%s; exec 9<%s; exec /usr/local/bin/astonish-host node --internal-home-files", botPrivateMount, botHomeMount))
		p.cmd.WaitDelay = 3 * time.Second
	}
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
func (p *homeFileProcess) response(out any) error {
	err := p.line(out)
	if err != nil {
		return errors.Join(err, p.finish(err != io.EOF && !sandbox.BotHomeFileTerminalError(err)))
	}
	return nil
}
func (db *DockerBackend) homeFileJSON(ctx context.Context, b *sandbox.BotHomeBinding, q map[string]any, out any) (err error) {
	p, e := db.startHomeFiles(ctx, b, q)
	if e != nil {
		return e
	}
	defer func() { err = errors.Join(err, p.Close()) }()
	if e = p.response(out); e != nil {
		return e
	}
	return p.finish(false)
}
func (db *DockerBackend) ListBotHomeFiles(ctx context.Context, b *sandbox.BotHomeBinding, path, after string, n int) (*store.BotFilePage, error) {
	if n == 0 {
		n = store.BotFilePageDefault
	}
	if n < 1 || n > store.BotFilePageMax {
		return nil, store.ErrBotInvalid
	}
	var result store.BotFilePage
	if e := db.homeFileJSON(ctx, b, map[string]any{"op": "list", "path": path, "after": after, "limit": n}, &result); e != nil {
		return nil, e
	}
	return &result, nil
}
func (db *DockerBackend) StatBotHomeFile(ctx context.Context, b *sandbox.BotHomeBinding, path string) (*store.BotFileEntry, error) {
	var result store.BotFileEntry
	if e := db.homeFileJSON(ctx, b, map[string]any{"op": "stat", "path": path}, &result); e != nil {
		return nil, e
	}
	return &result, nil
}
func (db *DockerBackend) openHomeFile(ctx context.Context, b *sandbox.BotHomeBinding, q map[string]any, r store.BotFileRange) (*store.BotFileStream, error) {
	if e := r.Validate(); e != nil {
		return nil, e
	}
	q["op"] = "open"
	q["offset"] = r.Offset
	q["length"] = r.Length
	p, e := db.startHomeFiles(ctx, b, q)
	if e != nil {
		return nil, e
	}
	var entry store.BotFileEntry
	if e = p.response(&entry); e != nil {
		_ = p.Close()
		return nil, e
	}
	_ = p.input.Close()
	return &store.BotFileStream{ReadCloser: p, Entry: entry}, nil
}
func (db *DockerBackend) OpenBotHomeFile(ctx context.Context, b *sandbox.BotHomeBinding, path string, r store.BotFileRange) (*store.BotFileStream, error) {
	return db.openHomeFile(ctx, b, map[string]any{"path": path}, r)
}
func (db *DockerBackend) mutateHomeFile(ctx context.Context, b *sandbox.BotHomeBinding, q map[string]any, body io.Reader, size int64, barrier func(func() error) error, readback ...*[]byte) (entry *store.BotFileEntry, err error) {
	if barrier == nil {
		return nil, store.ErrBotForbidden
	}
	p, e := db.startHomeFiles(ctx, b, q)
	if e != nil {
		return nil, e
	}
	defer func() { err = errors.Join(err, p.Close()) }()
	if closer, ok := body.(io.Closer); ok {
		// A blocked source Read cannot be interrupted by closing helper stdin.
		stop := context.AfterFunc(ctx, func() { _ = closer.Close() })
		defer stop()
	}
	if body != nil {
		if _, e = io.CopyN(p.input, body, size); e != nil {
			return nil, e
		}
	}
	var ready struct{ Commit bool }
	if e = p.response(&ready); e != nil {
		return nil, e
	}
	if !ready.Commit {
		return nil, errors.New("missing commit barrier")
	}
	var result store.BotFileEntry
	called := false
	var commitErr error
	e = barrier(func() (err error) {
		defer func() { commitErr = errors.Join(commitErr, err) }()
		if called {
			return store.ErrBotConflict
		}
		called = true
		if e := ctx.Err(); e != nil {
			return e
		}
		if _, e := io.WriteString(p.input, "commit\n"); e != nil {
			return e
		}
		if e := p.response(&result); e != nil {
			return e
		}
		if len(readback) == 1 {
			raw, e := sandbox.ReadBotHomeContentReadback(p.reader, size, result.SHA256)
			if e != nil {
				return e
			}
			*readback[0] = raw
		}
		return p.finish(false)
	})
	e = errors.Join(e, commitErr)
	if e != nil {
		return nil, e
	}
	if !called {
		return nil, store.ErrBotForbidden
	}
	return &result, nil
}
func (db *DockerBackend) WriteBotHomeFile(ctx context.Context, b *sandbox.BotHomeBinding, w store.BotFileWrite) (*store.BotFileEntry, error) {
	if w.SizeBytes < 0 || w.Body == nil || w.CreateOnly == (w.ExpectedSHA256 != "") {
		return nil, store.ErrBotInvalid
	}
	return db.mutateHomeFile(ctx, b, map[string]any{"op": "write", "path": w.Path, "size": w.SizeBytes, "create_only": w.CreateOnly, "expected": w.ExpectedSHA256}, w.Body, w.SizeBytes, w.BeforeCommit)
}

// validContentKey binds semantic metadata to the server-selected immutable name.
func validContentKey(ref store.BotContentRef) bool {
	prefix := fmt.Sprintf("content-v1/%s-%d-", ref.Kind, ref.Revision)
	id := strings.TrimPrefix(ref.Key, prefix)
	parsed, err := uuid.Parse(id)
	return strings.HasPrefix(ref.Key, prefix) && err == nil && strings.ReplaceAll(parsed.String(), "-", "") == id
}
func fileContentScope(b *sandbox.BotHomeBinding) (store.BotContentScope, error) {
	if b == nil {
		return store.BotContentScope{}, store.ErrBotInvalid
	}
	id, e := uuid.Parse(b.Scope.BotID)
	if e != nil {
		return store.BotContentScope{}, store.ErrBotInvalid
	}
	s := store.BotContentScope{OrgID: b.Scope.OrgID, TeamID: b.Scope.TeamID, OwnerID: b.Scope.UserID, BotID: id}
	return s, s.Validate()
}
func contentRef(b *sandbox.BotHomeBinding, k store.BotContentKind, revision int64, e *store.BotFileEntry) (*store.BotContentRef, error) {
	scope, err := fileContentScope(b)
	if err != nil {
		return nil, err
	}
	r := &store.BotContentRef{Format: store.BotContentFormatVersion, Scope: scope, Kind: k, Key: e.Path, SHA256: e.SHA256, SizeBytes: e.SizeBytes, Revision: revision}
	return r, r.Validate()
}
func (db *DockerBackend) PutBotContent(ctx context.Context, b *sandbox.BotHomeBinding, k store.BotContentKind, size int64, body io.Reader, revision int64, barrier func(func() error) error) (*store.BotContentRef, error) {
	if size < 0 || body == nil {
		return nil, store.ErrBotInvalid
	}
	if _, e := contentRef(b, k, revision, &store.BotFileEntry{Path: "content-v1/check", SHA256: strings.Repeat("0", 64)}); e != nil {
		return nil, e
	}
	entry, e := db.mutateHomeFile(ctx, b, map[string]any{"op": "put", "size": size, "kind": k, "revision": revision}, body, size, barrier)
	if e != nil {
		return nil, e
	}
	return contentRef(b, k, revision, entry)
}

var _ sandbox.BotHomeContentReadbackBackend = (*DockerBackend)(nil)

func (db *DockerBackend) PutBotContentReadback(ctx context.Context, b *sandbox.BotHomeBinding, k store.BotContentKind, size int64, body io.Reader, revision int64, barrier func(func() error) error) (*store.BotContentRef, []byte, error) {
	if size < 0 || size > sandbox.BotHomeContentReadbackLimit || body == nil {
		return nil, nil, store.ErrBotInvalid
	}
	if _, err := contentRef(b, k, revision, &store.BotFileEntry{Path: "content-v1/check", SHA256: strings.Repeat("0", 64)}); err != nil {
		return nil, nil, err
	}
	var raw []byte
	entry, err := db.mutateHomeFile(ctx, b, map[string]any{"op": "put", "size": size, "kind": k, "revision": revision, "readback": true}, body, size, barrier, &raw)
	if err != nil {
		return nil, nil, err
	}
	ref, err := contentRef(b, k, revision, entry)
	if err != nil {
		return nil, nil, err
	}
	return ref, raw, nil
}

func (db *DockerBackend) OpenBotContent(ctx context.Context, b *sandbox.BotHomeBinding, ref store.BotContentRef, r store.BotFileRange) (*store.BotFileStream, error) {
	scope, e := fileContentScope(b)
	if e != nil {
		return nil, e
	}
	if ref.Validate() != nil || ref.Scope != scope || !validContentKey(ref) {
		return nil, store.ErrBotForbidden
	}
	return db.openHomeFile(ctx, b, map[string]any{"path": ref.Key, "private": true, "sha256": ref.SHA256, "size": ref.SizeBytes}, r)
}
func (db *DockerBackend) PublishBotArtifactSnapshot(ctx context.Context, b *sandbox.BotHomeBinding, path, name string, barrier func(func() error) error) (*store.BotContentRef, error) {
	if _, e := fileContentScope(b); e != nil {
		return nil, e
	}
	entry, e := db.mutateHomeFile(ctx, b, map[string]any{"op": "snapshot", "path": path}, nil, 0, barrier)
	if e != nil {
		return nil, e
	}
	return contentRef(b, store.BotContentArtifact, 1, entry)
}
