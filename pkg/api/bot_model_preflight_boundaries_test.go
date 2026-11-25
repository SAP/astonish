package api

import (
	"context"
	"iter"
	"testing"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/entstore"
	"google.golang.org/adk/model"
)

func TestBotAPIReadPreflightFreshAndClosesEveryExit(t *testing.T) {
	for _, mode := range []string{"unknown", "panic", "empty", "error", "consumer-stop", "consumer-panic"} {
		t.Run(mode, func(t *testing.T) {
			base, req, _ := botChatTestRequest(t)
			outer, endOuter := entstore.WithBotContentReadOperation(base)
			defer endOuter()
			parent := execution.WithModelPreflightEnd(outer, endOuter)
			work := &progressOperationWork{work: store.NewBotAgentWorkState(store.BotWorkContract{BotID: req.Bot.ID, RunID: req.Run.ID, Version: req.Run.Version, Instructions: req.Instructions})}
			parent = bot.WithBotWorkServices(parent, &botAgentWorkService{req: req, resources: &BotChatResources{Work: work}})
			var seen context.Context
			inner := &agentCheckModel{run: func(c context.Context, _ *model.LLMRequest) iter.Seq2[*model.LLMResponse, error] {
				seen = c
				if apiReadScopeActive(c) || apiReadScopeActive(outer) {
					t.Fatal("scope survived provider creation")
				}
				return func(yield func(*model.LLMResponse, error) bool) {
					if mode == "empty" {
						return
					}
					if mode == "error" {
						yield(nil, store.ErrBotForbidden)
						return
					}
					yield(&model.LLMResponse{}, nil)
				}
			}}
			var next model.LLM = inner
			if mode != "unknown" {
				next = &apiPreflightModel{LLM: inner, prepare: func(c context.Context) {
					seen = c
					endOuter()
					if !apiReadScopeActive(c) {
						t.Fatal("producer borrowed old operation instead of owning fresh bytes")
					}
					if mode == "panic" {
						panic("fixture")
					}
				}}
			}
			wrapped := &botAgentReadModel{LLM: next, journal: &botAgentReadJournal{}}
			func() {
				defer func() {
					if r := recover(); r != nil && mode != "panic" && mode != "consumer-panic" {
						panic(r)
					}
				}()
				for range wrapped.GenerateContent(parent, &model.LLMRequest{}, false) {
					if apiReadScopeActive(seen) || apiReadScopeActive(work.seen[0]) {
						t.Fatal("scope survived consumer callback")
					}
					if mode == "consumer-panic" {
						panic("consumer")
					}
					break
				}
			}()
			if apiReadScopeActive(seen) || apiReadScopeActive(work.seen[0]) {
				t.Fatal("scope survived return/panic")
			}
		})
	}
}

func TestBotAPIPreflightCapabilityRequiresInnerContract(t *testing.T) {
	unknown := &agentCheckModel{}
	known := &apiPreflightModel{}
	for _, inner := range []model.LLM{unknown, known} {
		want := inner == known
		approval := &botApprovalCaptureModel{LLM: inner}
		read := &botAgentReadModel{LLM: approval}
		if approval.SupportsModelPreflight() != want || read.SupportsModelPreflight() != want {
			t.Fatal("preflight capability did not follow inner contract")
		}
	}
}
