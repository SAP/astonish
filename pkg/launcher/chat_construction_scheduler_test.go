package launcher

import (
	"context"
	"fmt"
	"testing"

	"github.com/SAP/astonish/pkg/chatruntime"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/provider"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/tools"
	"google.golang.org/adk/session"
)

type runtimeMemoryStore struct {
	store.MemoryStore
	entries  []store.MemoryEntry
	searches int
}

func (s *runtimeMemoryStore) Search(context.Context, string, int, float64) ([]store.MemorySearchResult, error) {
	s.searches++
	return nil, nil
}
func (s *runtimeMemoryStore) Add(_ context.Context, e store.MemoryEntry) error {
	s.entries = append(s.entries, e)
	return nil
}

// Both fixtures run the actual shared tools. No daemon scheduler or model call
// is used; the process-wide scheduler is a tripwire, not an execution harness.
type runtimeSchedulerStore struct {
	store.SchedulerStore
	jobs []*store.ScheduledJob
}

func (s *runtimeSchedulerStore) Add(_ context.Context, j *store.ScheduledJob) error {
	j.ID = fmt.Sprint(len(s.jobs) + 1)
	s.jobs = append(s.jobs, j)
	return nil
}
func (s *runtimeSchedulerStore) Update(_ context.Context, j *store.ScheduledJob) error { return nil }
func (s *runtimeSchedulerStore) GetByName(_ context.Context, name string) *store.ScheduledJob {
	for _, j := range s.jobs {
		if j.Name == name {
			return j
		}
	}
	return nil
}
func (s *runtimeSchedulerStore) List(context.Context) []*store.ScheduledJob { return s.jobs }

type runtimeAmbientScheduler struct {
	tools.SchedulerAccess
	calls int
}

func (s *runtimeAmbientScheduler) ListJobs() []*tools.SchedulerJob         { s.calls++; return nil }
func (s *runtimeAmbientScheduler) GetJobByName(string) *tools.SchedulerJob { s.calls++; return nil }
func (s *runtimeAmbientScheduler) AddJob(*tools.SchedulerJob) error        { s.calls++; return nil }
func (s *runtimeAmbientScheduler) RunNow(context.Context, string) (string, error) {
	s.calls++
	return "ambient", nil
}

type scopedSchedulerInvocation struct {
	runtimeTestToolContext
	session string
}

func (c scopedSchedulerInvocation) SessionID() string { return c.session }

type schedulerRequestMarker struct{}

func TestChatRuntimeScopedSchedulerAuthority(t *testing.T) {
	ambient := &runtimeAmbientScheduler{}
	tools.SetSchedulerAccess(ambient)
	t.Cleanup(func() { tools.SetSchedulerAccess(nil) })
	for _, configured := range []bool{false, true} {
		for _, user := range []string{"alice", "bob"} {
			t.Run(fmt.Sprintf("%s/configured=%v", user, configured), func(t *testing.T) {
				f := newBotFactoryFixture(t)
				p := f.req.Principal
				p.Subject = user
				jobs := &runtimeSchedulerStore{}
				f.services.Scheduler = nil
				f.services.PersonalScheduler = nil
				if configured {
					f.services.PersonalScheduler = jobs
				}
				called := 0
				cfg := &config.AppConfig{}
				in, err := chatruntime.Build(t.Context(), chatruntime.BuildOptions{Principal: p, Services: f.services, ResourceScope: chatruntime.ResourceScopePersonal, AppConfig: cfg, RunJob: func(ctx context.Context, id string) (string, error) {
					called++
					principal, _ := execution.PrincipalFromContext(ctx)
					if principal.Subject != user || store.UserIDFromContext(ctx) != user || ctx.Value(schedulerRequestMarker{}) != "current" || store.SessionIDFromContext(ctx) != user+"-session" {
						t.Error("callback lost current request identity/session")
					}
					if len(jobs.jobs) != 1 || jobs.jobs[0].ID != id {
						t.Error("callback received another store's job")
					}
					return user + " result", nil
				}})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = in.Close() })
				runtime, err := NewScopedChatRuntime(t.Context(), ChatRuntimeDependencies{Inputs: in, Config: &ChatFactoryConfig{AppConfig: cfg}, LLM: provider.NewPlaceholderLLM(), Sessions: session.InMemoryService(), Resources: &ChatCatalogResources{Pool: &catalogTestPool{}}}, ChatRuntimeHooks{})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(runtime.Cleanup)
				inherited := store.WithPersonalSchedulerStore(t.Context(), &runtimeSchedulerStore{})
				inherited = store.WithRunJobFunc(inherited, func(context.Context, string) (string, error) {
					t.Fatal("inherited scheduler callback used")
					return "", nil
				})
				ctx := scopedSchedulerInvocation{runtimeTestToolContext: runtimeTestToolContext{lifetime: context.WithValue(inherited, schedulerRequestMarker{}, "current")}, session: user + "-session"}
				run := func(name string, args map[string]any) (map[string]any, error) {
					return runtime.ChatAgent.ToolIndex.GetToolEntry(name).Tool.(chatRuntimeRunnable).Run(ctx, args)
				}
				// A retained executable cannot be invoked as another principal or
				// after cancellation, even when its store is configured.
				original := ctx.lifetime
				other := p
				other.Subject = "another-owner"
				ctx.lifetime, err = execution.WithPrincipal(original, other)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = run("list_scheduled_jobs", map[string]any{}); err == nil {
					t.Fatal("foreign principal accepted")
				}
				cancelled, cancel := context.WithCancel(original)
				cancel()
				ctx.lifetime = cancelled
				if _, err = run("list_scheduled_jobs", map[string]any{}); err == nil {
					t.Fatal("cancelled request accepted")
				}
				ctx.lifetime = original
				result, err := run("schedule_job", map[string]any{"name": "daily", "mode": "adaptive", "schedule": "0 9 * * *", "instructions": "test fixture", "test_first": true, "scope": "personal"})
				if configured {
					if err != nil || result["status"] != "test_complete" || called != 1 {
						t.Fatalf("create=%v err=%v calls=%d", result, err, called)
					}
					if jobs.jobs[0].OwnerID != user || jobs.jobs[0].Scope != store.JobScopePersonal || jobs.jobs[0].Enabled {
						t.Fatal("wrong owner/scope or test job enabled")
					}
					result, err = run("list_scheduled_jobs", map[string]any{})
					if err != nil || fmt.Sprint(result["count"]) != "1" {
						t.Fatalf("list=%v err=%v", result, err)
					}
				} else {
					if err == nil {
						t.Fatalf("missing scoped scheduler accepted: %v", result)
					}
					_, _ = run("list_scheduled_jobs", map[string]any{})
				}
			})
		}
	}
	if ambient.calls != 0 {
		t.Fatalf("scoped runtime invoked ambient scheduler %d times", ambient.calls)
	}
}
