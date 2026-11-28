package k8s

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/google/uuid"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/remotecommand"
	"k8s.io/client-go/util/retry"
)

var _ sandbox.BotHomeStreamArchiveBackend = (*K8sBackend)(nil)

// Existing admission readers reject this state. It never expires: an ambiguous
// helper create/delete requires operator reconciliation, not automatic adoption.
const botHomeArchiving botHomeFenceState = "archiving"
const archiveOperationKey = "archive-operation"

type homeArchiveProcess struct {
	backend   *K8sBackend
	binding   *sandbox.BotHomeBinding
	name      string
	uid       types.UID
	expected  *corev1.Pod
	submitted bool
	input     *io.PipeWriter
	reader    *bufio.Reader
	cancel    context.CancelFunc
	done      chan error
	once      sync.Once
	err       error
}

func (b *K8sBackend) archiveFence(ctx context.Context, h *sandbox.BotHomeBinding, name string, acquire bool) error {
	cms := b.client.CoreV1().ConfigMaps(b.cfg.Namespace)
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		cm, err := cms.Get(ctx, botHomeFenceName(h), metav1.GetOptions{})
		if err != nil {
			return err
		} // Never create missing admission evidence.
		var stored sandbox.BotHomeBinding
		var rs map[string]botHomeReservation
		if cm.DeletionTimestamp != nil || json.Unmarshal([]byte(cm.Data["binding"]), &stored) != nil || !h.Equal(&stored) || json.Unmarshal([]byte(cm.Data["reservations"]), &rs) != nil || rs == nil || len(rs) != 0 {
			return errors.New("sandbox/k8s: archive requires valid detached admission")
		}
		if acquire {
			if botHomeFenceState(cm.Data["state"]) != botHomeActive || cm.Data[archiveOperationKey] != "" {
				return errors.New("sandbox/k8s: archive admission fenced")
			}
			cm.Data["state"], cm.Data[archiveOperationKey] = string(botHomeArchiving), name
		} else {
			if botHomeFenceState(cm.Data["state"]) != botHomeArchiving || cm.Data[archiveOperationKey] != name {
				return errors.New("sandbox/k8s: archive fence changed")
			}
			cm.Data["state"] = string(botHomeActive)
			delete(cm.Data, archiveOperationKey)
		}
		_, err = cms.Update(ctx, cm, metav1.UpdateOptions{})
		return err
	})
}

func (p *homeArchiveProcess) check(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	b, h := p.backend, p.binding
	cm, err := b.client.CoreV1().ConfigMaps(b.cfg.Namespace).Get(ctx, botHomeFenceName(h), metav1.GetOptions{})
	if err != nil {
		return err
	}
	var stored sandbox.BotHomeBinding
	var rs map[string]botHomeReservation
	if cm.DeletionTimestamp != nil || json.Unmarshal([]byte(cm.Data["binding"]), &stored) != nil || !h.Equal(&stored) || json.Unmarshal([]byte(cm.Data["reservations"]), &rs) != nil || rs == nil || len(rs) != 0 || botHomeFenceState(cm.Data["state"]) != botHomeArchiving || cm.Data[archiveOperationKey] != p.name {
		return errors.New("sandbox/k8s: archive fence lost")
	}
	pods, err := b.client.CoreV1().Pods(b.cfg.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return err
	}
	own := false
	for i := range pods.Items {
		pod := &pods.Items[i]
		if pod.Name == p.name {
			if p.uid == "" || pod.UID != p.uid || !safeBotWorkspacePod(pod, p.expected) {
				return errors.New("sandbox/k8s: archive helper changed")
			}
			own = true
			continue
		}
		// Fail closed even for another scope: claim-root and injected mounts must
		// not be treated as proof of isolation merely by trusting a subpath string.
		for _, v := range pod.Spec.Volumes {
			if v.PersistentVolumeClaim != nil && v.PersistentVolumeClaim.ClaimName == b.cfg.HomesPVCName {
				return errors.New("sandbox/k8s: archive claim has another attachment")
			}
		}
	}
	if p.submitted && !own {
		return errors.New("sandbox/k8s: archive helper disappeared")
	}
	return nil
}

