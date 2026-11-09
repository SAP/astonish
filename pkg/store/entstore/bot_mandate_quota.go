package entstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"math"
)

type mandateModelCharge struct {
	Tokens  int64
	Settled bool
}

type mandateRunUsage struct {
	Models               map[string]mandateModelCharge
	Tools, Tokens, Spend int64
	Ref                  store.BotMandateRef
}
type mandateUsage struct {
	Day                             string
	DailyRuns, Tools, Tokens, Spend int64
	Active                          map[string]mandateRunUsage
}
type mandateQuotaAction uint8

const (
	mandateReserve mandateQuotaAction = iota
	mandateConsume
	mandateRelease
	mandateModelReserve
	mandateModelSettle
)

func (s *botMandateRuntime) ReserveBotMandateRun(ctx context.Context, ref store.BotMandateRef, run uuid.UUID) error {
	return s.quota(ctx, ref, run, mandateReserve, 0, 0, 0)
}
func (s *botMandateRuntime) ConsumeBotMandateQuota(ctx context.Context, ref store.BotMandateRef, run uuid.UUID, tools, tokens, spend int64) error {
	return s.quota(ctx, ref, run, mandateConsume, tools, tokens, spend)
}
func (s *botMandateRuntime) ReleaseBotMandateRun(ctx context.Context, ref store.BotMandateRef, run uuid.UUID) error {
	return s.quota(ctx, ref, run, mandateRelease, 0, 0, 0)
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
	return s.repo.db.Transaction(ctx, func(tx botDraftDB) error {
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
			if rec.Bot.Status == store.BotPaused && m.Mandate.Purpose != execution.BotMandatePurposeOperation {
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
				return store.ErrBotConflict
			} else if !errors.Is(err, store.ErrBotNotFound) {
				return err
			}
			if usage.DailyRuns >= l.MaxDailyRuns || int64(len(usage.Active)) >= l.MaxConcurrentRuns {
				return store.ErrBotForbidden
			}
			usage.DailyRuns++
			usage.Active[id] = mandateRunUsage{Ref: ref}
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
			if exceeds(current.Tokens, tokens, l.MaxTokens) {
				return &store.BotMandateQuotaError{Dimension: "run_tokens", Used: current.Tokens, Requested: tokens, Limit: l.MaxTokens}
			}
			if exceeds(usage.Tokens, tokens, l.MaxTotalTokens) {
				return &store.BotMandateQuotaError{Dimension: "total_tokens", Used: usage.Tokens, Requested: tokens, Limit: l.MaxTotalTokens}
			}
			if exceeds(current.Tools, tools, l.MaxToolCalls) || exceeds(current.Spend, spend, l.MaxSpendMicros) || exceeds(usage.Tools, tools, l.MaxTotalToolCalls) || exceeds(usage.Spend, spend, l.MaxSpendMicros) {
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
			// Actual usage is never discarded, even if a provider exceeded its
			// reservation. Saturation fails future admission closed on overflow.
			add := func(a, b int64) int64 {
				if b > math.MaxInt64-a {
					return math.MaxInt64
				}
				return a + b
			}
			current.Tokens = add(current.Tokens-charge.Tokens, tokens)
			usage.Tokens = add(usage.Tokens-charge.Tokens, tokens)
			current.Models[modelID[0].String()] = mandateModelCharge{Tokens: tokens, Settled: true}
			usage.Active[id] = current
		case mandateRelease:
			if !active {
				return nil
			}
			if current.Ref != ref {
				return store.ErrBotForbidden
			}
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
