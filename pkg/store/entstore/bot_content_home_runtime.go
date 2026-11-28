package entstore

import (
	"context"
	"encoding/json"
	"errors"
	"sync"

	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
)

// Runtime capability is installed once at daemon composition, never by a request.
// Existing scoped codecs retain the getter, so installation does not require
// mutating request repositories or lose their optional interfaces.
type botContentHomeRuntime struct {
	mu      sync.RWMutex
	backend sandbox.BotHomeFilesBackend
}

func (s *Store) InstallBotContentHomeBackend(backend sandbox.BotHomeFilesBackend) error {
	if backend == nil {
		return store.ErrBotUnsupported
	}
	s.botContentHome.mu.Lock()
	defer s.botContentHome.mu.Unlock()
	if s.botContentHome.backend != nil {
		return store.ErrBotConflict
	}
	s.botContentHome.backend = backend
	return nil
}
func (s *Store) BotContentHomeInstalled() bool {
	s.botContentHome.mu.RLock()
	defer s.botContentHome.mu.RUnlock()
	return s.botContentHome.backend != nil
}
func (s *Store) scopedBotContentCodec(org, team string) *botContentCodec {
	return newScopedBotContentCodec(botContentDirectory(s.dataDir), org, team, func() sandbox.BotHomeFilesBackend {
		s.botContentHome.mu.RLock()
		defer s.botContentHome.mu.RUnlock()
		return s.botContentHome.backend
	})
}

const botContentHomeSelectionKey = "bot_content_home_v1"

// This is a registry attestation, not an authority grant or a host path.
// It pins personal Home's original team rather than the viewer's active team.
type botContentHomeSelection struct {
	Home    store.BotHomeBinding   `json:"home"`
	Binding sandbox.BotHomeBinding `json:"binding"`
}

func openSelectedBotHome(ctx context.Context, staging store.BotContentStaging, scope store.BotContentScope, row *botStoredRecord, lookup botContentLookup, backend sandbox.BotHomeFilesBackend) (store.BotContentStaging, error) {
	raw, selected := row.Metadata[botContentHomeSelectionKey]
	if !selected {
		return staging, nil
	}
	fail := func(err error) (store.BotContentStaging, error) { return nil, errors.Join(err, staging.Close()) }
	if backend == nil {
		return fail(store.ErrBotUnsupported)
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return fail(err)
	}
	var selection botContentHomeSelection
	if err = json.Unmarshal(data, &selection); err != nil {
		return fail(err)
	}
	if selection.Home != row.Bot.Home {
		return fail(store.ErrBotConflict)
	}
	guard := func(commit func() error) error {
		current, err := lookup(ctx, row.Bot.ID)
		if err != nil {
			return err
		}
		if current.Bot.OwnerID != row.Bot.OwnerID || current.Bot.Scope != row.Bot.Scope || current.Bot.Home != selection.Home || current.Bot.PermanentDelete {
			return store.ErrBotConflict
		}
		return commit()
	}
	home, err := newBotHomeContent(staging, scope, row.Bot, selection.Binding, backend, guard)
	if err != nil {
		return fail(err)
	}
	// Content hydration is also used by read-only history, run inspection and
	// backup. It must never start compute. The retained-file transport owns cold
	// attachments; execution admission explicitly prepares the original session
	// before entering transactions that require live tools.
	return home, nil
}
