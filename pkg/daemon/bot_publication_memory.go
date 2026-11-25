package daemon

import (
	"context"
	"errors"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/launcher"
	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
)

func (w *BotWorker) indexPublication(ctx context.Context, repo store.BotStore, p *store.BotPublication) error {
	// Old unit compositions without a file backend cannot exercise file-first IO.
	if w.config.Sandbox == nil {
		return nil
	}
	backend, ok := w.config.Sandbox.(sandbox.BotHomeBackend)
	if !ok {
		return store.ErrBotUnsupported
	}
	receipts, ok := repo.(store.BotPublicationMemoryStore)
	if !ok {
		return store.ErrBotUnsupported
	}
	b, err := repo.Get(ctx, p.BotID)
	if err != nil {
		return err
	}
	files := &launcher.BotHomeManager{Backend: backend, Bots: repo, Scope: b.Scope}
	files.AuthorizeFileCommit = func(c context.Context, h store.BotHomeBinding, commit func() error) error {
		return receipts.WithBotPublicationFileCommit(c, h, p.RunID, p.ReportSequence, commit)
	}
	index := bot.MemoryIndex{Files: files, Receipts: bot.PublicationMemoryResolver{Store: receipts, Files: files}}
	for attempt := 0; attempt < 3; attempt++ {
		_, err = index.Update(ctx, p.BotID, bot.MemoryIndexReceipt{RunID: p.RunID, Publication: p.ReportSequence})
		if !errors.Is(err, store.ErrBotConflict) {
			return err
		}
	}
	return err
}
