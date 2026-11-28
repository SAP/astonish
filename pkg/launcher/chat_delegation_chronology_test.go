package launcher

import (
	"github.com/SAP/astonish/pkg/client"
	"testing"
)

func TestDelegationPendingDoesNotDuplicateTerminalLifecycle(t *testing.T) {
	if got := mapSSEToEvents(&client.SSEEvent{Type: "subtask_progress", Data: `{"event_type":"delegation_pending","batch_id":"a"}`}, false); len(got) != 0 {
		t.Fatalf("Studio call-position reservation created terminal lifecycle: %+v", got)
	}
	got := mapSSEToEvents(&client.SSEEvent{Type: "subtask_progress", Data: `{"event_type":"delegation_start","batch_id":"a","sequence":1,"tasks":[{"name":"worker","task_id":"t"}]}`}, false)
	if len(got) != 1 || got[0].DelegationType != "start" || len(got[0].DelegationTasks) != 1 {
		t.Fatalf("real start lost: %+v", got)
	}
}
