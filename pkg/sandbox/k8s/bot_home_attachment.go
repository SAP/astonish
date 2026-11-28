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
	"k8s.io/apimachinery/pkg/util/uuid"
	"k8s.io/client-go/util/retry"
)

type botHomeFenceState string

const (
	botHomeActive   botHomeFenceState = "active"
	botHomeDeleting botHomeFenceState = "deleting"
	botHomeDeleted  botHomeFenceState = "deleted"
)

type botHomeReservation struct {
	Pod string `json:"pod"`
	// Pending means a worker may still submit a create. It NEVER expires, even
	// when the named pod is absent. Only that worker can settle its reservation.
	Pending bool `json:"pending"`
}

func botHomeFenceName(h *sandbox.BotHomeBinding) string {
	return fmt.Sprintf("astn-home-fence-%x", sha256.Sum256([]byte(h.Ref)))[:63]
}

// mutateBotHomeFence serializes attachment admission with future deletion via
// resourceVersion CAS. No owner references/TTL: losing this state is unsafe.
func (b *K8sBackend) mutateBotHomeFence(ctx context.Context, h *sandbox.BotHomeBinding, change func(botHomeFenceState, map[string]botHomeReservation) error) error {
	if err := b.validateBotHome(h); err != nil {
		return err
	}
	if h == nil {
		return nil
	}
	cms := b.client.CoreV1().ConfigMaps(b.cfg.Namespace)
	return retry.OnError(retry.DefaultRetry, func(err error) bool { return apierrors.IsConflict(err) || apierrors.IsAlreadyExists(err) }, func() error {
		cm, err := cms.Get(ctx, botHomeFenceName(h), metav1.GetOptions{})
		fresh := apierrors.IsNotFound(err)
		if fresh {
			raw, _ := json.Marshal(h)
			cm = &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: botHomeFenceName(h), Namespace: b.cfg.Namespace}, Data: map[string]string{"binding": string(raw), "state": string(botHomeActive), "reservations": "{}"}}
		} else if err != nil {
			return err
		}
		var stored sandbox.BotHomeBinding
		var reservations map[string]botHomeReservation
		state := botHomeFenceState(cm.Data["state"])
		if cm.DeletionTimestamp != nil || json.Unmarshal([]byte(cm.Data["binding"]), &stored) != nil || !h.Equal(&stored) || json.Unmarshal([]byte(cm.Data["reservations"]), &reservations) != nil || reservations == nil {
			return fmt.Errorf("sandbox/k8s: invalid home attachment fence")
		}
		switch state {
		case botHomeActive, botHomeDeleting, botHomeDeleted:
		default:
			return fmt.Errorf("sandbox/k8s: invalid home fence state")
		}
		for token, r := range reservations {
			if token == "" || r.Pod == "" {
				return fmt.Errorf("sandbox/k8s: invalid home reservation")
			}
		}
		if err := change(state, reservations); err != nil {
			return err
		}
		raw, _ := json.Marshal(reservations)
		cm.Data["reservations"] = string(raw)
		if fresh {
			_, err = cms.Create(ctx, cm, metav1.CreateOptions{})
		} else {
			_, err = cms.Update(ctx, cm, metav1.UpdateOptions{})
		}
		return err
	})
}

// Every invocation gets its own reservation: a second backend cannot settle a
// paused/ambiguous first backend's right to create merely by observing no pod.
func (b *K8sBackend) reserveBotHome(ctx context.Context, h *sandbox.BotHomeBinding, pod string) (string, error) {
	if h == nil {
		return "", nil
	}
	token := string(uuid.NewUUID())
	err := b.mutateBotHomeFence(ctx, h, func(state botHomeFenceState, rs map[string]botHomeReservation) error {
		if state != botHomeActive {
			return fmt.Errorf("sandbox/k8s: home attachment fenced (%s)", state)
		}
		rs[token] = botHomeReservation{Pod: pod, Pending: true}
		return nil
	})
	return token, err
}

// cancelUnsubmittedBotHome withdraws only this invocation's pending admission.
// Callers must prove no pod create was submitted; neither an API error nor pod
// absence supplies that proof. Never use this to recover another worker's token.
func (b *K8sBackend) cancelUnsubmittedBotHome(ctx context.Context, h *sandbox.BotHomeBinding, token string) error {
	if h == nil {
		return nil
	}
	return b.mutateBotHomeFence(ctx, h, func(state botHomeFenceState, rs map[string]botHomeReservation) error {
		r, ok := rs[token]
		if state != botHomeActive || !ok || !r.Pending {
			return fmt.Errorf("sandbox/k8s: cannot cancel unsubmitted home reservation")
		}
		delete(rs, token)
		return nil
	})
}

func (b *K8sBackend) settleBotHome(ctx context.Context, h *sandbox.BotHomeBinding, token string) error {
	if h == nil {
		return nil
	}
	return b.mutateBotHomeFence(ctx, h, func(state botHomeFenceState, rs map[string]botHomeReservation) error {
		r, ok := rs[token]
		if !ok {
			return fmt.Errorf("sandbox/k8s: missing home reservation")
		}
		if state != botHomeActive {
			return fmt.Errorf("sandbox/k8s: home fenced while attached")
		}
		r.Pending = false
		// Coalesce only completed attempts for the same physical pod name.
		// Never erase another invocation's unresolved right to create.
		for other, prior := range rs {
			if other != token && prior.Pod == r.Pod && !prior.Pending {
				delete(rs, other)
			}
		}
		rs[token] = r
		return nil
	})
}

// Called only once the creating invocation has finished. A successful DELETE
// is not evidence of absence (finalizers and termination can retain the pod).
func (b *K8sBackend) releaseBotHomePod(ctx context.Context, h *sandbox.BotHomeBinding, pod string) error {
	if h == nil {
		return nil
	}
	return b.mutateBotHomeFence(ctx, h, func(_ botHomeFenceState, rs map[string]botHomeReservation) error {
		_, err := b.client.CoreV1().Pods(b.cfg.Namespace).Get(ctx, pod, metav1.GetOptions{})
		if !apierrors.IsNotFound(err) {
			if err != nil {
				return err
			}
			return fmt.Errorf("sandbox/k8s: home attachment pod still exists")
		}
		for token, r := range rs {
			if r.Pod == pod {
				if r.Pending {
					return fmt.Errorf("sandbox/k8s: unresolved home attachment reservation")
				}
				delete(rs, token)
			}
		}
		return nil
	})
}
