package entstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	botdomain "github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"math"
	"reflect"
	"time"
)

type mandateModelCharge struct {
	Tokens  int64
	Settled bool
}

type mandateRunUsage struct {
	PublicResearchCalls  int64
	Models               map[string]mandateModelCharge
	Tools, Tokens, Spend int64
	Ref                  store.BotMandateRef
}
type mandateTitleContinuation struct {
	Usage      mandateRunUsage
	ApprovalID uuid.UUID
	Checkpoint string
}

type mandateQuestionContinuation struct {
	Usage      mandateRunUsage
	ApprovalID uuid.UUID
	Checkpoint string
}

type mandateUsage struct {
	// Ordinary questions retain accounting only for same-mandate occupancy
	// reacquisition at the exact completed checkpoint; never for general replay.
	QuestionContinuations map[string]mandateQuestionContinuation
	// Only a released, checkpointed initial title question may transfer its
	// cumulative usage to the owner's fresh confirmed-assignment mandate.
	TitleContinuations              map[string]mandateTitleContinuation
	Day                             string
	DailyRuns, Tools, Tokens, Spend int64
	// Released retains cumulative charges without concurrency occupancy or replay.
	Released map[string]mandateRunUsage
	Active   map[string]mandateRunUsage
}
type mandateQuotaAction uint8

const (
	mandateReserve mandateQuotaAction = iota
	mandateConsume
	mandateRelease
	mandateModelReserve
	mandateModelSettle
	mandateResearchReserve
)

// quotaQuestionApproval binds retained occupancy to a typed ordinary question,
// never an action/plan grant, and to the completed lease rather than a newer fence.
func (s *botMandateRuntime) quotaQuestionApproval(ctx context.Context, tx botDB, saved *botStoredRun, rec *botStoredRecord, status store.ApprovalStatus) (*store.BotApproval, error) {
	run := saved.Run
	if !run.IsInteractiveChat() || run.Version != rec.Bot.Version || (run.TitleConfirmation != nil && run.TitleConfirmation.State != store.BotTitleConfirmationConfirmed) {
		return nil, store.ErrBotForbidden
	}
	c, err := botdomain.DecodeApprovalCheckpoint(run.Checkpoint, run)
	if err != nil || c.Format != botdomain.InteractionCheckpointFormat || c.Fence != run.Lease.Fence || rec.ActiveRunID != nil {
		return nil, store.ErrBotConflict
	}
	a, err := tx.ReadApproval(ctx, run.BotID, c.ApprovalID)
	if err != nil {
		return nil, err
	}
	if !c.Matches(*a) || a.Status != status || a.Interaction == nil || a.Interaction.Kind != store.BotInteractionQuestion || !s.repo.clock().Before(a.ExpiresAt) {
		return nil, store.ErrBotForbidden
	}
	if status == store.ApprovalApproved && (a.DecidedAt == nil || a.DecidedBy == nil || *a.DecidedBy != rec.Bot.OwnerID || a.Interaction.ValidateResponse(a.Response) != nil) {
		return nil, store.ErrBotForbidden
	}
	return a, nil
}
func (s *botMandateRuntime) ReserveBotPublicResearch(ctx context.Context, ref store.BotMandateRef, session string) error {
	id, ok := execution.BotRunIdentityFromContext(ctx)
	if !ok || id.BotID != ref.BotID || id.RunID == uuid.Nil || session == "" {
		return store.ErrBotForbidden
	}
	return s.quota(ctx, ref, id.RunID, mandateResearchReserve, 0, 0, 0)
}

func (s *botMandateRuntime) ReserveBotMandateRun(ctx context.Context, ref store.BotMandateRef, run uuid.UUID) error {
	return s.quota(ctx, ref, run, mandateReserve, 0, 0, 0)
}
func (s *botMandateRuntime) ConsumeBotMandateQuota(ctx context.Context, ref store.BotMandateRef, run uuid.UUID, tools, tokens, spend int64) error {
	return s.quota(ctx, ref, run, mandateConsume, tools, tokens, spend)
}
func (s *botMandateRuntime) ReleaseBotMandateRun(ctx context.Context, ref store.BotMandateRef, run uuid.UUID) error {
	return s.quota(ctx, ref, run, mandateRelease, 0, 0, 0)
}
func accountingOverflow(used, delta int64) bool {
	return used < 0 || delta < 0 || delta > math.MaxInt64-used
}

