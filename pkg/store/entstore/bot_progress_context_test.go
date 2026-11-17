package entstore

import (
	"bytes"
	"context"
	"fmt"
	"iter"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

type progressCompositionModel struct{ calls int }

func (m *progressCompositionModel) Name() string { return "employee" }
func (m *progressCompositionModel) GenerateContent(ctx context.Context, req *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		m.calls++
		yield(&model.LLMResponse{Content: genai.NewContentFromText(`{"decision":"continue","summary":"Compared another source","next_steps":["compare remaining sources"],"evidence_refs":[]}`, "model")}, nil)
	}
}

// This is a real repository/controller composition test, not HTTP acceptance.
func TestBotProgressControllerDurableContextRestart(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			var reopen func()
			repo, _, ctx, _, lease, now := progressFixture(t, scope, &reopen)
			persistence := bot.ProgressCheckpointer{Store: repo, Contexts: repo}
			llm := &progressCompositionModel{}
			var state *store.BotProgressState
			var working []byte
			controller := &bot.ProgressController{LLM: llm, Interval: time.Second, Timeout: time.Second, Now: func() time.Time { return *now }, Boundary: func(context.Context) error { return nil }, AskUser: func(context.Context, string) error { t.Fatal("unexpected owner question"); return nil }}
			controller.Observe = func(context.Context) (bot.ProgressObservation, error) {
				return bot.ProgressObservation{Assignment: "Compare sources", Fingerprint: fmt.Sprint(llm.calls + 1), Settled: true}, nil
			}
			controller.Commit = func(ctx context.Context, observation bot.ProgressObservation, decision bot.ProgressDecision, at time.Time) error {
				request := progressCheckpoint(t, repo, ctx, lease, *now)
				if state != nil {
					request.State.Checkpoint.TurnID = state.Checkpoint.TurnID
				}
				working = []byte(fmt.Sprintf("private model/tool context after evaluation %d", llm.calls))
				next, err := persistence.Commit(ctx, request, working, observation, decision, at)
				if err == nil {
					state = next
				}
				return err
			}
			if _, err := controller.Check(ctx); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 2; i++ {
				*now = now.Add(2 * time.Second)
				if decision, err := controller.Check(ctx); err != nil || decision == nil {
					t.Fatalf("evaluation: %v %v", decision, err)
				}
			}
			if state.Checkpoint.Revision != 2 || llm.calls != 2 {
				t.Fatal("evaluations did not checkpoint")
			}
			reopen()
			request := progressCheckpoint(t, repo, ctx, lease, *now)
			restored := bot.ProgressController{LLM: llm, Interval: time.Second, Timeout: time.Second, Now: controller.Now, Boundary: controller.Boundary, Observe: controller.Observe, Commit: controller.Commit, AskUser: controller.AskUser}
			data, err := persistence.Restore(ctx, request.Mutation, state, &restored)
			if err != nil || !bytes.Equal(data, working) {
				t.Fatalf("restore: %q %v", data, err)
			}
			// A fresh lease-bound reader cannot fetch another run or altered digest.
			bad := state.Clone()
			bad.Checkpoint.ContextDigest = store.BotProgressContextDigest([]byte("tampered"))
			if _, err = persistence.Restore(ctx, request.Mutation, bad, &restored); err == nil {
				t.Fatal("tampered context accepted")
			}
			wrong := request.Mutation
			wrong.Lease.Fence++
			if _, _, err = repo.SaveProgressContext(ctx, wrong, []byte("stale worker")); err == nil {
				t.Fatal("stale lease stored context")
			}
			if _, _, err = repo.SaveProgressContext(ctx, request.Mutation, make([]byte, store.BotProgressMaxBytes+1)); err == nil {
				t.Fatal("oversized context accepted")
			}
			*now = now.Add(2 * time.Second)
			if decision, err := restored.Check(ctx); err != nil || decision == nil || state.Checkpoint.Revision != 3 || llm.calls != 3 {
				t.Fatalf("restored employee did not continue: %v %v", decision, err)
			}
		})
	}
}
