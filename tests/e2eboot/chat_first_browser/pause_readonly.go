package main

import (
	"encoding/json"
	"fmt"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

const pauseReadonlyTask = "pause-readonly-task"
const pauseReadonlyTitle = "Approval check"
const pauseReadonlyInstructions = "Propose a task_once for explicit review. task_once.instructions: use load_file exactly once with path /root/chat-first-proof.txt and title Approval check, to attach the existing file to this authenticated Home's private Files card. This is read-only local attachment, not external publication. No writes, network access, other tools, or permission changes."

func pauseTaskInstructions(cp smokeCheckpoint) string {
	if cp.IDs["pause-task"] != "" {
		return "Replace the previous ambiguous pause-test task with a separate task_once for explicit review. The previous task finished without tools; do not retry it or write anything. " + pauseReadonlyInstructions
	}
	return pauseReadonlyInstructions
}

func validatePauseOriginal(cp smokeCheckpoint, rs []store.BotRun, as []store.BotApproval) (store.BotRun, error) {
	var found store.BotRun
	id := cp.IDs["pause-task"]
	if id == "" || !cp.Sent["pause-task"] || cp.Consents[id] == "" {
		return found, fmt.Errorf("missing original reviewed pause task")
	}
	count := 0
	for _, r := range rs {
		if r.IdempotencyKey == "control-proposal:"+id {
			found = r
			count++
		}
	}
	if count != 1 || found.ID == uuid.Nil || !found.Status.Terminal() || found.Usage.ToolCalls != 0 {
		return found, fmt.Errorf("original pause task must be terminal with zero tools")
	}
	for _, a := range as {
		if a.RunID == found.ID {
			return found, fmt.Errorf("original pause task has an approval; replacement refused")
		}
	}
	return found, nil
}

func validatePauseAction(a store.BotApproval) error {
	if err := validateExactAction(a, "load_file"); err != nil {
		return err
	}
	raw, err := json.Marshal(a.Action)
	if err != nil {
		return err
	}
	var action struct{ Arguments map[string]any }
	if err := json.Unmarshal(raw, &action); err != nil {
		return err
	}
	if len(action.Arguments) != 2 || action.Arguments["title"] != pauseReadonlyTitle {
		return fmt.Errorf("pause action must contain only exact path and Approval check title")
	}
	return nil
}
