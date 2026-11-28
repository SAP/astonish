package entstore

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	personalbot "github.com/SAP/astonish/ent/personal/bot"
	personalapproval "github.com/SAP/astonish/ent/personal/botapproval"
	personalgoal "github.com/SAP/astonish/ent/personal/botgoal"
	personalknowledge "github.com/SAP/astonish/ent/personal/botknowledgeextraction"
	personalactivity "github.com/SAP/astonish/ent/personal/botoutbox"
	personalrun "github.com/SAP/astonish/ent/personal/botrun"
	personalskillroot "github.com/SAP/astonish/ent/personal/botskill"
	personalskill "github.com/SAP/astonish/ent/personal/botskillrevision"
	personaltask "github.com/SAP/astonish/ent/personal/bottaskrevision"
	personaltracked "github.com/SAP/astonish/ent/personal/bottrackeditem"
	personalversion "github.com/SAP/astonish/ent/personal/botversion"
	personalsession "github.com/SAP/astonish/ent/personal/session"
	personalevent "github.com/SAP/astonish/ent/personal/sessionevent"
	teambot "github.com/SAP/astonish/ent/team/bot"
	teamapproval "github.com/SAP/astonish/ent/team/botapproval"
	teamgoal "github.com/SAP/astonish/ent/team/botgoal"
	teamknowledge "github.com/SAP/astonish/ent/team/botknowledgeextraction"
	teamactivity "github.com/SAP/astonish/ent/team/botoutbox"
	teamrun "github.com/SAP/astonish/ent/team/botrun"
	teamskillroot "github.com/SAP/astonish/ent/team/botskill"
	teamskill "github.com/SAP/astonish/ent/team/botskillrevision"
	teamtask "github.com/SAP/astonish/ent/team/bottaskrevision"
	teamtracked "github.com/SAP/astonish/ent/team/bottrackeditem"
	teamversion "github.com/SAP/astonish/ent/team/botversion"
	teamsession "github.com/SAP/astonish/ent/team/session"
	teamevent "github.com/SAP/astonish/ent/team/sessionevent"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

// SelectBotHomeConfiguration atomically selects the complete typed SQL content
// reference closure. Immutable bytes are copied and verified before selection;
// staged objects are never removed here. Intake and templates remain independent.
// The exact binding must have been independently verified by trusted composition.
func SelectBotHomeConfiguration(ctx context.Context, bots store.BotStore, exact sandbox.BotHomeBinding, backend sandbox.BotHomeFilesBackend) error {
	var r *botRepository
	switch s := bots.(type) {
	case *personalBotStore:
		r = s.repository()
	case *teamBotStore:
		r = s.repository()
	default:
		return store.ErrBotUnsupported
	}
	return r.selectHomeConfiguration(ctx, exact, backend)
}

type botHomeConfigurationDB interface {
	contentCodec() *botContentCodec
	homeConfigurationRecords(context.Context, store.BotID) ([]botHomeConfigurationRecord, error)
	selectHomeConfigurationRecords(context.Context, botStoredRecord, []botHomeConfigurationRecord, map[string]any) error
}
type botHomeConfigurationKind uint8

const (
	botHomeVersion botHomeConfigurationKind = iota + 1
	botHomeGoal
	botHomeRun
	botHomeTask
	botHomeSkill
	botHomeApproval
	botHomeActivity
	botHomeKnowledge
	botHomeSessionEvent
	botHomeTracked
	botHomeSkillRoot
)

type botHomeConfigurationRecord struct {
	kind         botHomeConfigurationKind
	id           uuid.UUID
	revision     int64
	data         map[string]any
	sessionID    string
	eventID      int64
	activityKind store.BotActivityKind
	createdAt    time.Time
}

