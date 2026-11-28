package daemon

import (
	"context"
	"errors"
	"testing"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/entstore"
	"github.com/google/uuid"
)

func startupReadsActive(ctx context.Context) bool {
	return entstore.WithBotContentReadOperationFromContext(context.Background(), ctx) != context.Background()
}

type startupBoundaryRuntime struct {
	bot.Runtime
	t        *testing.T
	retained context.Context
	calls    int
}

func (r *startupBoundaryRuntime) check(ctx context.Context) {
	r.t.Helper()
	r.calls++
	if startupReadsActive(ctx) || startupReadsActive(r.retained) {
		r.t.Fatal("startup immutable bytes survived runtime boundary")
	}
}
func (r *startupBoundaryRuntime) Start(ctx context.Context, _ bot.RunRequest, _ bot.EventSink) (bot.RunResult, error) {
	r.check(ctx)
	return bot.RunResult{}, nil
}
func (r *startupBoundaryRuntime) Resume(ctx context.Context, _ bot.RunRequest, _ bot.EventSink) (bot.RunResult, error) {
	r.check(ctx)
	return bot.RunResult{}, nil
}
func (r *startupBoundaryRuntime) Cancel(ctx context.Context, _ store.BotLease) error {
	r.check(ctx)
	return nil
}

func TestBotStartupReadsEndBeforeRuntime(t *testing.T) {
	for _, method := range []string{"start", "resume", "cancel"} {
		t.Run(method, func(t *testing.T) {
			ctx, end := entstore.WithBotContentReadOperation(context.Background())
			defer end()
			retained := entstore.WithBotContentReadOperationFromContext(botWorkerContext{ctx}, ctx)
			if !startupReadsActive(retained) {
				t.Fatal("missing startup operation")
			}
			probe := &startupBoundaryRuntime{t: t, retained: retained}
			r := botStartupReadRuntime{Runtime: probe, end: end}
			var err error
			switch method {
			case "start":
				_, err = r.Start(ctx, bot.RunRequest{}, nil)
			case "resume":
				_, err = r.Resume(ctx, bot.RunRequest{}, nil)
			case "cancel":
				err = r.Cancel(ctx, store.BotLease{})
			}
			if err != nil || probe.calls != 1 {
				t.Fatalf("calls=%d err=%v", probe.calls, err)
			}
		})
	}
}

type startupAdapterFailureRuntime struct {
	bot.Runtime
	ctx context.Context
}

func (r *startupAdapterFailureRuntime) Start(ctx context.Context, _ bot.RunRequest, _ bot.EventSink) (bot.RunResult, error) {
	r.ctx = ctx
	if !startupReadsActive(ctx) {
		return bot.RunResult{}, errors.New("missing adapter read operation")
	}
	return bot.RunResult{}, store.ErrBotConflict
}

func (r *startupAdapterFailureRuntime) Resume(ctx context.Context, q bot.RunRequest, sink bot.EventSink) (bot.RunResult, error) {
	return r.Start(ctx, q, sink)
}

func TestBotStartupAdapterReadsCloseOnFailure(t *testing.T) {
	for _, resume := range []bool{false, true} {
		probe := &startupAdapterFailureRuntime{}
		r := botMeteredRuntime{Runtime: probe}
		var err error
		if resume {
			_, err = r.Resume(context.Background(), bot.RunRequest{}, nil)
		} else {
			_, err = r.Start(context.Background(), bot.RunRequest{}, nil)
		}
		if !errors.Is(err, store.ErrBotConflict) || probe.ctx == nil || startupReadsActive(probe.ctx) {
			t.Fatalf("resume=%v err=%v; failed adapter retained bytes", resume, err)
		}
	}
}

type startupFailureStore struct {
	store.BotStore
	ctx context.Context
	err error
}

func (s *startupFailureStore) GetRun(ctx context.Context, _, _ uuid.UUID) (*store.BotRun, error) {
	s.ctx = ctx
	if !startupReadsActive(ctx) {
		return nil, errors.New("missing startup operation")
	}
	return nil, s.err
}
func TestBotStartupReadsCloseOnPreflightFailure(t *testing.T) {
	want := errors.New("preflight storage failure")
	repo := &startupFailureStore{err: want}
	w := &BotWorker{}
	if err := w.execute(context.Background(), repo, nil, uuid.New(), uuid.New()); !errors.Is(err, want) {
		t.Fatal(err)
	}
	if repo.ctx == nil || startupReadsActive(repo.ctx) {
		t.Fatal("failed startup retained immutable bytes")
	}
}
