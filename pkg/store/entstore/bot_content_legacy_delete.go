package entstore

// This file is deliberately a cleanup-only compatibility boundary. It never
// returns a legacy document to execution or repairs/migrates a content reference.
import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	personalent "github.com/SAP/astonish/ent/personal"
	po "github.com/SAP/astonish/ent/personal/botoutbox"
	pr "github.com/SAP/astonish/ent/personal/botrun"
	ps "github.com/SAP/astonish/ent/personal/session"
	teament "github.com/SAP/astonish/ent/team"
	to "github.com/SAP/astonish/ent/team/botoutbox"
	tr "github.com/SAP/astonish/ent/team/botrun"
	ts "github.com/SAP/astonish/ent/team/session"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	adksession "google.golang.org/adk/session"
)

const botCleanupRecordKey = "bot_cleanup_v1"
const botOperationCleanupPrefix = "operation_cleanup:"
const botCleanupAuthorityMaxBytes = 16 << 20

// botCleanupAuthority contains only immutable identity and SQL state-machine
// coordinates. No action payload, source text, or result is decoded here.
type botCleanupAuthority struct {
	Mandate *struct {
		Ref store.BotMandateRef `json:"ref"`
	} `json:"mandate,omitempty"`
	ID         uuid.UUID                `json:"id,omitempty"`
	MandateRef store.BotMandateRef      `json:"mandate_ref,omitempty"`
	Revision   int64                    `json:"revision,omitempty"`
	Status     store.BotOperationStatus `json:"status,omitempty"`
}

func cleanupAuthority(raw []byte) (*botCleanupAuthority, error) {
	if len(raw) == 0 || len(raw) > botCleanupAuthorityMaxBytes {
		return nil, store.ErrBotInvalid
	}
	var a botCleanupAuthority
	if err := json.Unmarshal(raw, &a); err != nil {
		return nil, store.ErrBotInvalid
	}
	if len(a.MandateRef.OrgSlug) > 256 || len(a.MandateRef.TeamSlug) > 256 || len(a.MandateRef.OwnerID) > 256 {
		return nil, store.ErrBotInvalid
	}
	encoded, err := json.Marshal(a)
	if err != nil || len(encoded) > 4096 {
		return nil, store.ErrBotInvalid
	}
	return &a, nil
}

// Only the closed cleanup DTO can be written without retained storage. Its
// contents are coordination and revocation, never report or request bodies.
type botCleanupRecord struct {
	RunState            *botCleanupRunState        `json:"run_state,omitempty"`
	BotID               store.BotID                `json:"bot_id"`
	Sequence            int64                      `json:"sequence"`
	Kind                store.BotActivityKind      `json:"kind"`
	CreatedAt           time.Time                  `json:"created_at"`
	Status              store.BotStatus            `json:"status,omitempty"`
	Event               string                     `json:"event,omitempty"`
	ActorID             uuid.UUID                  `json:"actor_id,omitempty"`
	Revision            int64                      `json:"revision,omitempty"`
	RestoreUntil        *time.Time                 `json:"restore_until,omitempty"`
	Intent              store.BotOutboxKind        `json:"intent,omitempty"`
	NotBefore           *time.Time                 `json:"not_before,omitempty"`
	Home                store.BotHomeBinding       `json:"home,omitempty"`
	CancelRunID         *store.RunID               `json:"cancel_run_id,omitempty"`
	Ref                 *store.BotMandateRef       `json:"ref,omitempty"`
	Revoked             bool                       `json:"revoked,omitempty"`
	CleanupConfirmation *store.BotCleanupRequest   `json:"cleanup_confirmation,omitempty"`
	Claim               *store.BotHomeCleanupClaim `json:"claim,omitempty"`
}

