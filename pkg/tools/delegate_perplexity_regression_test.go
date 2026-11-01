package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/common"
	"github.com/SAP/astonish/pkg/config"
	"google.golang.org/adk/model"
	adksession "google.golang.org/adk/session"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
	"google.golang.org/genai"
)

type perplexityDiscoveryLLM struct {
	step         int
	declarations string
}

func (*perplexityDiscoveryLLM) Name() string { return "perplexity-discovery-regression" }
func (m *perplexityDiscoveryLLM) GenerateContent(_ context.Context, req *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		declarations, _ := json.Marshal(req.Config.Tools)
		if m.step == 0 {
			m.declarations = string(declarations)
			if strings.Contains(m.declarations, `"name":"perplexity_web_search"`) {
				yield(nil, fmt.Errorf("Perplexity was selected initially"))
				return
			}
		} else if string(declarations) != m.declarations {
			yield(nil, fmt.Errorf("discovery expanded declarations"))
			return
		}
		if m.step > 0 {
			var response *genai.FunctionResponse
			for _, content := range req.Contents {
				for _, part := range content.Parts {
					if part.FunctionResponse != nil {
						response = part.FunctionResponse
					}
				}
			}
			if response == nil {
				yield(nil, fmt.Errorf("missing response"))
				return
			}
			data, _ := json.Marshal(response.Response)
			switch m.step {
			case 1, 3:
				var result SearchToolsResult
				if err := json.Unmarshal(data, &result); err != nil || len(result.Matches) == 0 || result.Matches[0].ToolName != "perplexity_web_search" {
					yield(nil, fmt.Errorf("Perplexity not first: %s", data))
					return
				}
			case 2:
				var result SearchToolsResult
				if err := json.Unmarshal(data, &result); err != nil || result.Count != 1 || len(result.Matches) != 1 {
					yield(nil, fmt.Errorf("wildcard ignored limit: %s", data))
					return
				}
			case 4:
				if !strings.Contains(string(data), "perplexity_web_search") || !strings.Contains(string(data), "query") {
					yield(nil, fmt.Errorf("schema missing: %s", data))
					return
				}
			case 5:
				if !strings.Contains(string(data), "Paris") || !strings.Contains(string(data), "What is the capital of France?") {
					yield(nil, fmt.Errorf("search not executed: %s", data))
					return
				}
			}
		}
		var part *genai.Part
		switch m.step {
		case 0:
			part = &genai.Part{FunctionCall: &genai.FunctionCall{Name: "search_tools", Args: map[string]any{"query": "perplexity_web_search", "max_results": 5}}}
		case 1:
			part = &genai.Part{FunctionCall: &genai.FunctionCall{Name: "search_tools", Args: map[string]any{"query": "*", "max_results": 1}}}
		case 2:
			part = &genai.Part{FunctionCall: &genai.FunctionCall{Name: "search_tools", Args: map[string]any{"query": "Perplexity web search factual lookup", "max_results": 3}}}
		case 3:
			part = &genai.Part{FunctionCall: &genai.FunctionCall{Name: "describe_tools", Args: map[string]any{"names": []string{"perplexity_web_search"}}}}
		case 4:
			part = &genai.Part{FunctionCall: &genai.FunctionCall{Name: "execute_tool", Args: map[string]any{"name": "perplexity_web_search", "arguments": map[string]any{"query": "What is the capital of France?"}}}}
		default:
			part = &genai.Part{Text: "PERPLEXITY_EXECUTED: Paris"}
		}
		m.step++
		yield(&model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{part}}}, nil)
	}
}

type perplexitySearchAnswerLLM struct{ calls *atomic.Int32 }

func (*perplexitySearchAnswerLLM) Name() string { return "sonar-test" }
func (m *perplexitySearchAnswerLLM) GenerateContent(_ context.Context, req *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		data, _ := json.Marshal(req.Contents)
		if !strings.Contains(string(data), "What is the capital of France?") {
			yield(nil, fmt.Errorf("wrong search query: %s", data))
			return
		}
		m.calls.Add(1)
		yield(&model.LLMResponse{Content: genai.NewContentFromText("Paris. Sources: https://www.paris.fr/", genai.RoleModel)}, nil)
	}
}

func TestDelegatePerplexityDiscoveryExecution(t *testing.T) {
	var calls atomic.Int32
	cfg := &config.AppConfig{PerplexityWebSearch: config.PerplexityWebSearchConfig{Provider: "test", Model: "sonar-test"}}
	search, err := NewPerplexityWebSearchTool(cfg, func(context.Context, string, string, *config.AppConfig) (model.LLM, error) {
		return &perplexitySearchAnswerLLM{calls: &calls}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	manager := agent.NewSubAgentManager(agent.SubAgentConfig{})
	llm := &perplexityDiscoveryLLM{}
	manager.LLM = llm
	manager.AppName, manager.UserID = "perplexity-discovery", "test-user"
	manager.SessionService = common.NewAutoInitService(adksession.InMemoryService())
	manager.SearchToolsFactory = NewScopedSearchToolsTool
	manager.WebSearchToolName = "perplexity_web_search"
	manager.ToolGroups = map[string]*agent.ToolGroup{}
	for _, group := range []string{"core", "browser", "web"} {
		manager.ToolGroups[group] = &agent.ToolGroup{Name: group}
	}
	for i := 0; i < 120; i++ {
		distractor, err := functiontool.New(functiontool.Config{Name: fmt.Sprintf("browser_search_%03d", i), Description: "Wait for browser page state or search the DOM."}, func(tool.Context, struct{}) (map[string]any, error) {
			return nil, fmt.Errorf("must not execute distractor")
		})
		if err != nil {
			t.Fatal(err)
		}
		manager.ToolGroups["browser"].Tools = append(manager.ToolGroups["browser"].Tools, distractor)
	}
	seed, err := functiontool.New(functiontool.Config{Name: "read_file"}, func(tool.Context, struct{}) (map[string]any, error) { return nil, fmt.Errorf("must not execute seed") })
	if err != nil {
		t.Fatal(err)
	}
	manager.ToolGroups["core"].Tools = []tool.Tool{seed}
	// Exercise the real request-local selected provider, not a global index tool.
	ctx := agent.WithPromptOverrides(context.Background(), &agent.PromptOverrides{WebSearchToolName: "perplexity_web_search", AdditionalTools: []tool.Tool{search}})
	bound, err := NewDelegateTasksToolForManager(manager)
	if err != nil {
		t.Fatal(err)
	}
	out, err := bound.(interface {
		Run(tool.Context, any) (map[string]any, error)
	}).Run(&toolContextStub{Context: ctx, sessionID: "parent"}, map[string]any{"tasks": []any{map[string]any{"name": "research", "task": "Find the capital of France using Perplexity", "tools": []string{"core"}}}})
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(out)
	if calls.Load() != 1 || llm.step != 6 || !strings.Contains(string(data), "PERPLEXITY_EXECUTED: Paris") {
		t.Fatalf("delegation failed: calls=%d step=%d output=%s", calls.Load(), llm.step, data)
	}
}
