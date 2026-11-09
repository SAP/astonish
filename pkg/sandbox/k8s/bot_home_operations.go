package k8s

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"time"

	"github.com/SAP/astonish/pkg/sandbox"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/uuid"
	"k8s.io/client-go/tools/remotecommand"
)

var _ sandbox.BotHomeArtifactBackend = (*K8sBackend)(nil)

// ReadBotHomeArtifact uses a read-only scoped helper, never a session or a
// provisioning init container. Authorization of the binding belongs to callers.
func (b *K8sBackend) ReadBotHomeArtifact(ctx context.Context, h *sandbox.BotHomeBinding, path string, limit int64) (data []byte, err error) {
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	if h == nil {
		return nil, errors.New("sandbox/k8s: home binding required")
	}
	if err = b.validateBotHome(h); err != nil {
		return nil, err
	}
	if err = sandbox.ValidateBotHomeArtifactPath(path); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > sandbox.MaxBotHomeArtifactBytes {
		return nil, errors.New("sandbox/k8s: invalid artifact limit")
	}
	if b.client == nil {
		return nil, ErrNotImplementedYet
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	name := "astn-home-read-" + string(uuid.NewUUID())
	token, reserveErr := b.reserveBotHome(ctx, h, name)
	if reserveErr != nil {
		return nil, reserveErr
	}
	deadline := int64(120)
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: b.cfg.Namespace},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever, ActiveDeadlineSeconds: &deadline,
			AutomountServiceAccountToken: botHomeBool(false),
			Containers: []corev1.Container{{Name: containerName, Image: b.cfg.SandboxImage, ImagePullPolicy: imagePullPolicy(b.cfg.SandboxImage),
				Command:         []string{"/bin/sh", "-c", "exec sleep 120"},
				SecurityContext: &corev1.SecurityContext{RunAsUser: int64Ptr(0), AllowPrivilegeEscalation: botHomeBool(false), ReadOnlyRootFilesystem: botHomeBool(true), Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}},
				VolumeMounts:    []corev1.VolumeMount{{Name: volumeBotHome, MountPath: mountBotHome, SubPath: h.Scope.Key(), ReadOnly: true}},
			}},
			Volumes: []corev1.Volume{{Name: volumeBotHome, VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: b.cfg.HomesPVCName, ReadOnly: true}}}},
		},
	}
	pods := b.client.CoreV1().Pods(b.cfg.Namespace)
	var helperUID types.UID
	cleanupOwned := true
	// A create transport error may still have created the pod. The unguessable
	// name is owned by this invocation; cleanup also covers uncertain creation.
	defer func() {
		if !cleanupOwned {
			return
		}
		cleanup, done := context.WithTimeout(context.Background(), 10*time.Second)
		defer done()
		zero := int64(0)
		options := metav1.DeleteOptions{GracePeriodSeconds: &zero}
		if helperUID != "" {
			options.Preconditions = &metav1.Preconditions{UID: &helperUID}
		}
		cleanupErr := pods.Delete(cleanup, name, options)
		if cleanupErr != nil && !apierrors.IsNotFound(cleanupErr) {
			err = errors.Join(err, fmt.Errorf("home helper cleanup: %w", cleanupErr))
			data = nil
			return
		}
		// Ambiguous creates keep a pending reservation forever: even an absent
		// pod cannot prove the API server will not finish that create later.
		if releaseErr := b.releaseBotHomePod(cleanup, h, name); releaseErr != nil {
			err = errors.Join(err, fmt.Errorf("home helper release: %w", releaseErr))
			data = nil
		}
	}()
	created, createErr := pods.Create(ctx, pod, metav1.CreateOptions{})
	if createErr != nil {
		if apierrors.IsAlreadyExists(createErr) {
			cleanupOwned = false
		}
		return nil, createErr
	}
	helperUID = created.UID
	if err = b.settleBotHome(ctx, h, token); err != nil {
		return nil, err
	}
	for {
		current, getErr := pods.Get(ctx, name, metav1.GetOptions{})
		if getErr != nil {
			return nil, getErr
		}
		// Never exec into a replacement or admission-mutated storage mount.
		if current.UID != created.UID || !safeBotHomeReadPod(current, pod) {
			return nil, errors.New("sandbox/k8s: home helper changed")
		}
		ready := false
		for _, c := range current.Status.ContainerStatuses {
			if c.Name == containerName && c.Ready && c.State.Running != nil {
				ready = true
			}
		}
		if current.Status.Phase == corev1.PodRunning && ready {
			break
		}
		if current.Status.Phase == corev1.PodFailed || current.Status.Phase == corev1.PodSucceeded {
			return nil, errors.New("sandbox/k8s: home helper terminated")
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	command := []string{"/bin/sh", "-c", botHomeReadScript, "home-read", h.Ref, path, strconv.FormatInt(limit, 10)}
	method, u, buildErr := b.buildExecURL(name, command, false, false, true, true)
	if buildErr != nil {
		return nil, buildErr
	}
	executor, buildErr := b.execExecutorFactory(b.restConfig, method, u)
	if buildErr != nil {
		return nil, buildErr
	}
	stdout := &botHomeBoundedBuffer{limit: limit + 1}
	stderr := &botHomeBoundedBuffer{limit: 4096}
	streamErr := executor.StreamWithContext(ctx, remotecommand.StreamOptions{Stdout: stdout, Stderr: stderr})
	code, execErr := decodeExitError(streamErr)
	if execErr != nil {
		return nil, execErr
	}
	if code != 0 || stdout.exceeded || stderr.exceeded || int64(stdout.Len()) > limit {
		return nil, errors.New("sandbox/k8s: artifact read rejected")
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	return stdout.Bytes(), nil
}

func safeBotHomeReadPod(actual, expected *corev1.Pod) bool {
	if len(actual.Spec.Volumes) != 1 || len(actual.Spec.Containers) != 1 || len(actual.Spec.InitContainers) != 0 || len(actual.Spec.EphemeralContainers) != 0 || actual.Spec.AutomountServiceAccountToken == nil || *actual.Spec.AutomountServiceAccountToken {
		return false
	}
	v := actual.Spec.Volumes[0]
	if v.Name != volumeBotHome || v.PersistentVolumeClaim == nil || *v.PersistentVolumeClaim != *expected.Spec.Volumes[0].PersistentVolumeClaim {
		return false
	}
	c := actual.Spec.Containers[0]
	return c.Name == containerName && c.Image == expected.Spec.Containers[0].Image && reflect.DeepEqual(c.Command, expected.Spec.Containers[0].Command) && len(c.Args) == 0 && len(c.Env) == 0 && len(c.EnvFrom) == 0 && reflect.DeepEqual(c.SecurityContext, expected.Spec.Containers[0].SecurityContext) && !actual.Spec.HostPID && !actual.Spec.HostIPC && !actual.Spec.HostNetwork && len(c.VolumeMounts) == 1 && reflect.DeepEqual(c.VolumeMounts[0], expected.Spec.Containers[0].VolumeMounts[0])
}

type botHomeBoundedBuffer struct {
	bytes.Buffer
	limit    int64
	exceeded bool
}

func (w *botHomeBoundedBuffer) Write(p []byte) (int, error) {
	if int64(len(p)) > w.limit-int64(w.Len()) {
		w.exceeded = true
		return 0, errors.New("home helper output limit exceeded")
	}
	return w.Buffer.Write(p)
}

// Validate both canonical paths and the opened descriptor before reading. The
// second check closes a symlink-replacement race between realpath and open.
// /proc is the helper's own procfs, not workload-controlled storage. Size and
// output are independently bounded; a racing growth cannot overflow the caller.
const botHomeReadScript = `set -eu
root=/mnt/astonish-bot-home
[ ! -L "$root/.owner" ] && [ -f "$root/.owner" ] || exit 1
exec 4< "$root/.owner"
[ "$(readlink -f /proc/$$/fd/4)" = "$root/.owner" ] || exit 1
[ "$(stat -Lc %s /proc/$$/fd/4)" -eq "$(( ${#1} + 1 ))" ] || exit 1
[ "$(head -c 128 <&4)" = "$1" ] || exit 1
[ ! -L "$root/data" ] && [ -d "$root/data" ] || exit 1
base=$(readlink -f "$root/data")
[ "$base" = "$root/data" ] || exit 1
target=$(readlink -f "$base/$2")
case "$target" in "$base"/*) ;; *) exit 1 ;; esac
[ -f "$target" ] && [ ! -L "$base/$2" ] || exit 1
exec 3< "$target"
opened=$(readlink -f /proc/$$/fd/3)
case "$opened" in "$base"/*) ;; *) exit 1 ;; esac
[ -f /proc/$$/fd/3 ] || exit 1
size=$(stat -Lc %s /proc/$$/fd/3)
[ "$size" -le "$3" ] || exit 1
head -c "$(( $3 + 1 ))" <&3
`
