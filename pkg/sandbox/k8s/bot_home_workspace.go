package k8s

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"time"

	"github.com/SAP/astonish/pkg/sandbox"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/uuid"
	"k8s.io/client-go/tools/remotecommand"
)

// Pre-provision this digest-pinned Python/SQLite userland. Workspace helpers
// never install dependencies or execute retained code. Script execution is a
// separate optional capability and is intentionally not advertised here.
const BotWorkspaceHelperImage = "python@sha256:e48f59cdba69a3cbd9ed2cf0f348d90d45a8d350d98134cf1070bc106ee9ed90"

var _ sandbox.BotHomeWorkspaceBackend = (*K8sBackend)(nil)

func (b *K8sBackend) botWorkspacePod(h *sandbox.BotHomeBinding, name string) *corev1.Pod {
	deadline := int64(120)
	return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: b.cfg.Namespace, Annotations: map[string]string{"astonish.io/home-workspace": name}}, Spec: corev1.PodSpec{
		RestartPolicy: corev1.RestartPolicyNever, ActiveDeadlineSeconds: &deadline, AutomountServiceAccountToken: botHomeBool(false),
		Containers: []corev1.Container{{Name: containerName, Image: BotWorkspaceHelperImage, ImagePullPolicy: corev1.PullNever,
			Command:         []string{"python3", "-I", "-c", "import time; time.sleep(120)"},
			SecurityContext: &corev1.SecurityContext{RunAsUser: int64Ptr(0), AllowPrivilegeEscalation: botHomeBool(false), ReadOnlyRootFilesystem: botHomeBool(true), Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}},
			Resources:       corev1.ResourceRequirements{Limits: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("256Mi"), corev1.ResourceCPU: resource.MustParse("500m")}},
			VolumeMounts:    []corev1.VolumeMount{{Name: volumeBotHome, MountPath: mountBotHome, SubPath: botHomeStorageSubPath(h)}},
		}},
		Volumes: []corev1.Volume{{Name: volumeBotHome, VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: b.cfg.HomesPVCName}}}},
	}}
}

func safeBotWorkspacePod(actual, expected *corev1.Pod) bool {
	if !safeBotHomeReadPod(actual, expected) || actual.DeletionTimestamp != nil || actual.Annotations["astonish.io/home-workspace"] != expected.Name || actual.Spec.RestartPolicy != corev1.RestartPolicyNever || !reflect.DeepEqual(actual.Spec.ActiveDeadlineSeconds, expected.Spec.ActiveDeadlineSeconds) {
		return false
	}
	c, e := actual.Spec.Containers[0], expected.Spec.Containers[0]
	return c.ImagePullPolicy == corev1.PullNever && reflect.DeepEqual(c.Resources, e.Resources) && len(c.VolumeDevices) == 0 && len(c.Ports) == 0 && c.Lifecycle == nil && c.LivenessProbe == nil && c.ReadinessProbe == nil && c.StartupProbe == nil && actual.Spec.SecurityContext == nil
}

