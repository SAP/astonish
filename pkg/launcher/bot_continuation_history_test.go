package launcher

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
)

// ListRuns hydrates disk-backed historical bodies. Optional bootstrap context
// must not call it, even when it would exceed the old two-second lookup budget.
func TestBotFileFirstSlowHistoryDoesNotBlockContinuation(t *testing.T) {
	for _, interactive := range []bool{false, true} {
		t.Run(map[bool]string{false: "autonomous", true: "interactive"}[interactive], func(t *testing.T) {
			f, repo := newBotFactoryCallbackFixture(t, store.BotScopePersonal)
			f.services.PersonalSessions = &provenanceSessionStore{}
			repo.beforeRead = func(c context.Context) { <-c.Done() }
			if interactive {
				f.req.Run.ExecutionMode = execution.BotExecutionModeChatInteractive
				f.req.Run.CallerCeiling = &execution.BotCallerCeiling{Principal: f.req.Principal, ExpiresAt: time.Now().Add(time.Hour)}
				f.req.Authorization = interactiveFactoryAuthorization{}
				f.req.Policy = nil
				f.cfg.Prepare = func(context.Context, bot.RunRequest, *BotChatScope) (*BotChatSafeResources, error) {
					return &BotChatSafeResources{ChatConfig: &config.AppConfig{}, Catalog: &ChatCatalogResources{Pool: &catalogTestPool{}}, WorkspaceDir: "/isolated", VerifyIsolation: func(context.Context) error { return nil }, Cleanup: func() {}}, nil
				}
			}
			c, cancel := context.WithTimeout(f.ctx, 5*time.Second)
			defer cancel()
			f.ctx = c
			resources, err := f.construct(t)
			if err != nil {
				t.Fatalf("optional slow history failed continuation construction: %v", err)
			}
			defer resources.Cleanup()
			if resources.PreviousWork != nil {
				previous, err := resources.PreviousWork(c)
				if err != nil || previous != nil {
					t.Fatalf("historical work bootstrap: %v %v", previous, err)
				}
			}
			if resources.PriorRun != nil {
				// This callback is optional occurrence metadata, not the active
				// lease boundary. A manual continuation needs no repository read.
				repo.getErr = errors.New("unexpected optional prior-run lookup")
				prior, err := resources.PriorRun(c)
				if err != nil || prior != nil {
					t.Fatalf("manual continuation looked up prior occurrence: %v %v", prior, err)
				}
				repo.getErr = nil
			}
			if len(repo.runPages) != 0 {
				t.Fatalf("bootstrap hydrated %d history pages", len(repo.runPages))
			}
			if c.Err() != nil {
				t.Fatalf("optional history canceled active run: %v", c.Err())
			}
		})
	}
}

type continuationExactWorkRepo struct {
	botFactoryTestRepo
	state  *store.BotWorkState
	err    error
	loaded store.BotLease
}

func (r *continuationExactWorkRepo) LoadWork(_ context.Context, lease store.BotLease) (*store.BotWorkState, error) {
	r.loaded = lease
	return r.state, r.err
}
func (r *continuationExactWorkRepo) SaveWork(context.Context, store.BotWorkUpdate) (*store.BotWorkState, error) {
	return nil, store.ErrBotUnsupported
}

func TestBotFileFirstHistoryRemovalPreservesExactWork(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			f := newBotFactoryFixture(t)
			f.req.Bot.Scope = scope
			state := store.NewBotWorkState(store.BotWorkContract{BotID: f.req.Bot.ID, RunID: f.req.Run.ID, Version: f.req.Version.Version, Instructions: "exact current assignment"})
			repo := &continuationExactWorkRepo{botFactoryTestRepo: botFactoryTestRepo{fixture: f}, state: state}
			if scope == store.BotScopePersonal {
				f.services.PersonalBots = repo
			} else {
				f.services.Bots = repo
			}
			resources, err := f.construct(t)
			if err != nil {
				t.Fatal(err)
			}
			defer resources.Cleanup()
			got, err := resources.Work.LoadWork(f.ctx, f.req.Run.Lease)
			if err != nil || got != state || repo.loaded != f.req.Run.Lease || resources.PreviousWork != nil {
				t.Fatalf("current work changed: %v %v", got, err)
			}
			for _, failure := range []error{store.ErrBotForbidden, store.ErrBotConflict, errors.New("corrupt active checkpoint"), context.DeadlineExceeded} {
				repo.state, repo.err = nil, failure
				if _, err := resources.Work.LoadWork(f.ctx, f.req.Run.Lease); !errors.Is(err, failure) {
					t.Fatalf("active failure swallowed: %v", err)
				}
			}
		})
	}
}
