package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

const coherentContract = 2

func validateCheckpoint(cp smokeCheckpoint) error {
	if cp.Contract != coherentContract {
		return fmt.Errorf("BLOCK: legacy checkpoint cannot be reinterpreted; never replay an already-consumed write; start a separately authorized fresh run")
	}
	return nil
}

func validateExactAction(a store.BotApproval, tool string) error {
	raw, err := json.Marshal(a.Action)
	if err != nil {
		return err
	}
	var action struct {
		Tool      string
		Arguments map[string]any
	}
	if err := json.Unmarshal(raw, &action); err != nil {
		return err
	}
	if action.Tool != tool || action.Arguments["path"] != retainedArtifactPath || a.ActionDigest == "" {
		return fmt.Errorf("unexpected exact %s action", tool)
	}
	if tool == "write_file" && action.Arguments["content"] != retainedArtifactBytes {
		return fmt.Errorf("write bytes changed")
	}
	return nil
}

func validateSingleAction(r store.BotRun, approvals []store.BotApproval, consents map[string]string, tool string) (string, error) {
	if r.ID == uuid.Nil || r.Status != store.RunSucceeded || r.Usage.ToolCalls != 1 {
		return "", fmt.Errorf("single %s task must succeed with exactly one tool, got %s/%d; no replay", tool, r.Status, r.Usage.ToolCalls)
	}
	var found []store.BotApproval
	for _, a := range approvals {
		if a.RunID == r.ID {
			found = append(found, a)
		}
	}
	if len(found) != 1 {
		return "", fmt.Errorf("expected exactly one approval for %s, got %d", tool, len(found))
	}
	a := found[0]
	if err := validateExactAction(a, tool); err != nil {
		return "", err
	}
	if a.ID == uuid.Nil || a.Status != store.ApprovalConsumed || consents[a.ID.String()] != a.ActionDigest {
		return "", fmt.Errorf("approval lacks exact consent or single consumption")
	}
	return a.ID.String(), nil
}

// Mirrors pkg/sandbox/docker/backend.go:containerName for UUID session IDs.
// Query exact names, never an unrecognized label that trivially matches nothing.
func smokeContainerName(id uuid.UUID) string {
	s := id.String()
	sum := sha256.Sum256([]byte(s))
	return fmt.Sprintf("astonish-session-%s-%x", strings.Trim(s[:20], "-"), sum[:6])
}

type smokeSchedulerJob struct {
	ID      string     `json:"id"`
	Enabled bool       `json:"enabled"`
	NextRun *time.Time `json:"next_run"`
	Payload struct {
		BotID     string `json:"bot_id"`
		TriggerID string `json:"bot_trigger_id"`
	} `json:"payload"`
}

func schedulerState(jobs []smokeSchedulerJob, botID, triggerID string, enabled bool) bool {
	count := 0
	for _, j := range jobs {
		if j.Payload.BotID != botID || j.Payload.TriggerID != triggerID {
			continue
		}
		count++
		if j.Enabled != enabled {
			return false
		}
		if enabled && (j.NextRun == nil || j.NextRun.IsZero()) {
			return false
		}
		if !enabled && j.NextRun != nil {
			return false
		}
	}
	// Disabled rows may be removed by reconciliation. Activation must have one.
	if enabled {
		return count == 1
	}
	return count <= 1
}
