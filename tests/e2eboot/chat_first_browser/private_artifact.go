package main

import (
	"fmt"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"strings"
)

const artifactLoadClarification = "artifact-load-clarification"
const privateArtifactInstructions = "Propose a separate task_once.instructions: use load_file exactly once on /root/chat-first-proof.txt to attach the existing file to this authenticated Home's private Files/download card. load_file reads the local file and returns its private attachment; this is not external publishing, network access, or contacting anyone. This task contains only that one read-only action. Do not write or modify anything; no other tools. Preserve all existing permissions."

// Only an unambiguous terminal, tool-free refusal with no new proposal permits
// one distinct clarification. The original Sent/Before/IDs/Consents stay intact.
func loadRefusal(cp smokeCheckpoint, rs []store.BotRun, ps []store.BotControlProposal) (store.BotRun, error) {
	var zero store.BotRun
	if !cp.Sent["artifact-load"] || cp.IDs["artifact-load"] != "" || cp.Before["artifact-load"] == nil {
		return zero, fmt.Errorf("load clarification lacks original unproposed checkpoint")
	}
	for _, p := range ps {
		if !cp.Before["artifact-load"][p.ID.String()] {
			return zero, fmt.Errorf("new proposal exists; do not clarify")
		}
	}
	var write store.BotRun
	for _, r := range rs {
		if r.ID.String() == cp.IDs["artifact-write-run"] {
			write = r
		}
	}
	if write.ID == uuid.Nil || write.Status != store.RunSucceeded || write.FinishedAt == nil {
		return zero, fmt.Errorf("verified write boundary missing")
	}
	var candidates []store.BotRun
	for _, r := range rs {
		if !r.Status.Terminal() {
			return zero, fmt.Errorf("active run; no clarification")
		}
		if r.CreatedAt.After(*write.FinishedAt) {
			candidates = append(candidates, r)
		}
	}
	if len(candidates) != 1 {
		return zero, fmt.Errorf("ambiguous original load source: %d", len(candidates))
	}
	r := candidates[0]
	summary := strings.ToLower(r.Report.Summary)
	if !r.ControlOnly() || r.Status != store.RunSucceeded || r.Usage.ToolCalls != 0 || !strings.Contains(summary, "load_file") || !strings.Contains(summary, "publish") {
		return zero, fmt.Errorf("not a terminal tool-free load publishing refusal")
	}
	return r, nil
}
