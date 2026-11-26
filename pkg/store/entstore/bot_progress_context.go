package entstore

import (
	"context"
	"fmt"

	"github.com/SAP/astonish/pkg/store"
)

type botProgressContextRecord struct {
	Binding store.BotProgressBinding `json:"binding"`
	Digest  string                   `json:"digest"`
	Data    []byte                   `json:"data"`
}

var _ store.BotProgressContextStore = (*personalBotStore)(nil)
var _ store.BotProgressContextStore = (*teamBotStore)(nil)

func (s *personalBotStore) SaveProgressContext(ctx context.Context, m store.BotProgressMutation, data []byte) (string, string, error) {
	return s.repository().SaveProgressContext(ctx, m, data)
}
func (s *teamBotStore) SaveProgressContext(ctx context.Context, m store.BotProgressMutation, data []byte) (string, string, error) {
	return s.repository().SaveProgressContext(ctx, m, data)
}
func (s *personalBotStore) LoadProgressContext(ctx context.Context, m store.BotProgressMutation, ref, digest string) ([]byte, error) {
	return s.repository().LoadProgressContext(ctx, m, ref, digest)
}
func (s *teamBotStore) LoadProgressContext(ctx context.Context, m store.BotProgressMutation, ref, digest string) ([]byte, error) {
	return s.repository().LoadProgressContext(ctx, m, ref, digest)
}

func progressContextKey(m store.BotProgressMutation, digest string) string {
	return fmt.Sprintf("progress-context:%s:%d:%s", m.Binding.RunID, m.Binding.WorkRevision, digest)
}

func (r *botRepository) SaveProgressContext(ctx context.Context, m store.BotProgressMutation, data []byte) (string, string, error) {
	if len(data) == 0 || len(data) > store.BotProgressMaxBytes {
		return "", "", store.ErrBotInvalid
	}
	data = append([]byte(nil), data...)
	digest := store.BotProgressContextDigest(data)
	ref := progressContextKey(m, digest)
	err := r.db.Transaction(ctx, func(tx botDB) error {
		rec, run, err := r.progressMutation(ctx, tx, m)
		if err != nil {
			return err
		}
		var old botProgressContextRecord
		if err = progressRead(ctx, tx, m.Binding.BotID, ref, &old); err == nil {
			if !old.Binding.Matches(m.Binding) || old.Digest != digest || store.BotProgressContextDigest(old.Data) != digest {
				return store.ErrBotConflict
			}
			return nil
		} else if err != store.ErrBotNotFound {
			return err
		}
		return r.progressWrite(ctx, tx, rec, run, m, ref, botProgressContextRecord{Binding: m.Binding, Digest: digest, Data: data})
	})
	if err != nil {
		return "", "", err
	}
	return ref, digest, nil
}

func (r *botRepository) LoadProgressContext(ctx context.Context, m store.BotProgressMutation, ref, digest string) ([]byte, error) {
	if len(digest) != 64 || ref != progressContextKey(m, digest) {
		return nil, store.ErrBotInvalid
	}
	var data []byte
	err := r.db.Transaction(ctx, func(tx botDB) error {
		_, run, err := r.progressMutation(ctx, tx, m)
		if err != nil {
			return err
		}
		if run.Progress == nil || run.Progress.Checkpoint.ContextRef != ref || run.Progress.Checkpoint.ContextDigest != digest {
			return store.ErrBotConflict
		}
		var saved botProgressContextRecord
		if err := progressRead(ctx, tx, m.Binding.BotID, ref, &saved); err != nil {
			return err
		}
		if !saved.Binding.Matches(m.Binding) || saved.Digest != digest || len(saved.Data) == 0 || len(saved.Data) > store.BotProgressMaxBytes || store.BotProgressContextDigest(saved.Data) != digest {
			return store.ErrBotConflict
		}
		data = append([]byte(nil), saved.Data...)
		return nil
	})
	return data, err
}
