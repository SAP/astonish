package entstore

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"time"

	personalbot "github.com/SAP/astonish/ent/personal/bot"
	teambot "github.com/SAP/astonish/ent/team/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

var _ store.BotSpendStore = (*teamBotStore)(nil)
var _ store.BotSpendStore = (*personalBotStore)(nil)

func (s *teamBotStore) ReserveSpend(ctx context.Context, q store.BotSpendRequest) (*store.BotSpendReservation, error) {
	return s.repository().ReserveSpend(ctx, q)
}
func (s *personalBotStore) ReserveSpend(ctx context.Context, q store.BotSpendRequest) (*store.BotSpendReservation, error) {
	return s.repository().ReserveSpend(ctx, q)
}
func (s *teamBotStore) ResolveSpend(ctx context.Context, q store.BotSpendResolution) (*store.BotSpendReservation, error) {
	return s.repository().ResolveSpend(ctx, q)
}
func (s *personalBotStore) ResolveSpend(ctx context.Context, q store.BotSpendResolution) (*store.BotSpendReservation, error) {
	return s.repository().ResolveSpend(ctx, q)
}
func (s *teamBotStore) GetSpend(ctx context.Context, id store.BotID, key string) (*store.BotSpendReservation, error) {
	return s.repository().GetSpend(ctx, id, key)
}
func (s *personalBotStore) GetSpend(ctx context.Context, id store.BotID, key string) (*store.BotSpendReservation, error) {
	return s.repository().GetSpend(ctx, id, key)
}

const botSpendMetadataKey = "purchase_spend_v1"
const botSpendMonthLayout = "2006-01"

var errBotSpendCAS = errors.New("bot spend root CAS conflict")

type botSpendLedger struct {
	Version int                                  `json:"version"`
	Entries map[string]store.BotSpendReservation `json:"entries"`
}

// The nested JSON string is intentional: Ent's map[string]any JSON decoding uses
// float64, which would silently round minor-unit values above 2^53 otherwise.
func readBotSpend(rec *botStoredRecord) (botSpendLedger, error) {
	l := botSpendLedger{Version: 1, Entries: make(map[string]store.BotSpendReservation)}
	raw, ok := rec.Metadata[botSpendMetadataKey]
	if !ok {
		return l, nil
	}
	text, ok := raw.(string)
	if !ok || json.Unmarshal([]byte(text), &l) != nil || l.Version != 1 || l.Entries == nil {
		return l, store.ErrBotInvalid
	}
	for key, e := range l.Entries {
		q := store.BotSpendRequest{BotID: e.BotID, RunID: e.RunID, Version: e.Version, Key: e.Key, Currency: e.Currency, Month: e.Month, AmountMinor: e.AmountMinor, ActionDigest: e.ActionDigest}
		if key != e.Key || e.BotID != rec.Bot.ID || validateBotSpendRequest(q) != nil || e.ChargedMinor < 0 || e.ChargedMinor > e.AmountMinor {
			return l, store.ErrBotInvalid
		}
		switch e.State {
		case store.BotSpendReserved, store.BotSpendUncertain:
			if e.ChargedMinor != 0 {
				return l, store.ErrBotInvalid
			}
		case store.BotSpendSettled:
			if e.ChargedMinor == 0 || strings.TrimSpace(e.Evidence) == "" {
				return l, store.ErrBotInvalid
			}
		case store.BotSpendReleased:
			if e.ChargedMinor != 0 || strings.TrimSpace(e.Evidence) == "" {
				return l, store.ErrBotInvalid
			}
		default:
			return l, store.ErrBotInvalid
		}
	}
	return l, nil
}

func validateBotSpendRequest(q store.BotSpendRequest) error {
	if q.BotID == uuid.Nil || q.RunID == uuid.Nil || q.Version < 1 || strings.TrimSpace(q.Key) == "" || len(q.Key) > 200 || q.AmountMinor <= 0 || len(q.Currency) != 3 {
		return store.ErrBotInvalid
	}
	for _, c := range q.Currency {
		if c < 'A' || c > 'Z' {
			return store.ErrBotInvalid
		}
	}
	if month, err := time.Parse(botSpendMonthLayout, q.Month); err != nil || month.Format(botSpendMonthLayout) != q.Month {
		return store.ErrBotInvalid
	}
	digest, err := hex.DecodeString(q.ActionDigest)
	if err != nil || len(digest) != 32 || strings.ToLower(q.ActionDigest) != q.ActionDigest {
		return store.ErrBotInvalid
	}
	return nil
}

func (r *botRepository) authorizeSpend(ctx context.Context, tx botDraftDB, rec *botStoredRecord, runID uuid.UUID, version int64, lease *store.BotLease) error {
	if lease != nil {
		if lease.BotID != rec.Bot.ID || lease.RunID != runID {
			return store.ErrBotForbidden
		}
		checked, err := r.checkLease(ctx, tx, *lease, r.clock())
		if err != nil {
			return err
		}
		if checked.Bot.Version != version {
			return store.ErrBotConflict
		}
		return nil
	}
	// A worker cannot discard its lease and fall back to owner privileges.
	if _, worker := execution.BotRunIdentityFromContext(ctx); worker {
		return store.ErrBotForbidden
	}
	actor, err := r.auth.authorize(ctx, rec.Bot.OwnerID, true)
	if err != nil {
		return err
	}
	if actor != rec.Bot.OwnerID {
		return store.ErrBotForbidden
	}
	return nil
}

