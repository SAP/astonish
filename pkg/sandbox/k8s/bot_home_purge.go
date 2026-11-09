package k8s

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/SAP/astonish/pkg/sandbox"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/uuid"
	"k8s.io/client-go/util/retry"
)

var _ sandbox.BotHomeDeleteBackend = (*K8sBackend)(nil)

type botHomePurge struct {
	Name     string    `json:"name"`
	UID      types.UID `json:"uid"`
	Image    string    `json:"image"`
	Complete bool      `json:"complete"`
}

// Existing-only CAS: deletion must never synthesize evidence of no attachments.
func (b *K8sBackend) mutateBotHomePurge(ctx context.Context, h *sandbox.BotHomeBinding, change func(*corev1.ConfigMap, *botHomePurge) error) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		cms := b.client.CoreV1().ConfigMaps(b.cfg.Namespace)
		cm, err := cms.Get(ctx, botHomeFenceName(h), metav1.GetOptions{})
		if err != nil {
			return err
		}
		var binding sandbox.BotHomeBinding
		var rs map[string]botHomeReservation
		if cm.DeletionTimestamp != nil || json.Unmarshal([]byte(cm.Data["binding"]), &binding) != nil || !h.Equal(&binding) || json.Unmarshal([]byte(cm.Data["reservations"]), &rs) != nil || rs == nil {
			return errors.New("sandbox/k8s: invalid purge fence")
		}
		if len(rs) != 0 {
			return errors.New("sandbox/k8s: home still has attachment reservations")
		}
		state := botHomeFenceState(cm.Data["state"])
		if state != botHomeActive && state != botHomeDeleting && state != botHomeDeleted {
			return errors.New("sandbox/k8s: invalid purge state")
		}
		p := botHomePurge{}
		if state != botHomeActive {
			if json.Unmarshal([]byte(cm.Data["purge"]), &p) != nil || p.Name == "" || p.Image == "" || (p.Complete && p.UID == "") || (state == botHomeDeleted && !p.Complete) {
				return errors.New("sandbox/k8s: invalid purge operation")
			}
		}
		if err := change(cm, &p); err != nil {
			return err
		}
		raw, _ := json.Marshal(p)
		cm.Data["purge"] = string(raw)
		_, err = cms.Update(ctx, cm, metav1.UpdateOptions{})
		return err
	})
}

// DeleteBotHome logically purges retained data, keeping the scoped mount root,
// owner marker and purge receipt. The durable fence is never reopened or removed.
// Only the CAS winner may submit a helper, exactly once. A crash before submission
// deliberately strands deleting: absence cannot prove a delayed create is dead.
func (b *K8sBackend) DeleteBotHome(ctx context.Context, h *sandbox.BotHomeBinding) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if h == nil {
		return errors.New("sandbox/k8s: home binding required")
	}
	if err := b.validateBotHome(h); err != nil {
		return err
	}
	if b.client == nil {
		return ErrNotImplementedYet
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	operation := botHomePurge{Name: "astn-home-purge-" + string(uuid.NewUUID()), Image: b.cfg.SandboxImage}
	var recorded botHomePurge
	create, deleted := false, false
	err := b.mutateBotHomePurge(ctx, h, func(cm *corev1.ConfigMap, p *botHomePurge) error {
		create = false
		deleted = false
		switch botHomeFenceState(cm.Data["state"]) {
		case botHomeActive:
			*p = operation
			cm.Data["state"] = string(botHomeDeleting)
			create = true
		case botHomeDeleted:
			deleted = true
		}
		recorded = *p
		return nil
	})
	if err != nil {
		return err
	}
	if deleted {
		return nil
	}
	expected := b.botHomePurgePod(h, recorded)
	pods := b.client.CoreV1().Pods(b.cfg.Namespace)
	if create {
		// Never retry Create, including transport ambiguity. All subsequent invocations
		// may only GET this recorded identity, not submit another physical helper.
		if _, err = pods.Create(ctx, expected, metav1.CreateOptions{}); err != nil {
			return fmt.Errorf("sandbox/k8s: purge create unresolved: %w", err)
		}
	}
	for !recorded.Complete {
		pod, getErr := pods.Get(ctx, recorded.Name, metav1.GetOptions{})
		if getErr != nil {
			return fmt.Errorf("sandbox/k8s: purge helper unresolved: %w", getErr)
		}
		if pod.UID == "" || (recorded.UID != "" && pod.UID != recorded.UID) || !safeBotHomeReadPod(pod, expected) || pod.Annotations["astonish.io/home-purge"] != recorded.Name || pod.Spec.RestartPolicy != corev1.RestartPolicyNever {
			return errors.New("sandbox/k8s: purge helper identity changed")
		}
		if recorded.UID == "" {
			err = b.mutateBotHomePurge(ctx, h, func(_ *corev1.ConfigMap, p *botHomePurge) error {
				if p.Name != recorded.Name || (p.UID != "" && p.UID != pod.UID) {
					return errors.New("sandbox/k8s: purge identity conflict")
				}
				p.UID = pod.UID
				recorded = *p
				return nil
			})
			if err != nil {
				return err
			}
		}
		if pod.Status.Phase == corev1.PodFailed {
			return errors.New("sandbox/k8s: purge helper failed; retaining deleting fence")
		}
		if pod.Status.Phase == corev1.PodSucceeded {
			if len(pod.Status.ContainerStatuses) != 1 || pod.Status.ContainerStatuses[0].Name != containerName || pod.Status.ContainerStatuses[0].State.Terminated == nil || pod.Status.ContainerStatuses[0].State.Terminated.ExitCode != 0 {
				return errors.New("sandbox/k8s: purge outcome not proven")
			}
			err = b.mutateBotHomePurge(ctx, h, func(_ *corev1.ConfigMap, p *botHomePurge) error {
				if p.Name != recorded.Name || p.UID != pod.UID {
					return errors.New("sandbox/k8s: purge outcome identity conflict")
				}
				p.Complete = true
				recorded = *p
				return nil
			})
			if err != nil {
				return err
			}
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	// Record success before DELETE; a crash after deletion can recover from the
	// persisted receipt without re-running any destructive command.
	zero := int64(0)
	err = pods.Delete(ctx, recorded.Name, metav1.DeleteOptions{GracePeriodSeconds: &zero, Preconditions: &metav1.Preconditions{UID: &recorded.UID}})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("sandbox/k8s: purge helper cleanup: %w", err)
	}
	if _, err = pods.Get(ctx, recorded.Name, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		if err != nil {
			return err
		}
		return errors.New("sandbox/k8s: purge helper still exists")
	}
	return b.mutateBotHomePurge(ctx, h, func(cm *corev1.ConfigMap, p *botHomePurge) error {
		if p.Name != recorded.Name || p.UID != recorded.UID || !p.Complete {
			return errors.New("sandbox/k8s: purge receipt changed")
		}
		cm.Data["state"] = string(botHomeDeleted)
		return nil
	})
}

func (b *K8sBackend) botHomePurgePod(h *sandbox.BotHomeBinding, p botHomePurge) *corev1.Pod {
	deadline := int64(120)
	return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: p.Name, Namespace: b.cfg.Namespace, Annotations: map[string]string{"astonish.io/home-purge": p.Name}}, Spec: corev1.PodSpec{
		RestartPolicy: corev1.RestartPolicyNever, ActiveDeadlineSeconds: &deadline, AutomountServiceAccountToken: botHomeBool(false),
		Containers: []corev1.Container{{Name: containerName, Image: p.Image, ImagePullPolicy: imagePullPolicy(p.Image), Command: []string{"/bin/sh", "-c", botHomePurgeScript, "home-purge", h.Ref, p.Name},
			SecurityContext: &corev1.SecurityContext{RunAsUser: int64Ptr(0), AllowPrivilegeEscalation: botHomeBool(false), ReadOnlyRootFilesystem: botHomeBool(true), Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}, Add: []corev1.Capability{"DAC_OVERRIDE", "FOWNER"}}},
			VolumeMounts:    []corev1.VolumeMount{{Name: volumeBotHome, MountPath: mountBotHome, SubPath: h.Scope.Key()}}}},
		Volumes: []corev1.Volume{{Name: volumeBotHome, VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: b.cfg.HomesPVCName}}}},
	}}
}

