package api

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

// The private repository already marks these leaves as non-replayable. The
// adapter must carry that typed fact to the supervisor, not merely retain prose.
type preparedBoundaryStore struct {
	*resumeTestStore
	store.BotPreparedPlanStore
	record *store.BotPreparedPlan
}

func (s *preparedBoundaryStore) LoadBotPlanByApproval(context.Context, store.BotLease, uuid.UUID) (*store.BotPreparedPlan, error) {
	return s.record, nil
}

func TestBotPreparedBoundaryUnsettledRecoveryPreservesUncertainOutcome(t *testing.T) {
	for _, stage := range []store.BotPreparedPlanStage{store.BotPlanDispatchStarted, store.BotPlanUncertain} {
		t.Run(string(stage), func(t *testing.T) {
			ctx, req, _ := botChatTestRequest(t)
			action := execution.ActionDescriptor{Tool: "write_file", Arguments: json.RawMessage(`{"path":"/workspace/PLAN.md","content":"saved"}`), Effect: execution.EffectSend}
			digest, err := action.Digest()
			if err != nil {
				t.Fatal(err)
			}
			approvalID := uuid.New()
			checkpoint, err := json.Marshal(bot.ApprovalCheckpoint{Format: bot.ApprovalCheckpointFormat, ApprovalID: approvalID, BotID: req.Run.BotID, RunID: req.Run.ID, SessionID: req.Run.SessionID, Version: req.Run.Version, ActionDigest: digest, Fence: req.Run.Lease.Fence})
			if err != nil {
				t.Fatal(err)
			}
			req.Run.Checkpoint = checkpoint
			req.Run.Lease.Fence++
			s := &preparedBoundaryStore{resumeTestStore: &resumeTestStore{}, record: &store.BotPreparedPlan{Stage: stage, Action: action, ActionDigest: digest, ApprovalID: approvalID}}
			ctx = bot.WithApprovalStore(ctx, s)
			constructed := 0
			adapter, err := NewBotChatRuntimeAdapter(func(context.Context, bot.RunRequest) (*BotChatResources, error) {
				constructed++
				return nil, store.ErrBotUnsupported
			})
			if err != nil {
				t.Fatal(err)
			}
			result, err := adapter.Resume(ctx, req, func(context.Context, bot.RuntimeEvent) error { return nil })
			if err == nil || constructed != 0 {
				t.Fatalf("unsettled recovery reached construction: err=%v constructed=%d", err, constructed)
			}
			if result.Failure == nil || !result.Failure.UncertainEffect {
				t.Errorf("adapter discarded durable uncertain-effect authority: failure=%+v", result.Failure)
			}
			if result.Report.EffectState != store.BotReportEffectsUncertain {
				t.Errorf("uncertain effect became report state %q", result.Report.EffectState)
			}
			if string(result.Checkpoint) != string(checkpoint) {
				t.Error("adapter discarded the exact reconciliation checkpoint")
			}
		})
	}
}
