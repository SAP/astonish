package api_test

import (
	"context"
	"encoding/json"
	"iter"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/api"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"

	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

type interactiveFixtureGuard struct {
	repo store.BotStore
	req  bot.RunRequest
}

func (g interactiveFixtureGuard) Authorize(ctx context.Context, a execution.ActionDescriptor) (execution.PolicyDecision, error) {
	if !g.req.Run.IsInteractiveChat() {
		return (journeyGuard{repo: g.repo, req: g.req}).Authorize(ctx, a)
	}
	if err := g.req.Budget.Check(ctx); err != nil {
		return execution.PolicyDecision{}, err
	}
	if g.req.Authorization == nil {
		return execution.PolicyDecision{}, store.ErrBotForbidden
	}
	if err := g.req.Authorization.AuthorizeTool(ctx, a.Tool); err != nil {
		return execution.PolicyDecision{}, err
	}
	b, err := g.repo.Get(ctx, g.req.Bot.ID)
	if err != nil {
		return execution.PolicyDecision{}, err
	}
	r, err := g.repo.GetRun(ctx, g.req.Bot.ID, g.req.Run.ID)
	if err != nil {
		return execution.PolicyDecision{}, err
	}
	if b.Version != g.req.Version.Version || b.Status == store.BotDeleted || r.Status != store.RunRunning || r.Lease.Fence != g.req.Run.Lease.Fence || time.Now().After(g.req.Run.Lease.ExpiresAt) {
		return execution.PolicyDecision{}, store.ErrBotConflict
	}
	digest, err := a.Digest()
	return execution.PolicyDecision{Kind: execution.PolicyAllow, ActionDigest: digest}, err
}

// employeeMediatedHTTPModel adapts old action scripts to the Employee contract.
// It delegates the requested action to the real private Chat service; it never
// manufactures a tool response or relaxes production authority or accounting.
type employeeMediatedHTTPModel struct {
	model.LLM
	mu         sync.Mutex
	pending    *model.LLMResponse
	dispatched bool
}

func TestEmployeeMediatedHTTPModelRoutesRealAction(t *testing.T) {
	inner := api.NewMockLLM(api.ToolCallTurn("read_file", map[string]any{"path": "receipt.txt"}), api.TextTurn("Observed"))
	adapted := mediateHTTPModel(inner)
	request := func(names ...string) *model.LLMRequest {
		r := &model.LLMRequest{Config: &genai.GenerateContentConfig{}}
		for _, name := range names {
			r.Config.Tools = append(r.Config.Tools, &genai.Tool{FunctionDeclarations: []*genai.FunctionDeclaration{{Name: name}}})
		}
		return r
	}
	var first *model.LLMResponse
	for r, err := range adapted.GenerateContent(t.Context(), request("employee_chat"), false) {
		if err != nil {
			t.Fatal(err)
		}
		first = r
	}
	if first.Content.Parts[0].FunctionCall.Name != "employee_chat" || first.Content.Parts[0].FunctionResponse != nil {
		t.Fatal("did not dispatch actual Employee service")
	}
	worker := request("execute_tool")
	var dispatched *model.LLMResponse
	for r, err := range adapted.GenerateContent(t.Context(), worker, true) {
		if err != nil {
			t.Fatal(err)
		}
		dispatched = r
	}
	if c := dispatched.Content.Parts[0].FunctionCall; c.Name != "execute_tool" || c.Args["name"] != "read_file" {
		t.Fatalf("lost private source action: %+v", c)
	}
	worker.Contents = []*genai.Content{{Role: "tool", Parts: []*genai.Part{{FunctionResponse: &genai.FunctionResponse{Name: "execute_tool", Response: map[string]any{"error": "denied-source"}}}}}}
	for r, err := range adapted.GenerateContent(t.Context(), worker, true) {
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(r.Content.Parts[0].Text, "denied-source") {
			t.Fatal("private result invented success")
		}
	}
}

func TestEmployeeMediatedHTTPModelPreservesMalformedBatch(t *testing.T) {
	inner := api.NewMockLLM(api.ToolCallTurn("read_file", map[string]any{"path": "receipt.txt"}))
	adapted := mediateHTTPModel(inner)
	request := &model.LLMRequest{Config: &genai.GenerateContentConfig{Tools: []*genai.Tool{{FunctionDeclarations: []*genai.FunctionDeclaration{{Name: "execute_tool"}}}}}}
	for response, err := range adapted.GenerateContent(t.Context(), request, false) {
		if err != nil {
			t.Fatal(err)
		}
		if response.Content.Parts[0].FunctionCall.Name != "execute_tool" || response.Content.Parts[0].FunctionCall.Args["name"] != "read_file" {
			t.Fatal("private bridge bypassed")
		}
	}
	response := &model.LLMResponse{Content: &genai.Content{Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{Name: "read_file"}}, {FunctionCall: &genai.FunctionCall{Name: "send_email"}}}}}
	if len(scriptedHTTPProgressiveResponse(request, response).Content.Parts) != 2 {
		t.Fatal("malformed batch collapsed")
	}
}

