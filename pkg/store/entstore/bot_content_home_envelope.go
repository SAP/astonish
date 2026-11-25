package entstore

import (
	"context"
	"encoding/json"
	"github.com/SAP/astonish/pkg/store"
)

// homeRecordEnvelope is an explicit allowlist of SQL envelope types, not a
// recursive JSON search. Only the task publication's named candidate nests a
// second SQL envelope; run contents are immutable bytes, not SQL references.
func homeRecordEnvelope(row botHomeConfigurationRecord) (any, *store.BotContentRef, func() error, error) {
	var record any
	var ref *store.BotContentRef
	finish := func() error { return nil }
	switch row.kind {
	case botHomeVersion:
		v := new(botVersionRecord)
		record, ref = v, &v.Content
	case botHomeGoal:
		v := new(botGoalRecord)
		record, ref = v, &v.Content
	case botHomeRun:
		v := new(botRunRecord)
		record = v
	case botHomeSkill:
		v := new(botSkillRevisionRecord)
		record, ref = v, &v.Content
	case botHomeApproval:
		v := new(botApprovalRecord)
		record = v
	case botHomeActivity:
		v := new(botActivityRecord)
		record = v
	case botHomeKnowledge:
		v := new(botKnowledgeRecord)
		record, ref = v, &v.Content
	case botHomeSessionEvent:
		v := new(botSessionEventRecord)
		record = v
	case botHomeTracked:
		v := new(botTrackedRecord)
		record, ref = v, &v.Content
	case botHomeSkillRoot:
		record = new(botSkillRootRecord) // Metadata only, including replay heads.
	case botHomeTask:
		v := new(botTaskPublicationRecord)
		if err := decodeBotRecord(row.data, v); err != nil {
			return nil, nil, nil, err
		}
		candidate := new(botTaskRevisionRecord)
		if err := decodeBotRecord(v.Candidate, candidate); err != nil {
			return nil, nil, nil, err
		}
		finish = func() error {
			raw, err := json.Marshal(candidate)
			if err == nil {
				v.Candidate = map[string]any{"bot_record_v2": string(raw)}
			}
			return err
		}
		return v, &candidate.Content, finish, nil
	default:
		return nil, nil, nil, store.ErrBotUnsupported
	}
	if err := decodeBotRecord(row.data, record); err != nil {
		return nil, nil, nil, err
	}
	return record, ref, finish, nil
}

// Validate through the same logical codecs used by readers on both sides of
// selection. Physical row identity is checked independently of content digests.
func validateHomeRecord(ctx context.Context, c *botContentCodec, id store.BotID, row botHomeConfigurationRecord) error {
	switch row.kind {
	case botHomeVersion:
		_, err := c.decodeVersion(ctx, id, row.revision, row.data)
		return err
	case botHomeGoal:
		v, err := c.decodeGoal(ctx, id, row.data)
		if err != nil {
			return err
		}
		if v.ID != row.id || v.Revision != row.revision {
			return store.ErrBotConflict
		}
	case botHomeRun:
		v, err := c.decodeRun(ctx, id, row.data)
		if err != nil {
			return err
		}
		if v.Run.ID != row.id || v.Run.SessionID.String() != row.sessionID {
			return store.ErrBotConflict
		}
	case botHomeTask:
		v, err := decodeTaskPublication(ctx, c, id, row.data)
		if err != nil {
			return err
		}
		if v.Publication.ID != row.id || v.Publication.Candidate.Ref.Revision != row.revision {
			return store.ErrBotConflict
		}
	case botHomeSkill:
		v, err := c.decodeSkillRevision(ctx, id, row.data)
		if err != nil {
			return err
		}
		if v.ID != row.id || v.Revision != row.revision {
			return store.ErrBotConflict
		}
	case botHomeApproval:
		v, err := c.decodeApproval(ctx, id, row.data)
		if err != nil {
			return err
		}
		if v.ID != row.id {
			return store.ErrBotConflict
		}
	case botHomeActivity:
		_, err := c.decodeActivity(ctx, id, row.revision, row.activityKind, row.createdAt, row.data)
		return err
	case botHomeKnowledge:
		_, err := c.decodeKnowledge(ctx, id, row.id, row.data)
		return err
	case botHomeSessionEvent:
		_, err := c.decodeSessionEvent(ctx, id, row.sessionID, row.data)
		return err
	case botHomeTracked:
		v, err := c.decodeTracked(ctx, id, row.revision, row.data)
		if err != nil {
			return err
		}
		if v.ID != row.id {
			return store.ErrBotConflict
		}
	case botHomeSkillRoot:
		v, err := decodeSkillRoot(row.data)
		if err != nil {
			return err
		}
		if v.Skill.ID != row.id || v.Skill.BotID != id || v.Skill.Revision != row.revision {
			return store.ErrBotConflict
		}
	default:
		return store.ErrBotUnsupported
	}
	return nil
}
