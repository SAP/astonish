package api

import (
	"context"
	"errors"
	"iter"
	"testing"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/entstore"
	"google.golang.org/adk/model"
)

type apiPreflightModel struct {
	model.LLM
	prepare func(context.Context)
}

func (m *apiPreflightModel) SupportsModelPreflight() bool { return true }
func (m *apiPreflightModel) GenerateContent(ctx context.Context, req *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		m.prepare(ctx)
		execution.EndModelPreflight(ctx)
		for r, e := range m.LLM.GenerateContent(ctx, req, stream) {
			if !yield(r, e) {
				return
			}
		}
	}
}
func apiReadScopeActive(ctx context.Context) bool {
	return ctx != nil && entstore.WithBotContentReadOperationFromContext(context.Background(), ctx) != context.Background()
}

func TestBotAPIModelPreflightSharesWorkLoad(t *testing.T) {
	ctx, req, _ := botChatTestRequest(t)
	work := &progressOperationWork{work: store.NewBotAgentWorkState(store.BotWorkContract{BotID: req.Bot.ID, RunID: req.Run.ID, Version: req.Run.Version, Instructions: req.Instructions})}
	service := &botAgentWorkService{req: req, resources: &BotChatResources{Work: work}}
	ctx = bot.WithBotWorkServices(ctx, service)
	calls := 0
	provider := &agentCheckModel{run: func(c context.Context, _ *model.LLMRequest) iter.Seq2[*model.LLMResponse, error] {
		if apiReadScopeActive(c) {
			t.Fatal("read scope survived provider iterator creation")
		}
		return func(yield func(*model.LLMResponse, error) bool) { calls++; yield(&model.LLMResponse{}, nil) }
	}}
	prepared := &apiPreflightModel{LLM: provider, prepare: func(c context.Context) {
		if !apiReadScopeActive(c) || !apiReadScopeActive(work.seen[len(work.seen)-1]) {
			t.Fatal("work load scope ended before model preflight")
		}
		execution.EndModelPreflight(c)
		if apiReadScopeActive(work.seen[len(work.seen)-1]) {
			t.Fatal("work load used a separate scope")
		}
	}}
	wrapped := &botAgentReadModel{LLM: &botApprovalCaptureModel{LLM: prepared, capture: &botApprovalCapture{}}, journal: &botAgentReadJournal{}}
	for i := 0; i < 2; i++ {
		for _, err := range wrapped.GenerateContent(ctx, &model.LLMRequest{}, false) {
			if err != nil {
				t.Fatal(err)
			}
			if apiReadScopeActive(work.seen[i]) {
				t.Fatal("scope survived consumer yield")
			}
		}
	}
	if calls != 2 || len(work.seen) != 2 {
		t.Fatalf("provider=%d work=%d", calls, len(work.seen))
	}
	work.err = store.ErrBotForbidden
	for _, err := range wrapped.GenerateContent(ctx, &model.LLMRequest{}, false) {
		if !errors.Is(err, store.ErrBotForbidden) {
			t.Fatal(err)
		}
		if apiReadScopeActive(work.seen[2]) {
			t.Fatal("scope survived load error yield")
		}
	}
	if calls != 2 {
		t.Fatal("revoked work reached provider")
	}
}

func TestBotAPIApprovalPreflightBoundaries(t *testing.T) {
	for _, mode := range []string{"unknown", "waiting", "panic", "empty", "error"} {
		t.Run(mode, func(t *testing.T) {
			ctx, end := entstore.WithBotContentReadOperation(context.Background())
			defer end()
			ctx = execution.WithModelPreflightEnd(ctx, end)
			inner := &agentCheckModel{run: func(c context.Context, _ *model.LLMRequest) iter.Seq2[*model.LLMResponse, error] {
				if mode == "unknown" && apiReadScopeActive(c) {
					t.Fatal("unknown wrapper inherited scope")
				}
				if mode == "panic" {
					panic("fixture")
				}
				return func(yield func(*model.LLMResponse, error) bool) {
					if mode == "error" {
						yield(nil, store.ErrBotForbidden)
					}
				}
			}}
			var next model.LLM = inner
			if mode != "unknown" {
				next = &apiPreflightModel{LLM: inner, prepare: func(context.Context) {
					if mode == "panic" {
						panic("fixture")
					}
				}}
			}
			capture := &botApprovalCapture{}
			if mode == "waiting" {
				capture.approval = &store.BotApproval{}
			}
			wrapped := &botApprovalCaptureModel{LLM: next, capture: capture}
			func() {
				defer func() {
					if r := recover(); r != nil && mode != "panic" {
						panic(r)
					}
				}()
				for range wrapped.GenerateContent(ctx, &model.LLMRequest{}, false) {
					if apiReadScopeActive(ctx) {
						t.Fatal("scope survived approval yield")
					}
					break
				}
			}()
			if apiReadScopeActive(ctx) {
				t.Fatal("scope survived approval return/panic")
			}
		})
	}
}
