package daemon

import (
	"context"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/store"
)

// botStartupReadRuntime ends preflight's read operation before entering any
// runtime method. Closing invalidates every retained/rebound startup context;
// the factory and individual runtime operations own their separate lifetimes.
// Keep this at the daemon composition boundary, not in generic Supervisor.
type botStartupReadRuntime struct {
	bot.Runtime
	end func()
}

func (r botStartupReadRuntime) Start(ctx context.Context, q bot.RunRequest, sink bot.EventSink) (bot.RunResult, error) {
	r.end()
	return r.Runtime.Start(ctx, q, sink)
}

func (r botStartupReadRuntime) Resume(ctx context.Context, q bot.RunRequest, sink bot.EventSink) (bot.RunResult, error) {
	r.end()
	return r.Runtime.Resume(ctx, q, sink)
}

func (r botStartupReadRuntime) Cancel(ctx context.Context, lease store.BotLease) error {
	r.end()
	return r.Runtime.Cancel(ctx, lease)
}