func cleanupRecordData(a store.BotActivity) (map[string]any, error) {
	if a.BotID == uuid.Nil || a.Sequence < 1 || a.CreatedAt.IsZero() || len(a.Payload) > 64<<10 {
		return nil, store.ErrBotInvalid
	}
	var record botCleanupRecord
	if json.Unmarshal(a.Payload, &record) != nil {
		return nil, store.ErrBotInvalid
	}
	record.BotID, record.Sequence, record.Kind, record.CreatedAt = a.BotID, a.Sequence, a.Kind, a.CreatedAt
	if record.Intent != "" && record.Intent != store.BotOutboxCleanup {
		return nil, store.ErrBotInvalid
	}
	if a.Kind != store.BotActivityLifecycle && !(a.Kind == mandateRecordKind && record.Ref != nil && record.Revoked) {
		return nil, store.ErrBotInvalid
	}
	raw, err := json.Marshal(record)
	if err != nil {
		return nil, err
	}
	return map[string]any{botCleanupRecordKey: string(raw)}, nil
}

func decodeCleanupActivity(id store.BotID, sequence int64, kind store.BotActivityKind, created time.Time, data map[string]any) (*store.BotActivity, error) {
	raw, ok := data[botCleanupRecordKey].(string)
	if !ok || len(data) != 1 || len(raw) > 64<<10 {
		return nil, store.ErrBotInvalid
	}
	var record botCleanupRecord
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&record) != nil || !json.Valid([]byte(raw)) || id == uuid.Nil || sequence < 1 || created.IsZero() || record.BotID != id || record.Sequence != sequence || record.Kind != kind || !record.CreatedAt.Equal(created) {
		return nil, store.ErrBotInvalid
	}
	if record.Intent != "" && record.Intent != store.BotOutboxCleanup {
		return nil, store.ErrBotInvalid
	}
	if kind != store.BotActivityLifecycle && !(kind == mandateRecordKind && record.Ref != nil && record.Revoked && record.Ref.BotID == id && record.Ref.Validate() == nil) {
		return nil, store.ErrBotInvalid
	}
	return &store.BotActivity{BotID: id, Sequence: sequence, Kind: kind, CreatedAt: created, Payload: json.RawMessage(raw)}, nil
}

func insertCleanupActivity(ctx context.Context, db botDB, a store.BotActivity, key string) error {
	data, err := cleanupRecordData(a)
	if err != nil {
		return err
	}
	switch d := db.(type) {
	case personalBotDB:
		_, err = d.client.BotOutbox.Create().SetBotID(a.BotID).SetSequence(a.Sequence).SetKind(string(a.Kind)).SetDeduplicationKey(key).SetPayload(data).SetAvailableAt(a.CreatedAt).SetCreatedAt(a.CreatedAt).Save(ctx)
		return personalBotError(err)
	case teamBotDB:
		_, err = d.client.BotOutbox.Create().SetBotID(a.BotID).SetSequence(a.Sequence).SetKind(string(a.Kind)).SetDeduplicationKey(key).SetPayload(data).SetAvailableAt(a.CreatedAt).SetCreatedAt(a.CreatedAt).Save(ctx)
		return teamBotError(err)
	default:
		return store.ErrBotUnsupported
	}
}

type personalBotDeleteDB struct{ personalBotDB }
type teamBotDeleteDB struct{ teamBotDB }

func (d personalBotDeleteDB) InsertActivity(ctx context.Context, a store.BotActivity, key string) error {
	return insertCleanupActivity(ctx, d.personalBotDB, a, key)
}
func (d teamBotDeleteDB) InsertActivity(ctx context.Context, a store.BotActivity, key string) error {
	return insertCleanupActivity(ctx, d.teamBotDB, a, key)
}
func botDeleteDB(db botDB) botDB {
	switch d := db.(type) {
	case personalBotDB:
		return personalBotDeleteDB{d}
	case teamBotDB:
		return teamBotDeleteDB{d}
	default:
		return db
	}
}
func cleanupBaseDB(db botDB) botDB {
	switch d := db.(type) {
	case personalBotDeleteDB:
		return d.personalBotDB
	case teamBotDeleteDB:
		return d.teamBotDB
	default:
		return db
	}
}

