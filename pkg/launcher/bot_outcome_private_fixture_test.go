package launcher

import (
	"context"
	"github.com/SAP/astonish/pkg/api"
	"github.com/SAP/astonish/pkg/bot"
)

// outcomeWorkerFactory runs the outcome-focused harness through the genuine
// private WorkerFactory. It retains the public factory's scoped stores and
// authorization; no executable is installed in Employee's public tool view.
func outcomeWorkerFactory(factory api.BotChatAgentFactory) api.BotChatAgentFactory {
	return func(ctx context.Context, req bot.RunRequest) (*api.BotChatResources, error) {
		employee, err := factory(ctx, req)
		if err != nil {
			return employee, err
		}
		worker, err := employee.WorkerFactory(ctx, api.BotEmployeeWorkerRequest{Request: req, ConversationID: req.Run.SessionID.String(), Sessions: employee.SessionService})
		if err != nil {
			employee.Cleanup()
			return nil, err
		}
		cleanup := worker.Cleanup
		worker.Cleanup = func() { cleanup(); employee.Cleanup() }
		worker.Authorization = employee.Authorization
		worker.Transcript = employee.Transcript
		worker.Work = employee.Work
		worker.EvidenceReader = employee.EvidenceReader
		worker.ResearchFactory = employee.ResearchFactory
		worker.ArtifactBody = employee.ArtifactBody
		worker.ModelSelection = employee.ModelSelection
		worker.RecoverFileDelivery = employee.RecoverFileDelivery
		worker.RepairFileDelivery = employee.RepairFileDelivery
		return worker, nil
	}
}
