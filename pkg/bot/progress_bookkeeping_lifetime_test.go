package bot

import (
	"context"
	"iter"
	"testing"
	"time"

	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

type bookkeepingLifetimeModel struct {
	t      *testing.T
	active *bool
}

func (m bookkeepingLifetimeModel) Name() string { return "lifetime" }
func (m bookkeepingLifetimeModel) GenerateContent(context.Context, *model.LLMRequest, bool) iter.Seq2[*model.LLMResponse, error] {
	if *m.active {
		m.t.Fatal("bookkeeping scope crosses provider entry")
	}
	return func(yield func(*model.LLMResponse, error) bool) {
		if *m.active {
			m.t.Fatal("bookkeeping scope crosses provider iteration")
		}
		yield(&model.LLMResponse{Content: genai.NewContentFromText(`{"decision":"ask_user","summary":"Need owner input","question":"Which account?","next_steps":[],"evidence_refs":[]}`, "model")}, nil)
	}
}
func TestProgressSynchronousBookkeepingLifetime(t *testing.T) {
	active := false
	starts, boundaries, observations, commits := 0, 0, 0, 0
	c := &ProgressController{Interval: time.Second, Now: time.Now, lastAt: time.Now().Add(-time.Hour)}
	c.LLM = bookkeepingLifetimeModel{t, &active}
	c.BeginBookkeeping = func(ctx context.Context) (context.Context, func()) {
		if active {
			t.Fatal("nested scope lost shared immutable reads")
		}
		active = true
		starts++
		return ctx, func() { active = false }
	}
	c.Boundary = func(context.Context) error {
		if !active {
			t.Error("boundary outside scope")
		}
		boundaries++
		return nil
	}
	c.Observe = func(context.Context) (ProgressObservation, error) {
		if !active {
			t.Error("observe outside scope")
		}
		observations++
		return ProgressObservation{Settled: true, Fingerprint: "new"}, nil
	}
	c.Commit = func(context.Context, ProgressObservation, ProgressDecision, time.Time) error {
		if !active {
			t.Error("commit outside scope")
		}
		commits++
		return nil
	}
	c.AskUser = func(context.Context, string) error {
		if active {
			t.Error("scope crosses owner interaction")
		}
		return nil
	}
	if _, err := c.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	if active || starts != 2 || boundaries != 3 || observations != 1 || commits != 1 {
		t.Fatalf("active=%v scopes=%d boundaries=%d observations=%d commits=%d", active, starts, boundaries, observations, commits)
	}
}
