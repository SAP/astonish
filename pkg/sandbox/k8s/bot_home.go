package k8s

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"

	"github.com/SAP/astonish/pkg/sandbox"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
)

const (
	volumeBotHome     = "bot-home"
	mountBotHome      = "/mnt/astonish-bot-home"
	annotationBotHome = "astonish.io/bot-home"
	botHomeRefPrefix  = "k8s-home-v1:"
)

func botHomeBool(v bool) *bool { return &v }

var _ sandbox.BotHomeBackend = (*K8sBackend)(nil)

func (b *K8sBackend) botHomeRef(scope sandbox.BotHomeScope) string {
	return fmt.Sprintf("%s%x", botHomeRefPrefix, sha256.Sum256([]byte(b.cfg.Namespace+"\x00"+b.cfg.HomesPVCName+"\x00"+scope.Key())))
}

func (b *K8sBackend) validateBotHome(h *sandbox.BotHomeBinding) error {
	if h == nil {
		return nil
	}
	if err := h.Validate(); err != nil {
		return err
	}
	if b.cfg.HomesPVCName == "" {
		return sandbox.ErrBotHomeUnsupported
	}
	if h.Backend != sandbox.BackendKindK8s || h.Ref != b.botHomeRef(h.Scope) {
		return fmt.Errorf("sandbox/k8s: invalid or conflicting bot home reference")
	}
	return nil
}

// EnsureBotHome validates the operator-provisioned claim and issues only an
// opaque scope-derived reference. Kubelet creates the scoped subpath; the init
// container initializes ownership without ever mounting the claim root.
func (b *K8sBackend) EnsureBotHome(ctx context.Context, scope sandbox.BotHomeScope) (*sandbox.BotHomeBinding, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := scope.Validate(); err != nil {
		return nil, err
	}
	if b.cfg.HomesPVCName == "" {
		return nil, sandbox.ErrBotHomeUnsupported
	}
	if b.client == nil {
		return nil, ErrNotImplementedYet
	}
	pvc, err := b.client.CoreV1().PersistentVolumeClaims(b.cfg.Namespace).Get(ctx, b.cfg.HomesPVCName, metav1.GetOptions{})
	if err != nil {
		return nil, fmt.Errorf("sandbox/k8s: retained homes claim: %w", err)
	}
	if pvc.DeletionTimestamp != nil {
		return nil, fmt.Errorf("sandbox/k8s: retained homes claim is deleting")
	}
	h := &sandbox.BotHomeBinding{Scope: scope, Backend: sandbox.BackendKindK8s, Ref: b.botHomeRef(scope)}
	if _, err := b.client.CoreV1().ConfigMaps(b.cfg.Namespace).Get(ctx, botHomeFenceName(h), metav1.GetOptions{}); apierrors.IsNotFound(err) {
		return h, nil
	} else if err != nil {
		return nil, err
	}
	if err := b.mutateBotHomeFence(ctx, h, func(state botHomeFenceState, _ map[string]botHomeReservation) error {
		if state != botHomeActive {
			return fmt.Errorf("sandbox/k8s: home attachment fenced (%s)", state)
		}
		return nil
	}); err != nil {
		return nil, err
	}
	return h, nil
}

func validateHomesConfig(cfg Config) error {
	if cfg.HomesPVCName == "" {
		return nil
	}
	if len(validation.IsDNS1123Subdomain(cfg.HomesPVCName)) != 0 || cfg.HomesPVCName == cfg.LayersPVCName || cfg.HomesPVCName == cfg.UppersPVCName {
		return fmt.Errorf("sandbox/k8s: homes PVC must be a valid, independent claim")
	}
	return nil
}

