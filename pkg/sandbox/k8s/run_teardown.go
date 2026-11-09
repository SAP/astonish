package k8s

import (
	"context"
	"fmt"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// VerifySessionDestroyed observes the physical pod, not the drained registry.
// A terminating pod still owns compute and is not a destruction receipt.
func (b *K8sBackend) VerifySessionDestroyed(ctx context.Context, sessionID string) error {
	if b.client == nil || sessionID == "" {
		return fmt.Errorf("sandbox/k8s: physical verification unavailable")
	}
	for {
		_, err := b.client.CoreV1().Pods(b.cfg.Namespace).Get(ctx, podNameForSession(sessionID), metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}
