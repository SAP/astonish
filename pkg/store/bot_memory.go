package store

import (
	"context"
	"fmt"

	"github.com/google/uuid"
)

// BotMemoryPrefilterStore is optional. Implementations MUST constrain candidates
// to sessionIDs before ranking, top-K, pagination or aggregation. Empty sessionIDs
// means no candidates, never unrestricted access. Categories retain their normal
// meaning and are not an authorization namespace.
type BotMemoryPrefilterStore interface {
	SearchBySessions(context.Context, []string, string, int, float64, string) ([]MemorySearchResult, error)
	ListBySessions(context.Context, []string, string, int, int) ([]MemorySearchResult, error)
}

// BotMemoryWriteAuthorizer is an optional capability on the scoped Bot repository.
// It revalidates current write authority (including live membership/worker fences
// where applicable) and returns the authenticated actor. BotStore.Get alone is
// only read authorization. Existing repositories need an adapter before writes
// through this service can be enabled.
type BotMemoryWriteAuthorizer interface {
	AuthorizeBotMemoryWrite(context.Context, BotID) (uuid.UUID, error)
}

// BotMemoryMutationStore is required for interactive writes. Implementations must
// validate same-scope store composition, prepare external work before fresh write
// authorization, and constrain persistence to the Bot and expected session in a
// local transaction. A generic MemoryStore mutation is never a safe fallback.
type BotMemoryMutationStore interface {
	MutateBotMemory(context.Context, MemoryStore, BotID, BotMemoryMutation) error
}

type BotMemoryMutationKind uint8

const (
	BotMemoryAdd BotMemoryMutationKind = iota + 1
	BotMemoryUpdate
	BotMemoryDelete
)

type BotMemoryMutation struct {
	Kind  BotMemoryMutationKind
	Entry MemoryEntry
	// Previous is the service-authorized row. Update/delete must compare its
	// attribution again at persistence, not merely look up its ID.
	Previous MemorySearchResult
}

// BotMemoryService is a non-owning, request-context-aware view over existing
// memory. Composition MUST supply stores from the same authenticated router scope;
// identifiers and this wrapper cannot prove that two arbitrary stores share a DB.
// It deliberately does not implement MemoryStore: context-free Count cannot
// reauthorize and Close must not close a shared routed store. No second index is
// maintained. Search result attribution is preserved, not upgraded to evidence.
// Interactive writes require a scoped transactional mutation capability; runtime
// lease/fence enforcement remains the dedicated publication adapter's obligation.
type BotMemoryService struct {
	bots   BotStore
	memory MemoryStore
	id     BotID
	scope  BotScope
}

func NewBotMemoryService(bots BotStore, memory MemoryStore, id BotID, scope BotScope) (*BotMemoryService, error) {
	if bots == nil || memory == nil {
		return nil, ErrBotUnsupported
	}
	if id == uuid.Nil || (scope != BotScopePersonal && scope != BotScopeTeam) {
		return nil, ErrBotInvalid
	}
	return &BotMemoryService{bots: bots, memory: memory, id: id, scope: scope}, nil
}

func (s *BotMemoryService) sessions(ctx context.Context) ([]string, map[string]bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	b, err := s.bots.Get(ctx, s.id)
	if err != nil {
		return nil, nil, err
	}
	if b == nil {
		return nil, nil, ErrBotNotFound
	}
	if b.ID != s.id || b.Scope != s.scope {
		return nil, nil, ErrBotForbidden
	}
	ids := []string{}
	allowed := map[string]bool{}
	add := func(id uuid.UUID) {
		if id != uuid.Nil && !allowed[id.String()] {
			allowed[id.String()] = true
			ids = append(ids, id.String())
		}
	}
	add(b.HomeSessionID)
	const pageSize = 200
	after := ""
	seen := map[RunID]bool{}
	for {
		runs, err := s.bots.ListRuns(ctx, s.id, BotPage{Limit: pageSize, After: after})
		if err != nil {
			return nil, nil, err
		}
		for _, run := range runs {
			if run.BotID != s.id || run.ID == uuid.Nil || seen[run.ID] {
				return nil, nil, ErrBotForbidden
			}
			seen[run.ID] = true
			add(run.SessionID)
		}
		if len(runs) < pageSize {
			break
		}
		after = runs[len(runs)-1].ID.String()
	}
	return ids, allowed, nil
}

