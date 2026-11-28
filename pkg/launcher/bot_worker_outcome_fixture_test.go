package launcher

import (
	"context"
	"iter"
	"testing"

	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"google.golang.org/adk/model"
	"google.golang.org/adk/session"
	"google.golang.org/genai"
	"strings"
)

// workerOutcomeFixtureStore keeps production private dispatch/event persistence
// on the original public run; it does not give Employee execution tools.
type workerWorkOutcomeFixtureStore struct {
	workerOutcomeFixtureStore
	store.BotWorkStore
}

type workerOutcomeFixtureStore struct {
	store.BotStore
	fixture *botFactoryFixture
}

func (s workerOutcomeFixtureStore) AppendBotRunEvent(ctx context.Context, lease store.BotLease, id uuid.UUID, event *session.Event) error {
	f := s.fixture
	if lease != f.req.Run.Lease || id != f.req.Run.SessionID {
		return store.ErrBotForbidden
	}
	current, err := f.sessions.Get(ctx, &session.GetRequest{AppName: "astonish", UserID: f.req.Bot.OwnerID.String(), SessionID: id.String()})
	if err != nil {
		return err
	}
	return f.sessions.AppendEvent(ctx, current.Session, event)
}

// workerOutcomeFixtureModel orchestrates once in Employee and leaves all actual
// execution and outcome receipts to the existing private worker model.
type workerOutcomeFixtureModel struct {
	worker        model.LLM
	employeeCheck func(*model.LLMRequest)
	answer        string
	message       string
	dispatched    bool
}

func (m *workerOutcomeFixtureModel) Name() string { return m.worker.Name() }
func (m *workerOutcomeFixtureModel) GenerateContent(ctx context.Context, req *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	if req.Tools["employee_chat"] == nil {
		return m.worker.GenerateContent(ctx, req, stream)
	}
	if m.employeeCheck != nil {
		m.employeeCheck(req)
	}
	return func(yield func(*model.LLMResponse, error) bool) {
		answer, message := m.answer, m.message
		if answer == "" {
			answer = "Observed file operation."
		}
		if message == "" {
			message = "Perform the requested operation once and report the independently observed outcome."
		}
		content := genai.NewContentFromText(answer, "model")
		if !m.dispatched {
			m.dispatched = true
			content = &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{ID: "employee-outcome-dispatch", Name: "employee_chat", Args: map[string]any{"message": message}}}}}
		}
		yield(&model.LLMResponse{Content: content, TurnComplete: true, UsageMetadata: &genai.GenerateContentResponseUsageMetadata{}}, nil)
	}
}
func installWorkerOutcomeFixture(t *testing.T, f *botFactoryFixture, worker model.LLM) model.LLM {
	t.Helper()
	repo := workerOutcomeFixtureStore{BotStore: f.services.PersonalBots, fixture: f}
	f.services.PersonalBots, f.services.Bots = repo, repo
	return &workerOutcomeFixtureModel{worker: worker}
}

// requireEmployeeDispatchObservation checks the additional orchestration call
// separately, so file/source outcome assertions retain their original precision.
func requireEmployeeDispatchObservation(t *testing.T, observations []store.BotEvidence) []store.BotEvidence {
	t.Helper()
	var operations []store.BotEvidence
	dispatches := 0
	for _, observation := range observations {
		if strings.HasPrefix(observation.Source, "bot-employee-chat://") {
			dispatches++
			if observation.Kind != "observation" || observation.ObservedAt.IsZero() || observation.ActorID == uuid.Nil {
				t.Fatalf("invalid Employee dispatch observation: %+v", observation)
			}
		} else {
			operations = append(operations, observation)
		}
	}
	if dispatches != 1 {
		t.Fatalf("Employee dispatch observations=%d want=1", dispatches)
	}
	return operations
}
