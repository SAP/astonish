package api_test

import (
	"context"
	"encoding/json"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
	"iter"
	"testing"

	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/session"
)

// Read only runtime-owned private envelopes, binding each response to its
// started dispatch and original call. Home/public model prose is not evidence.
func progressPrivateResponses(t *testing.T, f *interactiveHTTPFixture) []*session.Event {
	t.Helper()
	runs, err := f.repo.ListRuns(f.ctx, f.b.ID, store.BotPage{})
	if err != nil || len(runs) != 1 {
		t.Fatalf("private run identity: %v %+v", err, runs)
	}
	org, err := f.db.ForOrg("interactive")
	if err != nil {
		t.Fatal(err)
	}
	ss := org.ForUser(f.b.OwnerID.String()).Sessions()
	if f.scope == store.BotScopeTeam {
		ss = org.ForTeam("team").Sessions()
	}
	return progressValidatedPrivateResponses(t, ss, f.ctx, f.b.OwnerID.String(), runs[0].SessionID.String())
}

func progressValidatedPrivateResponses(t *testing.T, ss interface {
	ReadTranscriptEvents(context.Context, string, string, string) ([]*session.Event, error)
}, ctx context.Context, owner, run string) []*session.Event {
	t.Helper()
	events, err := ss.ReadTranscriptEvents(ctx, "astonish", owner, run)
	if err != nil {
		t.Fatal(err)
	}
	active := map[string]string{}
	calls := map[string]string{}
	var responses []*session.Event
	for _, outer := range events {
		if outer.Author != "bot_runtime" || outer.Partial {
			continue
		}
		value := outer.Actions.StateDelta["bot_private_worker_v1"]
		if value == nil {
			continue
		}
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		var trace struct {
			WorkerID   string         `json:"worker_id"`
			DispatchID string         `json:"dispatch_id"`
			Phase      string         `json:"phase"`
			Event      *session.Event `json:"event"`
		}
		if err := json.Unmarshal(raw, &trace); err != nil {
			t.Fatal(err)
		}
		switch trace.Phase {
		case "started", "resuming":
			active[trace.WorkerID] = trace.DispatchID
		case "settled":
			delete(active, trace.WorkerID)
		case "event":
			if trace.DispatchID == "" || active[trace.WorkerID] != trace.DispatchID || trace.Event == nil || trace.Event.Partial || trace.Event.Content == nil {
				continue
			}
			for _, p := range trace.Event.Content.Parts {
				if c := p.FunctionCall; c != nil {
					calls[trace.DispatchID+":"+c.ID] = c.Name
				}
				if r := p.FunctionResponse; r != nil && calls[trace.DispatchID+":"+r.ID] == r.Name {
					responses = append(responses, trace.Event)
				}
			}
		}
	}
	return responses
}

// Controlled fixtures report their actual fixed usage on every provider turn;
// omitted metadata would correctly retain the large catalog reservation.
func progressMeteredModel(llm model.LLM) model.LLM { return &progressFixtureModel{LLM: llm} }

type progressFixtureModel struct{ model.LLM }

func (m *progressFixtureModel) GenerateContent(ctx context.Context, req *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		for response, err := range m.LLM.GenerateContent(ctx, req, stream) {
			if response != nil && response.UsageMetadata == nil {
				copy := *response
				copy.UsageMetadata = &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 5, CandidatesTokenCount: 3, TotalTokenCount: 8}
				response = &copy
			}
			if !yield(response, err) {
				return
			}
		}
	}
}
