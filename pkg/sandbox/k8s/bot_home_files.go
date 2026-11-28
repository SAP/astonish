package k8s

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/remotecommand"
)

var _ sandbox.BotHomeFilesBackend = (*K8sBackend)(nil)

// homeFileProcess owns one exec stream, plus an offline attachment when used.
// Warm session streams never own pod deletion or the session admission.
type homeFileProcess struct {
	backend            *K8sBackend
	binding            *sandbox.BotHomeBinding
	name, token        string
	uid                types.UID
	sessionOwned       bool
	submitted, settled bool
	input              *io.PipeWriter
	output             *io.PipeReader
	reader             *bufio.Reader
	cancel             context.CancelFunc
	done               chan error
	once               sync.Once
	err                error
	remaining          int64
}

func (p *homeFileProcess) cleanup() error {
	if p.sessionOwned {
		return nil
	} // Never delete shared run compute or its admission.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if !p.submitted {
		return p.backend.cancelUnsubmittedBotHome(ctx, p.binding, p.token)
	}
	// An ambiguous create must retain its pending admission. Never delete by name.
	if p.uid == "" {
		return nil
	}
	pods := p.backend.client.CoreV1().Pods(p.backend.cfg.Namespace)
	current, err := pods.Get(ctx, p.name, metav1.GetOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	if err == nil {
		if current.UID != p.uid {
			return errors.New("sandbox/k8s: files cleanup identity changed")
		}
		zero := int64(0)
		err = pods.Delete(ctx, p.name, metav1.DeleteOptions{GracePeriodSeconds: &zero, Preconditions: &metav1.Preconditions{UID: &p.uid}})
		if err != nil && !apierrors.IsNotFound(err) {
			return err
		}
	}
	if !p.settled {
		return nil
	}
	// This rechecks absence; a successful DELETE alone does not release a finalizer.
	return p.backend.releaseBotHomePod(ctx, p.binding, p.name)
}
func (p *homeFileProcess) finish(abort bool) error {
	// Cancel before joining: an EOF reader may already own once while waiting
	// for the remote exec to exit. Close must be able to interrupt that wait.
	if abort {
		p.cancel()
		if p.output != nil {
			_ = p.output.Close()
		}
	}
	p.once.Do(func() {
		if p.input != nil {
			_ = p.input.Close()
		}
		if p.done != nil {
			p.err = <-p.done
		}
		p.cancel()
		if p.output != nil {
			_ = p.output.Close()
		}
		p.err = errors.Join(p.err, p.cleanup())
	})
	return p.err
}
func (p *homeFileProcess) Close() error { return p.finish(true) }
func (p *homeFileProcess) Read(b []byte) (int, error) {
	n, err := p.reader.Read(b)
	if p.remaining >= 0 {
		p.remaining -= int64(n)
		if p.remaining < 0 {
			return n, errors.Join(errors.New("file stream exceeds range"), p.finish(true))
		}
	}
	if err == io.EOF {
		if p.remaining > 0 {
			err = io.ErrUnexpectedEOF
		}
		if done := p.finish(false); done != nil {
			err = errors.Join(err, done)
		}
	} else if err != nil {
		err = errors.Join(err, p.finish(true))
	}
	return n, err
}
func (p *homeFileProcess) line(out any) error {
	var line []byte
	for {
		part, err := p.reader.ReadSlice('\n')
		if len(line)+len(part) > 4<<20 {
			return errors.New("file metadata exceeds bound")
		}
		line = append(line, part...)
		if err == bufio.ErrBufferFull {
			continue
		}
		if err != nil {
			return err
		}
		return sandbox.DecodeBotHomeFileLine(line, out)
	}
}

// Only server-selected root and mode arguments reach this fixed entrypoint. The
// shell opens one directory descriptor; compiled code validates ownership and
// all relative paths. No overlay executable or PATH lookup is involved.
func botHomeFilesCommand(h *sandbox.BotHomeBinding, mount string, private, mutate bool) []string {
	visibility, access := "public", "read"
	if private {
		visibility = "private"
	}
	if mutate {
		access = "mutate"
	}
	return []string{"/bin/sh", "-ec", "exec 9<" + shellQuote(mount) + "; exec /usr/local/bin/astonish-host node --internal-scoped-home-files " + shellQuote(botHomeStorageOwner(h)) + " " + visibility + " " + access}
}

