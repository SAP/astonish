package launcher

import (
	"context"
	"sync"

	"github.com/SAP/astonish/pkg/store/entstore"
	"google.golang.org/adk/model"
)

// botDeclarationReads belongs to one constructed agent and keys bytes by the
// actual ADK request identity. It never caches an authorization result. A new
// request cannot reuse an older request's bytes; entering generation or cleaning
// up a failed attempt destroys the cache, including for retained contexts.
type botDeclarationReads struct {
	mu      sync.Mutex
	request *model.LLMRequest
	owner   context.Context
	end     func()
}

func (s *botDeclarationReads) begin(ctx context.Context) context.Context {
	s.close()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.owner, s.end = entstore.WithBotContentReadOperationIfAbsent(ctx)
	return s.owner
}

func (s *botDeclarationReads) bind(b *botFactoryBinding, req *model.LLMRequest) *botFactoryBinding {
	if s == nil || req == nil {
		return b
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.request == nil && s.owner != nil {
		s.request = req
	}
	if s.request != req {
		if s.end != nil {
			s.end()
		}
		if end, ok := b.owner.Value(botStartupReadsKey{}).(func()); ok && entstore.WithBotContentReadOperationFromContext(context.Background(), b.owner) != context.Background() {
			s.owner, s.end = b.owner, end
		} else {
			s.owner, s.end = entstore.WithBotContentReadOperationIfAbsent(b.owner)
		}
		s.request = req
	}
	copy := *b
	copy.owner = s.owner
	return &copy
}

func (s *botDeclarationReads) close() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.end != nil {
		s.end()
	}
	s.request, s.owner, s.end = nil, nil, nil
}
