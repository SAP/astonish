package entstore

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

type reviewJobTamper struct {
	store.SchedulerStore
	change func(*store.ScheduledJob)
}

func (s reviewJobTamper) Get(ctx context.Context, id string) *store.ScheduledJob {
	j := s.SchedulerStore.Get(ctx, id)
	if j != nil {
		s.change(j)
	}
	return j
}

func TestBotControlProposalExactScheduleAuthority(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			r, ctx, b, _ := scheduledControlSafety(t, scope)
			v, err := r.db.ReadVersion(ctx, b.ID, b.Version)
			if err != nil {
				t.Fatal(err)
			}
			jobs := r.db.(botTriggerDB).triggerScheduler()
			for name, change := range map[string]func(*store.ScheduledJob){
				"other_bot":     func(j *store.ScheduledJob) { j.Payload.BotID = uuid.NewString() },
				"other_trigger": func(j *store.ScheduledJob) { j.Payload.BotTriggerID = uuid.NewString() },
				"other_owner":   func(j *store.ScheduledJob) { j.OwnerID = uuid.NewString() },
				"other_team":    func(j *store.ScheduledJob) { j.TeamSlug = "foreign-team" },
				"other_version": func(j *store.ScheduledJob) { j.Payload.BotVersion++ },
				"other_mode":    func(j *store.ScheduledJob) { j.Mode = "adaptive" },
			} {
				t.Run(name, func(t *testing.T) {
					err := r.validateControlScheduleBindings(ctx, reviewJobTamper{jobs, change}, *b, v.Triggers, b.Version)
					if !errors.Is(err, store.ErrBotConflict) {
						t.Fatalf("foreign schedule accepted: %v", err)
					}
				})
			}
		})
	}
}

func TestBotControlProposalMaliciousPayloadAndDecision(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			r, ctx, worker, run, lease, _ := botRunFixture(t, scope)
			p, err := r.ProposeControl(worker, store.BotControlProposalRequest{Lease: lease, IdempotencyKey: "cross-bot", ExpiresAt: r.clock().Add(time.Hour), Payload: store.BotControlPayload{Operation: store.BotControlGoalCreate, Reason: "malicious target", Goal: &store.BotControlGoal{Goal: store.BotGoal{BotID: uuid.New(), Title: "foreign", CompletionCriteria: "foreign"}}}})
			if err != nil {
				t.Fatal(err)
			}
			if err = r.CommitRun(worker, store.BotRunCommit{Lease: lease, Status: store.RunSucceeded, Report: store.BotReport{Summary: "review"}}); err != nil {
				t.Fatal(err)
			}
			before, err := r.Get(ctx, run.BotID)
			if err != nil {
				t.Fatal(err)
			}
			d := store.BotControlProposalDecision{BotID: run.BotID, ProposalID: p.ID, Digest: "wrong"}
			if _, err = r.ConfirmControlProposal(ctx, d); !errors.Is(err, store.ErrBotConflict) {
				t.Fatalf("digest: %v", err)
			}
			d.Digest = p.Digest
			if _, err = r.ConfirmControlProposal(worker, d); !errors.Is(err, store.ErrBotForbidden) {
				t.Fatalf("worker consent: %v", err)
			}
			if _, err = r.ConfirmControlProposal(ctx, d); !errors.Is(err, store.ErrBotInvalid) {
				t.Fatalf("cross bot: %v", err)
			}
			after, err := r.Get(ctx, run.BotID)
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatalf("mutated: %+v %v", after, err)
			}
			saved, err := r.ReadControlProposal(ctx, run.BotID, p.ID)
			if err != nil || saved.Status != store.BotControlProposed {
				t.Fatalf("receipt: %+v %v", saved, err)
			}
		})
	}
}

