// fleet.go — Fleet container management for DockerBackend.
//
// EnsureFleetContainer is the idempotent "ensure a long-running fleet worker
// container exists" primitive. It delegates to CreateSession with fleet-specific
// naming and labeling.
package docker

import (
	"context"
	"fmt"
	"time"

	"github.com/SAP/astonish/pkg/sandbox"
)

// EnsureFleetContainer creates (if absent) a long-running fleet container.
// Called repeatedly by the fleet controller; idempotent and cheap on the hot path.
func (db *DockerBackend) EnsureFleetContainer(ctx context.Context, spec sandbox.FleetSpec) (*sandbox.Session, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	// Fleet containers use a stable session ID derived from the fleet key + org/team.
	sessionID := fleetSessionID(spec.FleetKey, spec.OrgSlug, spec.TeamSlug)

	// Check if it already exists.
	state, err := db.SessionState(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("sandbox/docker: EnsureFleetContainer state check: %w", err)
	}

	if state == sandbox.SessionStateRunning {
		return &sandbox.Session{
			SessionID:  sessionID,
			Type:       sandbox.SessionTypeFleet,
			TemplateID: spec.TemplateID,
			OrgSlug:    spec.OrgSlug,
			TeamSlug:   spec.TeamSlug,
			State:      sandbox.SessionStateRunning,
			BackendRef: containerName(sessionID),
			Labels:     spec.Labels,
			CreatedAt:  time.Now().UTC(),
		}, nil
	}

	if state == sandbox.SessionStateStopped {
		// Resume a stopped fleet container.
		if err := db.StartSession(ctx, sessionID); err != nil {
			return nil, fmt.Errorf("sandbox/docker: EnsureFleetContainer restart: %w", err)
		}
		if err := db.WaitForSessionReady(ctx, sessionID); err != nil {
			return nil, fmt.Errorf("sandbox/docker: EnsureFleetContainer wait: %w", err)
		}
		return &sandbox.Session{
			SessionID:  sessionID,
			Type:       sandbox.SessionTypeFleet,
			TemplateID: spec.TemplateID,
			OrgSlug:    spec.OrgSlug,
			TeamSlug:   spec.TeamSlug,
			State:      sandbox.SessionStateRunning,
			BackendRef: containerName(sessionID),
			Labels:     spec.Labels,
			CreatedAt:  time.Now().UTC(),
		}, nil
	}

	// No container exists yet; create one.
	labels := make(map[string]string)
	for k, v := range spec.Labels {
		labels[k] = v
	}
	labels["astonish.fleet_key"] = spec.FleetKey

	createSpec := sandbox.SessionSpec{
		SessionID:  sessionID,
		Type:       sandbox.SessionTypeFleet,
		TemplateID: spec.TemplateID,
		OrgSlug:    spec.OrgSlug,
		TeamSlug:   spec.TeamSlug,
		Image:      spec.Image,
		Labels:     labels,
		Limits:     spec.Limits,
	}

	sess, err := db.CreateSession(ctx, createSpec)
	if err != nil {
		return nil, fmt.Errorf("sandbox/docker: EnsureFleetContainer create: %w", err)
	}
	if err := db.WaitForSessionReady(ctx, sessionID); err != nil {
		return nil, fmt.Errorf("sandbox/docker: EnsureFleetContainer wait ready: %w", err)
	}
	sess.State = sandbox.SessionStateRunning
	return sess, nil
}

// fleetSessionID derives a stable, deterministic session ID for a fleet worker.
// Fleet containers must survive daemon restarts so the ID must be deterministic.
func fleetSessionID(fleetKey, orgSlug, teamSlug string) string {
	key := fleetKey
	if orgSlug != "" {
		key += "-" + orgSlug
	}
	if teamSlug != "" {
		key += "-" + teamSlug
	}
	return "fleet-" + sanitizeName(key)
}

// sanitizeName replaces characters that are invalid in container names.
func sanitizeName(s string) string {
	var b []byte
	for _, c := range []byte(s) {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_':
			b = append(b, c)
		default:
			b = append(b, '-')
		}
	}
	return string(b)
}
