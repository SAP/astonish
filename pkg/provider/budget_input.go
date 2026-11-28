package provider

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"math"
	"sync"

	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

// EstimateModelInput consults the same request-local estimator used for model
// admission. Wrappers without that capability retain the conservative byte bound.
// It neither reserves capacity nor learns from an unexecuted request.
func EstimateModelInput(ctx context.Context, llm model.LLM, req *model.LLMRequest) (int64, error) {
	if req == nil {
		return 0, errors.New("nil budget model request")
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if estimator, ok := llm.(interface {
		EstimateInputTokens(context.Context, *model.LLMRequest) (int64, error)
	}); ok {
		return estimator.EstimateInputTokens(ctx, req)
	}
	return estimateBudgetInput(ctx, req)
}

func (b *BudgetLLM) EstimateInputTokens(ctx context.Context, req *model.LLMRequest) (int64, error) {
	if req == nil {
		return 0, errors.New("nil budget model request")
	}
	n, _, err := b.estimateInput(ctx, req)
	if err == nil && n < 0 {
		err = errors.New("invalid input token estimate")
	}
	return n, err
}

type budgetInputGuidanceKey struct{}
type budgetInputGuidance struct {
	request *model.LLMRequest
	content *genai.Content
	digest  [32]byte
}

// WithBudgetInputGuidance marks only the runtime's newly appended, request-local
// last message, never user/tool text discovered by matching a string. The caller
// must not persist the message. Identity and digest checks fail closed if the
// request is replaced or the message changes. This affects prefix matching only:
// all current guidance bytes and the full previous measured input remain charged.
func WithBudgetInputGuidance(ctx context.Context, req *model.LLMRequest) context.Context {
	if req == nil || len(req.Contents) == 0 {
		return ctx
	}
	content := req.Contents[len(req.Contents)-1]
	data, err := json.Marshal(content)
	if err != nil {
		return ctx
	}
	return context.WithValue(ctx, budgetInputGuidanceKey{}, budgetInputGuidance{req, content, sha256.Sum256(data)})
}

func budgetInputCanonical(ctx context.Context, req *model.LLMRequest) ([]*genai.Content, int64) {
	mark, ok := ctx.Value(budgetInputGuidanceKey{}).(budgetInputGuidance)
	if !ok || mark.request != req || len(req.Contents) == 0 || req.Contents[len(req.Contents)-1] != mark.content {
		return req.Contents, 0
	}
	data, err := json.Marshal(mark.content)
	if err != nil || sha256.Sum256(data) != mark.digest {
		return req.Contents, 0
	}
	return req.Contents[:len(req.Contents)-1], int64(len(data)) + 64
}

// Keep one immutable, request-scoped observation, not a learned bytes/token
// ratio. A different prompt, configuration, model or decorator cannot borrow it.
// Hashes avoid retaining a second copy of potentially sensitive full history.
type budgetInputSnapshot struct {
	identity [32]byte
	prefix   [32]byte
	contents int
	tokens   int64
}

type budgetInputHistory struct {
	mu   sync.Mutex
	last *budgetInputSnapshot
}

func (b *BudgetLLM) estimateInput(ctx context.Context, req *model.LLMRequest) (int64, *budgetInputSnapshot, error) {
	if b.options.EstimateInput != nil {
		n, err := b.options.EstimateInput(ctx, req)
		return n, nil, err
	}
	// Always validate the entire request, even on a prefix hit. In particular,
	// cached/remote/media content must never inherit a textual estimate.
	bound, err := estimateBudgetInput(ctx, req)
	if err != nil {
		return 0, nil, err
	}
	base := *req
	base.Contents = nil
	identity, err := json.Marshal(struct {
		Name    string
		Request *model.LLMRequest
	}{b.inner.Name(), &base})
	if err != nil {
		return 0, nil, err
	}
	canonical, guidanceBytes := budgetInputCanonical(ctx, req)
	contents, err := json.Marshal(canonical)
	if err != nil {
		return 0, nil, err
	}
	snapshot := &budgetInputSnapshot{identity: sha256.Sum256(identity), prefix: sha256.Sum256(contents), contents: len(canonical)}
	b.inputHistory.mu.Lock()
	previous := b.inputHistory.last
	b.inputHistory.mu.Unlock()
	if previous == nil || previous.identity != snapshot.identity || previous.contents > len(canonical) {
		return bound, snapshot, nil
	}
	prefix, err := json.Marshal(canonical[:previous.contents])
	if err != nil || sha256.Sum256(prefix) != previous.prefix {
		return bound, snapshot, err
	}
	// The same prompt is charged again, at its proven full input count. Only
	// complete appended messages use the byte bound, with framing headroom. This
	// is not a cache discount and never extrapolates a compression ratio to new
	// text or a modified system instruction/tool schema/history message.
	extra := int64(256) + guidanceBytes
	for _, content := range canonical[previous.contents:] {
		data, err := json.Marshal(content)
		if err != nil {
			return 0, nil, err
		}
		n := int64(len(data)) + 64
		if n > math.MaxInt64-extra {
			return 0, nil, errors.New("input token estimate overflow")
		}
		extra += n
	}
	if previous.tokens > math.MaxInt64-extra {
		return 0, nil, errors.New("input token estimate overflow")
	}
	return previous.tokens + extra, snapshot, nil
}

func (h *budgetInputHistory) observe(snapshot *budgetInputSnapshot, tokens int64) {
	if snapshot == nil || tokens <= 0 {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	// Concurrent identical requests may report different counts: never replace
	// the larger authoritative observation with a smaller one.
	if h.last != nil && h.last.identity == snapshot.identity && h.last.prefix == snapshot.prefix {
		tokens = max(tokens, h.last.tokens)
	}
	snapshot.tokens = tokens
	h.last = snapshot
}