// SQL status is authoritative already; keep immutable content and all receipts
// untouched. Running attempts remain interrupted (uncertain), never cancelled.
func (d personalBotDeleteDB) QuiesceBotRuns(ctx context.Context, id uuid.UUID, stop bool, now time.Time) ([]store.BotRun, error) {
	if !stop {
		return nil, store.ErrBotInvalid
	}
	rows, err := d.client.BotRun.Query().Where(pr.BotIDEQ(id), pr.StatusIn(pr.StatusRunning, pr.StatusQueued, pr.StatusWaitingApproval)).All(ctx)
	if err != nil {
		return nil, personalBotError(err)
	}
	runs := make([]store.BotRun, 0, len(rows))
	for _, row := range rows {
		sid, err := uuid.Parse(row.SessionID)
		if err != nil {
			return nil, store.ErrBotInvalid
		}
		status := store.RunCancelled
		if row.Status == pr.StatusRunning {
			status = store.RunInterrupted
		}
		presentation, err := cleanupRunPresentation(row.Data)
		if err != nil {
			return nil, err
		}
		runs = append(runs, store.BotRun{ID: row.ID, BotID: id, SessionID: sid, Status: status, Presentation: presentation})
	}
	_, err = d.client.BotRun.Update().Where(pr.BotIDEQ(id), pr.StatusEQ(pr.StatusRunning)).SetStatus(pr.StatusInterrupted).ClearLeaseExpiresAt().SetUpdatedAt(now).Save(ctx)
	if err != nil {
		return nil, personalBotError(err)
	}
	_, err = d.client.BotRun.Update().Where(pr.BotIDEQ(id), pr.StatusIn(pr.StatusQueued, pr.StatusWaitingApproval)).SetStatus(pr.StatusCancelled).ClearLeaseExpiresAt().SetUpdatedAt(now).Save(ctx)
	return runs, personalBotError(err)
}
func (d teamBotDeleteDB) QuiesceBotRuns(ctx context.Context, id uuid.UUID, stop bool, now time.Time) ([]store.BotRun, error) {
	if !stop {
		return nil, store.ErrBotInvalid
	}
	rows, err := d.client.BotRun.Query().Where(tr.BotIDEQ(id), tr.StatusIn(tr.StatusRunning, tr.StatusQueued, tr.StatusWaitingApproval)).All(ctx)
	if err != nil {
		return nil, teamBotError(err)
	}
	runs := make([]store.BotRun, 0, len(rows))
	for _, row := range rows {
		sid, err := uuid.Parse(row.SessionID)
		if err != nil {
			return nil, store.ErrBotInvalid
		}
		status := store.RunCancelled
		if row.Status == tr.StatusRunning {
			status = store.RunInterrupted
		}
		presentation, err := cleanupRunPresentation(row.Data)
		if err != nil {
			return nil, err
		}
		runs = append(runs, store.BotRun{ID: row.ID, BotID: id, SessionID: sid, Status: status, Presentation: presentation})
	}
	_, err = d.client.BotRun.Update().Where(tr.BotIDEQ(id), tr.StatusEQ(tr.StatusRunning)).SetStatus(tr.StatusInterrupted).ClearLeaseExpiresAt().SetUpdatedAt(now).Save(ctx)
	if err != nil {
		return nil, teamBotError(err)
	}
	_, err = d.client.BotRun.Update().Where(tr.BotIDEQ(id), tr.StatusIn(tr.StatusQueued, tr.StatusWaitingApproval)).SetStatus(tr.StatusCancelled).ClearLeaseExpiresAt().SetUpdatedAt(now).Save(ctx)
	return runs, teamBotError(err)
}

