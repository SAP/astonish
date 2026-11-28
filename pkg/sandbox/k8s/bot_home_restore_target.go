package k8s

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/google/uuid"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const botHomeRestoreSeparator = "-restore-"

// Generation references extend v1 without changing any existing reference or
// directory. The namespace/claim/scope digest remains mandatory; aliases are not
// accepted. All callers validate the binding before deriving mounts or owners.
func (b *K8sBackend) validBotHomeReference(h *sandbox.BotHomeBinding) bool {
	base := b.botHomeRef(h.Scope)
	if h.Ref == base {
		return true
	}
	suffix, ok := strings.CutPrefix(h.Ref, base+botHomeRestoreSeparator)
	if !ok {
		return false
	}
	id, err := uuid.Parse(suffix)
	return err == nil && id != uuid.Nil && id.String() == suffix
}

func botHomeIsRestoreTarget(h *sandbox.BotHomeBinding) bool {
	return h != nil && strings.Contains(h.Ref, botHomeRestoreSeparator)
}

func botHomeStorageSubPath(h *sandbox.BotHomeBinding) string {
	if !botHomeIsRestoreTarget(h) {
		return h.Scope.Key()
	}
	// A sibling, never a child of the original home: purging either cannot walk
	// into another generation. Hash the entire validated reference, not a user path.
	return fmt.Sprintf("%s-restore-%x", h.Scope.Key(), sha256.Sum256([]byte(h.Ref)))
}

func botHomeStorageOwner(h *sandbox.BotHomeBinding) string {
	if !botHomeIsRestoreTarget(h) {
		return h.Ref
	}
	// Preserve the compiled roots' existing versioned digest format and exact
	// .owner comparison, while binding it to this entire physical generation.
	return fmt.Sprintf("%s%x", botHomeRefPrefix, sha256.Sum256([]byte(h.Ref)))
}

func (b *K8sBackend) CreateBotHomeRestoreTarget(ctx context.Context, scope sandbox.BotHomeScope) (*sandbox.BotHomeBinding, error) {
	return b.CreateBotHomeRestoreTargetJournaled(ctx, scope, func(*sandbox.BotHomeBinding) error { return nil })
}

// CreateBotHomeRestoreTargetJournaled records the exact independently disposable
// generation before its durable admission fence is allocated. It never adopts an
// existing fence or touches source storage. Physical initialization is deferred
// to RestoreBotHomeArchive's exclusively owned attachment, not a Bot session.
func (b *K8sBackend) CreateBotHomeRestoreTargetJournaled(ctx context.Context, scope sandbox.BotHomeScope, reserve func(*sandbox.BotHomeBinding) error) (*sandbox.BotHomeBinding, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := scope.Validate(); err != nil {
		return nil, err
	}
	if reserve == nil {
		return nil, errors.New("sandbox/k8s: restore journal required")
	}
	if b.cfg.HomesPVCName == "" {
		return nil, sandbox.ErrBotHomeUnsupported
	}
	if b.client == nil {
		return nil, ErrNotImplementedYet
	}
	pvc, err := b.client.CoreV1().PersistentVolumeClaims(b.cfg.Namespace).Get(ctx, b.cfg.HomesPVCName, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	if pvc.DeletionTimestamp != nil {
		return nil, errors.New("sandbox/k8s: retained homes claim is deleting")
	}
	h := &sandbox.BotHomeBinding{Scope: scope, Backend: sandbox.BackendKindK8s, Ref: b.botHomeRef(scope) + botHomeRestoreSeparator + uuid.NewString()}
	if err := reserve(h.Clone()); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	raw, _ := json.Marshal(h)
	// No retry/adoption on AlreadyExists or transport ambiguity. The journal keeps
	// this exact reference for fenced cleanup even if the create response is lost.
	_, err = b.client.CoreV1().ConfigMaps(b.cfg.Namespace).Create(ctx, &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{Name: botHomeFenceName(h), Namespace: b.cfg.Namespace},
		Data:       map[string]string{"binding": string(raw), "state": string(botHomeActive), "reservations": "{}"},
	}, metav1.CreateOptions{})
	if err != nil {
		return nil, err
	}
	return h, nil
}
