package tools

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/tool"
)

type schedulerLifetime = context.Context
type scopedSchedulerTestContext struct {
	tool.Context
	schedulerLifetime
}

func (c scopedSchedulerTestContext) Deadline() (time.Time, bool) {
	return c.schedulerLifetime.Deadline()
}
func (c scopedSchedulerTestContext) Done() <-chan struct{} { return c.schedulerLifetime.Done() }
func (c scopedSchedulerTestContext) Err() error            { return c.schedulerLifetime.Err() }
func (c scopedSchedulerTestContext) Value(k any) any       { return c.schedulerLifetime.Value(k) }

func TestScopedSchedulerNoAmbientFallback(t *testing.T) {
	old := schedulerAccessVar
	ambient := &mockSchedulerAccess{jobs: []*SchedulerJob{{ID: "ambient", Name: "ambient"}}}
	schedulerAccessVar = ambient
	t.Cleanup(func() { schedulerAccessVar = old })
	ctx := scopedSchedulerTestContext{schedulerLifetime: WithScopedSchedulerContext(t.Context())}
	list, err := listScheduledJobs(ctx, ListScheduledJobsArgs{})
	if err != nil || list.Count != 0 || !strings.Contains(list.Message, "not available") {
		t.Fatalf("list=%+v err=%v", list, err)
	}
	if _, err = scheduleJob(ctx, ScheduleJobArgs{}); err == nil {
		t.Fatal("missing scheduler accepted")
	}
	update, _ := updateScheduledJob(ctx, UpdateScheduledJobArgs{JobID: "ambient", Name: "changed"})
	remove, _ := removeScheduledJob(ctx, RemoveScheduledJobArgs{JobID: "ambient"})
	if update.Status != "error" || remove.Status != "error" || len(ambient.jobs) != 1 || ambient.jobs[0].Name != "ambient" {
		t.Fatal("ambient mutation")
	}
	team := newMemSchedulerStore()
	ctx.schedulerLifetime = store.WithSchedulerStore(ctx.schedulerLifetime, team)
	if ss, _ := resolveSchedulerStore(ctx, store.JobScopePersonal); ss != nil {
		t.Fatal("explicit personal scope fell back to team")
	}
	result, err := scheduleJob(ctx, ScheduleJobArgs{Name: "test", Scope: store.JobScopeTeam, Mode: "adaptive", Schedule: "0 9 * * *", Instructions: "fixture", TestFirst: true})
	if err != nil || result.Status != "test_failed" || !strings.Contains(result.Message, "no execution path") {
		t.Fatalf("missing callback fell back: result=%+v err=%v", result, err)
	}
}