func cleanupRunPresentation(data map[string]any) (store.BotRunPresentation, error) {
	var value struct{ Presentation store.BotRunPresentation }
	if raw, ok := data["bot_record_v2"].(string); ok {
		var record struct {
			Control struct{ Presentation store.BotRunPresentation }
		}
		if len(raw) > botCleanupAuthorityMaxBytes || json.Unmarshal([]byte(raw), &record) != nil {
			return "", store.ErrBotInvalid
		}
		value.Presentation = record.Control.Presentation
	} else if raw, ok := data["bot_document"].(string); ok {
		var record struct {
			Run struct{ Presentation store.BotRunPresentation }
		}
		if len(raw) > botCleanupAuthorityMaxBytes || json.Unmarshal([]byte(raw), &record) != nil {
			return "", store.ErrBotInvalid
		}
		value.Presentation = record.Run.Presentation
	}
	if value.Presentation != "" && value.Presentation != store.BotRunPresentationHomeInteractive {
		return "", store.ErrBotInvalid
	}
	return value.Presentation, nil
}

// Only the recovery marker is read from JSON. Session, status, run identity and
// fence come from SQL columns. Legacy report/work/task bodies are never hydrated.
func cleanupRunMarker(data map[string]any) (string, error) {
	if raw, ok := data["bot_record_v2"].(string); ok {
		if len(raw) > botCleanupAuthorityMaxBytes {
			return "", store.ErrBotInvalid
		}
		var record struct {
			Control struct{ CommitHash string } `json:"control"`
		}
		if json.Unmarshal([]byte(raw), &record) != nil || len(record.Control.CommitHash) > 128 {
			return "", store.ErrBotInvalid
		}
		return record.Control.CommitHash, nil
	}
	if raw, ok := data["bot_document"].(string); ok {
		if len(raw) > botCleanupAuthorityMaxBytes {
			return "", store.ErrBotInvalid
		}
		var record struct{ CommitHash string }
		if json.Unmarshal([]byte(raw), &record) != nil || len(record.CommitHash) > 128 {
			return "", store.ErrBotInvalid
		}
		return record.CommitHash, nil
	}
	return "", nil
}
func readCleanupRun(ctx context.Context, db botDB, id, rid uuid.UUID) (*botStoredRun, error) {
	var sid, marker string
	var status store.RunStatus
	var fence int64
	var err error
	switch d := cleanupBaseDB(db).(type) {
	case personalBotDB:
		row, e := d.client.BotRun.Query().Where(pr.BotIDEQ(id), pr.IDEQ(rid)).Only(ctx)
		if e != nil {
			return nil, personalBotError(e)
		}
		sid, status, fence = row.SessionID, store.RunStatus(row.Status), row.Fence
		marker, err = cleanupRunMarker(row.Data)
	case teamBotDB:
		row, e := d.client.BotRun.Query().Where(tr.BotIDEQ(id), tr.IDEQ(rid)).Only(ctx)
		if e != nil {
			return nil, teamBotError(e)
		}
		sid, status, fence = row.SessionID, store.RunStatus(row.Status), row.Fence
		marker, err = cleanupRunMarker(row.Data)
	default:
		return nil, store.ErrBotUnsupported
	}
	if err != nil {
		return nil, err
	}
	session, err := uuid.Parse(sid)
	if err != nil {
		return nil, store.ErrBotInvalid
	}
	return &botStoredRun{Run: store.BotRun{ID: rid, BotID: id, SessionID: session, Status: status, Lease: store.BotLease{BotID: id, RunID: rid, Fence: fence}}, CommitHash: marker}, nil
}

type cleanupAuthorityRow struct {
	Key       string
	Authority *botCleanupAuthority
}

