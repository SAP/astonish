package bot

import (
	"context"
	"github.com/SAP/astonish/pkg/execution"
	"google.golang.org/adk/model"
	"iter"
	"testing"
)

type progressPreflightModel struct{ bookkeepingLifetimeModel }

func (m progressPreflightModel) SupportsModelPreflight() bool { return true }
func (m progressPreflightModel) GenerateContent(ctx context.Context, req *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	if !*m.active {
		m.t.Fatal("evaluator preflight lost synchronous read scope")
	}
	execution.EndModelPreflight(ctx)
	return m.bookkeepingLifetimeModel.GenerateContent(ctx, req, stream)
}
func TestProgressEvaluatorPreflightLifetime(t *testing.T) {
	active := true
	c := &ProgressController{LLM: progressPreflightModel{bookkeepingLifetimeModel{t, &active}}, Boundary: func(context.Context) error { return nil }}
	_, err := c.evaluateBookkeeping(context.Background(), []byte(`{}`), ProgressObservation{}, func() { active = false })
	if err != nil {
		t.Fatal(err)
	}
	if active {
		t.Fatal("scope retained after evaluator")
	}
}