func (b *K8sBackend) applyBotHome(pod *corev1.Pod, h *sandbox.BotHomeBinding) {
	if h == nil {
		return
	}
	raw, _ := json.Marshal(h)
	pod.Annotations[annotationBotHome] = string(raw)
	pod.Spec.Volumes = append(pod.Spec.Volumes, corev1.Volume{Name: volumeBotHome, VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: b.cfg.HomesPVCName}}})
	c := &pod.Spec.Containers[0]
	c.Env = append(c.Env, corev1.EnvVar{Name: "ASTONISH_BOT_HOME", Value: "1"}, corev1.EnvVar{Name: "HOME", Value: "/root"})
	c.VolumeMounts = append(c.VolumeMounts, corev1.VolumeMount{Name: volumeBotHome, MountPath: mountBotHome, SubPath: h.Scope.Key() + "/data"})
	pod.Spec.AutomountServiceAccountToken = botHomeBool(false)
	pod.Spec.InitContainers = append(pod.Spec.InitContainers, corev1.Container{
		Name: "bot-home-init", Image: b.cfg.SandboxImage, ImagePullPolicy: imagePullPolicy(b.cfg.SandboxImage),
		Command:         []string{"/bin/sh", "-c", botHomeInitScript},
		Env:             []corev1.EnvVar{{Name: "EXPECTED_OWNER", Value: h.Ref}},
		SecurityContext: &corev1.SecurityContext{RunAsUser: int64Ptr(0)},
		VolumeMounts:    []corev1.VolumeMount{{Name: volumeBotHome, MountPath: mountBotHome, SubPath: h.Scope.Key()}},
	})
}

// Metadata is outside data/, so workload writes cannot alter the owner marker.
// A crash mid-initialization fails closed for operator review, never adopts data.
const botHomeMountScript = `# Retained Bot HOME: fixed source and fixed chroot destination only.
# This bind is outside upperdir; eviction and template captures never include it.
case "${ASTONISH_BOT_HOME:-}" in
  "") ;;
  1)
    [ "${HOME:-/root}" = /root ] || { echo "unsafe Bot HOME" >&2; exit 1; }
    [ ! -L /mnt/astonish-bot-home ] && [ -d /mnt/astonish-bot-home ] || exit 1
    mountpoint -q /mnt/astonish-bot-home || { echo "Bot HOME volume missing" >&2; exit 1; }
    [ ! -L "$MOUNT_POINT/root" ] || { echo "unsafe Bot HOME target" >&2; exit 1; }
    mkdir -p "$MOUNT_POINT/root"
    [ "$(readlink -f "$MOUNT_POINT/root")" = "$(readlink -f "$MOUNT_POINT")/root" ] || exit 1
    mount --bind /mnt/astonish-bot-home "$MOUNT_POINT/root"
    mountpoint -q "$MOUNT_POINT/root" || exit 1
    export HOME=/root
    ;;
  *) echo "invalid retained Bot HOME mode" >&2; exit 1 ;;
esac

`

const botHomeInitScript = `set -eu
cd /mnt/astonish-bot-home
[ ! -L data ] && [ ! -L .owner ] || exit 1
if [ -f .owner ]; then
  [ "$(cat .owner)" = "$EXPECTED_OWNER" ] || exit 1
  [ -d data ] || exit 1
else
  mkdir .initializing
  for item in .[^.]* ..?* *; do
    case "$item" in .initializing|data|'.[^.]*'|'..?*'|'*') continue ;; esac
    echo "unowned bot home metadata" >&2; exit 1
  done
  mkdir -p data
  [ -z "$(ls -A data)" ] || { echo "refusing unowned bot home data" >&2; exit 1; }
  chown 0:0 data
  chmod 0700 data
  printf '%s\n' "$EXPECTED_OWNER" > .initializing/owner
  mv .initializing/owner .owner
  rmdir .initializing
fi
`

func botHomeSessionName(id string) string {
	return fmt.Sprintf("astn-home-session-%x", sha256.Sum256([]byte(id)))[:63]
}

