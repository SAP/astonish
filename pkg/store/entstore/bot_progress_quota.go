package entstore

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/SAP/astonish/pkg/store"
)

var _ store.BotMandateProgressQuotaStore = (*botMandateRuntime)(nil)

func progressQuotaUsage(rec *botStoredRecord) (mandateUsage, error) {
	u := mandateUsage{Active: map[string]mandateRunUsage{}}
	if raw, ok := rec.Metadata[mandateUsageKey]; ok {
		b, e := json.Marshal(raw)
		if e != nil {
			return u, e
		}
		if e = json.Unmarshal(b, &u); e != nil {
			return u, e
		}
	}
	if u.Active == nil {
		u.Active = map[string]mandateRunUsage{}
	}
	return u, nil
}

// Caller owns the root fence. The logical ledger moves to this run's private
// envelope, not a growing root suspended-run map. Lifetime/daily totals stay put.
func suspendProgressQuota(ctx context.Context, tx botDB, rec *botStoredRecord, s *botStoredRun) error {
	if s.Run.MandateRef == nil {
		return nil
	}
	u, err := progressQuotaUsage(rec)
	if err != nil {
		return err
	}
	current, ok := u.Active[s.Run.ID.String()]
	if !ok {
		if s.ProgressQuota != nil {
			return nil
		}
		return store.ErrBotConflict
	}
	if current.Ref != *s.Run.MandateRef {
		return store.ErrBotForbidden
	}
	s.ProgressQuota = &current
	delete(u.Active, s.Run.ID.String())
	if rec.Metadata == nil {
		rec.Metadata = map[string]any{}
	}
	rec.Metadata[mandateUsageKey] = u
	return saveMandateMetadata(ctx, tx, rec)
}
func (s *botMandateRuntime) SuspendBotMandateRun(ctx context.Context, q store.BotMandateProgressQuotaRequest) error {
	return s.progressOccupancy(ctx, q, false)
}
func (s *botMandateRuntime) ReacquireBotMandateRun(ctx context.Context, q store.BotMandateProgressQuotaRequest) error {
	return s.progressOccupancy(ctx, q, true)
}
func (s *botMandateRuntime) progressOccupancy(ctx context.Context, q store.BotMandateProgressQuotaRequest, reacquire bool) error {
	if err := q.Validate(s.repo.clock()); err != nil {
		return err
	}
	return s.repo.db.Transaction(ctx, func(tx botDB) error {
		rec, run, err := s.repo.progressMutation(ctx, tx, q.Mutation)
		if err != nil {
			return err
		}
		if run.Run.MandateRef == nil || *run.Run.MandateRef != q.MandateRef || !progressSafe(run) {
			return store.ErrBotConflict
		}
		if reacquire {
			m, err := readMandate(ctx, tx, q.MandateRef)
			if err != nil {
				return err
			}
			m, _, err = s.verify(ctx, tx, q.MandateRef, m.Mandate.Purpose)
			if err != nil {
				return err
			}
			u, err := progressQuotaUsage(rec)
			if err != nil {
				return err
			}
			if current, ok := u.Active[run.Run.ID.String()]; ok {
				if current.Ref != q.MandateRef {
					return store.ErrBotForbidden
				}
				return nil
			}
			if run.ProgressQuota == nil || run.ProgressQuota.Ref != q.MandateRef || int64(len(u.Active)) >= m.Mandate.Limits.MaxConcurrentRuns {
				return store.ErrBotForbidden
			}
			u.Active[run.Run.ID.String()] = *run.ProgressQuota
			run.ProgressQuota = nil
			rec.Metadata[mandateUsageKey] = u
			if err := saveMandateMetadata(ctx, tx, rec); err != nil {
				return err
			}
		} else {
			if err := suspendProgressQuota(ctx, tx, rec, run); err != nil {
				return err
			}
		}
		return s.repo.progressWrite(ctx, tx, rec, run, q.Mutation, fmt.Sprintf("progress-occupancy:%s:%d", run.Run.ID, rec.LastSequence+1), q)
	})
}