func (r *botRepository) ReserveSpend(ctx context.Context, q store.BotSpendRequest) (*store.BotSpendReservation, error) {
	if _, err := r.auth.authorize(ctx, uuid.Nil, false); err != nil {
		return nil, err
	}
	if err := validateBotSpendRequest(q); err != nil {
		return nil, err
	}
	var out store.BotSpendReservation
	err := botSpendRetry(ctx, func() error {
		return r.db.Transaction(ctx, func(tx botDraftDB) error {
			rec, err := tx.GetBot(ctx, q.BotID)
			if err != nil {
				return err
			}
			if err = r.authorizeSpend(ctx, tx, rec, q.RunID, q.Version, q.Lease); err != nil {
				return err
			}
			l, err := readBotSpend(rec)
			if err != nil {
				return err
			}
			if e, ok := l.Entries[q.Key]; ok {
				if e.RunID != q.RunID || e.Version != q.Version || e.Currency != q.Currency || e.Month != q.Month || e.AmountMinor != q.AmountMinor || e.ActionDigest != q.ActionDigest {
					return store.ErrBotConflict
				}
				out = e
				return nil
			}
			now := r.clock()
			if rec.Bot.Status != store.BotActive {
				return store.ErrBotForbidden
			}
			if rec.Bot.Version != q.Version {
				return store.ErrBotConflict
			}
			if q.Month != now.Format(botSpendMonthLayout) {
				return store.ErrBotInvalid
			}
			run, err := tx.ReadRun(ctx, q.BotID, q.RunID)
			if err != nil {
				return err
			}
			if run.Run.Version != q.Version || run.Run.Status != store.RunRunning {
				return store.ErrBotConflict
			}
			v, err := tx.ReadVersion(ctx, q.BotID, q.Version)
			if err != nil {
				return err
			}
			if v.Autonomy.SpendCurrency != q.Currency || v.Autonomy.MaxSpendPerRunMinor <= 0 || v.Autonomy.MaxSpendPerMonthMinor <= 0 {
				return store.ErrBotForbidden
			}
			var runTotal, monthTotal int64
			for _, e := range l.Entries {
				if e.Currency != q.Currency {
					continue
				}
				amount := e.AmountMinor
				if e.State == store.BotSpendSettled {
					amount = e.ChargedMinor
				}
				if e.State == store.BotSpendReleased {
					amount = 0
				}
				if e.RunID == q.RunID {
					if amount > math.MaxInt64-runTotal {
						return store.ErrBotForbidden
					}
					runTotal += amount
				}
				if e.Month == q.Month {
					if amount > math.MaxInt64-monthTotal {
						return store.ErrBotForbidden
					}
					monthTotal += amount
				}
			}
			if runTotal > v.Autonomy.MaxSpendPerRunMinor || monthTotal > v.Autonomy.MaxSpendPerMonthMinor || q.AmountMinor > v.Autonomy.MaxSpendPerRunMinor-runTotal || q.AmountMinor > v.Autonomy.MaxSpendPerMonthMinor-monthTotal {
				return store.ErrBotForbidden
			}
			out = store.BotSpendReservation{BotID: q.BotID, RunID: q.RunID, Version: q.Version, Key: q.Key, Currency: q.Currency, Month: q.Month, AmountMinor: q.AmountMinor, ActionDigest: q.ActionDigest, State: store.BotSpendReserved, CreatedAt: now, UpdatedAt: now}
			l.Entries[q.Key] = out
			return saveBotSpend(ctx, tx, rec, l)
		})
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func (r *botRepository) ResolveSpend(ctx context.Context, q store.BotSpendResolution) (*store.BotSpendReservation, error) {
	if _, err := r.auth.authorize(ctx, uuid.Nil, false); err != nil {
		return nil, err
	}
	if q.BotID == uuid.Nil || strings.TrimSpace(q.Key) == "" || len(q.Key) > 200 || len(q.Evidence) > 2000 {
		return nil, store.ErrBotInvalid
	}
	switch q.Outcome {
	case store.BotSpendOutcomeUnknown:
		if q.ChargedMinor != 0 {
			return nil, store.ErrBotInvalid
		}
	case store.BotSpendOutcomeCharged:
		if q.ChargedMinor <= 0 || strings.TrimSpace(q.Evidence) == "" {
			return nil, store.ErrBotInvalid
		}
	case store.BotSpendOutcomeNoEffect:
		if q.ChargedMinor != 0 || strings.TrimSpace(q.Evidence) == "" {
			return nil, store.ErrBotInvalid
		}
	default:
		return nil, store.ErrBotInvalid
	}
	var out store.BotSpendReservation
	err := botSpendRetry(ctx, func() error {
		return r.db.Transaction(ctx, func(tx botDraftDB) error {
			rec, err := tx.GetBot(ctx, q.BotID)
			if err != nil {
				return err
			}
			l, err := readBotSpend(rec)
			if err != nil {
				return err
			}
			e, ok := l.Entries[q.Key]
			if !ok {
				return store.ErrBotNotFound
			}
			if err = r.authorizeSpend(ctx, tx, rec, e.RunID, e.Version, q.Lease); err != nil {
				return err
			}
			next := store.BotSpendUncertain
			if q.Outcome == store.BotSpendOutcomeCharged {
				next = store.BotSpendSettled
			}
			if q.Outcome == store.BotSpendOutcomeNoEffect {
				next = store.BotSpendReleased
			}
			if q.ChargedMinor > e.AmountMinor {
				return store.ErrBotInvalid
			}
			if e.State == store.BotSpendSettled || e.State == store.BotSpendReleased {
				if e.State != next || e.ChargedMinor != q.ChargedMinor || e.Evidence != q.Evidence {
					return store.ErrBotConflict
				}
				out = e
				return nil
			}
			if e.State == next && e.Evidence == q.Evidence {
				out = e
				return nil
			}
			e.State = next
			e.ChargedMinor = q.ChargedMinor
			e.Evidence = q.Evidence
			e.UpdatedAt = r.clock()
			l.Entries[q.Key] = e
			out = e
			return saveBotSpend(ctx, tx, rec, l)
		})
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func (r *botRepository) GetSpend(ctx context.Context, id store.BotID, key string) (*store.BotSpendReservation, error) {
	if _, err := r.auth.authorize(ctx, uuid.Nil, false); err != nil {
		return nil, err
	}
	rec, err := r.db.GetBot(ctx, id)
	if err != nil {
		return nil, err
	}
	// Read access follows the scoped Bot read policy (including live membership).
	if _, err = r.auth.authorize(ctx, rec.Bot.OwnerID, false); err != nil {
		return nil, err
	}
	l, err := readBotSpend(rec)
	if err != nil {
		return nil, err
	}
	e, ok := l.Entries[key]
	if !ok {
		return nil, store.ErrBotNotFound
	}
	return &e, nil
}

type botSpendWriter interface {
	writeSpend(context.Context, botStoredRecord, map[string]any) error
}

func saveBotSpend(ctx context.Context, tx botDraftDB, rec *botStoredRecord, l botSpendLedger) error {
	writer, ok := tx.(botSpendWriter)
	if !ok {
		return store.ErrBotUnsupported
	}
	raw, err := json.Marshal(l)
	if err != nil {
		return err
	}
	data := make(map[string]any, len(rec.Metadata)+1)
	for k, v := range rec.Metadata {
		data[k] = v
	}
	data[botSpendMetadataKey] = string(raw)
	return writer.writeSpend(ctx, *rec, data)
}

// Both counters participate: revision fences metadata writers (read cursors),
// sequence fences authority/lifecycle/run writers. No process-local lock is used.
func (d teamBotDB) writeSpend(ctx context.Context, rec botStoredRecord, data map[string]any) error {
	n, err := d.client.Bot.Update().Where(teambot.IDEQ(rec.Bot.ID), teambot.RevisionEQ(rec.Bot.Revision), teambot.LastSeqEQ(rec.LastSequence), teambot.FenceEQ(rec.Fence), teambot.VersionEQ(rec.Bot.Version), teambot.StatusEQ(teambot.Status(rec.Bot.Status))).SetData(data).AddRevision(1).AddLastSeq(1).Save(ctx)
	if err != nil {
		return err
	}
	if n != 1 {
		return errBotSpendCAS
	}
	return nil
}
func (d personalBotDB) writeSpend(ctx context.Context, rec botStoredRecord, data map[string]any) error {
	n, err := d.client.Bot.Update().Where(personalbot.IDEQ(rec.Bot.ID), personalbot.RevisionEQ(rec.Bot.Revision), personalbot.LastSeqEQ(rec.LastSequence), personalbot.FenceEQ(rec.Fence), personalbot.VersionEQ(rec.Bot.Version), personalbot.StatusEQ(personalbot.Status(rec.Bot.Status))).SetData(data).AddRevision(1).AddLastSeq(1).Save(ctx)
	if err != nil {
		return err
	}
	if n != 1 {
		return errBotSpendCAS
	}
	return nil
}

func botSpendRetry(ctx context.Context, fn func() error) error {
	for attempt := 0; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := fn()
		if !botSpendTransient(err) {
			return err
		}
		if attempt >= 15 {
			return store.ErrBotConflict
		}
		timer := time.NewTimer(time.Duration(attempt+1) * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
func botSpendTransient(err error) bool {
	if errors.Is(err, errBotSpendCAS) {
		return true
	}
	// modernc SQLite exposes primary/extended result codes through Code().
	var sqlite interface{ Code() int }
	if errors.As(err, &sqlite) {
		code := sqlite.Code() & 255
		return code == 5 || code == 6
	}
	var postgres interface{ SQLState() string }
	if errors.As(err, &postgres) {
		return postgres.SQLState() == "40001" || postgres.SQLState() == "40P01"
	}
	return false
}
