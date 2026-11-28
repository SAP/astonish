package bot

import (
	"context"
	"errors"
	"iter"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

type progressTestModel struct {
	t       *testing.T
	replies []string
	calls   int
}

func (m *progressTestModel) Name() string { return "employee" }
func (m *progressTestModel) GenerateContent(ctx context.Context, req *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		if ModelCallPurposeFromContext(ctx) != ModelCallPurposeProgress {
			m.t.Error("wrong purpose")
		}
		if _, ok := ctx.Deadline(); !ok {
			m.t.Error("missing per-call timeout")
		}
		if len(req.Tools) != 0 || len(req.Config.Tools) != 0 {
			m.t.Error("evaluation has tools")
		}
		reply := m.replies[min(m.calls, len(m.replies)-1)]
		m.calls++
		yield(&model.LLMResponse{Content: genai.NewContentFromText(reply, "model")}, nil)
	}
}
func TestBotProgressControllerDecisions(t *testing.T) {
	for _, decision := range []store.BotProgressDecision{store.BotProgressContinue, store.BotProgressReplan, store.BotProgressSynthesize, store.BotProgressAskUser} {
		t.Run(string(decision), func(t *testing.T) {
			now := time.Now()
			commits, questions := 0, 0
			question := ""
			if decision == store.BotProgressAskUser {
				question = `,"question":"Which account?"`
			}
			llm := &progressTestModel{t: t, replies: []string{`{"decision":"` + string(decision) + `","summary":"Read source; remaining analysis","next_steps":["compare sources"],"evidence_refs":["observation:1"]` + question + `}`}}
			c := &ProgressController{LLM: llm, Interval: time.Minute, Timeout: time.Second, Now: func() time.Time { return now }, Boundary: func(context.Context) error { return nil }, Observe: func(context.Context) (ProgressObservation, error) {
				return ProgressObservation{Assignment: "Analyze sources", Fingerprint: "source:1", EvidenceRefs: []string{"observation:1"}, Settled: true}, nil
			}, Commit: func(context.Context, ProgressObservation, ProgressDecision, time.Time) error { commits++; return nil }, AskUser: func(context.Context, string) error { questions++; return nil }}
			if d, e := c.Check(context.Background()); e != nil || d != nil {
				t.Fatalf("initial %v %v", d, e)
			}
			now = now.Add(7 * time.Hour)
			d, e := c.Check(context.Background())
			if e != nil || d == nil || d.Decision != decision || commits != 1 {
				t.Fatalf("decision %v %v commits=%d", d, e, commits)
			}
			if (questions == 1) != (decision == store.BotProgressAskUser) {
				t.Fatal("incorrect owner interaction")
			}
			if llm.calls != 1 {
				t.Fatal("unexpected extra reviewer")
			}
		})
	}
}
func TestBotProgressControllerCorrectionAndUnsafeBoundary(t *testing.T) {
	now := time.Now()
	settled := false
	commits := 0
	llm := &progressTestModel{t: t, replies: []string{`{"decision":"continue","summary":"invented","next_steps":[],"evidence_refs":["invented"]}`, `{"decision":"replan","summary":"change failed approach","next_steps":["different read"],"evidence_refs":[]}`}}
	c := &ProgressController{LLM: llm, Interval: time.Minute, Timeout: time.Second, Now: func() time.Time { return now }, Boundary: func(context.Context) error { return nil }, Observe: func(context.Context) (ProgressObservation, error) {
		return ProgressObservation{Fingerprint: "1", Settled: settled}, nil
	}, Commit: func(context.Context, ProgressObservation, ProgressDecision, time.Time) error { commits++; return nil }, AskUser: func(context.Context, string) error { return nil }}
	c.Check(context.Background())
	now = now.Add(time.Minute)
	if _, e := c.Check(context.Background()); !errors.Is(e, ErrProgressBoundaryUnsafe) || llm.calls != 0 {
		t.Fatal("unsafe boundary dispatched")
	}
	settled = true
	if d, e := c.Check(context.Background()); e != nil || d == nil || llm.calls != 2 || commits != 1 {
		t.Fatalf("correction %v %v calls=%d", d, e, llm.calls)
	}
}
func TestBotProgressControllerPersistenceFailure(t *testing.T) {
	now := time.Now()
	boom := errors.New("checkpoint write failed")
	llm := &progressTestModel{t: t, replies: []string{`{"decision":"continue","summary":"new evidence","next_steps":[],"evidence_refs":[]}`}}
	c := &ProgressController{LLM: llm, Interval: time.Minute, Timeout: time.Second, Now: func() time.Time { return now }, Boundary: func(context.Context) error { return nil }, Observe: func(context.Context) (ProgressObservation, error) {
		return ProgressObservation{Fingerprint: "1", Settled: true}, nil
	}, Commit: func(context.Context, ProgressObservation, ProgressDecision, time.Time) error { return boom }, AskUser: func(context.Context, string) error { return nil }}
	c.Check(context.Background())
	now = now.Add(time.Minute)
	if _, e := c.Check(context.Background()); !errors.Is(e, boom) || c.last != nil {
		t.Fatal("uncommitted context accepted")
	}
}
