package launcher

import (
	"errors"
	"testing"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/tools"
)

func TestBotScheduleUpdatePatchBoundary(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			f, repo := scheduleFixture(t, scope)
			resources, ctx := scheduleFactory(t, f, repo)
			proposal := decodeScheduleReview(t, basicCall(t, resources, ctx, "schedule_job", map[string]any{"name": "Daily", "mode": "adaptive", "schedule": "0 9 * * *", "instructions": "Report weather"}))
			resources.Cleanup()
			if _, err := applyScheduleReview(f.ctx, repo, f.req.Bot.ID, proposal); err != nil {
				t.Fatal(err)
			}
			resources, ctx = scheduleFactory(t, f, repo)
			defer resources.Cleanup()
			var chosen botRunnableTool
			for _, entry := range resources.Agent.Tools {
				if entry.Name() == "update_scheduled_job" {
					chosen = entry.(botRunnableTool)
				}
			}
			if chosen == nil {
				t.Fatal("update missing")
			}
			for _, args := range []map[string]any{
				{"job_id": proposal.JobID},
				{"job_id": proposal.JobID, "enabled": nil},
				{"job_id": proposal.JobID, "schedule": ""},
				{"job_id": proposal.JobID, "timezone": ""},
				{"job_id": proposal.JobID, "instructions": "Use Celsius"},
				{"job_id": proposal.JobID, "name": "renamed"},
				{"job_id": proposal.JobID, "patch": map[string]any{"schedule": "*/2 * * * *"}},
				{"job_id": proposal.JobID, "schedule": "*/2 * * * *", "instructions": "Use Celsius"},
			} {
				if out, err := chosen.Run(ctx, args); err == nil {
					t.Errorf("invalid patch accepted: %v => %v", args, out)
				}
			}
			// Internal callers must still reject empty mutations even without schema validation.
			for _, mandate := range []*bot.MandateExecution{nil, {}} {
				request := f.req
				request.Mandate = mandate
				adapter := botScheduleTools{request: request, repo: repo, services: f.services}
				if _, err := adapter.update(ctx, tools.UpdateScheduledJobArgs{JobID: proposal.JobID}); !errors.Is(err, store.ErrBotInvalid) {
					t.Errorf("empty internal patch: %v", err)
				}
			}
			// Top-level false is a real patch, not a missing value.
			paused := decodeScheduleReview(t, basicCall(t, resources, ctx, "update_scheduled_job", map[string]any{"job_id": proposal.JobID, "enabled": false}))
			if paused.Review.Action != store.BotTriggerDisable {
				t.Fatalf("false lost: %+v", paused)
			}
			changed := decodeScheduleReview(t, basicCall(t, resources, ctx, "update_scheduled_job", map[string]any{"job_id": proposal.JobID, "schedule": "*/2 * * * *"}))
			tr := changed.Review.Body.Definition
			if tr == nil || tr.Cron != "*/2 * * * *" || tr.Timezone != "UTC" || tr.Instructions != "Report weather" {
				t.Fatalf("patch lost preserved fields: %+v", tr)
			}
			current, err := repo.GetVersion(f.ctx, f.req.Bot.ID, f.req.Version.Version)
			if err != nil || len(current.Triggers) != 1 || current.Triggers[0].Cron != "0 9 * * *" {
				t.Fatalf("capture mutated persisted schedule: %+v %v", current, err)
			}
		})
	}
}