// Linux coreutils/findutils only. No eval, recursive rm, claim-root mount or
// symlink traversal. The ledger excludes writers; nested mounts fail closed,
// including same-device bind mounts which find -xdev alone cannot detect.
const botHomePurgeScript = `set -eu
root=/mnt/astonish-bot-home
[ ! -L "$root" ] && [ -d "$root" ] || exit 1
[ ! -L "$root/.owner" ] && [ -f "$root/.owner" ] || exit 1
exec 4< "$root/.owner"
[ "$(readlink -f /proc/$$/fd/4)" = "$root/.owner" ] || exit 1
[ "$(stat -Lc %s /proc/$$/fd/4)" -eq "$(( ${#1} + 1 ))" ] || exit 1
[ "$(head -c 128 <&4)" = "$1" ] || exit 1
awk -v root="$root/" 'index($5,root)==1 {bad=1} END {exit bad}' /proc/self/mountinfo || exit 1
[ ! -L "$root/.purged" ] || exit 1
if [ -f "$root/.purged" ]; then
 [ "$(cat "$root/.purged")" = "$2" ] && [ ! -e "$root/data" ] && [ ! -L "$root/data" ] || exit 1
 exit 0
fi
[ ! -L "$root/data" ] || exit 1
if [ -e "$root/data" ]; then
 [ -d "$root/data" ] && [ "$(readlink -f "$root/data")" = "$root/data" ] || exit 1
else
 [ ! -L "$root/.purging" ] && [ -f "$root/.purging" ] && [ "$(cat "$root/.purging")" = "$2" ] || exit 1
fi
# Publish intent atomically before deletion. Interrupted invocations can safely
# resume this same operation; missing unowned scopes are never accepted.
[ ! -L "$root/.purging" ] || exit 1
if [ -e "$root/.purging" ]; then
 [ -f "$root/.purging" ] && [ "$(cat "$root/.purging")" = "$2" ] || exit 1
else
 tmp=$(mktemp "$root/.purge-XXXXXXXX")
 printf '%s\n' "$2" > "$tmp"
 sync -f "$tmp"
 mv -T "$tmp" "$root/.purging"
 sync -f "$root"
fi
if [ -d "$root/data" ]; then find -P "$root/data" -xdev -depth -delete; fi
[ ! -e "$root/data" ] && [ ! -L "$root/data" ] || exit 1
mv -T "$root/.purging" "$root/.purged"
sync -f "$root"
`