func (b *K8sBackend) startHomeFiles(ctx context.Context, h *sandbox.BotHomeBinding, q map[string]any) (p *homeFileProcess, err error) {
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	if h == nil {
		return nil, store.ErrBotInvalid
	}
	// Reject malformed requests before attachment admission or any Kubernetes
	// access. The confined runtime still validates paths against actual roots;
	// this check must never be used as a substitute for descriptor confinement.
	op, _ := q["op"].(string)
	switch op {
	case "list", "stat", "open", "write", "snapshot":
		path, ok := q["path"].(string)
		if !ok || (op != "list" || path != ".") && sandbox.ValidateBotHomeArtifactPath(path) != nil {
			return nil, store.ErrBotInvalid
		}
	case "put": // The immutable object path is generated by the runtime.
	default:
		return nil, store.ErrBotInvalid
	}
	if value, present := q["after"]; present {
		after, ok := value.(string)
		if !ok || after != "" && sandbox.ValidateBotHomeArtifactPath(after) != nil {
			return nil, store.ErrBotInvalid
		}
	}
	if err = b.validateBotHome(h); err != nil {
		return nil, err
	}
	if b.client == nil || b.restConfig == nil || b.execExecutorFactory == nil {
		return nil, sandbox.ErrBotHomeUnsupported
	}
	setup, stop := context.WithTimeout(ctx, 2*time.Minute)
	defer stop()
	target, err := b.resolveBotHomeFileTarget(setup, h)
	if err != nil {
		return nil, err
	}
	name := "astn-home-files-" + uuid.NewString()
	var token string
	if target != nil {
		name = target.name
	} else {
		token, err = b.reserveBotHome(setup, h, name)
		if err != nil {
			return nil, err
		}
	}
	streamCtx, cancel := context.WithCancel(ctx)
	p = &homeFileProcess{backend: b, binding: h.Clone(), name: name, token: token, cancel: cancel, remaining: -1}
	defer func() {
		if err != nil {
			err = errors.Join(err, p.finish(true))
			p = nil
		}
	}()
	mutate := q["op"] == "write" || q["op"] == "put" || q["op"] == "snapshot"
	mount := mountBotHome
	if target != nil {
		p.sessionOwned, p.uid, mount = true, target.uid, mountBotHomeScope
	} else {
		expected := b.botWorkspacePod(h, name)
		// Startup is bounded, but the caller owns the stream lifetime. A fixed pod
		// deadline or sleep(120) would silently truncate valid large/paused downloads.
		expected.Spec.ActiveDeadlineSeconds = nil
		expected.Spec.Containers[0].Image = b.cfg.SandboxImage
		expected.Spec.Containers[0].Command = []string{"/bin/sleep", "infinity"}
		expected.Spec.Containers[0].VolumeMounts[0].ReadOnly = !mutate
		pods := b.client.CoreV1().Pods(b.cfg.Namespace)
		if err = setup.Err(); err != nil {
			return p, err
		}
		p.submitted = true
		created, err := pods.Create(setup, expected, metav1.CreateOptions{})
		if err != nil {
			return p, err
		}
		p.uid = created.UID
		if p.uid == "" || !safeBotWorkspacePod(created, expected) {
			return p, errors.New("sandbox/k8s: files helper admission changed")
		}
		if err = b.settleBotHome(setup, h, token); err != nil {
			return p, err
		}
		p.settled = true
		for {
			current, e := pods.Get(setup, name, metav1.GetOptions{})
			if e != nil {
				return p, e
			}
			if current.UID != p.uid || !safeBotWorkspacePod(current, expected) {
				return p, errors.New("sandbox/k8s: files helper changed")
			}
			if current.Status.Phase == corev1.PodRunning && len(current.Status.ContainerStatuses) == 1 && current.Status.ContainerStatuses[0].Name == containerName && current.Status.ContainerStatuses[0].Ready && current.Status.ContainerStatuses[0].State.Running != nil {
				break
			}
			if current.Status.Phase == corev1.PodFailed || current.Status.Phase == corev1.PodSucceeded {
				return p, errors.New("sandbox/k8s: files helper terminated")
			}
			select {
			case <-setup.Done():
				return p, setup.Err()
			case <-time.After(100 * time.Millisecond):
			}
		}
	}
	private := q["private"] == true || q["op"] == "put" || q["op"] == "snapshot"
	command := botHomeFilesCommand(h, mount, private, mutate)
	if target != nil {
		command[2] = "test \"${ASTONISH_POD_UID:-}\" = " + shellQuote(string(target.uid)) + "; " + command[2]
		current, e := b.resolveBotHomeFileTarget(setup, h)
		if e != nil {
			return p, e
		}
		if current == nil || *current != *target {
			return p, errors.New("sandbox/k8s: Home exec target changed")
		}
	}
	method, u, err := b.buildExecURL(name, command, false, true, true, true)
	if err != nil {
		return p, err
	}
	executor, err := b.execExecutorFactory(b.restConfig, method, u)
	if err != nil {
		return p, err
	}
	input, writer := io.Pipe()
	output, producer := io.Pipe()
	p.input = writer
	p.output = output
	p.reader = bufio.NewReader(output)
	p.done = make(chan error, 1)
	stderr := &botHomeBoundedBuffer{limit: 4096}
	// Cancellation closes local pipes too: remotecommand can be blocked writing
	// stdout to a paused reader, even after its network context is cancelled.
	unblock := context.AfterFunc(streamCtx, func() { _ = input.CloseWithError(streamCtx.Err()); _ = producer.CloseWithError(streamCtx.Err()) })
	go func() {
		e := executor.StreamWithContext(streamCtx, remotecommand.StreamOptions{Stdin: input, Stdout: producer, Stderr: stderr})
		if e != nil {
			e = fmt.Errorf("sandbox/k8s: files exec: %w: %s", e, stderr.String())
		}
		_ = input.CloseWithError(e)
		_ = producer.CloseWithError(e)
		unblock()
		p.done <- e
	}()
	// The initial protocol handoff is setup work too. Stop its timer before
	// handing ownership to the caller; it must not limit the download lifetime.
	stopSetup := context.AfterFunc(setup, cancel)
	if err = json.NewEncoder(writer).Encode(q); err != nil {
		stopSetup()
		return p, err
	}
	if !stopSetup() || setup.Err() != nil {
		return p, setup.Err()
	}
	// Once handed out, context cancellation also reaps a paused stream's helper.
	context.AfterFunc(streamCtx, func() { _ = p.finish(true) })
	return p, nil
}