func readCleanupAuthorities(ctx context.Context, db botDB, id uuid.UUID, after int64) ([]cleanupAuthorityRow, int64, error) {
	var result []cleanupAuthorityRow
	consume := func(key string, seq int64, data map[string]any) error {
		after = seq
		// Tombstones and accounting are not issued authority.
		if !strings.HasPrefix(key, "mandate:") && !strings.HasPrefix(key, "mandate-operation:") {
			return nil
		}
		var a *botCleanupAuthority
		if raw, ok := data["bot_record_v2"].(string); ok {
			if len(raw) > botCleanupAuthorityMaxBytes {
				return store.ErrBotInvalid
			}
			var record botActivityRecord
			if decodeBotRecord(data, &record) != nil || record.SchemaVersion != store.BotSchemaVersion || record.BotID != id || record.Sequence != seq || record.Kind != mandateRecordKind {
				return store.ErrBotInvalid
			}
			a = record.CleanupAuthority
			// A reference without authority is not permission to skip revocation.
			if a == nil {
				return store.ErrBotInvalid
			}
		} else if raw, ok := data["bot_document"].(string); ok {
			var err error
			a, err = cleanupAuthority([]byte(raw))
			if err != nil {
				return err
			}
		} else {
			raw, err := json.Marshal(data)
			if err != nil {
				return err
			}
			a, err = cleanupAuthority(raw)
			if err != nil {
				return err
			}
		}
		result = append(result, cleanupAuthorityRow{key, a})
		return nil
	}
	switch d := cleanupBaseDB(db).(type) {
	case personalBotDB:
		rows, err := d.client.BotOutbox.Query().Where(po.BotIDEQ(id), po.KindEQ(string(mandateRecordKind)), po.SequenceGT(after)).Order(personalent.Asc(po.FieldSequence)).Limit(200).All(ctx)
		if err != nil {
			return nil, after, err
		}
		for _, row := range rows {
			if err = consume(row.DeduplicationKey, row.Sequence, row.Payload); err != nil {
				return nil, after, err
			}
		}
		if len(rows) < 200 {
			after = -1
		}
	case teamBotDB:
		rows, err := d.client.BotOutbox.Query().Where(to.BotIDEQ(id), to.KindEQ(string(mandateRecordKind)), to.SequenceGT(after)).Order(teament.Asc(to.FieldSequence)).Limit(200).All(ctx)
		if err != nil {
			return nil, after, err
		}
		for _, row := range rows {
			if err = consume(row.DeduplicationKey, row.Sequence, row.Payload); err != nil {
				return nil, after, err
			}
		}
		if len(rows) < 200 {
			after = -1
		}
	default:
		return nil, after, store.ErrBotUnsupported
	}
	return result, after, nil
}

func cleanupRevokedExists(ctx context.Context, db botDB, ref store.BotMandateRef) (bool, error) {
	switch d := cleanupBaseDB(db).(type) {
	case personalBotDB:
		return d.client.BotOutbox.Query().Where(po.BotIDEQ(ref.BotID), po.DeduplicationKeyEQ(mandateRevokedKey(ref))).Exist(ctx)
	case teamBotDB:
		return d.client.BotOutbox.Query().Where(to.BotIDEQ(ref.BotID), to.DeduplicationKeyEQ(mandateRevokedKey(ref))).Exist(ctx)
	default:
		return false, store.ErrBotUnsupported
	}
}

