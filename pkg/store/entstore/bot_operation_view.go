package entstore

import (
	"context"
	"encoding/json"
	"github.com/SAP/astonish/pkg/store"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Uses the reserved head index and direct activity-key reads, never journal scans.
func snapshotBotOperations(ctx context.Context, db botDraftDB, rec *botStoredRecord) ([]store.BotOperationView, error) {
	out := []store.BotOperationView{}
	keys := []string{}
	for key := range rec.Metadata {
		if strings.HasPrefix(key, "mandate_operation_head:") {
			keys = append(keys, key)
		}
	}
	// Fail closed rather than returning a silently incomplete operation snapshot.
	if len(keys) > 1000 {
		return nil, store.ErrBotInvalid
	}
	sort.Strings(keys)
	for _, key := range keys {
		raw, err := json.Marshal(rec.Metadata[key])
		if err != nil {
			return nil, err
		}
		var revision int64
		if json.Unmarshal(raw, &revision) != nil || revision < 1 {
			return nil, store.ErrBotInvalid
		}
		a, err := db.FindActivity(ctx, rec.Bot.ID, "mandate-operation:"+strings.TrimPrefix(key, "mandate_operation_head:")+":"+strconv.FormatInt(revision, 10))
		if err != nil {
			return nil, err
		}
		var o store.BotOperation
		if a.Kind != mandateRecordKind || json.Unmarshal(a.Payload, &o) != nil || o.Revision != revision || operationHeadKey(o.ID) != key {
			return nil, store.ErrBotInvalid
		}
		v, err := store.ProjectBotOperation(rec.Bot, o, a.Sequence)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StateSequence < out[j].StateSequence })
	return out, nil
}

func appendBotOperationViewTx(ctx context.Context, tx botDraftDB, rec *botStoredRecord, o store.BotOperation, at time.Time) error {
	v, err := store.ProjectBotOperation(rec.Bot, o, rec.LastSequence)
	if err != nil {
		return err
	}
	event, err := store.NewBotOperationEvent(v, at)
	if err != nil {
		return err
	}
	db, ok := tx.(botPublicationDB)
	if !ok {
		return store.ErrBotUnsupported
	}
	if err = db.AppendHomeReport(ctx, rec.Bot, event); err != nil {
		return err
	}
	var callbacks *[]func()
	switch d := tx.(type) {
	case personalBotDB:
		callbacks = d.afterCommit
	case teamBotDB:
		callbacks = d.afterCommit
	}
	if notify := store.BotOperationNotifierFromContext(ctx); notify != nil && callbacks != nil {
		*callbacks = append(*callbacks, func() { notify(ctx, v) })
	}
	return nil
}
