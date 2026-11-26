package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/scheduler"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

// Unlike the exact-action matrix, this acceptance executes one complete safe
// script in one admitted run. It never manufactures grants or resumes between
// steps. The transport is scoped to test-owned bytes; physical backend evidence
// remains a separate acceptance tier.
func TestBotChatConfiguredContinuousAdmissionParityHTTP(t *testing.T) {
	runConfiguredAdmissionParityHTTP(t, true)
}

const configuredContinuousTask = "Read the fixture file, discover matching fixture files, list the bounded fixture tree, then read the same file again. Reply in chat only. Do not write, execute shell commands, or run flows."

func admitConfiguredContinuousSchedule(t *testing.T, ctx context.Context, worker *BotWorker, repo store.BotStore, b *store.Bot, task string) []byte {
	t.Helper()
	worker.schedulerReady.Store(true)
	var err error
	b, err = repo.Get(ctx, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	due := time.Now().UTC().Add(-time.Minute).Truncate(time.Second)
	goal, err := repo.(store.BotGoalStore).SaveGoal(ctx, store.BotGoalUpdate{BotID: b.ID, Goal: store.BotGoal{BotID: b.ID, Title: "Inspect fixture", CompletionCriteria: "Return fixture bytes in chat", DueAt: &due}})
	if err != nil {
		t.Fatal(err)
	}
	tid := uuid.New()
	for _, action := range []store.BotTriggerAction{store.BotTriggerSave, store.BotTriggerEnable} {
		b, err = repo.Get(ctx, b.ID)
		if err != nil {
			t.Fatal(err)
		}
		definition := store.TriggerSpec{}
		if action == store.BotTriggerSave {
			definition = store.TriggerSpec{Kind: store.TriggerGoalDeadline, GoalID: &goal.ID, Instructions: task}
		}
		b, err = repo.(store.BotTriggerStore).ChangeTrigger(ctx, store.BotTriggerChange{BotID: b.ID, TriggerID: tid, ExpectedRevision: b.Revision, Action: action, Reason: "reviewed read-only fixture", Definition: definition})
		if err != nil {
			t.Fatal(err)
		}
	}
	version, err := repo.GetVersion(ctx, b.ID, b.Version)
	if err != nil {
		t.Fatal(err)
	}
	if err = checkConfiguredContinuousControlCeiling(ctx, b, *version); err != nil {
		t.Fatal(err)
	}
	var jobID string
	for _, trigger := range version.Triggers {
		if trigger.ID == tid && trigger.ScheduleID != nil {
			jobID = trigger.ScheduleID.String()
		}
	}
	if jobID == "" {
		t.Fatal("missing durable schedule")
	}
	identity := fmt.Sprintf("%s/%d/%s/%s", b.ID, b.Version, tid, due.Format(time.RFC3339Nano))
	key := fmt.Sprintf("bot-schedule-v1:%x", sha256.Sum256([]byte(identity)))
	route := BotSchedulerScope{OrgSlug: "admission", TeamSlug: "team"}
	if b.Scope == store.BotScopePersonal {
		route.UserID = b.OwnerID.String()
	}
	callback := worker.SchedulerCallbacks().Admission()
	request := scheduler.BotTriggerRequest{JobID: jobID, BotID: b.ID, Version: b.Version, TriggerID: tid, OccurrenceAt: due, IdempotencyKey: key}
	receipt, err := callback(withBotSchedulerScope(ctx, route), request)
	if err != nil {
		t.Fatal(err)
	}
	duplicate, err := callback(withBotSchedulerScope(ctx, route), request)
	if err != nil || duplicate.RunID != receipt.RunID || duplicate.Status != scheduler.BotAdmissionDuplicate {
		t.Fatalf("scheduled duplicate changed logical run: %+v %v", duplicate, err)
	}
	run, err := repo.GetRun(ctx, b.ID, receipt.RunID)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(run)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// Probe the same saved policy that the worker installs, without attempting an
// effect or issuing approval. Allowed discovery never grants consequential work.
func checkConfiguredContinuousControlCeiling(ctx context.Context, b *store.Bot, v store.BotVersion) error {
	id := execution.BotRunIdentity{BotID: b.ID, RunID: uuid.New(), Version: b.Version, ActorID: b.OwnerID.String(), OrgSlug: "admission", TeamSlug: "team"}
	policy, err := execution.NewRunPolicy(execution.RunPolicySpec{Identity: id, Level: v.Autonomy.Level, AllowedTools: v.Autonomy.AllowedTools, DeniedTools: v.Autonomy.DeniedTools})
	if err != nil {
		return err
	}
	ctx = execution.WithRunPolicy(execution.WithBotRunIdentity(ctx, id), policy)
	for _, name := range []string{"write_file", "shell_command", "run_flow"} {
		decision := execution.EvaluateRunPolicy(ctx, execution.ActionDescriptor{Tool: name, Arguments: json.RawMessage(`{}`), Effect: execution.EffectUnknown}, time.Now())
		if decision.Kind != execution.PolicyDeny {
			return errors.New("read-only saved policy authorized consequential control: " + name)
		}
	}
	return nil
}
