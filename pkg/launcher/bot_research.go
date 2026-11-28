package launcher

import (
	"context"
	"errors"
	"math"
	"sync"

	"github.com/google/uuid"
)

type BotResearchMode string

const (
	BotResearchOpenWeb          BotResearchMode = "open_web"
	BotResearchExplicitURLs     BotResearchMode = "explicit_urls"
	BotResearchProvidedMaterial BotResearchMode = "provided_material"
)

type BotResearchState string

const (
	BotResearchNeedsSearch BotResearchState = "needs_search"
	BotResearchSearching   BotResearchState = "searching"
	BotResearchSearched    BotResearchState = "searched"
	BotResearchEmpty       BotResearchState = "empty"
	BotResearchFailed      BotResearchState = "failed"
	BotResearchBlocked     BotResearchState = "blocked"
	BotResearchInterrupted BotResearchState = "interrupted"
)

// Episode authority is process-private and run/session-bound. Restored prose and
// child tool results cannot manufacture search success or unlock the parent.
type BotResearchEpisode struct {
	mu             sync.Mutex
	changed        chan struct{}
	run            uuid.UUID
	session        string
	mode           BotResearchMode
	state          BotResearchState
	generation     uint64
	urls           map[string]bool
	reserved       int64
	validateDetail func(context.Context) error
}

func (e *BotResearchEpisode) searchCalls() int64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.reserved
}
func (e *BotResearchEpisode) recordSearchReservation() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.reserved < 0 || e.reserved == math.MaxInt64 {
		return errors.New("search accounting overflow")
	}
	e.reserved++
	return nil
}
func (e *BotResearchEpisode) setDetailValidator(validate func(context.Context) error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.validateDetail = validate
}

func NewBotResearchEpisode(run uuid.UUID, session string, mode BotResearchMode, urls []string) (*BotResearchEpisode, error) {
	if run == uuid.Nil || session == "" {
		return nil, errors.New("research run/session binding required")
	}
	e := &BotResearchEpisode{run: run, session: session, mode: mode, state: BotResearchNeedsSearch, changed: make(chan struct{}), urls: map[string]bool{}}
	switch mode {
	case BotResearchOpenWeb:
	case BotResearchExplicitURLs:
		if len(urls) == 0 || len(urls) > 20 {
			return nil, errors.New("explicit admitted URLs required")
		}
		for _, u := range urls {
			e.urls[u] = true
		}
	case BotResearchProvidedMaterial:
	default:
		return nil, errors.New("invalid research mode")
	}
	return e, nil
}
func (e *BotResearchEpisode) State() BotResearchState {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.state
}

// setStateLocked broadcasts completion or interruption to every queued search.
// A canceled waiter interrupts the episode; it never owns/releases an active
// generation, and late observations cannot restore authority after cancellation.
func (e *BotResearchEpisode) setStateLocked(state BotResearchState) {
	e.state = state
	close(e.changed)
	e.changed = make(chan struct{})
}

// Begin serializes concurrent configured searches without holding the mutex
// during provider I/O or waits. Binding and cancellation are rechecked after
// every wakeup; quotas remain charged only by the configured executable.
func (e *BotResearchEpisode) Begin(ctx context.Context, run uuid.UUID, session string) (uint64, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for {
		if run != e.run || session != e.session {
			return 0, errors.New("research episode dispatch denied")
		}
		if ctx.Err() != nil {
			e.setStateLocked(BotResearchInterrupted)
			return 0, ctx.Err()
		}
		if e.mode != BotResearchOpenWeb || e.state == BotResearchInterrupted {
			return 0, errors.New("research episode dispatch denied")
		}
		if e.state != BotResearchSearching {
			e.generation++
			e.setStateLocked(BotResearchSearching)
			return e.generation, nil
		}
		changed := e.changed
		e.mu.Unlock()
		select {
		case <-changed:
		case <-ctx.Done():
		}
		e.mu.Lock()
	}
}

// Observe is invoked only by the exact configured executable after dispatch.
func (e *BotResearchEpisode) observe(ctx context.Context, generation uint64, sources int, err error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.generation != generation || e.state != BotResearchSearching {
		return
	}
	if ctx.Err() != nil {
		e.setStateLocked(BotResearchInterrupted)
		return
	}
	switch {
	case err != nil:
		e.setStateLocked(BotResearchFailed)
	case sources == 0:
		e.setStateLocked(BotResearchEmpty)
	default:
		e.setStateLocked(BotResearchSearched)
	}
}
func (e *BotResearchEpisode) AllowDetail(ctx context.Context, run uuid.UUID, session, url string) error {
	e.mu.Lock()
	if run != e.run || session != e.session {
		e.mu.Unlock()
		return errors.New("detail retrieval belongs to a different research episode")
	}
	if ctx.Err() != nil {
		e.setStateLocked(BotResearchInterrupted)
		e.mu.Unlock()
		return ctx.Err()
	}
	validate := e.validateDetail
	e.mu.Unlock()
	// Discovery never replaces current route/grant/lease authority.
	if validate != nil {
		if err := validate(ctx); err != nil {
			return err
		}
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if run != e.run || session != e.session {
		return errors.New("detail retrieval belongs to a different research episode")
	}
	if ctx.Err() != nil {
		e.setStateLocked(BotResearchInterrupted)
		return ctx.Err()
	}
	if e.mode == BotResearchProvidedMaterial {
		return errors.New("provided material admits no external discovery; ask owner for public research scope")
	}
	if e.mode == BotResearchExplicitURLs && e.urls[url] {
		return nil
	}
	if e.mode == BotResearchOpenWeb && e.state == BotResearchSearched {
		return nil
	}
	return errors.New("configured search must return sources before detail retrieval; retry an admitted facet or report search unavailable")
}
