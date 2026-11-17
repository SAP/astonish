package entstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	personaloutbox "github.com/SAP/astonish/ent/personal/botoutbox"
	teamoutbox "github.com/SAP/astonish/ent/team/botoutbox"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

// This is admission evidence for a NEW occurrence, never authorization to resume
// the interrupted run. Bound both query size and ledger memory; omitted evidence
// means held, not safe. No run, checkpoint, dispatch receipt or quota is rewritten.
const botScheduledInterruptionEvidenceLimit = 4096

type botScheduledInterruptionRecord struct {
	key      string
	activity store.BotActivity
}

type botScheduledInterruptionDB interface {
	scheduledInterruptionRecords(context.Context, uuid.UUID, uuid.UUID) ([]botScheduledInterruptionRecord, error)
}

func (d personalBotDB) scheduledInterruptionRecords(ctx context.Context, bot, run uuid.UUID) ([]botScheduledInterruptionRecord, error) {
	rows, err := d.client.BotOutbox.Query().Where(personaloutbox.BotIDEQ(bot), personaloutbox.Or(personaloutbox.DeduplicationKeyHasPrefix("progress-dispatch:"+run.String()+":"), personaloutbox.DeduplicationKeyHasPrefix("cleanup-confirmed:"))).Order(personaloutbox.BySequence()).Limit(botScheduledInterruptionEvidenceLimit + 1).All(ctx)
	if err != nil {
		return nil, err
	}
	records := make([]botScheduledInterruptionRecord, 0, len(rows))
	for _, row := range rows {
		var a store.BotActivity
		if err := botDecode(row.Payload, &a); err != nil {
			return nil, err
		}
		if a.BotID != bot || a.Sequence != row.Sequence || a.Kind != store.BotActivityKind(row.Kind) {
			return nil, store.ErrBotInvalid
		}
		records = append(records, botScheduledInterruptionRecord{key: row.DeduplicationKey, activity: a})
	}
	return records, nil
}

func (d teamBotDB) scheduledInterruptionRecords(ctx context.Context, bot, run uuid.UUID) ([]botScheduledInterruptionRecord, error) {
	rows, err := d.client.BotOutbox.Query().Where(teamoutbox.BotIDEQ(bot), teamoutbox.Or(teamoutbox.DeduplicationKeyHasPrefix("progress-dispatch:"+run.String()+":"), teamoutbox.DeduplicationKeyHasPrefix("cleanup-confirmed:"))).Order(teamoutbox.BySequence()).Limit(botScheduledInterruptionEvidenceLimit + 1).All(ctx)
	if err != nil {
		return nil, err
	}
	records := make([]botScheduledInterruptionRecord, 0, len(rows))
	for _, row := range rows {
		var a store.BotActivity
		if err := botDecode(row.Payload, &a); err != nil {
			return nil, err
		}
		if a.BotID != bot || a.Sequence != row.Sequence || a.Kind != store.BotActivityKind(row.Kind) {
			return nil, store.ErrBotInvalid
		}
		records = append(records, botScheduledInterruptionRecord{key: row.DeduplicationKey, activity: a})
	}
	return records, nil
}

