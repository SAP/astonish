// network.go — Networking methods for DockerBackend.
//
// Org isolation uses Docker networks: each org gets a named Docker bridge
// network. Containers in the same org are attached to that network, preventing
// cross-org lateral movement.
//
// Port exposure uses the `docker port` mechanism or, for more dynamic exposure,
// a portforward proxy (TODO: implement full port proxy for Studio browser VNC).
package docker

import (
	"context"
	"fmt"
	"strings"

	"github.com/SAP/astonish/pkg/sandbox"
)

// orgNetworkName returns the Docker network name for an org slug.
func orgNetworkName(orgSlug string) string {
	// Sanitize: Docker network names must be [a-zA-Z0-9_.-].
	safe := strings.NewReplacer(
		"/", "_",
		"@", "_",
		" ", "_",
	).Replace(orgSlug)
	return "astonish-org-" + safe
}

// EnsureOrgNetwork provisions the Docker bridge network for an org.
// Idempotent: safe to call repeatedly — docker network create is only
// invoked once per org per process lifetime.
func (db *DockerBackend) EnsureOrgNetwork(ctx context.Context, orgSlug string) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	netName := orgNetworkName(orgSlug)

	// Check in-process cache first (fast path).
	db.mu.RLock()
	cached := db.networkRegistry[orgSlug]
	db.mu.RUnlock()
	if cached {
		return nil
	}

	// Check if the Docker network already exists (handles restart case).
	out, _ := runDocker(ctx, db.cfg.ContainerRuntimePath,
		"network", "inspect", "--format", "{{.Name}}", netName,
	)
	if strings.TrimSpace(string(out)) == netName {
		db.mu.Lock()
		db.networkRegistry[orgSlug] = true
		db.mu.Unlock()
		return nil
	}

	// Create the network with internal=false so containers can reach the
	// internet but are isolated from other orgs' containers.
	_, err := runDocker(ctx, db.cfg.ContainerRuntimePath,
		"network", "create",
		"--driver", "bridge",
		"--label", fmt.Sprintf("astonish.org=%s", orgSlug),
		netName,
	)
	if err != nil {
		// Tolerate "already exists" race (two goroutines EnsureOrgNetwork simultaneously).
		if strings.Contains(err.Error(), "already exists") {
			db.mu.Lock()
			db.networkRegistry[orgSlug] = true
			db.mu.Unlock()
			return nil
		}
		return fmt.Errorf("sandbox/docker: EnsureOrgNetwork %q: %w", orgSlug, err)
	}

	db.mu.Lock()
	db.networkRegistry[orgSlug] = true
	db.mu.Unlock()
	return nil
}

// DeleteOrgNetwork removes the Docker bridge network for an org.
// Idempotent: no error if the network does not exist.
func (db *DockerBackend) DeleteOrgNetwork(ctx context.Context, orgSlug string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	netName := orgNetworkName(orgSlug)
	_, err := runDocker(ctx, db.cfg.ContainerRuntimePath, "network", "rm", netName)
	if err != nil {
		// Ignore "No such network" errors for idempotency.
		if strings.Contains(err.Error(), "No such network") ||
			strings.Contains(err.Error(), "no such network") {
			err = nil
		}
	}
	db.mu.Lock()
	delete(db.networkRegistry, orgSlug)
	db.mu.Unlock()
	return err
}

// ExposePort opens an inbound route to a port inside the container.
// For Docker, we use `docker inspect` to get the container IP, then return
// it as the externally-visible address. Full port-forward proxy is a TODO.
func (db *DockerBackend) ExposePort(ctx context.Context, sessionID string, port int, proto string) (*sandbox.ExposedAddr, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	cname := containerName(sessionID)

	// Get the container's IP address on the bridge network.
	out, err := runDocker(ctx, db.cfg.ContainerRuntimePath,
		"inspect", "--format",
		"{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}",
		cname,
	)
	if err != nil {
		return nil, fmt.Errorf("sandbox/docker: ExposePort inspect %s: %w", sessionID, err)
	}
	ip := strings.TrimSpace(string(out))
	if ip == "" {
		ip = "127.0.0.1" // fallback for host networking
	}
	if proto == "" {
		proto = "tcp"
	}
	return &sandbox.ExposedAddr{
		Host:     ip,
		Port:     port,
		Protocol: proto,
	}, nil
}

// UnexposePort closes a previously-exposed port. For direct container IP
// access there is nothing to tear down; this is a no-op.
func (db *DockerBackend) UnexposePort(ctx context.Context, sessionID string, port int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil // no proxy to remove for direct IP access
}