func exceeds(used, delta, max int64) bool {
	return used < 0 || delta < 0 || used > max || delta > max-used
}
func (s *botMandateRuntime) ReserveBotMandateModel(ctx context.Context, ref store.BotMandateRef, run, id uuid.UUID, tokens int64) error {
	if id == uuid.Nil {
		return store.ErrBotInvalid
	}
	return s.quota(ctx, ref, run, mandateModelReserve, 0, tokens, 0, id)
}
func (s *botMandateRuntime) SettleBotMandateModel(ctx context.Context, ref store.BotMandateRef, run, id uuid.UUID, tokens int64) error {
	if id == uuid.Nil {
		return store.ErrBotInvalid
	}
	return s.quota(ctx, ref, run, mandateModelSettle, 0, tokens, 0, id)
}
func (s *botMandateRuntime) quota(ctx context.Context, ref store.BotMandateRef, run uuid.UUID, action mandateQuotaAction, tools, tokens, spend int64, modelID ...uuid.UUID) error {
	if run == uuid.Nil || tools < 0 || tokens < 0 || spend < 0 {
		return store.ErrBotInvalid
	}
	return s.repo.db.Transaction(ctx, func(tx botDB) error {
		// Only this composition-root runtime may release an exact persisted
		// reservation after membership revocation. No caller context bypass exists.
		var rec *botStoredRecord
		var err error
		if action == mandateRelease {
			if ref.Validate() != nil || ref.OrgSlug != s.repo.auth.orgSlug || (s.repo.auth.teamSlug != "" && ref.TeamSlug != s.repo.auth.teamSlug) || (s.repo.auth.scope == store.BotScopePersonal && ref.OwnerID != s.repo.auth.userID) {
				return store.ErrBotForbidden
			}
			rec, err = tx.GetBot(ctx, ref.BotID)
			if err == nil && rec.Bot.OwnerID.String() != ref.OwnerID {
				return store.ErrBotForbidden
			}
		} else {
			rec, err = s.repo.mandateScope(ctx, tx, ref)
		}
		if err != nil {
			return err
		}
		m, err := readMandate(ctx, tx, ref)
		if err != nil {
			return err
		}
		if action != mandateRelease {
			if rec.Bot.Status == store.BotPaused && m.Mandate.Purpose != execution.BotMandatePurposeOperation && !mandateProgressWhilePaused(ctx, tx, rec, ref) {
				return store.ErrBotForbidden
			}
			m, rec, err = s.verify(ctx, tx, ref, m.Mandate.Purpose)
			if err != nil {
				return err
			}
		}
		saved, err := tx.ReadRun(ctx, ref.BotID, run)
		if err != nil {
			return err
		}
		if saved.Run.MandateRef == nil || *saved.Run.MandateRef != ref {
			return store.ErrBotForbidden
		}
		if action != mandateRelease {
			if m.Mandate.Purpose == execution.BotMandatePurposeOperation && saved.Run.ID.String() != m.Provenance.RunID {
				return store.ErrBotForbidden
			}
			if binding, e := readScheduledTask(ctx, tx, ref); e == nil {
				if saved.Run.Trigger != store.TriggerCron || saved.Run.TriggerID != binding.TriggerID || saved.Instructions != binding.Instructions {
					return store.ErrBotForbidden
				}
			} else if !errors.Is(e, store.ErrBotNotFound) {
				return e
			} else if err := verifyRoutineTaskRun(ctx, tx, rec, m, saved); err != nil {
				return err
			}
		}
		usage := mandateUsage{Active: map[string]mandateRunUsage{}}
		if raw, ok := rec.Metadata[mandateUsageKey]; ok {
			b, err := json.Marshal(raw)
			if err != nil {
				return err
			}
			if err = json.Unmarshal(b, &usage); err != nil {
				return err
			}
		}
		if usage.Active == nil {
			usage.Active = map[string]mandateRunUsage{}
		}
		day := s.repo.clock().Format("2006-01-02")
		if usage.Day != day {
			usage.Day = day
			usage.DailyRuns = 0
		}
		id := run.String()
		current, active := usage.Active[id]
		l := m.Mandate.Limits
		reservationKey := "mandate-reservation:" + id
		switch action {
		case mandateReserve:
			if saved.Run.Status != store.RunQueued && saved.Run.Status != store.RunRunning {
				return store.ErrBotConflict
			}
			if saved.ProgressQuota != nil {
				return store.ErrBotConflict
			}
			// Preparation precedes ClaimRun. Do not consume this run's one-shot
			// reservation while another writer (including an interrupted writer
			// awaiting verified teardown) still owns the admission barrier.
			if rec.ActiveRunID != nil && *rec.ActiveRunID != run {
				return store.ErrBotConflict
			}
			if active {
				if current.Ref != ref {
					return store.ErrBotForbidden
				}
				return nil
			}
			if _, err := tx.FindActivity(ctx, ref.BotID, reservationKey); err == nil {
				if pending, ok := usage.QuestionContinuations[id]; ok {
					if saved.Run.Status != store.RunQueued || pending.Usage.Ref != ref || pending.Checkpoint != string(saved.Run.Checkpoint) {
						return store.ErrBotConflict
					}
					a, err := s.quotaQuestionApproval(ctx, tx, saved, rec, store.ApprovalApproved)
					if err != nil || a.ID != pending.ApprovalID {
						return store.ErrBotForbidden
					}
					for _, model := range pending.Usage.Models {
						if !model.Settled {
							return store.ErrBotConflict
						}
					}
					if int64(len(usage.Active)) >= l.MaxConcurrentRuns {
						return store.ErrBotForbidden
					}
					usage.Active[id] = pending.Usage
					delete(usage.QuestionContinuations, id)
					reservationKey += ":question:" + a.ID.String()
					break // Already charged: restore occupancy, not a new run.
				}
				pending, ok := usage.TitleContinuations[id]
				if !ok || saved.Run.Status != store.RunQueued || pending.Usage.Ref == ref || m.Provenance.InitiationKind != store.BotInitiationKindTitleConfirmation || pending.Checkpoint != string(saved.Run.Checkpoint) {
					return store.ErrBotConflict
				}
				c := saved.Run.TitleConfirmation
				if c == nil || c.State != store.BotTitleConfirmationConfirmed || c.ApprovalID != pending.ApprovalID {
					return store.ErrBotForbidden
				}
				a, err := resumeApproval(ctx, tx, saved.Run)
				if err != nil {
					return err
				}
				waiting := c.Clone()
				waiting.State = store.BotTitleConfirmationWaiting
				waiting.ConfirmedAssignment, waiting.AssignmentDigest = "", ""
				waiting.DecidedBy = uuid.Nil
				waiting.DecidedAt = time.Time{}
				confirmed, err := waiting.WithAnswer(*a)
				if err != nil || !reflect.DeepEqual(confirmed, c) {
					return store.ErrBotForbidden
				}
				if int64(len(usage.Active)) >= l.MaxConcurrentRuns {
					return store.ErrBotForbidden
				}
				current = pending.Usage
				current.Ref = ref
				usage.Active[id] = current
				delete(usage.TitleContinuations, id)
				reservationKey += ":title-confirmed:" + ref.ID.String()
				// The initial question already charged this run. Do not charge a
				// second daily run or reset any per-run/model/research usage.
				break
			} else if !errors.Is(err, store.ErrBotNotFound) {
				return err
			}
			if usage.DailyRuns >= l.MaxDailyRuns || int64(len(usage.Active)) >= l.MaxConcurrentRuns {
				return store.ErrBotForbidden
			}
			usage.DailyRuns++
			usage.Active[id] = mandateRunUsage{Ref: ref}
		case mandateResearchReserve:
			if !active || current.Ref != ref || m.Mandate.PublicResearch == nil {
				return store.ErrBotForbidden
			}
			if accountingOverflow(current.PublicResearchCalls, 1) {
				return store.ErrBotInvalid
			}
			current.PublicResearchCalls++
			usage.Active[id] = current
		case mandateConsume, mandateModelReserve:
			if action == mandateModelReserve {
				if current.Models == nil {
					current.Models = map[string]mandateModelCharge{}
				}
				if _, exists := current.Models[modelID[0].String()]; exists {
					return store.ErrBotConflict
				}
			}
			if !active || current.Ref != ref {
				return store.ErrBotForbidden
			}
			if accountingOverflow(current.Tokens, tokens) || accountingOverflow(usage.Tokens, tokens) || accountingOverflow(current.Tools, tools) || accountingOverflow(usage.Tools, tools) {
				return store.ErrBotInvalid
			}
			if exceeds(current.Spend, spend, l.MaxSpendMicros) || exceeds(usage.Spend, spend, l.MaxSpendMicros) {
				return store.ErrBotForbidden
			}
			if action == mandateModelReserve {
				current.Models[modelID[0].String()] = mandateModelCharge{Tokens: tokens}
			}
			current.Tools += tools
			current.Tokens += tokens
			current.Spend += spend
			usage.Active[id] = current
			usage.Tools += tools
			usage.Tokens += tokens
			usage.Spend += spend
		case mandateModelSettle:
			if !active || current.Ref != ref {
				return store.ErrBotForbidden
			}
			charge, ok := current.Models[modelID[0].String()]
			if !ok {
				return store.ErrBotInvalid
			}
			if charge.Settled {
				return nil
			}
			if charge.Tokens < 0 || current.Tokens < charge.Tokens || usage.Tokens < charge.Tokens {
				return store.ErrBotInvalid
			}
			// Reconcile actual usage without a token ceiling; refuse corrupt/overflowing accounting.
			if accountingOverflow(current.Tokens-charge.Tokens, tokens) || accountingOverflow(usage.Tokens-charge.Tokens, tokens) {
				return store.ErrBotInvalid
			}
			current.Tokens = current.Tokens - charge.Tokens + tokens
			usage.Tokens = usage.Tokens - charge.Tokens + tokens
			current.Models[modelID[0].String()] = mandateModelCharge{Tokens: tokens, Settled: true}
			usage.Active[id] = current
		case mandateRelease:
			if !active {
				return nil
			}
			if current.Ref != ref {
				return store.ErrBotForbidden
			}
			// Release never grants replay. Only a genuine pending ordinary question
			// with all model charges settled may retain its cumulative ledger.
			delete(usage.QuestionContinuations, id)
			if saved.Run.Status == store.RunWaitingApproval {
				if a, e := s.quotaQuestionApproval(ctx, tx, saved, rec, store.ApprovalPending); e == nil {
					settled := true
					for _, model := range current.Models {
						settled = settled && model.Settled
					}
					if settled {
						if usage.QuestionContinuations == nil {
							usage.QuestionContinuations = map[string]mandateQuestionContinuation{}
						}
						usage.QuestionContinuations[id] = mandateQuestionContinuation{Usage: current, ApprovalID: a.ID, Checkpoint: string(saved.Run.Checkpoint)}
					}
				}
			}
			// Initial title confirmation remains the separate fresh-mandate handoff.
			c := saved.Run.TitleConfirmation
			if c != nil && c.State == store.BotTitleConfirmationWaiting && c.ValidateBinding(saved.Run, rec.Bot.OwnerID) == nil && saved.Run.Status == store.RunWaitingApproval && m.Provenance.InitiationKind == store.BotInitiationKindTitle {
				checkpoint, e := botdomain.DecodeApprovalCheckpoint(saved.Run.Checkpoint, saved.Run)
				if e == nil && checkpoint.ApprovalID == c.ApprovalID {
					a, e := tx.ReadApproval(ctx, ref.BotID, c.ApprovalID)
					if e == nil && checkpoint.Matches(*a) && a.Status == store.ApprovalPending && reflect.DeepEqual(a.Interaction, c.Question) {
						if usage.TitleContinuations == nil {
							usage.TitleContinuations = map[string]mandateTitleContinuation{}
						}
						usage.TitleContinuations[id] = mandateTitleContinuation{Usage: current, ApprovalID: c.ApprovalID, Checkpoint: string(saved.Run.Checkpoint)}
					}
				}
			}
			if usage.Released == nil {
				usage.Released = map[string]mandateRunUsage{}
			}
			usage.Released[id] = current
			delete(usage.Active, id)
		}
		key := fmt.Sprintf("mandate-quota:%s:%d", id, rec.LastSequence+1)
		if action == mandateReserve {
			key = reservationKey
		}
		record := map[string]any{"run_id": id, "action": action, "tools": tools, "tokens": tokens, "spend": spend}
		if len(modelID) == 1 {
			record["model_reservation_id"] = modelID[0].String()
		}
		if err = appendMandateRecord(ctx, tx, rec, key, record, s.repo.clock()); err != nil {
			return err
		}
		if rec.Metadata == nil {
			rec.Metadata = map[string]any{}
		}
		rec.Metadata[mandateUsageKey] = usage
		return saveMandateMetadata(ctx, tx, rec)
	})
}