func (r *botRepository) selectHomeConfiguration(ctx context.Context, exact sandbox.BotHomeBinding, backend sandbox.BotHomeFilesBackend) error {
	p, ok := execution.PrincipalFromContext(ctx)
	if !ok || p.Validate() != nil || p.Kind != execution.PrincipalKindUser || exact.Validate() != nil || backend == nil || p.Subject != exact.Scope.UserID || p.OrgSlug != exact.Scope.OrgID || p.TeamSlug != exact.Scope.TeamID {
		return store.ErrBotForbidden
	}
	id, err := uuid.Parse(exact.Scope.BotID)
	if err != nil {
		return store.ErrBotInvalid
	}
	if _, err = r.auth.authorize(ctx, uuid.Nil, true); err != nil {
		return err
	}
	// Selection is one synchronous operation: destination verification and the
	// installed codec's final logical validation may share verified immutable
	// bytes, but neither the root fence nor the binding lookup is cached.
	ctx, endContentReads := WithBotContentReadOperation(ctx)
	defer endContentReads()
	return r.db.Transaction(ctx, func(tx botDB) (resultErr error) {
		db, ok := tx.(botHomeConfigurationDB)
		if !ok {
			return store.ErrBotUnsupported
		}
		row, err := tx.GetBot(ctx, id)
		if err != nil {
			return err
		}
		actor, err := r.auth.authorize(ctx, row.Bot.OwnerID, true)
		if err != nil {
			return err
		}
		if actor != row.Bot.OwnerID || row.Bot.PermanentDelete || row.Bot.Status == store.BotDeleted {
			return store.ErrBotForbidden
		}
		// An UPDATE of the root coordination row below fences concurrent writers.
		// Every copy and SQL replacement belongs to this exact transaction snapshot.
		if err := db.selectHomeConfigurationRecords(ctx, *row, nil, nil); err != nil {
			return err
		}
		locked, err := tx.GetBot(ctx, id)
		if err != nil {
			return err
		}
		if locked.Bot.Home != row.Bot.Home || locked.Bot.OwnerID != row.Bot.OwnerID || locked.Bot.Revision != row.Bot.Revision {
			return store.ErrBotConflict
		}
		row = locked
		codec := db.contentCodec()
		if codec == nil {
			return store.ErrBotUnsupported
		}
		staging, err := codec.Open(ctx, id)
		if err != nil {
			return err
		}
		defer func() { resultErr = errors.Join(resultErr, staging.Close()) }()
		var home *botHomeContent
		if selected, ok := staging.(*botHomeContent); ok {
			if selected.binding != exact {
				return store.ErrBotConflict
			}
			home = selected
		} else {
			scope := store.BotContentScope{OrgID: r.auth.orgSlug, TeamID: r.auth.teamSlug, OwnerID: row.Bot.OwnerID.String(), BotID: id}
			if row.Bot.Scope == store.BotScopePersonal {
				scope.TeamID = personalSchemaName(scope.OwnerID)
			}
			home, err = newBotHomeContent(staging, scope, row.Bot, exact, backend, func(commit func() error) error {
				current, err := tx.GetBot(ctx, id)
				if err != nil {
					return err
				}
				if current.Bot.Home != row.Bot.Home || current.Bot.OwnerID != row.Bot.OwnerID || current.Bot.Revision != row.Bot.Revision {
					return store.ErrBotConflict
				}
				return commit()
			})
			if err != nil {
				return err
			}
		}
		if cache, ok := ctx.Value(botContentReadOperationKey{}).(*botTransactionContent); ok {
			home.readCache = cache
		}
		records, err := db.homeConfigurationRecords(ctx, id)
		if err != nil {
			return err
		}
		// A committed selection already verified the complete closure. New writes
		// use this exact Home; retries must not reopen every historical body. Check
		// the current typed SQL references under the same writer fence instead.
		// Any remaining staged reference takes the full copy/verification path.
		if _, selected := staging.(*botHomeContent); selected {
			complete := true
			for _, record := range records {
				_, ref, _, err := homeRecordEnvelope(record)
				if err != nil {
					return err
				}
				if ref == nil {
					if err := validateHomeRecord(ctx, codec, id, record); err != nil {
						return err
					}
					continue
				}
				if ref.Validate() != nil {
					return store.ErrBotInvalid
				}
				if strings.HasPrefix(ref.Key, "staged-v1/") {
					if ref.Scope != home.stagedScope {
						return store.ErrBotForbidden
					}
					complete = false
				} else if ref.Scope != home.homeScope || !strings.HasPrefix(ref.Key, "content-v1/") {
					return store.ErrBotForbidden
				}
			}
			if complete {
				return ctx.Err()
			}
		}
		// Decode only named typed records; unknown formats/fields fail closed. Verify
		// logical codecs before copying, and every destination before any SQL selection.
		for i := range records {
			records[i].data, err = selectHomeConfigurationRecord(ctx, codec, home, id, records[i])
			if err != nil {
				return err
			}
		}
		metadata := make(map[string]any, len(row.Metadata)+1)
		for key, value := range row.Metadata {
			metadata[key] = value
		}
		metadata[botContentHomeSelectionKey] = botContentHomeSelection{Home: row.Bot.Home, Binding: exact}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := db.selectHomeConfigurationRecords(ctx, *row, records, metadata); err != nil {
			return err
		}
		// Prove the installed transaction codec can hydrate the selected rows
		// before committing SQL. A missing runtime installer rolls everything back.
		for _, record := range records {
			if err := validateHomeRecord(ctx, codec, id, record); err != nil {
				return err
			}
		}
		return nil
	})
}