// The persistent attachment ledger excludes purge while a helper may still
// write. Never release an ambiguous create or a helper whose deletion is not
// proven; cleanup errors turn otherwise successful operations into failures.
func (b *K8sBackend) workspace(ctx context.Context, h *sandbox.BotHomeBinding, q map[string]any, out any) (err error) {
	if err = ctx.Err(); err != nil {
		return err
	}
	if h == nil {
		return errors.New("sandbox/k8s: home binding required")
	}
	if err = b.validateBotHome(h); err != nil {
		return err
	}
	if b.client == nil || b.restConfig == nil || b.execExecutorFactory == nil {
		return sandbox.ErrBotHomeUnsupported
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	q["binding"] = h.Scope.Key()
	q["owner"] = h.Ref
	raw, err := json.Marshal(q)
	if err != nil {
		return err
	}
	compiled := sandbox.IsCompiledWorkspaceOperation(q["op"])
	if compiled {
		target, resolveErr := b.resolveBotHomeFileTarget(ctx, h)
		if resolveErr != nil {
			return resolveErr
		}
		if target != nil {
			command := botHomeFilesCommand(h, mountBotHomeScope, true, q["op"] == "publish" || q["op"] == "write" || q["op"] == "knowledge-publish")
			command[2] = "test \"${ASTONISH_POD_UID:-}\" = " + shellQuote(string(target.uid)) + "; " + strings.Replace(command[2], "--internal-scoped-home-files", "--internal-scoped-home-workspace", 1)
			current, e := b.resolveBotHomeFileTarget(ctx, h)
			if e != nil {
				return e
			}
			if current == nil || *current != *target {
				return errors.New("sandbox/k8s: workspace target changed")
			}
			return b.executeWorkspaceCommand(ctx, target.name, command, raw, out)
		}
	}
	name := "astn-home-work-" + string(uuid.NewUUID())
	token, err := b.reserveBotHome(ctx, h, name)
	if err != nil {
		return err
	}
	pods := b.client.CoreV1().Pods(b.cfg.Namespace)
	expected := b.botWorkspacePod(h, name)
	if compiled {
		expected.Spec.Containers[0].Image = b.cfg.SandboxImage
		expected.Spec.Containers[0].Command = []string{"/bin/sleep", "120"}
		// The existing lock is opened read/write even on read operations.
		// No public root is created by the compiled workspace reader.
	}
	var uid types.UID
	submitted, settled := false, false
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		if !submitted {
			err = errors.Join(err, b.cancelUnsubmittedBotHome(cleanup, h, token))
			return
		}
		// No UID means creation was ambiguous: do not DELETE by name or erase its
		// pending admission. An API server may still complete the original create.
		if uid == "" {
			return
		}
		current, getErr := pods.Get(cleanup, name, metav1.GetOptions{})
		if getErr != nil && !apierrors.IsNotFound(getErr) {
			err = errors.Join(err, getErr)
			return
		}
		if getErr == nil && current.UID != uid {
			err = errors.Join(err, errors.New("sandbox/k8s: workspace cleanup identity changed"))
			return
		}
		zero := int64(0)
		e := pods.Delete(cleanup, name, metav1.DeleteOptions{GracePeriodSeconds: &zero, Preconditions: &metav1.Preconditions{UID: &uid}})
		if e != nil && !apierrors.IsNotFound(e) {
			err = errors.Join(err, fmt.Errorf("workspace cleanup: %w", e))
			return
		}
		if !settled {
			return
		}
		err = errors.Join(err, b.releaseBotHomePod(cleanup, h, name))
	}()
	if err = ctx.Err(); err != nil {
		return err
	}
	submitted = true
	created, e := pods.Create(ctx, expected, metav1.CreateOptions{})
	if e != nil {
		return e
	}
	uid = created.UID
	if uid == "" {
		return errors.New("sandbox/k8s: workspace helper has no UID")
	}
	if !safeBotWorkspacePod(created, expected) {
		return errors.New("sandbox/k8s: workspace helper admission changed")
	}
	if err = b.settleBotHome(ctx, h, token); err != nil {
		return err
	}
	settled = true
	for {
		current, e := pods.Get(ctx, name, metav1.GetOptions{})
		if e != nil {
			return e
		}
		if current.UID != uid || !safeBotWorkspacePod(current, expected) {
			return errors.New("sandbox/k8s: workspace helper changed")
		}
		if current.Status.Phase == corev1.PodRunning && len(current.Status.ContainerStatuses) == 1 && current.Status.ContainerStatuses[0].Name == containerName && current.Status.ContainerStatuses[0].Ready && current.Status.ContainerStatuses[0].State.Running != nil {
			break
		}
		if current.Status.Phase == corev1.PodFailed || current.Status.Phase == corev1.PodSucceeded {
			return errors.New("sandbox/k8s: workspace helper terminated")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	command := []string{"python3", "-I", "-c", botWorkspaceProgram}
	if compiled {
		command = botHomeFilesCommand(h, mountBotHome, true, q["op"] == "publish" || q["op"] == "write" || q["op"] == "knowledge-publish")
		command[2] = strings.Replace(command[2], "--internal-scoped-home-files", "--internal-scoped-home-workspace", 1)
	}
	return b.executeWorkspaceCommand(ctx, name, command, raw, out)
}

func (b *K8sBackend) executeWorkspaceCommand(ctx context.Context, name string, command []string, raw []byte, out any) (err error) {
	method, u, e := b.buildExecURL(name, command, false, true, true, true)
	if e != nil {
		return e
	}
	executor, e := b.execExecutorFactory(b.restConfig, method, u)
	if e != nil {
		return e
	}
	stdout := &botHomeBoundedBuffer{limit: 24 << 20}
	stderr := &botHomeBoundedBuffer{limit: 4096}
	input, approval := io.Pipe()
	defer input.Close()
	defer approval.Close()
	stopInput := context.AfterFunc(ctx, func() { _ = input.CloseWithError(ctx.Err()); _ = approval.CloseWithError(ctx.Err()) })
	defer stopInput()
	requestWritten := make(chan error, 1)
	go func() { _, e := approval.Write(append(raw, '\n')); requestWritten <- e }()
	defer func() {
		_ = input.Close()
		_ = approval.Close()
		err = errors.Join(err, <-requestWritten)
	}()
	control := &sandbox.WorkspaceCommitStream{Context: ctx, Output: stdout, Approve: approval}
	streamErr := executor.StreamWithContext(ctx, remotecommand.StreamOptions{Stdin: input, Stdout: control, Stderr: stderr})
	code, e := decodeExitError(streamErr)
	if e != nil {
		return e
	}
	if code != 0 || stdout.exceeded || stderr.exceeded {
		return errors.New("sandbox/k8s: workspace confined operation rejected")
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = control.Finish(); err != nil {
		return err
	}
	if out != nil {
		return json.Unmarshal(stdout.Bytes(), out)
	}
	// Even writes require a valid protocol response, not merely a zero exit.
	var response any
	return json.Unmarshal(stdout.Bytes(), &response)
}

func workspaceTaskPath(p string) bool {
	a := strings.Split(p, "/")
	return len(a) == 2 && a[0] == "tasks" && sandbox.ValidateBotHomeArtifactPath(p) == nil && !strings.HasPrefix(a[1], ".")
}
func workspaceReference(p string) bool {
	return sandbox.ValidateBotHomeArtifactPath(p) == nil && !strings.Contains(p, "/") && !strings.HasPrefix(p, ".")
}

// Independently validate the helper's manifest identity, quotas and canonical
// digest. Readback additionally validates every actual file, not just metadata.
func validateWorkspaceReceipt(h *sandbox.BotHomeBinding, r *sandbox.BotHomeWorkspaceReceipt) error {
	if h == nil || r == nil || r.BindingKey != h.Scope.Key() || !workspaceTaskPath(r.TaskPath) || !workspaceReference(r.PublicationID) || r.ReceiptRef != r.PublicationID || len(r.Entries) == 0 || len(r.Entries) > sandbox.MaxBotHomeWorkspaceAssets {
		return errors.New("sandbox/k8s: invalid workspace receipt")
	}
	files := make([]sandbox.BotHomeWorkspaceFile, 0, len(r.Entries))
	rows := make([]map[string]any, 0, len(r.Entries))
	var size int64
	last := ""
	for _, e := range r.Entries {
		d, err := hex.DecodeString(e.Digest)
		if sandbox.ValidateBotHomeArtifactPath(e.Path) != nil || e.Path <= last || e.SizeBytes < 0 || e.SizeBytes > sandbox.MaxBotHomeWorkspaceAssetBytes || err != nil || len(d) != 32 || hex.EncodeToString(d) != e.Digest {
			return errors.New("sandbox/k8s: invalid workspace entry")
		}
		last = e.Path
		size += e.SizeBytes
		files = append(files, sandbox.BotHomeWorkspaceFile{Path: e.Path})
		rows = append(rows, map[string]any{"path": e.Path, "digest": e.Digest, "size_bytes": e.SizeBytes, "executable": e.Executable})
	}
	if err := (sandbox.BotHomeWorkspacePublishRequest{PublicationID: r.PublicationID, TaskPath: r.TaskPath, Files: files}).Validate(); err != nil {
		return err
	}
	var canonical bytes.Buffer
	encoder := json.NewEncoder(&canonical)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(rows); err != nil {
		return err
	}
	raw := bytes.TrimSuffix(canonical.Bytes(), []byte("\n"))
	// Match the retained Python ensure_ascii=False manifest format, including
	// separators that encoding/json escapes even with EscapeHTML disabled.
	raw = bytes.ReplaceAll(raw, []byte(`\u2028`), []byte("\u2028"))
	raw = bytes.ReplaceAll(raw, []byte(`\u2029`), []byte("\u2029"))
	sum := sha256.Sum256(raw)
	if size > sandbox.MaxBotHomeWorkspaceBundleBytes || hex.EncodeToString(sum[:]) != r.Digest {
		return errors.New("sandbox/k8s: workspace manifest digest mismatch")
	}
	return nil
}

func (b *K8sBackend) PublishBotHomeWorkspace(c context.Context, h *sandbox.BotHomeBinding, q sandbox.BotHomeWorkspacePublishRequest) (*sandbox.BotHomeWorkspaceReceipt, error) {
	if e := q.Validate(); e != nil {
		return nil, e
	}
	if !workspaceTaskPath(q.TaskPath) || !workspaceReference(q.PublicationID) {
		return nil, errors.New("invalid workspace publication path")
	}
	var r sandbox.BotHomeWorkspaceReceipt
	operation := func() error { return b.workspace(c, h, map[string]any{"op": "publish", "request": q}, &r) }
	var operationErr error
	if q.BeforeCommit != nil {
		operationErr = q.BeforeCommit(operation)
	} else {
		operationErr = operation()
	}
	if e := operationErr; e != nil {
		return nil, e
	}
	if e := validateWorkspaceReceipt(h, &r); e != nil {
		return nil, e
	}
	if r.TaskPath != q.TaskPath || r.PublicationID != q.PublicationID || len(r.Entries) != len(q.Files) {
		return nil, errors.New("workspace publication identity mismatch")
	}
	files := map[string]sandbox.BotHomeWorkspaceFile{}
	for _, f := range q.Files {
		files[f.Path] = f
	}
	for _, e := range r.Entries {
		f, ok := files[e.Path]
		if !ok || f.Digest() != e.Digest || int64(len(f.Data)) != e.SizeBytes || f.Executable != e.Executable {
			return nil, errors.New("workspace publication byte mismatch")
		}
	}
	return &r, nil
}
func (b *K8sBackend) ResolveBotHomeWorkspaceReceipt(c context.Context, h *sandbox.BotHomeBinding, ref string) (*sandbox.BotHomeWorkspaceReceipt, error) {
	if !workspaceReference(ref) {
		return nil, errors.New("invalid workspace receipt reference")
	}
	var r sandbox.BotHomeWorkspaceReceipt
	if e := b.workspace(c, h, map[string]any{"op": "resolve", "path": ref}, &r); e != nil {
		return nil, e
	}
	if e := validateWorkspaceReceipt(h, &r); e != nil {
		return nil, e
	}
	if r.ReceiptRef != ref {
		return nil, errors.New("workspace receipt identity mismatch")
	}
	return &r, nil
}
func (b *K8sBackend) ReadBotHomeWorkspace(c context.Context, h *sandbox.BotHomeBinding, p string, n int64) ([]byte, error) {
	if sandbox.ValidateBotHomeArtifactPath(p) != nil || n < 1 || n > sandbox.MaxBotHomeWorkspaceStateBytes {
		return nil, errors.New("invalid workspace read")
	}
	var r []byte
	if e := b.workspace(c, h, map[string]any{"op": "read", "path": p, "limit": n}, &r); e != nil {
		return nil, e
	}
	if int64(len(r)) > n {
		return nil, errors.New("workspace read limit")
	}
	return r, nil
}
func (b *K8sBackend) WriteBotHomeWorkspace(c context.Context, h *sandbox.BotHomeBinding, f sandbox.BotHomeWorkspaceFile) error {
	a := strings.Split(f.Path, "/")
	if sandbox.ValidateBotHomeArtifactPath(f.Path) != nil || f.Executable || int64(len(f.Data)) > sandbox.MaxBotHomeWorkspaceAssetBytes || len(a) < 4 || !workspaceTaskPath(strings.Join(a[:2], "/")) || (a[2] != "state" && a[2] != "outputs" && a[2] != "drafts") {
		return errors.New("invalid workspace write")
	}
	for _, p := range a {
		if strings.HasPrefix(p, ".") {
			return errors.New("invalid mutable workspace path")
		}
	}
	operation := func() error {
		return b.workspace(c, h, map[string]any{"op": "write", "path": f.Path, "data": f.Data}, nil)
	}
	if f.BeforeCommit != nil {
		return f.BeforeCommit(operation)
	}
	return operation()
}
func (b *K8sBackend) ListBotHomeWorkspace(c context.Context, h *sandbox.BotHomeBinding, p, after string, n int) ([]sandbox.BotHomeWorkspaceEntry, string, error) {
	if !workspaceTaskPath(p) || n < 1 || n > 100 || (after != "" && sandbox.ValidateBotHomeArtifactPath(after) != nil) {
		return nil, "", errors.New("invalid workspace list")
	}
	var r struct {
		Entries []sandbox.BotHomeWorkspaceEntry
		Next    string
	}
	if e := b.workspace(c, h, map[string]any{"op": "list", "path": p, "after": after, "limit": n}, &r); e != nil {
		return nil, "", e
	}
	if len(r.Entries) > n {
		return nil, "", errors.New("workspace list limit")
	}
	last := after
	for _, e := range r.Entries {
		digest, digestErr := hex.DecodeString(e.Digest)
		if sandbox.ValidateBotHomeArtifactPath(e.Path) != nil || e.Path <= last || e.SizeBytes < 0 || e.SizeBytes > sandbox.MaxBotHomeWorkspaceAssetBytes || digestErr != nil || len(digest) != sha256.Size || hex.EncodeToString(digest) != e.Digest {
			return nil, "", errors.New("invalid workspace list entry")
		}
		last = e.Path
	}
	if r.Next != "" && (len(r.Entries) != n || r.Next != last) {
		return nil, "", errors.New("invalid workspace continuation")
	}
	return r.Entries, r.Next, nil
}
func (b *K8sBackend) SnapshotBotHomeWorkspace(c context.Context, h *sandbox.BotHomeBinding, p string, n int64) (*sandbox.BotHomeWorkspaceSnapshot, error) {
	if !workspaceTaskPath(p) || n < 1 || n > sandbox.MaxBotHomeWorkspaceBundleBytes {
		return nil, errors.New("invalid workspace snapshot")
	}
	var r sandbox.BotHomeWorkspaceSnapshot
	if e := b.workspace(c, h, map[string]any{"op": "snapshot", "path": p, "limit": n}, &r); e != nil {
		return nil, e
	}
	if e := validateWorkspaceReceipt(h, &r.Receipt); e != nil {
		return nil, e
	}
	if r.Receipt.TaskPath != p || len(r.Files) != len(r.Receipt.Entries) {
		return nil, errors.New("workspace snapshot identity mismatch")
	}
	var total int64
	for i, f := range r.Files {
		e := r.Receipt.Entries[i]
		total += int64(len(f.Data))
		if f.Path != e.Path || f.Digest() != e.Digest || int64(len(f.Data)) != e.SizeBytes || f.Executable != e.Executable {
			return nil, errors.New("workspace snapshot byte mismatch")
		}
	}
	if total > n {
		return nil, errors.New("workspace snapshot limit")
	}
	return &r, nil
}

// Server-owned program; all retained path walks are descriptor-relative and
// O_NOFOLLOW. The scope root marker is outside writable data/, and no missing
// home is initialized or adopted by this capability.
const botWorkspaceProgram = `import os,sys,json,base64,hashlib,stat,fcntl,uuid
q=json.loads(sys.stdin.readline())
mount=os.open('/mnt/astonish-bot-home',os.O_RDONLY|os.O_DIRECTORY|os.O_NOFOLLOW)
f=os.open('.owner',os.O_RDONLY|os.O_NOFOLLOW|os.O_NONBLOCK,dir_fd=mount)
s=os.fstat(f)
if not stat.S_ISREG(s.st_mode) or s.st_nlink!=1 or s.st_size!=len(q['owner'])+1 or os.read(f,128)!=(q['owner']+'\n').encode(): raise ValueError('owner')
os.close(f)
try: os.mkdir('workspace-private',0o700,dir_fd=mount)
except FileExistsError: pass
root=os.open('workspace-private',os.O_RDONLY|os.O_DIRECTORY|os.O_NOFOLLOW,dir_fd=mount);os.close(mount)
with open('/proc/self/mountinfo') as mounts:
 for line in mounts:
  target=line.split()[4]
  if target.startswith('/mnt/astonish-bot-home/'): raise ValueError('nested mount')
` + sandbox.BotHomeWorkspaceProgram