func (s *BotMemoryService) check(ctx context.Context, rows []MemorySearchResult, allowed map[string]bool) error {
	for _, row := range rows {
		if err := s.checkAttribution(ctx, row.SessionID, row.Attribution); err != nil {
			return err
		}
		if !allowed[row.SessionID] || (row.Scope != "" && row.Scope != string(s.scope)) {
			return ErrBotForbidden
		}
	}
	return nil
}

func (s *BotMemoryService) Search(ctx context.Context, query string, maxResults int, minScore float64) ([]MemorySearchResult, error) {
	return s.SearchByCategory(ctx, query, maxResults, minScore, "")
}

func (s *BotMemoryService) SearchByCategory(ctx context.Context, query string, maxResults int, minScore float64, category string) ([]MemorySearchResult, error) {
	ids, allowed, err := s.sessions(ctx)
	if err != nil {
		return nil, err
	}
	backend, ok := s.memory.(BotMemoryPrefilterStore)
	if !ok {
		return nil, fmt.Errorf("%w: session-prefiltered memory search", ErrBotUnsupported)
	}
	rows, err := backend.SearchBySessions(ctx, ids, query, maxResults, minScore, category)
	if err != nil {
		return nil, err
	}
	if err := s.check(ctx, rows, allowed); err != nil {
		return nil, err
	}
	return rows, nil
}

func (s *BotMemoryService) List(ctx context.Context, category string, limit, offset int) ([]MemorySearchResult, error) {
	ids, allowed, err := s.sessions(ctx)
	if err != nil {
		return nil, err
	}
	backend, ok := s.memory.(BotMemoryPrefilterStore)
	if !ok {
		return nil, fmt.Errorf("%w: session-prefiltered memory list", ErrBotUnsupported)
	}
	rows, err := backend.ListBySessions(ctx, ids, category, limit, offset)
	if err != nil {
		return nil, err
	}
	if err := s.check(ctx, rows, allowed); err != nil {
		return nil, err
	}
	return rows, nil
}

func (s *BotMemoryService) Get(ctx context.Context, id string) (*MemorySearchResult, error) {
	_, allowed, err := s.sessions(ctx)
	if err != nil {
		return nil, err
	}
	row, err := s.memory.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, nil
	}
	if err := s.check(ctx, []MemorySearchResult{*row}, allowed); err != nil {
		return nil, err
	}
	return row, nil
}

func (s *BotMemoryService) ListBySession(ctx context.Context, sessionID string) ([]MemorySearchResult, error) {
	_, allowed, err := s.sessions(ctx)
	if err != nil {
		return nil, err
	}
	if !allowed[sessionID] {
		return nil, ErrBotForbidden
	}
	rows, err := s.memory.ListBySession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	// Validate the exact requested session as well as the Bot's scope.
	if err := s.check(ctx, rows, map[string]bool{sessionID: true}); err != nil {
		return nil, err
	}
	return rows, nil
}

func (s *BotMemoryService) authorizeWrite(ctx context.Context) (uuid.UUID, error) {
	authorizer, ok := s.bots.(BotMemoryWriteAuthorizer)
	if !ok {
		return uuid.Nil, ErrBotUnsupported
	}
	actor, err := authorizer.AuthorizeBotMemoryWrite(ctx, s.id)
	if err != nil {
		return uuid.Nil, err
	}
	if actor == uuid.Nil {
		return uuid.Nil, ErrBotForbidden
	}
	return actor, nil
}