func TestBotControlProposalApprovalRevocationRollback(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			r, ctx, b, schedule := scheduledControlSafety(t, scope)
			v, err := r.db.ReadVersion(ctx, b.ID, b.Version)
			if err != nil {
				t.Fatal(err)
			}
			p := pendingControlSafety(t, r, ctx, b, store.BotControlPayload{Operation: store.BotControlConfigurationOperation, Reason: "pause reviewed", Configuration: &store.BotControlConfiguration{Pause: true, Autonomy: v.Autonomy, Budget: v.Budget, Delivery: v.Delivery}})
			// Seed a pre-existing pending approval after the review run terminates so
			// terminal-run cleanup cannot pre-revoke the row being tested.
			action := execution.ActionDescriptor{Tool: "send", Effect: execution.EffectSend, Recipient: "alice", Arguments: []byte(`{"body":"review"}`)}
			digest, err := action.Digest()
			if err != nil {
				t.Fatal(err)
			}
			a := store.BotApproval{ID: uuid.New(), BotID: b.ID, RunID: p.SourceRunID, Version: b.Version, Action: action, ActionDigest: digest, Status: store.ApprovalPending, CreatedAt: r.clock(), ExpiresAt: r.clock().Add(time.Hour)}
			if err = r.db.InsertApproval(ctx, a); err != nil {
				t.Fatal(err)
			}
			approvalBefore, err := r.db.ReadApproval(ctx, b.ID, a.ID)
			if err != nil {
				t.Fatal(err)
			}
			rootBefore, err := r.db.GetBot(ctx, b.ID)
			if err != nil {
				t.Fatal(err)
			}
			jobs := r.db.(botTriggerDB).triggerScheduler()
			jobBefore := jobs.Get(ctx, schedule)
			original := r.db
			r.db = controlReceiptFailTransaction{original}
			d := store.BotControlProposalDecision{BotID: b.ID, ProposalID: p.ID, Digest: p.Digest}
			_, err = r.ConfirmControlProposal(ctx, d)
			r.db = original
			if err == nil || err.Error() != "injected receipt failure" {
				t.Fatalf("receipt fault: %v", err)
			}
			approvalAfter, err := r.db.ReadApproval(ctx, b.ID, a.ID)
			if err != nil || !reflect.DeepEqual(approvalBefore, approvalAfter) {
				t.Fatalf("approval escaped rollback: %+v %v", approvalAfter, err)
			}
			rootAfter, err := r.db.GetBot(ctx, b.ID)
			if err != nil || !reflect.DeepEqual(rootBefore, rootAfter) {
				t.Fatalf("root/sequence escaped rollback: %+v %v", rootAfter, err)
			}
			if !reflect.DeepEqual(jobBefore, jobs.Get(ctx, schedule)) {
				t.Fatal("schedule escaped rollback")
			}
			receipt, err := r.ReadControlProposal(ctx, b.ID, p.ID)
			if err != nil || receipt.Status != store.BotControlProposed {
				t.Fatalf("receipt escaped rollback: %+v %v", receipt, err)
			}
			if _, err = r.ConfirmControlProposal(ctx, d); err != nil {
				t.Fatal(err)
			}
			approvalAfter, err = r.db.ReadApproval(ctx, b.ID, a.ID)
			if err != nil || approvalAfter.Status != store.ApprovalRevoked {
				t.Fatalf("approval not revoked: %+v %v", approvalAfter, err)
			}
		})
	}
}

