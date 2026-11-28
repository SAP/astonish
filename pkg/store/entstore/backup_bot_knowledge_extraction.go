package entstore

import (
	"fmt"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"time"
)

func quiesceRestoredKnowledgeRow(row map[string]any) error {
	doc, e := restoredBotDocument(row["data"])
	if e != nil {
		return e
	}
	var v knowledgeEnvelope
	if e = botDecode(doc, &v); e != nil {
		return e
	}
	s := v.Record.Input.Source
	if v.Record.Input.Validate() != nil || v.Record.ID.String() != fmt.Sprint(row["id"]) || s.BotID.String() != fmt.Sprint(row["bot_id"]) || s.RunID.String() != fmt.Sprint(row["run_id"]) || s.OutcomeDigest != fmt.Sprint(row["outcome_digest"]) || fmt.Sprint(s.Sequence) != fmt.Sprint(row["source_sequence"]) || fmt.Sprint(s.Version) != fmt.Sprint(row["source_version"]) || fmt.Sprint(v.Record.Revision) != fmt.Sprint(row["revision"]) || string(v.Record.State) != fmt.Sprint(row["state"]) || v.ActorID == uuid.Nil {
		return store.ErrBotInvalid
	}
	switch v.Record.State {
	case store.BotKnowledgeExtractionPending, store.BotKnowledgeExtractionExtracting, store.BotKnowledgeExtractionPrepared, store.BotKnowledgeExtractionFilesReady:
		v.Record.State = store.BotKnowledgeExtractionHeld
		v.Record.HoldReason = "restore_reconciliation_required"
		v.Record.Revision++
	case store.BotKnowledgeExtractionHeld, store.BotKnowledgeExtractionAborted, store.BotKnowledgeExtractionCommitted, store.BotKnowledgeExtractionNoChange:
	default:
		return store.ErrBotInvalid
	}
	if v.Record.Decision != nil && (v.Record.Decision.Validate() != nil || v.Record.DecisionDigest != store.BotKnowledgeDigest(*v.Record.Decision)) {
		return store.ErrBotInvalid
	}
	if e = advanceRestoredBotCounter(row, "fence"); e != nil {
		return e
	}
	v.Lease.Fence++
	v.Lease.WorkerID = ""
	v.Lease.ExpiresAt = time.Time{}
	row["state"], row["revision"], row["worker_id"], row["lease_expires_at"] = string(v.Record.State), v.Record.Revision, "", nil
	row["data"], e = botJSONMap(v)
	return e
}
