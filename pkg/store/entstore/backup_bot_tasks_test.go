package entstore

import (
	"fmt"
	"github.com/SAP/astonish/pkg/backup"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"reflect"
	"testing"
)

func TestBotTaskRestoreValidation(t *testing.T) {
	q := taskTestCandidate(uuid.New())
	p := store.BotTaskPublication{ID: uuid.New(), BotID: q.Task.BotID, TaskID: q.Task.ID, Candidate: q.Candidate, State: store.BotTaskPublicationPrepared, AuthorityConstraintRef: "original-owner"}
	m := store.BotTaskMutation{BotID: p.BotID, AuthorityConstraintRef: p.AuthorityConstraintRef}
	doc, err := botJSONMap(taskPublicationEnvelope{Publication: p, Mutation: m, Task: q.Task})
	if err != nil {
		t.Fatal(err)
	}
	row := map[string]any{"id": p.ID.String(), "bot_id": p.BotID.String(), "task_id": p.TaskID.String(), "revision": p.Candidate.Ref.Revision, "digest": p.Candidate.Ref.Digest, "authority_constraint_ref": p.AuthorityConstraintRef, "publication_state": string(p.State), "workspace_receipt_ref": "", "data": doc}
	if err = validateRestoredBotTaskRow("bot_task_revisions", row); err != nil {
		t.Fatal(err)
	}
	original := fmt.Sprint(row["data"])
	if err = quiesceRestoredBotRow("bot_task_revisions", row); err != nil {
		t.Fatal(err)
	}
	if row["publication_state"] != string(store.BotTaskPublicationHeld) || fmt.Sprint(row["data"]) != original {
		t.Fatal("restore changed archival bytes or reacquired authority")
	}
	for _, field := range []string{"digest", "task_id", "bot_id", "authority_constraint_ref", "publication_state"} {
		t.Run(field, func(t *testing.T) {
			bad := map[string]any{}
			for k, v := range row {
				bad[k] = v
			}
			bad[field] = "malformed"
			if validateRestoredBotTaskRow("bot_task_revisions", bad) == nil {
				t.Fatal("malformed accepted")
			}
		})
	}
	if restoreTablePriority("bot_tasks") >= restoreTablePriority("bot_task_revisions") {
		t.Fatal("revision restore precedes registry")
	}
}

func TestBotTaskRestoreMappingKeepsHistoricalBytes(t *testing.T) {
	source := backup.Scope{Kind: "personal", OrgSlug: "source", UserID: uuid.NewString()}
	target := backup.Scope{Kind: "personal", OrgSlug: "target", UserID: uuid.NewString()}
	row := map[string]any{"id": uuid.NewString(), "data": map[string]any{"assignment": "source", "owner": source.UserID, "receipt": "source"}, "bot_id": uuid.NewString()}
	options := PlatformRestoreOptions{MapOrg: map[string]string{"source": "target"}, MapUser: map[string]string{"source/" + source.UserID: "target/" + target.UserID}}
	for _, table := range []string{"bot_tasks", "bot_task_revisions"} {
		if !reflect.DeepEqual(remapRestoreRow(source, target, table, row, options), row) {
			t.Fatalf("%s history rewritten", table)
		}
	}
}