// Exact recovery/cancellation evidence can be checked without its report body.
func readCleanupRunActivity(ctx context.Context, db botDB, id uuid.UUID, key string) (*store.BotActivity, error) {
	var data map[string]any
	var sequence int64
	var kind store.BotActivityKind
	var created time.Time
	switch d := cleanupBaseDB(db).(type) {
	case personalBotDB:
		row, err := d.client.BotOutbox.Query().Where(po.BotIDEQ(id), po.DeduplicationKeyEQ(key)).Only(ctx)
		if err != nil {
			return nil, personalBotError(err)
		}
		data, sequence, kind, created = row.Payload, row.Sequence, store.BotActivityKind(row.Kind), row.CreatedAt
	case teamBotDB:
		row, err := d.client.BotOutbox.Query().Where(to.BotIDEQ(id), to.DeduplicationKeyEQ(key)).Only(ctx)
		if err != nil {
			return nil, teamBotError(err)
		}
		data, sequence, kind, created = row.Payload, row.Sequence, store.BotActivityKind(row.Kind), row.CreatedAt
	default:
		return nil, store.ErrBotUnsupported
	}
	var state struct {
		RunID  uuid.UUID       `json:"run_id"`
		Status store.RunStatus `json:"status"`
	}
	if raw, ok := data["bot_record_v2"].(string); ok {
		if len(raw) > botCleanupAuthorityMaxBytes {
			return nil, store.ErrBotInvalid
		}
		var record botActivityRecord
		if decodeBotRecord(data, &record) != nil || validateRuntimePayload(record.botRuntimeInline) != nil || record.SchemaVersion != store.BotSchemaVersion || record.BotID != id || record.Sequence != sequence || record.Kind != kind || !record.CreatedAt.Equal(created) || record.RunID == nil {
			return nil, store.ErrBotInvalid
		}
		state.RunID, state.Status = *record.RunID, record.RunStatus
	} else if raw, ok := data["bot_document"].(string); ok {
		if len(raw) > botCleanupAuthorityMaxBytes || json.Unmarshal([]byte(raw), &state) != nil {
			return nil, store.ErrBotInvalid
		}
	} else {
		return nil, store.ErrBotInvalid
	}
	if state.RunID == uuid.Nil {
		return nil, store.ErrBotInvalid
	}
	raw, err := json.Marshal(state)
	if err != nil {
		return nil, err
	}
	return &store.BotActivity{BotID: id, RunID: &state.RunID, Sequence: sequence, Kind: kind, CreatedAt: created, Payload: raw}, nil
}

// A closed terminal status record is the only transcript exception to file-owned
// content. It cannot carry a message, work, progress, tool result or authority.
type botCleanupRunState struct {
	Presentation  store.BotRunPresentation `json:"presentation,omitempty"`
	BotID         uuid.UUID                `json:"bot_id"`
	RunID         uuid.UUID                `json:"run_id"`
	SessionID     uuid.UUID                `json:"session_id"`
	HomeSessionID uuid.UUID                `json:"home_session_id"`
	Status        store.RunStatus          `json:"status"`
	Sequence      int64                    `json:"sequence"`
	At            time.Time                `json:"at"`
}

const botCleanupRunStateKey = "bot_cleanup_run_state_v1"