func (s *BotMemoryService) Add(ctx context.Context, entry MemoryEntry) error {
	_, allowed, err := s.sessions(ctx)
	if err != nil {
		return err
	}
	if !allowed[entry.SessionID] {
		return ErrBotForbidden
	}
	if err := s.checkAttribution(ctx, entry.SessionID, entry.Attribution); err != nil {
		return err
	}
	actor, err := s.authorizeWrite(ctx)
	if err != nil {
		return err
	}
	if entry.CreatedBy != "" && entry.CreatedBy != actor.String() {
		return ErrBotForbidden
	}
	entry.CreatedBy = actor.String()
	return s.mutate(ctx, BotMemoryMutation{Kind: BotMemoryAdd, Entry: entry})
}

func (s *BotMemoryService) Update(ctx context.Context, id, content, category string) error {
	row, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	if row == nil {
		return ErrBotNotFound
	}
	if _, err := s.authorizeWrite(ctx); err != nil {
		return err
	}
	return s.mutate(ctx, BotMemoryMutation{Kind: BotMemoryUpdate, Previous: *row, Entry: MemoryEntry{Content: content, Category: category}})
}

func (s *BotMemoryService) Delete(ctx context.Context, id string) error {
	row, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	if row == nil {
		return ErrBotNotFound
	}
	if _, err := s.authorizeWrite(ctx); err != nil {
		return err
	}
	return s.mutate(ctx, BotMemoryMutation{Kind: BotMemoryDelete, Previous: *row})
}

func (s *BotMemoryService) mutate(ctx context.Context, mutation BotMemoryMutation) error {
	backend, ok := s.bots.(BotMemoryMutationStore)
	if !ok {
		return ErrBotUnsupported
	}
	return backend.MutateBotMemory(ctx, s.memory, s.id, mutation)
}

func (s *BotMemoryService) checkAttribution(ctx context.Context, session string, a *MemoryAttribution) error {
	if a == nil {
		return nil
	}
	if a.BotID != s.id {
		return ErrBotForbidden
	}
	if err := a.Validate(); err != nil {
		return err
	}
	if a.GoalID != uuid.Nil {
		goals, err := s.bots.ListGoals(ctx, s.id)
		if err != nil {
			return err
		}
		found := false
		for _, goal := range goals {
			if goal.ID == a.GoalID && goal.BotID == s.id {
				found = true
			}
		}
		if !found {
			return ErrBotForbidden
		}
	}
	if a.RunID != uuid.Nil {
		run, err := s.bots.GetRun(ctx, s.id, a.RunID)
		if err != nil {
			return err
		}
		if run == nil || run.BotID != s.id || run.SessionID.String() != session {
			return ErrBotForbidden
		}
	}
	return nil
}

// SearchAttributed excludes legacy un-attributed and superseded observations by
// default. The ordinary Search remains compatible with legacy session memories.
func (s *BotMemoryService) SearchAttributed(ctx context.Context, filter MemoryAttributionFilter, query string, maxResults int, minScore float64, category string) ([]MemorySearchResult, error) {
	if filter.BotID != s.id {
		return nil, ErrBotForbidden
	}
	ids, allowed, err := s.sessions(ctx)
	if err != nil {
		return nil, err
	}
	backend, ok := s.memory.(AttributedMemoryStore)
	if !ok {
		return nil, ErrBotUnsupported
	}
	rows, err := backend.SearchAttributed(ctx, ids, filter, query, maxResults, minScore, category)
	if err != nil {
		return nil, err
	}
	if err := s.check(ctx, rows, allowed); err != nil {
		return nil, err
	}
	for _, row := range rows {
		a := row.Attribution
		if a == nil || (filter.GoalID != uuid.Nil && a.GoalID != filter.GoalID) || (filter.RunID != uuid.Nil && a.RunID != filter.RunID) || (!filter.IncludeSuperseded && a.SupersededBy != "") {
			return nil, ErrBotForbidden
		}
	}
	return rows, nil
}
