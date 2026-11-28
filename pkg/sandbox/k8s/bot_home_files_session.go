package k8s

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/SAP/astonish/pkg/sandbox"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

const mountBotHomeScope = "/mnt/astonish-bot-scope"

// BindBotHomeSession is an in-memory trusted composition boundary. It never
// reenters a session repository (callers may already own its transaction).
func (b *K8sBackend) BindBotHomeSession(h *sandbox.BotHomeBinding, id string) error {
	if h == nil || id == "" {
		return errors.New("sandbox/k8s: Home and canonical session required")
	}
	if err := b.validateBotHome(h); err != nil {
		return err
	}
	if old, loaded := b.botFileSessions.LoadOrStore(*h, id); loaded && old.(string) != id {
		return errors.New("sandbox/k8s: Bot file session binding is immutable")
	}
	return nil
}

// PrepareBotHomeSession is explicitly outside file/SQL transactions. File
// dispatch itself never starts, waits for, or touches session activity.
func (b *K8sBackend) PrepareBotHomeSession(ctx context.Context, h *sandbox.BotHomeBinding, id string) error {
	if err := b.BindBotHomeSession(h, id); err != nil {
		return err
	}
	home, err := b.sessionBotHome(ctx, id, nil, true)
	if err != nil {
		return err
	}
	if !h.Equal(home) {
		return errors.New("sandbox/k8s: canonical session Home mismatch")
	}
	if err = b.StartSession(ctx, id); err != nil {
		return err
	}
	return b.WaitForSessionReady(ctx, id)
}

type botHomeFileTarget struct {
	name string
	uid  types.UID
}

// A bound but unavailable session fails closed; it is not permission to create
// alternate compute. Unbound offline operations retain their owned attachment.
func (b *K8sBackend) resolveBotHomeFileTarget(ctx context.Context, h *sandbox.BotHomeBinding) (*botHomeFileTarget, error) {
	value, ok := b.botFileSessions.Load(*h)
	if !ok {
		return nil, nil
	}
	id := value.(string)
	home, err := b.sessionBotHome(ctx, id, nil, true) // existing-only ConfigMap read; no SQL
	if err != nil {
		return nil, err
	}
	if !h.Equal(home) {
		return nil, errors.New("sandbox/k8s: canonical session Home mismatch")
	}
	name := podNameForSession(id)
	pod, err := b.client.CoreV1().Pods(b.cfg.Namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	if err = b.validatePodBotHome(pod, h); err != nil {
		return nil, err
	}
	if len(pod.Spec.Containers) != 1 {
		return nil, errors.New("sandbox/k8s: invalid Home containers")
	}
	// Reject any second mount shadowing the selected scope, even when it
	// uses another volume name. Child mounts must not redirect compiled roots.
	for _, m := range pod.Spec.Containers[0].VolumeMounts {
		if m.MountPath == mountBotHomeScope && m.Name != volumeBotHome || strings.HasPrefix(m.MountPath, mountBotHomeScope+"/") {
			return nil, errors.New("sandbox/k8s: shadowed Home scope mount")
		}
	}
	uidEnv := 0
	for _, env := range pod.Spec.Containers[0].Env {
		if env.Name == "ASTONISH_POD_UID" {
			uidEnv++
			if env.Value != "" || env.ValueFrom == nil || env.ValueFrom.FieldRef == nil || env.ValueFrom.FieldRef.FieldPath != "metadata.uid" || env.ValueFrom.FieldRef.APIVersion != "v1" || env.ValueFrom.ResourceFieldRef != nil || env.ValueFrom.ConfigMapKeyRef != nil || env.ValueFrom.SecretKeyRef != nil {
				return nil, errors.New("sandbox/k8s: invalid pod UID projection")
			}
		}
	}
	if uidEnv != 1 {
		return nil, errors.New("sandbox/k8s: missing pod UID projection")
	}
	if pod.UID == "" || pod.DeletionTimestamp != nil || pod.Status.Phase != corev1.PodRunning || len(pod.Spec.Containers) != 1 || len(pod.Status.ContainerStatuses) != 1 || pod.Status.ContainerStatuses[0].Name != containerName || !pod.Status.ContainerStatuses[0].Ready || pod.Status.ContainerStatuses[0].State.Running == nil {
		return nil, errors.New("sandbox/k8s: canonical Home pod is not ready")
	}
	cm, err := b.client.CoreV1().ConfigMaps(b.cfg.Namespace).Get(ctx, botHomeFenceName(h), metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	var stored sandbox.BotHomeBinding
	var reservations map[string]botHomeReservation
	if cm.DeletionTimestamp != nil || botHomeFenceState(cm.Data["state"]) != botHomeActive || json.Unmarshal([]byte(cm.Data["binding"]), &stored) != nil || !h.Equal(&stored) || json.Unmarshal([]byte(cm.Data["reservations"]), &reservations) != nil {
		return nil, errors.New("sandbox/k8s: invalid Home attachment fence")
	}
	for _, r := range reservations {
		if r.Pod == name && !r.Pending {
			return &botHomeFileTarget{name: name, uid: pod.UID}, nil
		}
	}
	return nil, errors.New("sandbox/k8s: canonical Home attachment missing")
}
