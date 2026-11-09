package docker

import (
	"context"
	"fmt"
)

// VerifySessionDestroyed requires Docker's explicit not-found response. Neither
// a missing registry row nor an unknown/stopped container state proves teardown.
func (db *DockerBackend) VerifySessionDestroyed(ctx context.Context, sessionID string) error {
	if sessionID == "" {
		return fmt.Errorf("sandbox/docker: session required")
	}
	_, err := runDocker(ctx, db.cfg.ContainerRuntimePath, "inspect", "--format", "{{.Id}}", containerName(sessionID))
	if err != nil && isDockerNotFoundError(err) {
		return nil
	}
	if err != nil {
		return err
	}
	return fmt.Errorf("sandbox/docker: session still exists")
}
