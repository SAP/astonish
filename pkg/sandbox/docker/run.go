// run.go — Low-level Docker CLI subprocess runner.
//
// runDocker is the single point through which all docker CLI invocations
// pass. It captures stdout, returns stderr merged into the error on non-zero
// exit, and respects context cancellation.
//
// Using the Docker CLI (rather than the Go Docker SDK) means:
//   - No version negotiation / API compatibility shim needed.
//   - Works with any Docker-compatible CLI (Podman, Rancher Desktop, etc.)
//     as long as the binary is on PATH.
//   - Smaller binary: no vendored Docker SDK.
package docker

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// runDocker executes the given docker subcommand, waits for it to complete,
// and returns its stdout as a byte slice. On non-zero exit the error includes
// the captured stderr.
func runDocker(ctx context.Context, binary string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, binary, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		stderrStr := strings.TrimSpace(stderr.String())
		if stderrStr != "" {
			return nil, fmt.Errorf("%w\nstderr: %s", err, stderrStr)
		}
		return nil, err
	}
	return stdout.Bytes(), nil
}

// isDockerNotFoundError reports whether the error looks like a "container not
// found" or "no such container" Docker error. Used to implement idempotent
// DestroySession and SessionState.
func isDockerNotFoundError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "no such container") ||
		strings.Contains(msg, "no such object") ||
		strings.Contains(msg, "not found") ||
		strings.Contains(msg, "does not exist")
}
