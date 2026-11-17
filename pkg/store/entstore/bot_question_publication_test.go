package entstore

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

type questionPublicationDB struct {
	botDraftDB
	approvals map[uuid.UUID]*store.BotApproval
}

func (d questionPublicationDB) ReadApproval(_ context.Context, _ uuid.UUID, id uuid.UUID) (*store.BotApproval, error) {
	if a := d.approvals[id]; a != nil {
		return a, nil
	}
	return nil, store.ErrBotNotFound
}

func TestQuestionOnlyWaitingPublication(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*store.BotRun, *store.BotApproval)
		want   bool
	}{
		{"question", func(*store.BotRun, *store.BotApproval) {}, true},
		{"answered replay", func(_ *store.BotRun, a *store.BotApproval) { a.Status = store.ApprovalApproved }, true},
		{"action approval", func(_ *store.BotRun, a *store.BotApproval) { a.Action.Tool = "write_file"; a.Interaction = nil }, false},
		{"foreign run", func(_ *store.BotRun, a *store.BotApproval) { a.RunID = uuid.New() }, false},
		{"missing approval", func(r *store.BotRun, _ *store.BotApproval) {
			r.Report.NeedsApproval = append(r.Report.NeedsApproval, uuid.New())
		}, false},
		{"mixed approval", func(r *store.BotRun, _ *store.BotApproval) {
			r.Report.NeedsApproval = append(r.Report.NeedsApproval, uuid.Nil)
		}, false},
		{"genuine limitation", func(r *store.BotRun, _ *store.BotApproval) {
			r.Report.Unverified = []string{"Pending external outcome"}
		}, false},
		{"failure", func(r *store.BotRun, _ *store.BotApproval) { r.Report.Failure = &store.BotReportFailure{} }, false},
		{"success", func(r *store.BotRun, _ *store.BotApproval) { r.Status = store.RunSucceeded }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id := uuid.New()
			run := store.BotRun{ID: uuid.New(), BotID: uuid.New(), Status: store.RunWaitingApproval, Report: store.BotReport{NeedsApproval: []uuid.UUID{id}, EmployeeMessage: &store.BotEmployeeMessage{Origin: store.BotEmployeeMessageFallback}}}
			a := &store.BotApproval{ID: id, RunID: run.ID, Interaction: &store.BotInteraction{Kind: store.BotInteractionQuestion, QuestionID: "q", Payload: json.RawMessage(`{"questionId":"q","kind":"text","prompt":"Which dates?"}`)}}
			a.Action.Tool = "ask_user"
			action := &store.BotApproval{RunID: run.ID}
			action.Action.Tool = "write_file"
			tc.change(&run, a)
			got, err := questionOnlyWaitingPublication(context.Background(), questionPublicationDB{approvals: map[uuid.UUID]*store.BotApproval{id: a, uuid.Nil: action}}, run)
			if err != nil || got != tc.want {
				t.Fatalf("got %v %v want %v", got, err, tc.want)
			}
		})
	}
}
