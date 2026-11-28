package entstore

import (
	"fmt"
	"strconv"

	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

// Archive documents retain their original identities. Restore does not issue a
// new grant, publication receipt, or editable copy of a committed definition.
func validateRestoredBotTaskRow(table string, row map[string]any) error {
	if table != "bot_tasks" && table != "bot_task_revisions" {
		return nil
	}
	if table == "bot_tasks" {
		id, bot := fmt.Sprint(row["id"]), fmt.Sprint(row["bot_id"])
		taskID, taskErr := uuid.Parse(id)
		botID, botErr := uuid.Parse(bot)
		revision, revisionErr := strconv.ParseInt(fmt.Sprint(row["revision"]), 10, 64)
		head, headErr := strconv.ParseInt(fmt.Sprint(row["head_revision"]), 10, 64)
		digest := fmt.Sprint(row["head_digest"])
		if taskErr != nil || botErr != nil || taskID == uuid.Nil || botID == uuid.Nil || revisionErr != nil || revision < 1 || headErr != nil || head < 0 {
			return store.ErrBotInvalid
		}
		if head > 0 {
			if (&store.BotTaskRef{TaskID: taskID, Revision: head, Digest: digest}).Validate() != nil {
				return store.ErrBotInvalid
			}
		} else if digest != "" {
			return store.ErrBotInvalid
		}
		if id == "" || bot == "" || fmt.Sprint(row["key"]) != id || fmt.Sprint(row["home_path"]) != "tasks/"+id {
			return store.ErrBotInvalid
		}
		status := store.BotTaskStatus(fmt.Sprint(row["status"]))
		if status != store.BotTaskDraft && status != store.BotTaskReady && status != store.BotTaskArchived {
			return store.ErrBotInvalid
		}
		return nil
	}
	doc, err := restoredBotDocument(row["data"])
	if err != nil {
		return err
	}
	var v taskPublicationEnvelope
	if err = botDecode(doc, &v); err != nil {
		return err
	}
	p := v.Publication
	if p.ID.String() != fmt.Sprint(row["id"]) || p.BotID.String() != fmt.Sprint(row["bot_id"]) || p.TaskID.String() != fmt.Sprint(row["task_id"]) || p.Candidate.Ref.TaskID != p.TaskID || p.Candidate.BotID != p.BotID || p.Candidate.Ref.Digest != fmt.Sprint(row["digest"]) || fmt.Sprint(p.Candidate.Ref.Revision) != fmt.Sprint(row["revision"]) || p.AuthorityConstraintRef != fmt.Sprint(row["authority_constraint_ref"]) || v.Mutation.BotID != p.BotID || v.Task.ID != p.TaskID || v.Task.BotID != p.BotID {
		return store.ErrBotInvalid
	}
	if err = p.Candidate.Validate(v.Task.Status); err != nil {
		return err
	}
	state := store.BotTaskPublicationState(fmt.Sprint(row["publication_state"]))
	switch state {
	case store.BotTaskPublicationPrepared, store.BotTaskPublicationFilesReady, store.BotTaskPublicationCommitted, store.BotTaskPublicationHeld, store.BotTaskPublicationAborted:
	default:
		return store.ErrBotInvalid
	}
	if state == store.BotTaskPublicationCommitted && fmt.Sprint(row["workspace_receipt_ref"]) != p.ID.String() {
		return store.ErrBotInvalid
	}
	return nil
}
