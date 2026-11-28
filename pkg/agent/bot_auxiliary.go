package agent

import (
	"context"
	"fmt"
	"iter"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/model"
)

type botAuxiliaryKey struct{}
type botAuxiliaryModel struct {
	identity execution.BotRunIdentity
	llm      model.LLM
}

// WithBotAuxiliaryLLM is a trusted composition hook. llm must already be wrapped
// by the same shared run-budget decorator as the parent; never pass a pooled or
// viewer model here. This capability is bound to a specific Bot/run identity.
func WithBotAuxiliaryLLM(ctx context.Context, llm model.LLM) context.Context {
	identity, _ := execution.BotRunIdentityFromContext(ctx)
	return context.WithValue(ctx, botAuxiliaryKey{}, botAuxiliaryModel{identity, llm})
}

func isBotModelContext(ctx context.Context) bool {
	_, ok := execution.BotRunIdentityFromContext(ctx)
	_, interactive := execution.BotTurnAuthorizerFromContext(ctx)
	return ok || interactive || execution.RunPolicyFromContext(ctx) != nil
}

// auxiliaryLLM deliberately ignores ordinary WithLLM overrides in Bot mode:
// they may be an unmetered model selected by an interactive viewer.
func auxiliaryLLM(ctx context.Context, fallback model.LLM) model.LLM {
	if isBotModelContext(ctx) {
		identity, ok := execution.BotRunIdentityFromContext(ctx)
		capability, found := ctx.Value(botAuxiliaryKey{}).(botAuxiliaryModel)
		if ok && found && capability.identity == identity && capability.llm != nil {
			if auth, interactive := execution.BotTurnAuthorizerFromContext(ctx); interactive {
				return authorizedBotAuxiliary{LLM: capability.llm, authorization: auth, identity: identity}
			}
			return capability.llm
		}
		return unavailableBotAuxiliary{}
	}
	if override := LLMFromContext(ctx); override != nil {
		return override
	}
	return fallback
}

type authorizedBotAuxiliary struct {
	model.LLM
	authorization execution.BotTurnAuthorizer
	identity      execution.BotRunIdentity
}

func (a authorizedBotAuxiliary) GenerateContent(ctx context.Context, req *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		id, ok := execution.BotRunIdentityFromContext(ctx)
		if !ok || id != a.identity {
			yield(nil, store.ErrBotForbidden)
			return
		}
		if err := a.authorization.Authorize(ctx, execution.CapabilityChat); err != nil {
			yield(nil, err)
			return
		}
		for response, err := range a.LLM.GenerateContent(ctx, req, stream) {
			if !yield(response, err) {
				return
			}
		}
	}
}

type unavailableBotAuxiliary struct{}

func (unavailableBotAuxiliary) Name() string { return "unavailable-bot-auxiliary" }
func (unavailableBotAuxiliary) GenerateContent(context.Context, *model.LLMRequest, bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		yield(nil, fmt.Errorf("%w: Bot auxiliary model requires trusted shared-budget binding", store.ErrBotUnsupported))
	}
}
