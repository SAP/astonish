package agent

import (
	"errors"
	"testing"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/google/uuid"
)

func TestEmployeeChatServiceReturnedEnvelope(t *testing.T) {
	worker := uuid.NewString()
	for _, status := range []bot.EmployeeChatStatus{bot.EmployeeChatCompleted, bot.EmployeeChatPartial, bot.EmployeeChatNeedsInput, bot.EmployeeChatFailed, bot.EmployeeChatCancelled} {
		t.Run(string(status), func(t *testing.T) {
			result := &bot.EmployeeChatResult{WorkerID: worker, Status: status, PrivateText: "untrusted external success"}
			for _, out := range []map[string]any{{"result": result}, {"worker_id": worker, "status": string(status)}} {
				if !employeeChatServiceReturned(out, nil) {
					t.Fatalf("definite service response rejected: %+v", out)
				}
				if employeeChatServiceReturned(out, errors.New("service failed")) {
					t.Fatal("errored response attested")
				}
			}
		})
	}
	for name, out := range map[string]map[string]any{
		"nil":                 nil,
		"nil pointer":         {"result": (*bot.EmployeeChatResult)(nil)},
		"untyped wrapper":     {"result": map[string]any{"worker_id": worker, "status": "completed"}},
		"value not pointer":   {"result": bot.EmployeeChatResult{WorkerID: worker, Status: bot.EmployeeChatCompleted}},
		"invalid worker":      {"result": &bot.EmployeeChatResult{WorkerID: "foreign", Status: bot.EmployeeChatCompleted}},
		"zero worker":         {"result": &bot.EmployeeChatResult{WorkerID: uuid.Nil.String(), Status: bot.EmployeeChatCompleted}},
		"invalid status":      {"result": &bot.EmployeeChatResult{WorkerID: worker, Status: "success"}},
		"shadowed projection": {"result": nil, "worker_id": worker, "status": "completed"},
	} {
		t.Run(name, func(t *testing.T) {
			if employeeChatServiceReturned(out, nil) {
				t.Fatal("invalid local service envelope attested")
			}
		})
	}
}