func (p *homeFileProcess) response(out any) error {
	err := p.line(out)
	if err != nil {
		return errors.Join(err, p.finish(err != io.EOF && !sandbox.BotHomeFileTerminalError(err)))
	}
	return nil
}
func (b *K8sBackend) homeFileJSON(ctx context.Context, h *sandbox.BotHomeBinding, q map[string]any, out any) (err error) {
	p, err := b.startHomeFiles(ctx, h, q)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, p.Close()) }()
	if err = p.response(out); err != nil {
		return err
	}
	return p.finish(false)
}
func (b *K8sBackend) ListBotHomeFiles(ctx context.Context, h *sandbox.BotHomeBinding, path, after string, n int) (*store.BotFilePage, error) {
	if n == 0 {
		n = store.BotFilePageDefault
	}
	if n < 1 || n > store.BotFilePageMax {
		return nil, store.ErrBotInvalid
	}
	var result store.BotFilePage
	if err := b.homeFileJSON(ctx, h, map[string]any{"op": "list", "path": path, "after": after, "limit": n}, &result); err != nil {
		return nil, err
	}
	return &result, nil
}
func (b *K8sBackend) StatBotHomeFile(ctx context.Context, h *sandbox.BotHomeBinding, path string) (*store.BotFileEntry, error) {
	var result store.BotFileEntry
	if err := b.homeFileJSON(ctx, h, map[string]any{"op": "stat", "path": path}, &result); err != nil {
		return nil, err
	}
	return &result, nil
}
func (b *K8sBackend) openHomeFile(ctx context.Context, h *sandbox.BotHomeBinding, q map[string]any, r store.BotFileRange) (*store.BotFileStream, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	q["op"] = "open"
	q["offset"] = r.Offset
	q["length"] = r.Length
	p, err := b.startHomeFiles(ctx, h, q)
	if err != nil {
		return nil, err
	}
	var entry store.BotFileEntry
	if err = p.response(&entry); err != nil {
		return nil, errors.Join(err, p.Close())
	}
	if entry.SizeBytes < r.Offset {
		return nil, errors.Join(store.ErrBotInvalid, p.Close())
	}
	p.remaining = entry.SizeBytes - r.Offset
	if r.Length > 0 && r.Length < p.remaining {
		p.remaining = r.Length
	}
	_ = p.input.Close()
	return &store.BotFileStream{ReadCloser: p, Entry: entry}, nil
}
func (b *K8sBackend) OpenBotHomeFile(ctx context.Context, h *sandbox.BotHomeBinding, path string, r store.BotFileRange) (*store.BotFileStream, error) {
	return b.openHomeFile(ctx, h, map[string]any{"path": path}, r)
}

