package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"testing"
	"time"
)

func TestLegacyCheckpointBlocksConsumedWrite(t *testing.T) {
	var cp smokeCheckpoint
	if err := json.Unmarshal([]byte(`{"IDs":{"artifact-run":"already-written"},"Consents":{"approval":"digest"}}`), &cp); err != nil {
		t.Fatal(err)
	}
	if validateCheckpoint(cp) == nil {
		t.Fatal("legacy work reinterpreted")
	}
	cp.Contract = coherentContract
	if err := validateCheckpoint(cp); err != nil {
		t.Fatal(err)
	}
}
func TestSingleActionRequiresIndependentSuccessAndExactConsumption(t *testing.T) {
	r := store.BotRun{ID: uuid.New(), Status: store.RunSucceeded}
	r.Usage.ToolCalls = 1
	args, _ := json.Marshal(map[string]any{"path": retainedArtifactPath, "content": retainedArtifactBytes})
	a := store.BotApproval{ID: uuid.New(), RunID: r.ID, Status: store.ApprovalConsumed, ActionDigest: "exact", Action: execution.ActionDescriptor{Tool: "write_file", Arguments: args}}
	consent := map[string]string{a.ID.String(): a.ActionDigest}
	if _, err := validateSingleAction(r, []store.BotApproval{a}, consent, "write_file"); err != nil {
		t.Fatal(err)
	}
	for _, status := range []store.RunStatus{store.RunPartial, store.RunWaitingApproval} {
		bad := r
		bad.Status = status
		if _, err := validateSingleAction(bad, []store.BotApproval{a}, consent, "write_file"); err == nil {
			t.Fatal("non-success accepted")
		}
	}
	for _, approvals := range [][]store.BotApproval{nil, {a, a}} {
		if _, err := validateSingleAction(r, approvals, consent, "write_file"); err == nil {
			t.Fatal("approval cardinality accepted")
		}
	}
	if _, err := validateSingleAction(r, []store.BotApproval{a}, nil, "write_file"); err == nil {
		t.Fatal("unconsented action accepted")
	}
	if _, err := validateSingleAction(r, []store.BotApproval{a}, consent, "load_file"); err == nil {
		t.Fatal("wrong action accepted")
	}
	r.Usage.ToolCalls = 2
	if _, err := validateSingleAction(r, []store.BotApproval{a}, consent, "write_file"); err == nil {
		t.Fatal("two actions in one run accepted")
	}
}
func TestSchedulerRequiresPersistedNextRunAndNoNextAfterDisable(t *testing.T) {
	var response struct {
		Jobs []smokeSchedulerJob `json:"jobs"`
	}
	err := json.Unmarshal([]byte(`{"jobs":[{"id":"j","enabled":true,"next_run":"2026-11-06T20:00:00Z","payload":{"bot_id":"b","bot_trigger_id":"t"}}]}`), &response)
	if err != nil {
		t.Fatal(err)
	}
	jobs := response.Jobs
	if !schedulerState(jobs, "b", "t", true) {
		t.Fatal("active row rejected")
	}
	if schedulerState(jobs, "b", "t", false) {
		t.Fatal("active row passed disabled")
	}
	jobs[0].Enabled = false
	if schedulerState(jobs, "b", "t", false) {
		t.Fatal("stale next_run passed")
	}
	jobs[0].NextRun = nil
	if !schedulerState(jobs, "b", "t", false) {
		t.Fatal("disabled row rejected")
	}
	jobs[0].Enabled = true
	if schedulerState(jobs, "b", "t", true) {
		t.Fatal("missing next_run passed")
	}
	now := time.Now()
	jobs[0].NextRun = &now
	if schedulerState(append(jobs, jobs[0]), "b", "t", true) {
		t.Fatal("duplicate jobs accepted")
	}
	if schedulerState(jobs, "foreign", "t", true) {
		t.Fatal("foreign Bot accepted")
	}
}
func TestContainerNameUsesProductionUUIDAlgorithm(t *testing.T) {
	id := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	sum := sha256.Sum256([]byte(id.String()))
	want := fmt.Sprintf("astonish-session-00000000-0000-0000-0-%x", sum[:6])
	if got := smokeContainerName(id); got != want {
		t.Fatalf("got %s want %s", got, want)
	}
}