func selectHomeConfigurationRecord(ctx context.Context, codec *botContentCodec, home *botHomeContent, id store.BotID, row botHomeConfigurationRecord) (map[string]any, error) {
	if err := validateHomeRecord(ctx, codec, id, row); err != nil {
		return nil, err
	}
	record, ref, finish, err := homeRecordEnvelope(row)
	if err != nil {
		return nil, err
	}
	if ref == nil {
		return row.data, nil
	}
	if strings.HasPrefix(ref.Key, "staged-v1/") {
		next, err := home.bindStaged(ctx, *ref, func(commit func() error) error { return commit() })
		if err != nil {
			return nil, err
		}
		*ref = *next
	} else if ref.Scope != home.homeScope || !strings.HasPrefix(ref.Key, "content-v1/") {
		return nil, store.ErrBotForbidden
	}
	if err := finish(); err != nil {
		return nil, err
	}
	data, err := json.Marshal(record)
	if err != nil {
		return nil, err
	}
	return map[string]any{"bot_record_v2": string(data)}, nil
}

func (d personalBotDB) homeConfigurationRecords(ctx context.Context, id store.BotID) ([]botHomeConfigurationRecord, error) {
	versions, err := d.client.BotVersion.Query().Where(personalversion.BotIDEQ(id)).All(ctx)
	if err != nil {
		return nil, err
	}
	goals, err := d.client.BotGoal.Query().Where(personalgoal.BotIDEQ(id)).All(ctx)
	if err != nil {
		return nil, err
	}
	records := make([]botHomeConfigurationRecord, 0, len(versions)+len(goals))
	for _, v := range versions {
		records = append(records, botHomeConfigurationRecord{kind: botHomeVersion, id: v.ID, revision: v.Version, data: v.Definition})
	}
	for _, g := range goals {
		records = append(records, botHomeConfigurationRecord{kind: botHomeGoal, id: g.ID, revision: g.Revision, data: g.Data})
	}
	return d.homeContentRecords(ctx, id, records)
}
func (d teamBotDB) homeConfigurationRecords(ctx context.Context, id store.BotID) ([]botHomeConfigurationRecord, error) {
	versions, err := d.client.BotVersion.Query().Where(teamversion.BotIDEQ(id)).All(ctx)
	if err != nil {
		return nil, err
	}
	goals, err := d.client.BotGoal.Query().Where(teamgoal.BotIDEQ(id)).All(ctx)
	if err != nil {
		return nil, err
	}
	records := make([]botHomeConfigurationRecord, 0, len(versions)+len(goals))
	for _, v := range versions {
		records = append(records, botHomeConfigurationRecord{kind: botHomeVersion, id: v.ID, revision: v.Version, data: v.Definition})
	}
	for _, g := range goals {
		records = append(records, botHomeConfigurationRecord{kind: botHomeGoal, id: g.ID, revision: g.Revision, data: g.Data})
	}
	return d.homeContentRecords(ctx, id, records)
}
func (d personalBotDB) selectHomeConfigurationRecords(ctx context.Context, row botStoredRecord, records []botHomeConfigurationRecord, metadata map[string]any) error {
	u := d.client.Bot.Update().Where(personalbot.IDEQ(row.Bot.ID), personalbot.RevisionEQ(row.Bot.Revision), personalbot.LastSeqEQ(row.LastSequence), personalbot.FenceEQ(row.Fence)).SetRevision(row.Bot.Revision).SetUpdatedAt(row.Bot.UpdatedAt)
	if metadata != nil {
		u.SetData(metadata)
	}
	n, err := u.Save(ctx)
	if err != nil {
		return err
	}
	if n != 1 {
		return store.ErrBotConflict
	}
	for _, record := range records {
		switch record.kind {
		case botHomeVersion:
			v, readErr := d.client.BotVersion.Get(ctx, record.id)
			if readErr != nil {
				return readErr
			}
			// Ent intentionally has no setters for immutable version fields.
			// Replace the physical row inside this transaction, preserving its ID,
			// logical digest and every immutable coordination field verbatim.
			if err = d.client.BotVersion.DeleteOneID(record.id).Exec(ctx); err != nil {
				return err
			}
			err = d.client.BotVersion.Create().SetID(v.ID).SetBotID(v.BotID).SetVersion(v.Version).SetSchemaVersion(v.SchemaVersion).SetDefinition(record.data).SetContentHash(v.ContentHash).SetCreatedBy(v.CreatedBy).SetChangeReason(v.ChangeReason).SetCreatedAt(v.CreatedAt).Exec(ctx)
		case botHomeGoal:
			v, readErr := d.client.BotGoal.Get(ctx, record.id)
			if readErr != nil {
				return readErr
			}
			err = d.client.BotGoal.UpdateOneID(record.id).SetData(record.data).SetUpdatedAt(v.UpdatedAt).Exec(ctx)
		default:
			err = d.selectHomeContentRecord(ctx, record)
		}
		if err != nil {
			return err
		}
	}
	return nil
}
func (d teamBotDB) selectHomeConfigurationRecords(ctx context.Context, row botStoredRecord, records []botHomeConfigurationRecord, metadata map[string]any) error {
	u := d.client.Bot.Update().Where(teambot.IDEQ(row.Bot.ID), teambot.RevisionEQ(row.Bot.Revision), teambot.LastSeqEQ(row.LastSequence), teambot.FenceEQ(row.Fence)).SetRevision(row.Bot.Revision).SetUpdatedAt(row.Bot.UpdatedAt)
	if metadata != nil {
		u.SetData(metadata)
	}
	n, err := u.Save(ctx)
	if err != nil {
		return err
	}
	if n != 1 {
		return store.ErrBotConflict
	}
	for _, record := range records {
		switch record.kind {
		case botHomeVersion:
			v, readErr := d.client.BotVersion.Get(ctx, record.id)
			if readErr != nil {
				return readErr
			}
			// Ent intentionally has no setters for immutable version fields.
			// Replace the physical row inside this transaction, preserving its ID,
			// logical digest and every immutable coordination field verbatim.
			if err = d.client.BotVersion.DeleteOneID(record.id).Exec(ctx); err != nil {
				return err
			}
			err = d.client.BotVersion.Create().SetID(v.ID).SetBotID(v.BotID).SetVersion(v.Version).SetSchemaVersion(v.SchemaVersion).SetDefinition(record.data).SetContentHash(v.ContentHash).SetCreatedBy(v.CreatedBy).SetChangeReason(v.ChangeReason).SetCreatedAt(v.CreatedAt).Exec(ctx)
		case botHomeGoal:
			v, readErr := d.client.BotGoal.Get(ctx, record.id)
			if readErr != nil {
				return readErr
			}
			err = d.client.BotGoal.UpdateOneID(record.id).SetData(record.data).SetUpdatedAt(v.UpdatedAt).Exec(ctx)
		default:
			err = d.selectHomeContentRecord(ctx, record)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func retainedHomeActivity(id store.BotID, sequence int64, kind store.BotActivityKind, created time.Time, data map[string]any) (bool, error) {
	if _, cleanup := data[botCleanupRecordKey]; !cleanup {
		return true, nil
	}
	if _, err := decodeCleanupActivity(id, sequence, kind, created, data); err != nil {
		return false, err
	}
	// Cleanup receipts contain coordination only and no retained-content reference.
	// Their dedicated lifecycle readers keep them durable; generic Home hydration
	// must not reinterpret them as executable/history activity documents.
	return false, nil
}

func (d personalBotDB) homeContentRecords(ctx context.Context, id store.BotID, records []botHomeConfigurationRecord) ([]botHomeConfigurationRecord, error) {
	runs, err := d.client.BotRun.Query().Where(personalrun.BotIDEQ(id)).All(ctx)
	if err != nil {
		return nil, err
	}
	for _, v := range runs {
		records = append(records, botHomeConfigurationRecord{kind: botHomeRun, id: v.ID, revision: v.Version, data: v.Data, sessionID: v.SessionID})
	}
	tasks, err := d.client.BotTaskRevision.Query().Where(personaltask.BotIDEQ(id)).All(ctx)
	if err != nil {
		return nil, err
	}
	for _, v := range tasks {
		records = append(records, botHomeConfigurationRecord{kind: botHomeTask, id: v.ID, revision: v.Revision, data: v.Data})
	}
	skills, err := d.client.BotSkillRevision.Query().Where(personalskill.BotIDEQ(id)).All(ctx)
	if err != nil {
		return nil, err
	}
	for _, v := range skills {
		records = append(records, botHomeConfigurationRecord{kind: botHomeSkill, id: v.ID, revision: v.Revision, data: v.Data})
	}
	roots, err := d.client.BotSkill.Query().Where(personalskillroot.BotIDEQ(id)).All(ctx)
	if err != nil {
		return nil, err
	}
	for _, v := range roots {
		records = append(records, botHomeConfigurationRecord{kind: botHomeSkillRoot, id: v.ID, revision: v.Revision, data: v.Data})
	}
	approvals, err := d.client.BotApproval.Query().Where(personalapproval.BotIDEQ(id)).All(ctx)
	if err != nil {
		return nil, err
	}
	for _, v := range approvals {
		records = append(records, botHomeConfigurationRecord{kind: botHomeApproval, id: v.ID, revision: v.Version, data: v.Data})
	}
	activities, err := d.client.BotOutbox.Query().Where(personalactivity.BotIDEQ(id)).All(ctx)
	if err != nil {
		return nil, err
	}
	for _, v := range activities {
		keep, err := retainedHomeActivity(id, v.Sequence, store.BotActivityKind(v.Kind), v.CreatedAt, v.Payload)
		if err != nil {
			return nil, err
		}
		if keep {
			records = append(records, botHomeConfigurationRecord{kind: botHomeActivity, id: v.ID, revision: v.Sequence, data: v.Payload, activityKind: store.BotActivityKind(v.Kind), createdAt: v.CreatedAt})
		}
	}
	knowledge, err := d.client.BotKnowledgeExtraction.Query().Where(personalknowledge.BotIDEQ(id)).All(ctx)
	if err != nil {
		return nil, err
	}
	for _, v := range knowledge {
		records = append(records, botHomeConfigurationRecord{kind: botHomeKnowledge, id: v.ID, revision: v.Revision, data: v.Data})
	}
	tracked, err := d.client.BotTrackedItem.Query().Where(personaltracked.HasGoalWith(personalgoal.BotIDEQ(id))).All(ctx)
	if err != nil {
		return nil, err
	}
	for _, v := range tracked {
		records = append(records, botHomeConfigurationRecord{kind: botHomeTracked, id: v.ID, revision: v.Revision, data: v.Data})
	}
	events, err := d.client.SessionEvent.Query().Where(personalevent.HasSessionWith(personalsession.BotIDEQ(id))).All(ctx)
	if err != nil {
		return nil, err
	}
	for _, v := range events {
		records = append(records, botHomeConfigurationRecord{kind: botHomeSessionEvent, eventID: int64(v.ID), data: v.EventData, sessionID: v.SessionID})
	}
	return records, nil
}

func (d personalBotDB) selectHomeContentRecord(ctx context.Context, record botHomeConfigurationRecord) error {
	switch record.kind {
	case botHomeRun:
		v, err := d.client.BotRun.Get(ctx, record.id)
		if err != nil {
			return err
		}
		return d.client.BotRun.UpdateOneID(record.id).SetData(record.data).SetUpdatedAt(v.UpdatedAt).Exec(ctx)
	case botHomeTask:
		v, err := d.client.BotTaskRevision.Get(ctx, record.id)
		if err != nil {
			return err
		}
		if err = d.client.BotTaskRevision.DeleteOneID(record.id).Exec(ctx); err != nil {
			return err
		}
		return d.client.BotTaskRevision.Create().SetID(v.ID).SetBotID(v.BotID).SetTaskID(v.TaskID).SetRevision(v.Revision).SetDigest(v.Digest).SetSizeBytes(v.SizeBytes).SetParentRevision(v.ParentRevision).SetParentDigest(v.ParentDigest).SetPublicationState(v.PublicationState).SetAuthorityConstraintRef(v.AuthorityConstraintRef).SetWorkspaceReceiptRef(v.WorkspaceReceiptRef).SetData(record.data).SetCreatedAt(v.CreatedAt).Exec(ctx)
	case botHomeSkill:
		v, err := d.client.BotSkillRevision.Get(ctx, record.id)
		if err != nil {
			return err
		}
		if err = d.client.BotSkillRevision.DeleteOneID(record.id).Exec(ctx); err != nil {
			return err
		}
		return d.client.BotSkillRevision.Create().SetID(v.ID).SetBotID(v.BotID).SetSkillID(v.SkillID).SetRevision(v.Revision).SetDigest(v.Digest).SetSizeBytes(v.SizeBytes).SetData(record.data).SetCreatedAt(v.CreatedAt).Exec(ctx)
	case botHomeSkillRoot:
		return nil // Validated metadata-only index; no physical reference to replace.
	case botHomeApproval:
		return d.client.BotApproval.UpdateOneID(record.id).SetData(record.data).Exec(ctx)
	case botHomeActivity:
		return d.client.BotOutbox.UpdateOneID(record.id).SetPayload(record.data).Exec(ctx)
	case botHomeKnowledge:
		v, err := d.client.BotKnowledgeExtraction.Get(ctx, record.id)
		if err != nil {
			return err
		}
		return d.client.BotKnowledgeExtraction.UpdateOneID(record.id).SetData(record.data).SetUpdatedAt(v.UpdatedAt).Exec(ctx)
	case botHomeTracked:
		return d.client.BotTrackedItem.UpdateOneID(record.id).SetData(record.data).Exec(ctx)
	case botHomeSessionEvent:
		return d.client.SessionEvent.UpdateOneID(int(record.eventID)).SetEventData(record.data).Exec(ctx)
	default:
		return store.ErrBotUnsupported
	}
}

func (d teamBotDB) homeContentRecords(ctx context.Context, id store.BotID, records []botHomeConfigurationRecord) ([]botHomeConfigurationRecord, error) {
	runs, err := d.client.BotRun.Query().Where(teamrun.BotIDEQ(id)).All(ctx)
	if err != nil {
		return nil, err
	}
	for _, v := range runs {
		records = append(records, botHomeConfigurationRecord{kind: botHomeRun, id: v.ID, revision: v.Version, data: v.Data, sessionID: v.SessionID})
	}
	tasks, err := d.client.BotTaskRevision.Query().Where(teamtask.BotIDEQ(id)).All(ctx)
	if err != nil {
		return nil, err
	}
	for _, v := range tasks {
		records = append(records, botHomeConfigurationRecord{kind: botHomeTask, id: v.ID, revision: v.Revision, data: v.Data})
	}
	skills, err := d.client.BotSkillRevision.Query().Where(teamskill.BotIDEQ(id)).All(ctx)
	if err != nil {
		return nil, err
	}
	for _, v := range skills {
		records = append(records, botHomeConfigurationRecord{kind: botHomeSkill, id: v.ID, revision: v.Revision, data: v.Data})
	}
	roots, err := d.client.BotSkill.Query().Where(teamskillroot.BotIDEQ(id)).All(ctx)
	if err != nil {
		return nil, err
	}
	for _, v := range roots {
		records = append(records, botHomeConfigurationRecord{kind: botHomeSkillRoot, id: v.ID, revision: v.Revision, data: v.Data})
	}
	approvals, err := d.client.BotApproval.Query().Where(teamapproval.BotIDEQ(id)).All(ctx)
	if err != nil {
		return nil, err
	}
	for _, v := range approvals {
		records = append(records, botHomeConfigurationRecord{kind: botHomeApproval, id: v.ID, revision: v.Version, data: v.Data})
	}
	activities, err := d.client.BotOutbox.Query().Where(teamactivity.BotIDEQ(id)).All(ctx)
	if err != nil {
		return nil, err
	}
	for _, v := range activities {
		keep, err := retainedHomeActivity(id, v.Sequence, store.BotActivityKind(v.Kind), v.CreatedAt, v.Payload)
		if err != nil {
			return nil, err
		}
		if keep {
			records = append(records, botHomeConfigurationRecord{kind: botHomeActivity, id: v.ID, revision: v.Sequence, data: v.Payload, activityKind: store.BotActivityKind(v.Kind), createdAt: v.CreatedAt})
		}
	}
	knowledge, err := d.client.BotKnowledgeExtraction.Query().Where(teamknowledge.BotIDEQ(id)).All(ctx)
	if err != nil {
		return nil, err
	}
	for _, v := range knowledge {
		records = append(records, botHomeConfigurationRecord{kind: botHomeKnowledge, id: v.ID, revision: v.Revision, data: v.Data})
	}
	tracked, err := d.client.BotTrackedItem.Query().Where(teamtracked.HasGoalWith(teamgoal.BotIDEQ(id))).All(ctx)
	if err != nil {
		return nil, err
	}
	for _, v := range tracked {
		records = append(records, botHomeConfigurationRecord{kind: botHomeTracked, id: v.ID, revision: v.Revision, data: v.Data})
	}
	events, err := d.client.SessionEvent.Query().Where(teamevent.HasSessionWith(teamsession.BotIDEQ(id))).All(ctx)
	if err != nil {
		return nil, err
	}
	for _, v := range events {
		records = append(records, botHomeConfigurationRecord{kind: botHomeSessionEvent, eventID: int64(v.ID), data: v.EventData, sessionID: v.SessionID})
	}
	return records, nil
}

func (d teamBotDB) selectHomeContentRecord(ctx context.Context, record botHomeConfigurationRecord) error {
	switch record.kind {
	case botHomeRun:
		v, err := d.client.BotRun.Get(ctx, record.id)
		if err != nil {
			return err
		}
		return d.client.BotRun.UpdateOneID(record.id).SetData(record.data).SetUpdatedAt(v.UpdatedAt).Exec(ctx)
	case botHomeTask:
		v, err := d.client.BotTaskRevision.Get(ctx, record.id)
		if err != nil {
			return err
		}
		if err = d.client.BotTaskRevision.DeleteOneID(record.id).Exec(ctx); err != nil {
			return err
		}
		return d.client.BotTaskRevision.Create().SetID(v.ID).SetBotID(v.BotID).SetTaskID(v.TaskID).SetRevision(v.Revision).SetDigest(v.Digest).SetSizeBytes(v.SizeBytes).SetParentRevision(v.ParentRevision).SetParentDigest(v.ParentDigest).SetPublicationState(v.PublicationState).SetAuthorityConstraintRef(v.AuthorityConstraintRef).SetWorkspaceReceiptRef(v.WorkspaceReceiptRef).SetData(record.data).SetCreatedAt(v.CreatedAt).Exec(ctx)
	case botHomeSkill:
		v, err := d.client.BotSkillRevision.Get(ctx, record.id)
		if err != nil {
			return err
		}
		if err = d.client.BotSkillRevision.DeleteOneID(record.id).Exec(ctx); err != nil {
			return err
		}
		return d.client.BotSkillRevision.Create().SetID(v.ID).SetBotID(v.BotID).SetSkillID(v.SkillID).SetRevision(v.Revision).SetDigest(v.Digest).SetSizeBytes(v.SizeBytes).SetData(record.data).SetCreatedAt(v.CreatedAt).Exec(ctx)
	case botHomeSkillRoot:
		return nil // Validated metadata-only index; no physical reference to replace.
	case botHomeApproval:
		return d.client.BotApproval.UpdateOneID(record.id).SetData(record.data).Exec(ctx)
	case botHomeActivity:
		return d.client.BotOutbox.UpdateOneID(record.id).SetPayload(record.data).Exec(ctx)
	case botHomeKnowledge:
		v, err := d.client.BotKnowledgeExtraction.Get(ctx, record.id)
		if err != nil {
			return err
		}
		return d.client.BotKnowledgeExtraction.UpdateOneID(record.id).SetData(record.data).SetUpdatedAt(v.UpdatedAt).Exec(ctx)
	case botHomeTracked:
		return d.client.BotTrackedItem.UpdateOneID(record.id).SetData(record.data).Exec(ctx)
	case botHomeSessionEvent:
		return d.client.SessionEvent.UpdateOneID(record.eventID).SetEventData(record.data).Exec(ctx)
	default:
		return store.ErrBotUnsupported
	}
}