// Session metadata is immutable and survives pod eviction. It is not mounted
// into workloads and contains no storage credentials. Ordinary sessions create
// no ConfigMap. Nil requests cannot downgrade an existing retained binding.
func (b *K8sBackend) sessionBotHome(ctx context.Context, id string, requested *sandbox.BotHomeBinding, recover bool) (*sandbox.BotHomeBinding, error) {
	if err := b.validateBotHome(requested); err != nil {
		return nil, err
	}
	cm, err := b.client.CoreV1().ConfigMaps(b.cfg.Namespace).Get(ctx, botHomeSessionName(id), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		if requested == nil {
			return nil, nil
		}
		rec, lookupErr := b.sessions.GetSession(id)
		if lookupErr != nil {
			return nil, lookupErr
		}
		if rec != nil {
			return nil, fmt.Errorf("sandbox/k8s: cannot attach retained home to existing unbound session")
		}
		raw, _ := json.Marshal(requested)
		cm, err = b.client.CoreV1().ConfigMaps(b.cfg.Namespace).Create(ctx, &corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: botHomeSessionName(id), Namespace: b.cfg.Namespace},
			Immutable:  botHomeBool(true), Data: map[string]string{"session": id, "binding": string(raw)},
		}, metav1.CreateOptions{})
		if apierrors.IsAlreadyExists(err) {
			return b.sessionBotHome(ctx, id, requested, recover)
		}
	}
	if err != nil {
		return nil, fmt.Errorf("sandbox/k8s: session home metadata: %w", err)
	}
	var stored sandbox.BotHomeBinding
	if cm.Data["session"] != id || json.Unmarshal([]byte(cm.Data["binding"]), &stored) != nil {
		return nil, fmt.Errorf("sandbox/k8s: invalid session home metadata")
	}
	if err := b.validateBotHome(&stored); err != nil {
		return nil, err
	}
	if !(recover && requested == nil) && !stored.Equal(requested) {
		return nil, fmt.Errorf("sandbox/k8s: session bot home conflict")
	}
	return &stored, nil
}

func (b *K8sBackend) validatePodBotHome(pod *corev1.Pod, h *sandbox.BotHomeBinding) error {
	expected := ""
	if h != nil {
		raw, _ := json.Marshal(h)
		expected = string(raw)
	}
	if pod.Annotations[annotationBotHome] != expected {
		return fmt.Errorf("sandbox/k8s: existing pod bot home conflict")
	}
	volumes, mounts := 0, 0
	for _, v := range pod.Spec.Volumes {
		if v.Name == volumeBotHome {
			volumes++
			if h == nil || v.PersistentVolumeClaim == nil || v.PersistentVolumeClaim.ClaimName != b.cfg.HomesPVCName {
				return fmt.Errorf("sandbox/k8s: existing pod home volume conflict")
			}
		}
	}
	for _, c := range pod.Spec.Containers {
		for _, m := range c.VolumeMounts {
			if m.Name == volumeBotHome {
				mounts++
				if h == nil || c.Name != containerName || m.SubPath != h.Scope.Key()+"/data" || m.SubPathExpr != "" || m.MountPath != mountBotHome {
					return fmt.Errorf("sandbox/k8s: existing pod home mount conflict")
				}
			}
		}
	}
	if h != nil && (volumes != 1 || mounts != 1) {
		return fmt.Errorf("sandbox/k8s: existing pod missing retained home")
	}
	initMounts := 0
	for _, c := range pod.Spec.InitContainers {
		for _, m := range c.VolumeMounts {
			if m.Name != volumeBotHome {
				continue
			}
			initMounts++
			if h == nil || c.Name != "bot-home-init" || m.SubPath != h.Scope.Key() || m.SubPathExpr != "" || m.MountPath != mountBotHome {
				return fmt.Errorf("sandbox/k8s: existing pod home initializer conflict")
			}
		}
	}
	if h != nil {
		if initMounts != 1 || pod.Spec.AutomountServiceAccountToken == nil || *pod.Spec.AutomountServiceAccountToken {
			return fmt.Errorf("sandbox/k8s: existing pod home isolation conflict")
		}
		for _, c := range pod.Spec.Containers {
			if c.Name != containerName {
				continue
			}
			mode, home := 0, 0
			for _, env := range c.Env {
				switch env.Name {
				case "ASTONISH_BOT_HOME":
					if env.Value != "1" || env.ValueFrom != nil {
						return fmt.Errorf("sandbox/k8s: existing pod home mode conflict")
					}
					mode++
				case "HOME":
					if env.Value != "/root" || env.ValueFrom != nil {
						return fmt.Errorf("sandbox/k8s: existing pod home target conflict")
					}
					home++
				}
			}
			if mode != 1 || home != 1 {
				return fmt.Errorf("sandbox/k8s: existing pod missing home environment")
			}
		}
	}
	return nil
}