func decodeCleanupRunEvent(id store.BotID, sessionID string, data map[string]any) (*adksession.Event, error) {
	raw, ok := data[botCleanupRunStateKey].(string)
	if !ok || len(data) != 1 || len(raw) > 4096 {
		return nil, store.ErrBotInvalid
	}
	var s botCleanupRunState
	decoder := json.NewDecoder(bytes.NewReader([]byte(raw)))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&s) != nil || !json.Valid([]byte(raw)) || s.BotID != id || (sessionID != s.SessionID.String() && sessionID != s.HomeSessionID.String()) || s.At.IsZero() || (s.Status != store.RunInterrupted && s.Status != store.RunCancelled) {
		return nil, store.ErrBotInvalid
	}
	return store.NewBotRunStateEvent(store.Bot{ID: id, HomeSessionID: s.HomeSessionID}, store.BotRun{ID: s.RunID, BotID: id, SessionID: s.SessionID, Status: s.Status, Presentation: s.Presentation}, s.Sequence, s.At)
}
func appendCleanupRunState(ctx context.Context, db botDB, b store.Bot, run store.BotRun, seq int64, at time.Time) error {
	s := botCleanupRunState{BotID: b.ID, RunID: run.ID, SessionID: run.SessionID, HomeSessionID: b.HomeSessionID, Status: run.Status, Sequence: seq, At: at, Presentation: run.Presentation}
	raw, err := json.Marshal(s)
	if err != nil {
		return err
	}
	data := map[string]any{botCleanupRunStateKey: string(raw)}
	event, err := decodeCleanupRunEvent(b.ID, run.SessionID.String(), data)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(botCleanupRecord{RunState: &s})
	if err != nil {
		return err
	}
	if err = insertCleanupActivity(ctx, cleanupBaseDB(db), store.BotActivity{BotID: b.ID, Kind: store.BotActivityLifecycle, Sequence: seq, CreatedAt: at, Payload: payload}, fmt.Sprintf("lifecycle-result:%d:%s", b.Revision, run.ID)); err != nil {
		return err
	}
	// The root lifecycle CAS serializes these inserts and makes retries idempotent.
	switch d := cleanupBaseDB(db).(type) {
	case personalBotDB:
		exists, err := d.client.Session.Query().Where(ps.IDEQ(run.SessionID.String()), ps.BotIDEQ(b.ID), ps.BotRunIDEQ(run.ID), ps.UserIDEQ(b.OwnerID), ps.PurposeEQ(ps.PurposeBotRun)).Exist(ctx)
		if err != nil {
			return err
		}
		if !exists {
			return store.ErrBotForbidden
		}
		exists, err = d.client.Session.Query().Where(ps.IDEQ(b.HomeSessionID.String()), ps.BotIDEQ(b.ID), ps.UserIDEQ(b.OwnerID), ps.PurposeEQ(ps.PurposeBotHome)).Exist(ctx)
		if err != nil {
			return err
		}
		if !exists {
			return store.ErrBotForbidden
		}
		if _, err = d.client.SessionEvent.Create().SetSessionID(run.SessionID.String()).SetEventData(data).Save(ctx); err != nil {
			return err
		}
		if run.Presentation == store.BotRunPresentationHomeInteractive {
			if _, err = d.client.SessionEvent.Create().SetSessionID(b.HomeSessionID.String()).SetEventID(event.ID).SetEventData(data).Save(ctx); err != nil {
				return err
			}
		}
		queueBotRunStateNotification(ctx, d.afterCommit, event.Actions.StateDelta[store.BotRunStateKey].(store.BotRunState))
	case teamBotDB:
		exists, err := d.client.Session.Query().Where(ts.IDEQ(run.SessionID.String()), ts.BotIDEQ(b.ID), ts.BotRunIDEQ(run.ID), ts.UserIDEQ(b.OwnerID), ts.PurposeEQ(ts.PurposeBotRun)).Exist(ctx)
		if err != nil {
			return err
		}
		if !exists {
			return store.ErrBotForbidden
		}
		exists, err = d.client.Session.Query().Where(ts.IDEQ(b.HomeSessionID.String()), ts.BotIDEQ(b.ID), ts.UserIDEQ(b.OwnerID), ts.PurposeEQ(ts.PurposeBotHome)).Exist(ctx)
		if err != nil {
			return err
		}
		if !exists {
			return store.ErrBotForbidden
		}
		if _, err = d.client.SessionEvent.Create().SetSessionID(run.SessionID.String()).SetEventData(data).Save(ctx); err != nil {
			return err
		}
		if run.Presentation == store.BotRunPresentationHomeInteractive {
			if _, err = d.client.SessionEvent.Create().SetSessionID(b.HomeSessionID.String()).SetEventID(event.ID).SetEventData(data).Save(ctx); err != nil {
				return err
			}
		}
		queueBotRunStateNotification(ctx, d.afterCommit, event.Actions.StateDelta[store.BotRunStateKey].(store.BotRunState))
	default:
		return store.ErrBotUnsupported
	}
	return nil
}

// Overlays only a revoked operation's terminal coordination state after normal
// content validation. It cannot make missing/legacy bytes executable.
func applyOperationCleanup(rec *botStoredRecord, o *store.BotOperation) error {
	raw, ok := rec.Metadata[botOperationCleanupPrefix+o.ID.String()]
	if !ok {
		return nil
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return err
	}
	var a botCleanupAuthority
	if json.Unmarshal(b, &a) != nil || a.ID != o.ID || a.MandateRef != o.MandateRef || a.Revision != o.Revision+1 || a.Status != store.BotOperationCancelled {
		return store.ErrBotInvalid
	}
	if o.Status != store.BotOperationPending && o.Status != store.BotOperationBlocked {
		return store.ErrBotConflict
	}
	o.Status, o.Revision, o.Receipt = a.Status, a.Revision, nil
	return nil
}