func (p *homeArchiveProcess) finish(abort bool) error {
	if abort {
		p.cancel()
	}
	p.once.Do(func() {
		if p.input != nil {
			_ = p.input.Close()
		}
		if p.done != nil {
			p.err = <-p.done
		}
		p.cancel()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if p.submitted {
			if p.uid == "" {
				return
			} // Ambiguous submission: retain durable exclusion.
			pods := p.backend.client.CoreV1().Pods(p.backend.cfg.Namespace)
			current, err := pods.Get(ctx, p.name, metav1.GetOptions{})
			if err == nil {
				if current.UID != p.uid {
					p.err = errors.Join(p.err, errors.New("archive cleanup UID changed"))
					return
				}
				zero := int64(0)
				err = pods.Delete(ctx, p.name, metav1.DeleteOptions{GracePeriodSeconds: &zero, Preconditions: &metav1.Preconditions{UID: &p.uid}})
			}
			if err != nil && !apierrors.IsNotFound(err) {
				p.err = errors.Join(p.err, err)
				return
			}
			// Deletion acceptance is not physical absence. Keep admission fenced
			// while kubelet/API deletion completes; never adopt a replacement UID.
			for {
				current, err = pods.Get(ctx, p.name, metav1.GetOptions{})
				if apierrors.IsNotFound(err) {
					break
				}
				if err != nil || current.UID != p.uid {
					p.err = errors.Join(p.err, errors.New("archive cleanup absence unproven"), err)
					return
				}
				select {
				case <-ctx.Done():
					p.err = errors.Join(p.err, errors.New("archive cleanup absence unproven"), ctx.Err())
					return
				case <-time.After(100 * time.Millisecond):
				}
			}
		}
		p.err = errors.Join(p.err, p.backend.archiveFence(ctx, p.binding, p.name, false))
	})
	return p.err
}