func TestBotControlProposalSchedulePayloadPermissions(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		for _, kind := range []string{"supplied_schedule", "credential", "goal", "other_trigger_kind"} {
			t.Run(string(scope)+"/"+kind, func(t *testing.T) {
				r, ctx, worker, run, lease, _ := botRunFixture(t, scope)
				b, err := r.Get(ctx, run.BotID)
				if err != nil {
					t.Fatal(err)
				}
				tr := store.TriggerSpec{Kind: store.TriggerCron, Cron: "0 8 * * *", Timezone: "UTC", Instructions: "check", Enabled: true}
				id := uuid.New()
				want := store.ErrBotForbidden
				switch kind {
				case "supplied_schedule":
					tr.ScheduleID = &id
				case "credential":
					tr.CredentialRef = "foreign-secret"
				case "goal":
					tr.GoalID = &id
				case "other_trigger_kind":
					tr.Kind = store.TriggerManual
					want = store.ErrBotUnsupported
				}
				p, err := r.ProposeControl(worker, store.BotControlProposalRequest{Lease: lease, IdempotencyKey: kind, ExpiresAt: r.clock().Add(time.Hour), Payload: store.BotControlPayload{Operation: store.BotControlScheduleCreate, Reason: "untrusted schedule", Schedule: &store.BotControlSchedule{ExpectedRevision: b.Revision, Trigger: tr}}})
				if err != nil {
					t.Fatal(err)
				}
				if err = r.CommitRun(worker, store.BotRunCommit{Lease: lease, Status: store.RunSucceeded, Report: store.BotReport{Summary: "review"}}); err != nil {
					t.Fatal(err)
				}
				if _, err = r.ConfirmControlProposal(ctx, store.BotControlProposalDecision{BotID: b.ID, ProposalID: p.ID, Digest: p.Digest}); !errors.Is(err, want) {
					t.Fatalf("payload permitted: %v want %v", err, want)
				}
				after, err := r.Get(ctx, b.ID)
				if err != nil || after.Version != b.Version || after.Revision != b.Revision {
					t.Fatalf("root changed: %+v %v", after, err)
				}
				saved, err := r.ReadControlProposal(ctx, b.ID, p.ID)
				if err != nil || saved.Status != store.BotControlProposed {
					t.Fatalf("receipt: %+v %v", saved, err)
				}
			})
		}
	}
}

func TestBotControlProposalMemberInitiatingActor(t *testing.T) {
	r, owner, worker, initial, lease, _ := botRunFixture(t, store.BotScopeTeam)
	if err := r.CommitRun(worker, store.BotRunCommit{Lease: lease, Status: store.RunSucceeded, Report: store.BotReport{Summary: "done"}}); err != nil {
		t.Fatal(err)
	}
	principal, _ := execution.PrincipalFromContext(owner)
	memberID := uuid.New()
	principal.Subject = memberID.String()
	member, err := execution.WithPrincipal(context.Background(), principal)
	if err != nil {
		t.Fatal(err)
	}
	run, err := r.RequestRun(member, store.BotRunRequest{BotID: initial.BotID, Version: initial.Version, Intent: store.BotIntentRegular, Trigger: store.TriggerManual, Instructions: "review", IdempotencyKey: "member"})
	if err != nil {
		t.Fatal(err)
	}
	// Production worker executes as owner, not as the conversation's initiating member.
	b, err := r.Get(owner, initial.BotID)
	if err != nil {
		t.Fatal(err)
	}
	worker = execution.WithBotRunIdentity(owner, execution.BotRunIdentity{BotID: b.ID, RunID: run.ID, Version: b.Version, ActorID: b.OwnerID.String(), OrgSlug: "org", TeamSlug: "team"})
	lease, err = r.ClaimRun(worker, store.BotClaimRequest{BotID: b.ID, RunID: run.ID, WorkerID: "member-review", ExpiresAt: r.clock().Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	text := "reviewed responsibility"
	p, err := r.ProposeControl(worker, store.BotControlProposalRequest{Lease: lease, IdempotencyKey: "member", ExpiresAt: r.clock().Add(time.Hour), Payload: store.BotControlPayload{Operation: store.BotControlResponsibility, Reason: "member asks", Responsibility: &text}})
	if err != nil {
		t.Fatal(err)
	}
	if p.SourceUserID != memberID {
		t.Fatalf("worker owner spoofed source: %s want %s", p.SourceUserID, memberID)
	}
	if err = r.CommitRun(worker, store.BotRunCommit{Lease: lease, Status: store.RunSucceeded, Report: store.BotReport{Summary: "ready"}}); err != nil {
		t.Fatal(err)
	}
	d := store.BotControlProposalDecision{BotID: b.ID, ProposalID: p.ID, Digest: p.Digest}
	if _, err = r.ConfirmControlProposal(member, d); !errors.Is(err, store.ErrBotForbidden) {
		t.Fatalf("member edit: %v", err)
	}
	receipt, err := r.ConfirmControlProposal(owner, d)
	if err != nil || receipt.DecidedBy == nil || *receipt.DecidedBy != b.OwnerID || receipt.SourceUserID != memberID {
		t.Fatalf("attribution: %+v %v", receipt, err)
	}
}
