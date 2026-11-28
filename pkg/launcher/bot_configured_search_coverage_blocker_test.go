package launcher

import (
	"context"
	"encoding/json"
	"iter"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/config"
	"google.golang.org/adk/model"
)

// This regression probe exercises the real configured executable, not a prompt
// claiming that employee guidance can be sent. It intentionally records the
// current disclosure boundary: coverage text has no owner-backed admission path.
type researchCoverageProbeModel struct {
	knowledgeModel
	requests []string
}

func (m *researchCoverageProbeModel) GenerateContent(ctx context.Context, req *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	raw, _ := json.Marshal(req.Contents)
	m.requests = append(m.requests, string(raw))
	return m.knowledgeModel.GenerateContent(ctx, req, stream)
}

func TestBotConfiguredSearchCoverageGuidanceAdmissionBlocker(t *testing.T) {
	f, runtime, episode := researchFixture(t)
	search := &researchCoverageProbeModel{knowledgeModel: knowledgeModel{text: "Sources: https://example.com/decisive"}}
	factory := func(context.Context, string, string, *config.AppConfig) (model.LLM, error) { return search, nil }
	executable, err := BotConfiguredResearchTool(f.ctx, f.req, knowledgeScope(f), factory, nil, episode)
	if err != nil {
		t.Fatal(err)
	}
	native := executable.(botRunnableTool)
	ctx := optionalKnowledgeContext{knowledgeCtx(f)}
	// Both legitimate coverage guidance and private/foreign data are rejected
	// identically before reservation. Subject anchoring alone cannot authorize
	// disclosure of a model-produced suffix.
	for name, extra := range map[string]map[string]any{
		"public-coverage-query": {"query": "Stock Market News central bank policy official releases"},
		"public-dimension":      {"dimension": "central bank policy official releases"},
		"private-query":         {"query": "Stock Market News private owner portfolio account balances"},
		"foreign-subject":       {"subject": "Garden care"},
		"entity":                {"entity": "central bank"},
	} {
		t.Run(name, func(t *testing.T) {
			extra["facet"] = "overview"
			if _, err := native.Run(ctx, extra); err == nil {
				t.Fatal("unadmitted coverage/disclosure accepted")
			}
			if runtime.calls != 0 || len(search.requests) != 0 {
				t.Fatal("rejected coverage reached quota or provider")
			}
		})
	}
	for _, facet := range []string{"overview", "recent"} {
		result, err := native.Run(ctx, map[string]any{"facet": facet})
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(result)
		if strings.Contains(string(raw), "central bank policy") {
			t.Fatal("provider received nonexistent directed coverage")
		}
	}
	if runtime.calls != 2 || len(search.requests) != 2 ||
		!strings.Contains(search.requests[0], "Stock Market News overview") ||
		!strings.Contains(search.requests[1], "Stock Market News recent developments") {
		t.Fatalf("actual dispatched requests: %v; reservations: %d", search.requests, runtime.calls)
	}
}