func (b *K8sBackend) startHomeArchive(ctx context.Context, h *sandbox.BotHomeBinding, q map[string]any) (p *homeArchiveProcess, err error) {
	pvc, err := b.client.CoreV1().PersistentVolumeClaims(b.cfg.Namespace).Get(ctx, b.cfg.HomesPVCName, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	if pvc.DeletionTimestamp != nil {
		return nil, errors.New("archive claim deleting")
	}
	name := "astn-home-archive-" + uuid.NewString()
	if err = b.archiveFence(ctx, h, name, true); err != nil {
		return nil, err
	}
	streamCtx, cancel := context.WithCancel(ctx)
	p = &homeArchiveProcess{backend: b, binding: h.Clone(), name: name, cancel: cancel}
	defer func() {
		if err != nil {
			err = errors.Join(err, p.finish(true))
			p = nil
		}
	}()
	p.expected = b.botWorkspacePod(h, name)
	p.expected.Spec.ActiveDeadlineSeconds = nil
	p.expected.Spec.Containers[0].Image = b.cfg.SandboxImage
	p.expected.Spec.Containers[0].Command = []string{"/bin/sleep", "infinity"}
	if err = p.check(ctx); err != nil {
		return p, err
	}
	setup, stop := context.WithTimeout(ctx, 2*time.Minute)
	defer stop()
	p.submitted = true
	created, err := b.client.CoreV1().Pods(b.cfg.Namespace).Create(setup, p.expected, metav1.CreateOptions{})
	if err != nil {
		return p, err
	}
	p.uid = created.UID
	if p.uid == "" || !safeBotWorkspacePod(created, p.expected) {
		return p, errors.New("archive helper admission changed")
	}
	for {
		if err = p.check(setup); err != nil {
			return p, err
		}
		current, e := b.client.CoreV1().Pods(b.cfg.Namespace).Get(setup, name, metav1.GetOptions{})
		if e != nil {
			return p, e
		}
		if current.UID != p.uid || !safeBotWorkspacePod(current, p.expected) {
			return p, errors.New("archive target changed")
		}
		if current.Status.Phase == corev1.PodRunning && len(current.Status.ContainerStatuses) == 1 && current.Status.ContainerStatuses[0].Name == containerName && current.Status.ContainerStatuses[0].Ready && current.Status.ContainerStatuses[0].State.Running != nil {
			break
		}
		if current.Status.Phase == corev1.PodFailed || current.Status.Phase == corev1.PodSucceeded {
			return p, errors.New("archive helper terminated")
		}
		select {
		case <-setup.Done():
			return p, setup.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	mode := "export"
	if q["op"] == "restore" {
		mode = "restore"
	}
	command := []string{"/bin/sh", "-ec", "exec 9<" + shellQuote(mountBotHome) + "; exec /usr/local/bin/astonish-host node --internal-scoped-home-archive " + shellQuote(botHomeStorageOwner(h)) + " " + mode}
	if mode == "restore" && botHomeIsRestoreTarget(h) {
		// Exclusivity and the pinned helper were proven above. Only new restore
		// generations may initialize here; ordinary offline reads never provision.
		// Reject even an empty interrupted target instead of replaying its restore.
		command[2] = "( EXPECTED_OWNER=" + shellQuote(botHomeStorageOwner(h)) + "; test ! -e " + shellQuote(mountBotHome+"/.owner") + "; test ! -L " + shellQuote(mountBotHome+"/.owner") + "; " + botHomeInitScript + " ); " + command[2]
	}
	method, u, err := b.buildExecURL(name, command, false, true, true, true)
	if err != nil {
		return p, err
	}
	executor, err := b.execExecutorFactory(b.restConfig, method, u)
	if err != nil {
		return p, err
	}
	if err = p.check(setup); err != nil {
		return p, err
	}
	input, writer := io.Pipe()
	output, producer := io.Pipe()
	p.input, p.reader, p.done = writer, bufio.NewReader(output), make(chan error, 1)
	stderr := &botHomeBoundedBuffer{limit: 4096}
	unblock := context.AfterFunc(streamCtx, func() {
		_ = input.CloseWithError(streamCtx.Err())
		_ = producer.CloseWithError(streamCtx.Err())
		_ = output.CloseWithError(streamCtx.Err())
	})
	go func() {
		e := executor.StreamWithContext(streamCtx, remotecommand.StreamOptions{Stdin: input, Stdout: producer, Stderr: stderr})
		if e != nil {
			e = fmt.Errorf("archive exec: %w: %s", e, stderr.String())
		}
		_ = input.CloseWithError(e)
		_ = producer.CloseWithError(e)
		unblock()
		p.done <- e
	}()
	q["binding"] = h.Scope.Key()
	stopSetup := context.AfterFunc(setup, cancel)
	if err = json.NewEncoder(writer).Encode(q); err != nil {
		stopSetup()
		return p, err
	}
	if !stopSetup() || setup.Err() != nil {
		return p, setup.Err()
	}
	return p, nil
}

func (b *K8sBackend) archiveBarrier(ctx context.Context, h *sandbox.BotHomeBinding, barrier sandbox.BotHomeArchiveBarrier, op func(context.Context) error) error {
	if h == nil || barrier == nil {
		return errors.New("archive binding and barrier required")
	}
	h = h.Clone()
	if err := b.validateBotHome(h); err != nil {
		return err
	}
	if b.client == nil || b.restConfig == nil || b.execExecutorFactory == nil || strings.TrimSpace(b.cfg.SandboxImage) == "" {
		return sandbox.ErrBotHomeUnsupported
	}
	var mu sync.Mutex
	closed, calls := false, 0
	var operationErr error
	err := barrier(ctx, h.Clone(), func(c context.Context) error {
		mu.Lock()
		defer mu.Unlock()
		if closed {
			return errors.New("archive callback escaped barrier")
		}
		calls++
		if calls != 1 {
			operationErr = errors.Join(operationErr, errors.New("archive repeated callback"))
			return operationErr
		}
		if c == nil {
			operationErr = errors.New("archive context required")
			return operationErr
		}
		if operationErr = ctx.Err(); operationErr != nil {
			return operationErr
		}
		operationErr = errors.Join(op(c), c.Err())
		return operationErr
	})
	mu.Lock()
	defer mu.Unlock()
	closed = true
	if calls != 1 {
		err = errors.Join(err, errors.New("archive callback must execute exactly once"))
	}
	return errors.Join(err, operationErr, ctx.Err())
}

func archiveLine(p *homeArchiveProcess, out any) error {
	var line []byte
	for {
		part, more, err := p.reader.ReadLine()
		if err != nil {
			return err
		}
		if len(line)+len(part) > sandbox.MaxBotHomeArchiveManifestBytes {
			return errors.New("archive response bound")
		}
		line = append(line, part...)
		if !more {
			return json.Unmarshal(line, out)
		}
	}
}
func archiveCopy(dst io.Writer, src io.Reader, m *sandbox.BotHomeArchiveManifest) error {
	for _, entry := range m.Entries {
		if entry.Directory {
			continue
		}
		hash := sha256.New()
		if _, err := io.CopyN(io.MultiWriter(dst, hash), src, entry.Size); err != nil {
			return err
		}
		if hex.EncodeToString(hash.Sum(nil)) != entry.SHA256 {
			return errors.New("archive digest mismatch")
		}
	}
	return nil
}

func (b *K8sBackend) ExportBotHomeArchive(ctx context.Context, h *sandbox.BotHomeBinding, out io.Writer, barrier sandbox.BotHomeArchiveBarrier) (*sandbox.BotHomeArchiveManifest, error) {
	if out == nil {
		return nil, errors.New("archive output required")
	}
	h = h.Clone()
	var m sandbox.BotHomeArchiveManifest
	err := b.archiveBarrier(ctx, h, barrier, func(c context.Context) (err error) {
		p, err := b.startHomeArchive(c, h, map[string]any{"op": "export"})
		if err != nil {
			return err
		}
		defer func() { err = errors.Join(err, p.finish(true)) }()
		if err = archiveLine(p, &m); err != nil {
			return err
		}
		if err = m.Validate(); err != nil {
			return err
		}
		if m.Binding != h.Scope.Key() {
			return errors.New("archive source mismatch")
		}
		if err = p.check(c); err != nil {
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
		if err = p.check(c); err != nil {
			return err
		}
		return p.finish(false)
	})
	if err != nil {
		return nil, err
	}
	return &m, nil
}

// Restore accepts an owned empty destination, or a separately fenced restore
// generation in the same logical scope. It never changes SQL selection.
// Generation initialization occurs only under the exclusive archive attachment.
func (b *K8sBackend) RestoreBotHomeArchive(ctx context.Context, h *sandbox.BotHomeBinding, m *sandbox.BotHomeArchiveManifest, in io.Reader, barrier sandbox.BotHomeArchiveBarrier) error {
	if err := m.Validate(); err != nil {
		return err
	}
	if h == nil || in == nil || (h.Scope.Key() == m.Binding && !botHomeIsRestoreTarget(h)) {
		return errors.New("archive requires fresh destination and input")
	}
	h = h.Clone()
	raw, _ := json.Marshal(m)
	var manifest sandbox.BotHomeArchiveManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return err
	}
	return b.archiveBarrier(ctx, h, barrier, func(c context.Context) (err error) {
		p, err := b.startHomeArchive(c, h, map[string]any{"op": "restore", "manifest": &manifest})
		if err != nil {
			return err
		}
		defer func() { err = errors.Join(err, p.finish(true)) }()
		var ready struct{ Ready bool }
		if err = archiveLine(p, &ready); err != nil {
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
		if err = archiveLine(p, &staged); err != nil {
			return err
		}
		if !staged.Commit {
			return errors.New("archive stage not verified")
		}
		if err = p.check(c); err != nil {
			return err
		}
		if _, err = io.WriteString(p.input, "commit\n"); err != nil {
			return err
		}
		var done struct{ Restored bool }
		if err = archiveLine(p, &done); err != nil {
			return err
		}
		if !done.Restored {
			return errors.New("archive restore not acknowledged")
		}
		if _, err = p.reader.ReadByte(); err != io.EOF {
			return errors.New("archive trailing response")
		}
		if err = p.check(c); err != nil {
			return err
		}
		return p.finish(false)
	})
}
