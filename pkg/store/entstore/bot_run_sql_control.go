package entstore

import (
	"context"
	"time"

	personalbotrun "github.com/SAP/astonish/ent/personal/botrun"
	teambotrun "github.com/SAP/astonish/ent/team/botrun"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

// sqlRunControl is the coordination record already stored in the SQL envelope.
// Lease fences, intent checks and interactive status do not need the checkpoint
// blob. Disk bytes stay authoritative for work, instructions and checkpoints.
type sqlRunControl struct {
	Version        int64
	Status         store.RunStatus
	Lease          store.BotLease
	Intent         store.BotExecutionIntent
	ExecutionMode  execution.BotExecutionMode
	Presentation   store.BotRunPresentation
	ProgressSet    bool
	ProgressCursor int64
}

func (c sqlRunControl) run() store.BotRun {
	return store.BotRun{ID: c.Lease.RunID, BotID: c.Lease.BotID, Version: c.Version, Status: c.Status, Lease: c.Lease, Intent: c.Intent, ExecutionMode: c.ExecutionMode, Presentation: c.Presentation}
}

func sqlRunControlFromRow(id, rid uuid.UUID, version int64, status, worker string, fence int64, expires *time.Time, data map[string]any) (sqlRunControl, error) {
	var record botRunRecord
	if err := decodeBotRecord(data, &record); err != nil {
		return sqlRunControl{}, err
	}
	control := record.Control
	if err := validateRuntimePayload(record.botRuntimeInline); err != nil {
		return sqlRunControl{}, err
	}
	if control.BotID != id || control.ID != rid || control.Version != version || version < 1 {
		return sqlRunControl{}, store.ErrBotInvalid
	}
	lease := store.BotLease{BotID: id, RunID: rid, WorkerID: worker, Fence: fence}
	if expires != nil {
		lease.ExpiresAt = *expires
	}
	return sqlRunControl{Version: version, Status: store.RunStatus(status), Lease: lease, Intent: control.Intent, ExecutionMode: control.ExecutionMode, Presentation: control.Presentation, ProgressSet: control.Progress != nil, ProgressCursor: control.ProgressCursor}, nil
}

func (d personalBotDB) readRunSQLControl(ctx context.Context, id, rid uuid.UUID) (sqlRunControl, error) {
	row, err := d.client.BotRun.Query().Where(personalbotrun.BotIDEQ(id), personalbotrun.IDEQ(rid)).Only(ctx)
	if err != nil {
		return sqlRunControl{}, personalBotError(err)
	}
	return sqlRunControlFromRow(id, rid, row.Version, string(row.Status), row.WorkerID, row.Fence, row.LeaseExpiresAt, row.Data)
}

func (d teamBotDB) readRunSQLControl(ctx context.Context, id, rid uuid.UUID) (sqlRunControl, error) {
	row, err := d.client.BotRun.Query().Where(teambotrun.BotIDEQ(id), teambotrun.IDEQ(rid)).Only(ctx)
	if err != nil {
		return sqlRunControl{}, teamBotError(err)
	}
	return sqlRunControlFromRow(id, rid, row.Version, string(row.Status), row.WorkerID, row.Fence, row.LeaseExpiresAt, row.Data)
}

type botRunSQLControl interface {
	readRunSQLControl(context.Context, uuid.UUID, uuid.UUID) (sqlRunControl, error)
}
