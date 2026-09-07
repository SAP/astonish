// detect.go — Docker availability detection for the Docker backend.
//
// DetectDocker checks whether the Docker daemon is reachable. It replaces the
// old incus.DetectPlatform() call sites (which detected Incus vs Docker+Incus).
// Now there is only one platform: Docker.
package docker

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// DetectResult describes the Docker environment found on the host.
type DetectResult struct {
	// Available is true if a Docker daemon is reachable and responsive.
	Available bool

	// Version is the Docker daemon version string (e.g. "24.0.7").
	Version string

	// Socket is the path or URL of the Docker socket in use
	// (e.g. "unix:///var/run/docker.sock" or "tcp://127.0.0.1:2375").
	Socket string

	// Reason is a human-readable explanation when Available is false.
	Reason string
}

// DetectDocker checks whether Docker is installed and the daemon is running.
// It probes using the docker binary on PATH (or the provided binary path).
// Unlike the old incus.DetectPlatform which had distinct Linux-native vs
// Docker-Incus code paths, Docker is the same on all platforms.
func DetectDocker(runtimePath string) DetectResult {
	if runtimePath == "" {
		runtimePath = "docker"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	out, err := runDocker(ctx, runtimePath, "version", "--format",
		"{{.Server.Version}}\t{{.Client.Context}}")
	if err != nil {
		return DetectResult{
			Available: false,
			Reason:    fmt.Sprintf("docker version failed: %v", err),
		}
	}

	parts := strings.SplitN(strings.TrimSpace(string(out)), "\t", 2)
	version := parts[0]
	socket := ""
	if len(parts) > 1 {
		socket = parts[1]
	}

	return DetectResult{
		Available: true,
		Version:   version,
		Socket:    socket,
	}
}

// IsDockerAvailable is a convenience wrapper that returns true if Docker is
// reachable using the default binary.
func IsDockerAvailable() bool {
	return DetectDocker("").Available
}
