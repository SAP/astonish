package launcher

import (
	"context"
	"iter"
	"runtime"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store/entstore"
	"google.golang.org/adk/model"
	"google.golang.org/adk/tool"
)

// Count the actual configured adapter's repeated mandate preflights, not a
// synthetic loop over check(). A missing scope means the production mandate
// runtime must open its own immutable-read operation on every invocation.
type nativeSearchReadProbe struct {
	*researchTestRuntime
	t        *testing.T
	unscoped int
	retained []context.Context
}

func (p *nativeSearchReadProbe) VerifyBotMandate(ctx context.Context, ref execution.BotMandateRef, purpose execution.BotMandatePurpose) (execution.BotMandate, error) {
	if entstore.WithBotContentReadOperationFromContext(context.Background(), ctx) == context.Background() {
		p.unscoped++
		var pcs [16]uintptr
		n := runtime.Callers(2, pcs[:])
		frames := runtime.CallersFrames(pcs[:n])
		var names []string
		for {
			f, more := frames.Next()
			if strings.Contains(f.Function, "astonish/") {
				names = append(names, f.Function)
			}
			if !more {
				break
			}
		}
		p.t.Logf("fresh mandate read callpath: %s", strings.Join(names, " <- "))
	}
	p.retained = append(p.retained, ctx)
	return p.researchTestRuntime.VerifyBotMandate(ctx, ref, purpose)
}

type nativeBoundarySearchModel struct {
	*knowledgeModel
	probe *nativeSearchReadProbe
}

func (m *nativeBoundarySearchModel) GenerateContent(c context.Context, r *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	for _, retained := range m.probe.retained {
		if entstore.WithBotContentReadOperationFromContext(context.Background(), retained) != context.Background() {
			m.probe.t.Error("preflight scope survived generation")
		}
	}
	return m.knowledgeModel.GenerateContent(c, r, stream)
}
func TestNativeReadOperationPreservesBinding(t *testing.T) {
	f := newBotFactoryFixture(t)
	for _, bound := range []bool{false, true} {
		original := tool.Context(optionalKnowledgeContext{knowledgeCtx(f)})
		if bound {
			original = botBoundToolContext{Context: original, owner: f.ctx}
		}
		wrapped, end := botToolReadOperation(original)
		_, gotBound := wrapped.(botBoundToolContext)
		if gotBound != bound {
			t.Fatal("manufactured or lost binding marker")
		}
		if entstore.WithBotContentReadOperationFromContext(context.Background(), wrapped) == context.Background() {
			t.Fatal("operation missing")
		}
		end()
		if entstore.WithBotContentReadOperationFromContext(context.Background(), wrapped) != context.Background() {
			t.Fatal("operation survived end")
		}
	}
}

func TestConfiguredNativeSearchReadBoundaries(t *testing.T) {
	f, runtime, episode := researchFixture(t)
	probe := &nativeSearchReadProbe{researchTestRuntime: runtime, t: t}
	f.req.Mandate.Runtime = probe
	searchModel := &knowledgeModel{text: "Sources: https://example.com/decisive"}
	search, err := BotConfiguredResearchTool(f.ctx, f.req, knowledgeScope(f), func(ctx context.Context, _, _ string, _ *config.AppConfig) (model.LLM, error) {
		for _, retained := range probe.retained {
			if entstore.WithBotContentReadOperationFromContext(context.Background(), retained) != context.Background() {
				t.Error("immutable preflight survived provider initialization")
			}
		}
		return &nativeBoundarySearchModel{knowledgeModel: searchModel, probe: probe}, nil
	}, nil, episode)
	if err != nil {
		t.Fatal(err)
	}
	result, err := search.(botRunnableTool).Run(optionalKnowledgeContext{knowledgeCtx(f)}, map[string]any{"facet": "overview"})
	if err != nil || result == nil || searchModel.calls != 1 {
		t.Fatalf("search result=%v calls=%d err=%v", result, searchModel.calls, err)
	}
	t.Logf("unscoped native mandate preflights=%d", probe.unscoped)
	if probe.unscoped != 0 {
		t.Errorf("configured native adapter left %d mandate preflights without a bounded operation", probe.unscoped)
	}
	for _, retained := range probe.retained {
		if entstore.WithBotContentReadOperationFromContext(context.Background(), retained) != context.Background() {
			t.Error("operation survived adapter return")
		}
	}
}
