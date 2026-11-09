package provider

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"math"
	"sync"

	"google.golang.org/adk/model"
)

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
	contents, err := json.Marshal(req.Contents)
	if err != nil {
		return 0, nil, err
	}
	snapshot := &budgetInputSnapshot{identity: sha256.Sum256(identity), prefix: sha256.Sum256(contents), contents: len(req.Contents)}
	b.inputHistory.mu.Lock()
	previous := b.inputHistory.last
	b.inputHistory.mu.Unlock()
	if previous == nil || previous.identity != snapshot.identity || previous.contents > len(req.Contents) {
		return bound, snapshot, nil
	}
	prefix, err := json.Marshal(req.Contents[:previous.contents])
	if err != nil || sha256.Sum256(prefix) != previous.prefix {
		return bound, snapshot, err
	}
	// The same prompt is charged again, at its proven full input count. Only
	// complete appended messages use the byte bound, with framing headroom. This
	// is not a cache discount and never extrapolates a compression ratio to new
	// text or a modified system instruction/tool schema/history message.
	extra := int64(256)
	for _, content := range req.Contents[previous.contents:] {
		data, err := json.Marshal(content)
		if err != nil {
			return 0, nil, err
		}
		n := int64(len(data)) + 64
		if n > math.MaxInt64-extra {
			return bound, snapshot, nil
		}
		extra += n
	}
	if previous.tokens > math.MaxInt64-extra {
		return bound, snapshot, nil
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
