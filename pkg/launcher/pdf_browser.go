package launcher

import (
	"context"
	"fmt"
	"time"

	"github.com/SAP/astonish/pkg/sandbox"
)

// newBackendPDFResolveFunc returns a PDF-specific browser resolver for
// backend-managed sandboxes such as direct K8s. Browser manager callbacks for
// this path operate on session IDs, not pod names, so the returned handle is the
// chat session ID with a loopback address that is tunneled by ContainerDialFunc.
func newBackendPDFResolveFunc(backend sandbox.Backend, _ *sandbox.SessionRegistry) func(sessionID string) (string, string, error) {
	return func(sessionID string) (string, string, error) {
		if backend == nil {
			return "", "", fmt.Errorf("PDF resolve: backend is nil")
		}
		if sessionID == "" {
			return "", "", fmt.Errorf("PDF resolve: session ID is required")
		}

		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if err := backend.StartSession(ctx, sessionID); err != nil {
			return "", "", fmt.Errorf("PDF resolve: start session: %w", err)
		}
		if err := backend.WaitForSessionReady(ctx, sessionID); err != nil {
			return "", "", fmt.Errorf("PDF resolve: wait for ready: %w", err)
		}

		return sessionID, "127.0.0.1", nil
	}
}