func (b *K8sBackend) mutateHomeFile(ctx context.Context, h *sandbox.BotHomeBinding, q map[string]any, body io.Reader, size int64, barrier func(func() error) error, readback ...*[]byte) (entry *store.BotFileEntry, err error) {
	if barrier == nil {
		return nil, store.ErrBotForbidden
	}
	p, err := b.startHomeFiles(ctx, h, q)
	if err != nil {
		return nil, err
	}
	defer func() {
		err = errors.Join(err, p.Close())
		if err != nil {
			entry = nil
		}
	}()
	if closer, ok := body.(io.Closer); ok {
		// A blocked source Read cannot be interrupted by closing helper stdin.
		stop := context.AfterFunc(ctx, func() { _ = closer.Close() })
		defer stop()
	}
	if body != nil {
		if _, err = io.CopyN(p.input, body, size); err != nil {
			return nil, err
		}
	}
	var ready struct{ Commit bool }
	if err = p.response(&ready); err != nil {
		return nil, err
	}
	if !ready.Commit {
		return nil, errors.New("missing file commit barrier")
	}
	var result store.BotFileEntry
	called := false
	var commitErr error
	err = barrier(func() (e error) {
		defer func() { commitErr = errors.Join(commitErr, e) }()
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
	err = errors.Join(err, commitErr)
	if err != nil {
		return nil, err
	}
	if !called {
		return nil, store.ErrBotForbidden
	}
	return &result, nil
}
func (b *K8sBackend) WriteBotHomeFile(ctx context.Context, h *sandbox.BotHomeBinding, w store.BotFileWrite) (*store.BotFileEntry, error) {
	if w.SizeBytes < 0 || w.Body == nil || w.CreateOnly == (w.ExpectedSHA256 != "") {
		return nil, store.ErrBotInvalid
	}
	return b.mutateHomeFile(ctx, h, map[string]any{"op": "write", "path": w.Path, "size": w.SizeBytes, "create_only": w.CreateOnly, "expected": w.ExpectedSHA256}, w.Body, w.SizeBytes, w.BeforeCommit)
}

// validContentKey binds semantic metadata to the server-selected immutable name.
func validContentKey(ref store.BotContentRef) bool {
	prefix := fmt.Sprintf("content-v1/%s-%d-", ref.Kind, ref.Revision)
	id := strings.TrimPrefix(ref.Key, prefix)
	parsed, err := uuid.Parse(id)
	return strings.HasPrefix(ref.Key, prefix) && err == nil && strings.ReplaceAll(parsed.String(), "-", "") == id
}
func fileContentScope(h *sandbox.BotHomeBinding) (store.BotContentScope, error) {
	if h == nil {
		return store.BotContentScope{}, store.ErrBotInvalid
	}
	id, err := uuid.Parse(h.Scope.BotID)
	if err != nil {
		return store.BotContentScope{}, store.ErrBotInvalid
	}
	s := store.BotContentScope{OrgID: h.Scope.OrgID, TeamID: h.Scope.TeamID, OwnerID: h.Scope.UserID, BotID: id}
	return s, s.Validate()
}
func contentRef(h *sandbox.BotHomeBinding, k store.BotContentKind, revision int64, e *store.BotFileEntry) (*store.BotContentRef, error) {
	scope, err := fileContentScope(h)
	if err != nil {
		return nil, err
	}
	r := &store.BotContentRef{Format: store.BotContentFormatVersion, Scope: scope, Kind: k, Key: e.Path, SHA256: e.SHA256, SizeBytes: e.SizeBytes, Revision: revision}
	return r, r.Validate()
}
func (b *K8sBackend) PutBotContent(ctx context.Context, h *sandbox.BotHomeBinding, k store.BotContentKind, size int64, body io.Reader, revision int64, barrier func(func() error) error) (*store.BotContentRef, error) {
	if size < 0 || body == nil {
		return nil, store.ErrBotInvalid
	}
	if _, err := contentRef(h, k, revision, &store.BotFileEntry{Path: "content-v1/check", SHA256: strings.Repeat("0", 64)}); err != nil {
		return nil, err
	}
	entry, err := b.mutateHomeFile(ctx, h, map[string]any{"op": "put", "size": size, "kind": k, "revision": revision}, body, size, barrier)
	if err != nil {
		return nil, err
	}
	return contentRef(h, k, revision, entry)
}

var _ sandbox.BotHomeContentReadbackBackend = (*K8sBackend)(nil)

func (b *K8sBackend) PutBotContentReadback(ctx context.Context, h *sandbox.BotHomeBinding, k store.BotContentKind, size int64, body io.Reader, revision int64, barrier func(func() error) error) (*store.BotContentRef, []byte, error) {
	if size < 0 || size > sandbox.BotHomeContentReadbackLimit || body == nil {
		return nil, nil, store.ErrBotInvalid
	}
	if _, err := contentRef(h, k, revision, &store.BotFileEntry{Path: "content-v1/check", SHA256: strings.Repeat("0", 64)}); err != nil {
		return nil, nil, err
	}
	var raw []byte
	entry, err := b.mutateHomeFile(ctx, h, map[string]any{"op": "put", "size": size, "kind": k, "revision": revision, "readback": true}, body, size, barrier, &raw)
	if err != nil {
		return nil, nil, err
	}
	ref, err := contentRef(h, k, revision, entry)
	if err != nil {
		return nil, nil, err
	}
	return ref, raw, nil
}

func (b *K8sBackend) OpenBotContent(ctx context.Context, h *sandbox.BotHomeBinding, ref store.BotContentRef, r store.BotFileRange) (*store.BotFileStream, error) {
	scope, err := fileContentScope(h)
	if err != nil {
		return nil, err
	}
	if ref.Validate() != nil || ref.Scope != scope || !validContentKey(ref) {
		return nil, store.ErrBotForbidden
	}
	return b.openHomeFile(ctx, h, map[string]any{"path": ref.Key, "private": true, "sha256": ref.SHA256, "size": ref.SizeBytes}, r)
}
func (b *K8sBackend) PublishBotArtifactSnapshot(ctx context.Context, h *sandbox.BotHomeBinding, path, name string, barrier func(func() error) error) (*store.BotContentRef, error) {
	if _, err := fileContentScope(h); err != nil {
		return nil, err
	}
	entry, err := b.mutateHomeFile(ctx, h, map[string]any{"op": "snapshot", "path": path}, nil, 0, barrier)
	if err != nil {
		return nil, err
	}
	return contentRef(h, store.BotContentArtifact, 1, entry)
}