func mediateHTTPModel(llm model.LLM) model.LLM { return &employeeMediatedHTTPModel{LLM: llm} }

// Read actual response payloads, including the private Chat evidence string,
// without searching escaped JSON of the enclosing conversation.
func scriptedHTTPResponseEvidence(req *model.LLMRequest) string {
	var evidence []string
	for _, c := range req.Contents {
		for _, p := range c.Parts {
			if p.FunctionResponse == nil {
				continue
			}
			raw, _ := json.Marshal(p.FunctionResponse.Response)
			evidence = append(evidence, string(raw))
			if text, ok := p.FunctionResponse.Response["private_text"].(string); ok {
				evidence = append(evidence, text)
			}
		}
	}
	return strings.Join(evidence, "\n")
}

func TestScriptedHTTPResponseEvidenceUsesActualPrivateResponse(t *testing.T) {
	req := &model.LLMRequest{Contents: []*genai.Content{{Parts: []*genai.Part{
		{Text: `{"staged":false}`},
		{FunctionResponse: &genai.FunctionResponse{Response: map[string]any{"private_text": `Private source responses: [{"response":{"result":{"staged":true}}}]`}}},
	}}}}
	got := scriptedHTTPResponseEvidence(req)
	if !strings.Contains(got, `"staged":true`) || strings.Contains(got, `"staged":false`) {
		t.Fatalf("response evidence lost or user text treated as receipt: %s", got)
	}
}

func (m *employeeMediatedHTTPModel) GenerateContent(ctx context.Context, req *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		declared := map[string]bool{}
		if req != nil && req.Config != nil {
			for _, ts := range req.Config.Tools {
				for _, d := range ts.FunctionDeclarations {
					declared[d.Name] = true
				}
			}
		} // Only consume the queued action in private Chat, not tool-free assessment.
		if !declared["employee_chat"] && len(declared) > 0 {
			m.mu.Lock()
			pending, dispatched := m.pending, m.dispatched
			if pending != nil && !dispatched {
				m.dispatched = true
			}
			m.mu.Unlock()
			if pending != nil {
				if !dispatched {
					yield(scriptedHTTPProgressiveResponse(req, pending), nil)
					return
				}
				// Return the actual source response as private evidence, not invented success.
				var evidence []any
				for _, c := range req.Contents {
					for _, p := range c.Parts {
						if p.FunctionResponse != nil {
							evidence = append(evidence, p.FunctionResponse)
						}
					}
				}
				raw, _ := json.Marshal(evidence)
				yield(&model.LLMResponse{Content: genai.NewContentFromText("Private source responses: "+string(raw), "model"), TurnComplete: true, UsageMetadata: &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 5, CandidatesTokenCount: 3, TotalTokenCount: 8}}, nil)
				return
			}
		}
		for response, err := range m.LLM.GenerateContent(ctx, req, stream) {
			if declared["employee_chat"] && response != nil && response.Content != nil && len(response.Content.Parts) == 1 {
				for _, p := range response.Content.Parts {
					if p.FunctionCall == nil || declared[p.FunctionCall.Name] {
						continue
					}
					m.mu.Lock()
					m.pending = response
					m.dispatched = false
					m.mu.Unlock()
					raw, _ := json.Marshal(p.FunctionCall)
					clone := *response
					clone.Content = &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{ID: p.FunctionCall.ID, Name: "employee_chat", Args: map[string]any{"message": "Execute this requested scoped action and return its actual evidence: " + string(raw)}}}}}
					response = &clone
					break
				}
			}
			if !yield(scriptedHTTPProgressiveResponse(req, response), err) {
				return
			}
		}
	}
}
