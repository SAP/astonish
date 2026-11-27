package api

import (
	"google.golang.org/adk/session"
	"testing"
)

// Historical Home context is not resumable private execution authority. In
// particular an old incomplete/malformed dispatch must not block a fresh turn.
func TestEmployeeWorkerRestoreExcludesPriorHomeRuns(t *testing.T) {
	old := privateEvidenceTrace(t, "old-settled", botEmployeeWorkerTrace{WorkerID: "old-worker", DispatchID: "old-dispatch", Phase: botEmployeeWorkerSettled})
	current := privateEvidenceEvents(t)
	view := &botConversationSession{prior: []*session.Event{old}, appended: current}
	service := &botEmployeeChatService{workers: make(map[string]*botEmployeeWorker)}
	if err := service.restore(view.Events()); err != nil {
		t.Fatalf("fresh run was blocked by prior Home dispatch: %v", err)
	}
	if len(service.workers) != 1 || service.workers["worker"] == nil {
		t.Fatalf("restored foreign workers: %#v", service.workers)
	}
	// The same invalid transition in the current run still fails closed.
	invalidCurrent := &botConversationSession{appended: []*session.Event{old}}
	if err := (&botEmployeeChatService{workers: make(map[string]*botEmployeeWorker)}).restore(invalidCurrent.Events()); err == nil {
		t.Fatal("current-run invalid dispatch was silently ignored")
	}
}