func scheduledInterruptionAllowsNewOccurrence(ctx context.Context, tx botDraftDB, saved *botStoredRun, occurrence time.Time) (bool, error) {
	db, ok := tx.(botScheduledInterruptionDB)
	if !ok || saved.Run.Status != store.RunInterrupted || saved.ProgressCursor < 1 || saved.ProgressCursor > botScheduledInterruptionEvidenceLimit || saved.ProgressPending < 0 || saved.Run.Lease.Fence < 1 {
		return false, nil
	}
	root, err := tx.GetBot(ctx, saved.Run.BotID)
	if err != nil {
		return false, err
	}
	if root.ActiveRunID != nil && *root.ActiveRunID == saved.Run.ID || root.Fence <= saved.Run.Lease.Fence {
		return false, nil
	}
	records, err := db.scheduledInterruptionRecords(ctx, saved.Run.BotID, saved.Run.ID)
	if err != nil {
		if errors.Is(err, store.ErrBotInvalid) {
			return false, nil
		}
		return false, err
	}
	if len(records) > botScheduledInterruptionEvidenceLimit {
		return false, nil
	}
	pending := make(map[uuid.UUID]store.BotProgressBeginDispatchRequest)
	seen := make(map[uuid.UUID]bool)
	cursor := int64(0)
	teardown := false
	binding := progressBinding(saved)
	for _, record := range records {
		a := record.activity
		if strings.HasPrefix(record.key, "cleanup-confirmed:") {
			var payload struct {
				Confirmation store.BotCleanupRequest `json:"cleanup_confirmation"`
			}
			if a.Kind != store.BotActivityLifecycle || json.Unmarshal(a.Payload, &payload) != nil {
				return false, nil
			}
			q := payload.Confirmation
			if q.Kind == store.BotCleanupRunTeardown && q.BotID == saved.Run.BotID && q.RunID == saved.Run.ID && q.RunFence == saved.Run.Lease.Fence && q.Fence > q.RunFence && q.Home == root.Bot.Home && q.Home.ID != uuid.Nil && record.key == fmt.Sprintf("cleanup-confirmed:%s:%d:%d", q.Kind, q.ExpectedRevision, q.Fence) && !a.CreatedAt.IsZero() && occurrence.After(a.CreatedAt) {
				teardown = true
			}
			continue
		}
		var dispatch botProgressDispatch
		if a.Kind != progressRecordKind || a.RunID == nil || *a.RunID != saved.Run.ID || json.Unmarshal(a.Payload, &dispatch) != nil {
			return false, nil
		}
		q := dispatch.Request
		m := q.Mutation
		// Validate historical requests at their own lease time, not today's expiry.
		if q.Validate(m.Lease.ExpiresAt.Add(-time.Nanosecond)) != nil || m.Lease.Fence > saved.Run.Lease.Fence || m.Binding.WorkRevision > binding.WorkRevision {
			return false, nil
		}
		b := m.Binding
		b.WorkRevision = binding.WorkRevision
		if !b.Matches(binding) || dispatch.Receipt.DispatchID != q.DispatchID || dispatch.Receipt.LedgerCursor != cursor+1 {
			return false, nil
		}
		cursor++
		if dispatch.Settlement == nil {
			if record.key != progressKey(saved.Run.ID, q.DispatchID, false) || seen[q.DispatchID] || dispatch.Receipt.Revision != m.ExpectedRevision {
				return false, nil
			}
			seen[q.DispatchID] = true
			pending[q.DispatchID] = q
			continue
		}
		settlement := dispatch.Settlement
		original, exists := pending[q.DispatchID]
		if !exists || !reflect.DeepEqual(original, q) || record.key != progressKey(saved.Run.ID, q.DispatchID, true) || settlement.DispatchID != q.DispatchID || settlement.Validate(settlement.Mutation.Lease.ExpiresAt.Add(-time.Nanosecond)) != nil || settlement.Mutation.Lease.Fence != m.Lease.Fence || dispatch.Receipt.Revision != settlement.Mutation.ExpectedRevision {
			return false, nil
		}
		b = settlement.Mutation.Binding
		if b.WorkRevision < m.Binding.WorkRevision || b.WorkRevision > binding.WorkRevision {
			return false, nil
		}
		b.WorkRevision = binding.WorkRevision
		if !b.Matches(binding) {
			return false, nil
		}
		if q.Kind == store.BotProgressDispatchTool && (settlement.Outcome == store.BotProgressDispatchUncertain || strings.TrimSpace(settlement.EvidenceRef) == "") {
			return false, nil
		}
		delete(pending, q.DispatchID)
	}
	if !teardown || cursor != saved.ProgressCursor || int64(len(pending)) != saved.ProgressPending {
		return false, nil
	}
	for _, q := range pending {
		if q.Kind != store.BotProgressDispatchModel {
			return false, nil
		}
	}
	return true, nil
}
