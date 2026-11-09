package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/SAP/astonish/pkg/scheduler"
	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
)

// SchedulerAccess provides access to scheduler operations without importing
// the scheduler package directly (breaking the import cycle).
type SchedulerAccess interface {
	AddJob(job *SchedulerJob) error
	ListJobs() []*SchedulerJob
	RemoveJob(id string) error
	UpdateJob(job *SchedulerJob) error
	RunNow(ctx context.Context, jobID string) (string, error)
	GetJobByName(name string) *SchedulerJob
	ValidateCron(expr string) error
}

// SchedulerJob mirrors scheduler.Job but lives in the tools package to avoid
// import cycles. The daemon bridges between these types.
type SchedulerJob struct {
	ID        string            `json:"id"`
	Name      string            `json:"name"`
	Mode      string            `json:"mode"` // "routine", "adaptive", or "fleet_poll"
	Cron      string            `json:"cron"`
	Timezone  string            `json:"timezone,omitempty"`
	Flow      string            `json:"flow,omitempty"`
	Params    map[string]string `json:"params,omitempty"`
	Instr     string            `json:"instructions,omitempty"`
	Channel   string            `json:"channel,omitempty"`
	Target    string            `json:"target,omitempty"`
	Enabled   bool              `json:"enabled"`
	TestFirst bool              `json:"test_first,omitempty"`

	// Delivery configuration
	DeliveryMode   string              `json:"delivery_mode,omitempty"`   // owner, team, members, target
	MemberIDs      []string            `json:"member_ids,omitempty"`      // for "members" mode
	ChannelFilter  []string            `json:"channel_filter,omitempty"`  // restrict to these channel types
	MemberChannels map[string][]string `json:"member_channels,omitempty"` // per-member channel overrides

	// Scope is "personal" (default) or "team".
	Scope    string `json:"scope,omitempty"`
	TeamSlug string `json:"team_slug,omitempty"`

	// Runtime state (populated on list)
	LastRun    *time.Time `json:"last_run,omitempty"`
	LastStatus string     `json:"last_status,omitempty"`
	NextRun    *time.Time `json:"next_run,omitempty"`
	Failures   int        `json:"consecutive_failures,omitempty"`
	CreatedAt  time.Time  `json:"created_at,omitempty"`
}

// schedulerAccessVar holds the SchedulerAccess implementation.
// Set by the daemon via SetSchedulerAccess.
var schedulerAccessVar SchedulerAccess

// SetSchedulerAccess registers the scheduler access implementation.
// Called by the daemon after scheduler initialization.
func SetSchedulerAccess(sa SchedulerAccess) {
	schedulerAccessVar = sa
}

// getEffectiveSchedulerStore returns the team-scoped SchedulerStore from the
// context (platform mode) or nil (legacy personal mode uses schedulerAccessVar).
func getEffectiveSchedulerStore(ctx context.Context) store.SchedulerStore {
	if ctx == nil {
		return nil
	}
	return guardBotSchedules(store.SchedulerStoreFromContext(ctx))
}

// getPersonalSchedulerStore returns the user's personal SchedulerStore from context.
func getPersonalSchedulerStore(ctx context.Context) store.SchedulerStore {
	if ctx == nil {
		return nil
	}
	return guardBotSchedules(store.PersonalSchedulerStoreFromContext(ctx))
}

// resolveSchedulerStore picks the store for the given scope.
// Empty scope defaults to personal when a personal store is available, else team.
func resolveSchedulerStore(ctx context.Context, scope string) (store.SchedulerStore, string) {
	personal := getPersonalSchedulerStore(ctx)
	team := getEffectiveSchedulerStore(ctx)
	switch scope {
	case store.JobScopeTeam:
		return team, store.JobScopeTeam
	case store.JobScopePersonal:
		if personal != nil {
			return personal, store.JobScopePersonal
		}
		return team, store.JobScopeTeam
	default:
		if personal != nil {
			return personal, store.JobScopePersonal
		}
		return team, store.JobScopeTeam
	}
}

// findJobAcrossScopes looks up a job by ID/name in personal then team stores.
func findJobAcrossScopes(ctx context.Context, idOrName string) (*store.ScheduledJob, store.SchedulerStore, string) {
	personal := getPersonalSchedulerStore(ctx)
	team := getEffectiveSchedulerStore(ctx)
	for _, pair := range []struct {
		ss    store.SchedulerStore
		scope string
	}{
		{personal, store.JobScopePersonal},
		{team, store.JobScopeTeam},
	} {
		if pair.ss == nil {
			continue
		}
		if j := pair.ss.Get(ctx, idOrName); j != nil {
			j.Scope = pair.scope
			return j, pair.ss, pair.scope
		}
		if j := pair.ss.GetByName(ctx, idOrName); j != nil {
			j.Scope = pair.scope
			return j, pair.ss, pair.scope
		}
	}
	return nil, nil, ""
}

// storeJobToToolJob converts a store.ScheduledJob to a tools.SchedulerJob.
func storeJobToToolJob(sj *store.ScheduledJob) *SchedulerJob {
	return &SchedulerJob{
		ID:             sj.ID,
		Name:           sj.Name,
		Mode:           sj.Mode,
		Cron:           sj.Schedule.Cron,
		Timezone:       sj.Schedule.Timezone,
		Flow:           sj.Payload.Flow,
		Params:         sj.Payload.Params,
		Instr:          sj.Payload.Instructions,
		Channel:        sj.Delivery.Channel,
		Target:         sj.Delivery.Target,
		DeliveryMode:   sj.Delivery.Mode,
		MemberIDs:      sj.Delivery.MemberIDs,
		ChannelFilter:  sj.Delivery.ChannelFilter,
		MemberChannels: sj.Delivery.MemberChannels,
		Enabled:        sj.Enabled,
		LastRun:        sj.LastRun,
		LastStatus:     sj.LastStatus,
		NextRun:        sj.NextRun,
		Failures:       sj.ConsecutiveFailures,
		CreatedAt:      sj.CreatedAt,
		Scope:          sj.Scope,
		TeamSlug:       sj.TeamSlug,
	}
}

// toolJobToStoreJob converts a tools.SchedulerJob to a store.ScheduledJob.
func toolJobToStoreJob(tj *SchedulerJob) *store.ScheduledJob {
	return &store.ScheduledJob{
		ID:   tj.ID,
		Name: tj.Name,
		Mode: tj.Mode,
		Schedule: store.JobSchedule{
			Cron:     tj.Cron,
			Timezone: tj.Timezone,
		},
		Payload: store.JobPayload{
			Flow:         tj.Flow,
			Params:       tj.Params,
			Instructions: tj.Instr,
		},
		Delivery: store.JobDelivery{
			Channel:        tj.Channel,
			Target:         tj.Target,
			Mode:           tj.DeliveryMode,
			MemberIDs:      tj.MemberIDs,
			ChannelFilter:  tj.ChannelFilter,
			MemberChannels: tj.MemberChannels,
		},
		Enabled:             tj.Enabled,
		CreatedAt:           tj.CreatedAt,
		LastRun:             tj.LastRun,
		LastStatus:          tj.LastStatus,
		NextRun:             tj.NextRun,
		ConsecutiveFailures: tj.Failures,
		Scope:               tj.Scope,
		TeamSlug:            tj.TeamSlug,
	}
}

// --- schedule_job tool ---

type ScheduleJobArgs struct {
	Name         string            `json:"name" jsonschema:"A short descriptive name for this scheduled job"`
	Mode         string            `json:"mode" jsonschema:"Execution mode: 'routine' (run a flow with fixed params) or 'adaptive' (LLM-driven agentic execution)"`
	Schedule     string            `json:"schedule" jsonschema:"Cron expression (5-field: minute hour day month weekday). Example: '0 9 * * *' for 9 AM daily, '0 */2 * * *' for every 2 hours, '0 9 * * 1-5' for weekdays at 9 AM"`
	Timezone     string            `json:"timezone,omitempty" jsonschema:"IANA timezone (e.g. 'America/Sao_Paulo'). Defaults to system local timezone"`
	Flow         string            `json:"flow,omitempty" jsonschema:"Flow name to execute (required for routine mode)"`
	Params       map[string]string `json:"params,omitempty" jsonschema:"Flow parameters as key-value pairs (for routine mode)"`
	Instructions string            `json:"instructions,omitempty" jsonschema:"Task instructions for the AI agent (required for adaptive mode). Be specific and detailed. MUST include the exact output format the user last saw, with a concrete example copied from the conversation."`
	TestFirst    bool              `json:"test_first,omitempty" jsonschema:"If true, execute immediately to test, then ask user to confirm before enabling. Only set this AFTER the user has approved the test plan."`
	Scope        string            `json:"scope,omitempty" jsonschema:"Job ownership: 'personal' (default — uses your personal credentials, only you can manage it) or 'team' (shared team job using team credentials; requires team admin). Prefer 'personal' when the job needs a personal OAuth/API credential."`

	// Delivery configuration — ASK the user about these before scheduling
	DeliveryMode     string   `json:"delivery_mode,omitempty" jsonschema:"Who receives the results: 'owner' (only you), 'team' (all team members), 'members' (specific people). Default is 'owner'. Personal-scope jobs only support 'owner'. ALWAYS ask the user who should receive the scheduled results before creating the job."`
	DeliveryChannels []string `json:"delivery_channels,omitempty" jsonschema:"Which channels to deliver through (e.g. ['telegram', 'email', 'slack']). If empty, all linked channels are used. Ask the user which channel(s) they prefer for delivery."`
	DeliveryMembers  []string `json:"delivery_members,omitempty" jsonschema:"User IDs for 'members' delivery mode. Use list_team_members first to get available members and their linked channels, then ask the user which members should receive results. Not allowed for personal-scope jobs."`

	// Legacy fields (prefer delivery_mode instead)
	Channel string `json:"channel,omitempty" jsonschema:"DEPRECATED: Use delivery_mode instead. Legacy label for delivery channel."`
	Target  string `json:"target,omitempty" jsonschema:"DEPRECATED: Use delivery_mode instead. Legacy target ID for direct delivery."`
}

type ScheduleJobResult struct {
	Status  string `json:"status"`
	JobID   string `json:"job_id,omitempty"`
	Message string `json:"message"`
	Result  string `json:"result,omitempty"` // Only populated when test_first=true
}

func scheduleJob(ctx tool.Context, args ScheduleJobArgs) (ScheduleJobResult, error) {
	ss, jobScope := resolveSchedulerStore(ctx, args.Scope)
	if ss == nil && schedulerAccessVar == nil {
		return ScheduleJobResult{}, fmt.Errorf("scheduler is not available — the daemon must be running with scheduler enabled")
	}
	if args.Scope == store.JobScopeTeam && ss == nil {
		return ScheduleJobResult{
			Status:  "error",
			Message: "Team scheduler is not available in this context",
		}, nil
	}

	// Validate mode
	if args.Mode != "routine" && args.Mode != "adaptive" {
		return ScheduleJobResult{
			Status:  "error",
			Message: "Mode must be 'routine' or 'adaptive'. Note: 'fleet_poll' jobs are created automatically when a fleet plan is activated, not via this tool.",
		}, nil
	}

	// Validate required fields per mode
	if args.Mode == "routine" && args.Flow == "" {
		return ScheduleJobResult{
			Status:  "error",
			Message: "Routine mode requires a 'flow' name. If no flow exists for this task, call `distill_flow` first to create one from the conversation, then use the resulting flow name here.",
		}, nil
	}
	if args.Mode == "adaptive" && args.Instructions == "" {
		return ScheduleJobResult{
			Status:  "error",
			Message: "Adaptive mode requires 'instructions'",
		}, nil
	}

	// Validate cron expression
	if err := scheduler.ValidateCron(args.Schedule); err != nil {
		return ScheduleJobResult{
			Status:  "error",
			Message: fmt.Sprintf("Invalid cron expression: %v. Use 5-field format: minute hour day-of-month month day-of-week", err),
		}, nil
	}

	// Validate timezone if provided
	if args.Timezone != "" {
		if _, err := time.LoadLocation(args.Timezone); err != nil {
			return ScheduleJobResult{
				Status:  "error",
				Message: fmt.Sprintf("Invalid timezone %q: %v", args.Timezone, err),
			}, nil
		}
	}

	// Personal jobs may only deliver to the owner.
	if jobScope == store.JobScopePersonal {
		if args.DeliveryMode == "team" || args.DeliveryMode == "members" {
			return ScheduleJobResult{
				Status:  "error",
				Message: "Personal-scope jobs can only deliver to 'owner'. Use scope='team' for team/members delivery.",
			}, nil
		}
		if len(args.DeliveryMembers) > 0 {
			return ScheduleJobResult{
				Status:  "error",
				Message: "Personal-scope jobs cannot target delivery members. Use scope='team' instead.",
			}, nil
		}
	}

	// Reject duplicate names within the chosen scope
	if args.Name != "" {
		var existing *SchedulerJob
		if ss != nil {
			if sj := ss.GetByName(ctx, args.Name); sj != nil {
				existing = storeJobToToolJob(sj)
			}
		} else {
			existing = schedulerAccessVar.GetJobByName(args.Name)
		}
		if existing != nil {
			return ScheduleJobResult{
				Status:  "error",
				Message: fmt.Sprintf("A job named %q already exists (ID: %s). Use update_scheduled_job to modify or enable it instead of creating a duplicate.", args.Name, existing.ID),
			}, nil
		}
	}

	// Create job
	job := &SchedulerJob{
		Name:          args.Name,
		Mode:          args.Mode,
		Cron:          args.Schedule,
		Timezone:      args.Timezone,
		Flow:          args.Flow,
		Params:        args.Params,
		Instr:         args.Instructions,
		Channel:       args.Channel,
		Target:        args.Target,
		Enabled:       !args.TestFirst,
		TestFirst:     args.TestFirst,
		DeliveryMode:  args.DeliveryMode,
		MemberIDs:     args.DeliveryMembers,
		ChannelFilter: args.DeliveryChannels,
		Scope:         jobScope,
	}

	// Default delivery mode to "owner" if not specified
	if job.DeliveryMode == "" && job.Channel == "" && job.Target == "" {
		job.DeliveryMode = "owner"
	}
	if jobScope == store.JobScopePersonal {
		job.DeliveryMode = "owner"
		job.MemberIDs = nil
	}

	// Add job to store
	if ss != nil {
		storeJob := toolJobToStoreJob(job)
		storeJob.CreatedAt = time.Now()
		storeJob.LastStatus = "pending"
		storeJob.Scope = jobScope
		if uid := store.UserIDFromContext(ctx); uid != "" {
			storeJob.OwnerID = uid
		}
		// Capture active team for personal-job credential/flow fallback.
		if jobScope == store.JobScopePersonal {
			if tc := store.TenantContextFrom(ctx); tc != nil && tc.TeamSlug != "" {
				storeJob.TeamSlug = tc.TeamSlug
			} else if slug := store.TeamSlugFromContext(ctx); slug != "" {
				storeJob.TeamSlug = slug
			}
		}
		if err := ss.Add(ctx, storeJob); err != nil {
			return ScheduleJobResult{
				Status:  "error",
				Message: fmt.Sprintf("Failed to create job: %v", err),
			}, nil
		}
		job.ID = storeJob.ID
		if job.Enabled {
			storeJob.NextRun = scheduler.ComputeNextRun(storeJob.Schedule.Cron, storeJob.Schedule.Timezone)
			_ = ss.Update(ctx, storeJob)
		}
	} else {
		if err := schedulerAccessVar.AddJob(job); err != nil {
			return ScheduleJobResult{
				Status:  "error",
				Message: fmt.Sprintf("Failed to create job: %v", err),
			}, nil
		}
	}

	// If test_first, execute immediately with the same credential context cron will use.
	if args.TestFirst {
		var result string
		var execErr error
		if runJob := store.RunJobFuncFromContext(ctx); runJob != nil {
			result, execErr = runJob(ctx, job.ID)
		} else if schedulerAccessVar != nil {
			result, execErr = schedulerAccessVar.RunNow(ctx, job.ID)
		} else {
			execErr = fmt.Errorf("no execution path available (scheduler not configured)")
		}
		status := "test_complete"
		msg := fmt.Sprintf("Job %q created and tested (scope=%s). The job is currently DISABLED — ask the user if the test result looks good, and if so, call update_scheduled_job to enable it.", args.Name, jobScope)
		if execErr != nil {
			status = "test_failed"
			msg = fmt.Sprintf("Job %q created but test execution failed: %v. The job is DISABLED. Ask the user if they want to fix the issue or remove the job.", args.Name, execErr)
		}

		return ScheduleJobResult{
			Status:  status,
			JobID:   job.ID,
			Message: msg,
			Result:  result,
		}, nil
	}

	return ScheduleJobResult{
		Status:  "created",
		JobID:   job.ID,
		Message: fmt.Sprintf("Job %q created and enabled (scope=%s). Schedule: %s (%s)", args.Name, jobScope, args.Schedule, timezoneLabel(args.Timezone)),
	}, nil
}

// --- list_scheduled_jobs tool ---

type ListScheduledJobsArgs struct {
	Filter string `json:"filter,omitempty" jsonschema:"Optional filter to match jobs by name or mode (e.g., 'daily-report', 'flow'). If empty, lists all jobs."`
}

type ListScheduledJobsResult struct {
	Jobs    []JobSummary `json:"jobs"`
	Count   int          `json:"count"`
	Message string       `json:"message,omitempty"`
}

type JobSummary struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Mode     string `json:"mode"`
	Scope    string `json:"scope,omitempty"`
	Schedule string `json:"schedule"`
	Timezone string `json:"timezone,omitempty"`
	PlanKey  string `json:"plan_key,omitempty"` // fleet plan key for fleet_poll jobs
	Enabled  bool   `json:"enabled"`
	Status   string `json:"last_status"`
	NextRun  string `json:"next_run,omitempty"`
	Failures int    `json:"consecutive_failures,omitempty"`
}

func listScheduledJobs(ctx tool.Context, args ListScheduledJobsArgs) (ListScheduledJobsResult, error) {
	personal := getPersonalSchedulerStore(ctx)
	team := getEffectiveSchedulerStore(ctx)
	if personal == nil && team == nil && schedulerAccessVar == nil {
		return ListScheduledJobsResult{
			Message: "Scheduler is not available — the daemon must be running with scheduler enabled",
		}, nil
	}

	var jobs []*SchedulerJob
	if personal != nil || team != nil {
		if personal != nil {
			for _, sj := range personal.List(ctx) {
				tj := storeJobToToolJob(sj)
				tj.Scope = store.JobScopePersonal
				jobs = append(jobs, tj)
			}
		}
		if team != nil {
			for _, sj := range team.List(ctx) {
				tj := storeJobToToolJob(sj)
				tj.Scope = store.JobScopeTeam
				jobs = append(jobs, tj)
			}
		}
	} else {
		jobs = schedulerAccessVar.ListJobs()
	}
	summaries := make([]JobSummary, 0, len(jobs))
	for _, j := range jobs {
		if args.Filter != "" && !strings.Contains(j.Name, args.Filter) && !strings.Contains(j.Mode, args.Filter) && !strings.Contains(j.Scope, args.Filter) {
			continue
		}
		summary := JobSummary{
			ID:       j.ID,
			Name:     j.Name,
			Mode:     j.Mode,
			Scope:    j.Scope,
			Schedule: j.Cron,
			Timezone: j.Timezone,
			Enabled:  j.Enabled,
			Status:   j.LastStatus,
			Failures: j.Failures,
		}
		// For fleet_poll jobs, the Flow field holds the plan key
		if j.Mode == "fleet_poll" {
			summary.PlanKey = j.Flow
		}
		if j.NextRun != nil {
			summary.NextRun = j.NextRun.Format(time.RFC3339)
		}
		summaries = append(summaries, summary)
	}

	msg := fmt.Sprintf("%d scheduled job(s)", len(summaries))
	if len(summaries) == 0 {
		if args.Filter != "" {
			msg = fmt.Sprintf("No scheduled jobs matching %q", args.Filter)
		} else {
			msg = "No scheduled jobs"
		}
	}

	return ListScheduledJobsResult{
		Jobs:    summaries,
		Count:   len(summaries),
		Message: msg,
	}, nil
}

// --- remove_scheduled_job tool ---

type RemoveScheduledJobArgs struct {
	JobID   string `json:"job_id,omitempty" jsonschema:"The job ID to remove"`
	JobName string `json:"job_name,omitempty" jsonschema:"The job name to remove (alternative to job_id)"`
}

type RemoveScheduledJobResult struct {
	ToolResult
}

func removeScheduledJob(ctx tool.Context, args RemoveScheduledJobArgs) (RemoveScheduledJobResult, error) {
	personal := getPersonalSchedulerStore(ctx)
	team := getEffectiveSchedulerStore(ctx)
	if personal == nil && team == nil && schedulerAccessVar == nil {
		return RemoveScheduledJobResult{
			ToolResult: toolError("Scheduler is not available"),
		}, nil
	}

	jobID := args.JobID
	lookup := jobID
	if lookup == "" {
		lookup = args.JobName
	}
	if lookup == "" {
		return RemoveScheduledJobResult{
			ToolResult: toolError("Either job_id or job_name is required"),
		}, nil
	}

	if personal != nil || team != nil {
		job, ss, _ := findJobAcrossScopes(ctx, lookup)
		if job == nil || ss == nil {
			return RemoveScheduledJobResult{
				ToolResult: toolError("No job found with id/name %q", lookup),
			}, nil
		}
		if err := ss.Remove(ctx, job.ID); err != nil {
			return RemoveScheduledJobResult{
				ToolResult: toolError("Failed to remove job: %v", err),
			}, nil
		}
		return RemoveScheduledJobResult{
			ToolResult: ToolResult{Status: "removed", Message: "Job removed successfully"},
		}, nil
	}

	if jobID == "" && args.JobName != "" {
		if job := schedulerAccessVar.GetJobByName(args.JobName); job != nil {
			jobID = job.ID
		}
		if jobID == "" {
			return RemoveScheduledJobResult{
				ToolResult: toolError("No job found with name %q", args.JobName),
			}, nil
		}
	}
	if err := schedulerAccessVar.RemoveJob(jobID); err != nil {
		return RemoveScheduledJobResult{
			ToolResult: toolError("Failed to remove job: %v", err),
		}, nil
	}

	return RemoveScheduledJobResult{
		ToolResult: ToolResult{Status: "removed", Message: "Job removed successfully"},
	}, nil
}

// --- update_scheduled_job tool ---

type UpdateScheduledJobArgs struct {
	JobID    string `json:"job_id" jsonschema:"The job ID to update"`
	Enabled  *bool  `json:"enabled,omitempty" jsonschema:"Enable or disable the job"`
	Schedule string `json:"schedule,omitempty" jsonschema:"New cron expression"`
	Timezone string `json:"timezone,omitempty" jsonschema:"New timezone"`
	Name     string `json:"name,omitempty" jsonschema:"New job name"`
}

type UpdateScheduledJobResult struct {
	ToolResult
}

func updateScheduledJob(ctx tool.Context, args UpdateScheduledJobArgs) (UpdateScheduledJobResult, error) {
	personal := getPersonalSchedulerStore(ctx)
	team := getEffectiveSchedulerStore(ctx)
	if personal == nil && team == nil && schedulerAccessVar == nil {
		return UpdateScheduledJobResult{
			ToolResult: toolError("Scheduler is not available"),
		}, nil
	}

	if personal != nil || team != nil {
		job, ss, _ := findJobAcrossScopes(ctx, args.JobID)
		if job == nil || ss == nil {
			return UpdateScheduledJobResult{
				ToolResult: toolError("Job %q not found", args.JobID),
			}, nil
		}
		return updateScheduledJobFromStore(ctx, ss, job, args)
	}
	return updateScheduledJobFromAccess(args)
}

func updateScheduledJobFromStore(ctx context.Context, ss store.SchedulerStore, job *store.ScheduledJob, args UpdateScheduledJobArgs) (UpdateScheduledJobResult, error) {
	if job == nil {
		return UpdateScheduledJobResult{
			ToolResult: toolError("Job %q not found", args.JobID),
		}, nil
	}

	changes := []string{}
	if args.Enabled != nil {
		job.Enabled = *args.Enabled
		if *args.Enabled {
			changes = append(changes, "enabled")
		} else {
			changes = append(changes, "disabled")
		}
	}
	if args.Schedule != "" {
		if err := scheduler.ValidateCron(args.Schedule); err != nil {
			return UpdateScheduledJobResult{
				ToolResult: toolError("Invalid cron: %v", err),
			}, nil
		}
		job.Schedule.Cron = args.Schedule
		changes = append(changes, "schedule updated")
	}
	if args.Timezone != "" {
		if _, err := time.LoadLocation(args.Timezone); err != nil {
			return UpdateScheduledJobResult{
				ToolResult: toolError("Invalid timezone: %v", err),
			}, nil
		}
		job.Schedule.Timezone = args.Timezone
		changes = append(changes, "timezone updated")
	}
	if args.Name != "" {
		job.Name = args.Name
		changes = append(changes, "renamed")
	}

	// Recompute NextRun if schedule changed or job was enabled
	if job.Enabled {
		job.NextRun = scheduler.ComputeNextRun(job.Schedule.Cron, job.Schedule.Timezone)
	}

	if err := ss.Update(ctx, job); err != nil {
		return UpdateScheduledJobResult{
			ToolResult: toolError("Failed to update: %v", err),
		}, nil
	}

	msg := fmt.Sprintf("Job %q updated: %s", job.Name, strings.Join(changes, ", "))
	if job.Mode == "fleet_poll" && args.Schedule != "" {
		msg += ". Note: for fleet_poll jobs, consider deactivating and reactivating the fleet plan to keep the plan config in sync."
	}

	return UpdateScheduledJobResult{
		ToolResult: ToolResult{Status: "updated", Message: msg},
	}, nil
}

func updateScheduledJobFromAccess(args UpdateScheduledJobArgs) (UpdateScheduledJobResult, error) {
	jobs := schedulerAccessVar.ListJobs()
	var job *SchedulerJob
	for _, j := range jobs {
		if j.ID == args.JobID {
			job = j
			break
		}
	}
	if job == nil {
		for _, j := range jobs {
			if j.Name == args.JobID {
				job = j
				break
			}
		}
	}
	if job == nil {
		return UpdateScheduledJobResult{
			ToolResult: toolError("Job %q not found", args.JobID),
		}, nil
	}

	changes := []string{}
	if args.Enabled != nil {
		job.Enabled = *args.Enabled
		if *args.Enabled {
			changes = append(changes, "enabled")
		} else {
			changes = append(changes, "disabled")
		}
	}
	if args.Schedule != "" {
		if err := scheduler.ValidateCron(args.Schedule); err != nil {
			return UpdateScheduledJobResult{
				ToolResult: toolError("Invalid cron: %v", err),
			}, nil
		}
		job.Cron = args.Schedule
		changes = append(changes, "schedule updated")
	}
	if args.Timezone != "" {
		if _, err := time.LoadLocation(args.Timezone); err != nil {
			return UpdateScheduledJobResult{
				ToolResult: toolError("Invalid timezone: %v", err),
			}, nil
		}
		job.Timezone = args.Timezone
		changes = append(changes, "timezone updated")
	}
	if args.Name != "" {
		job.Name = args.Name
		changes = append(changes, "renamed")
	}

	if err := schedulerAccessVar.UpdateJob(job); err != nil {
		return UpdateScheduledJobResult{
			ToolResult: toolError("Failed to update: %v", err),
		}, nil
	}

	msg := fmt.Sprintf("Job %q updated: %s", job.Name, strings.Join(changes, ", "))
	if job.Mode == "fleet_poll" && args.Schedule != "" {
		msg += ". Note: for fleet_poll jobs, consider deactivating and reactivating the fleet plan to keep the plan config in sync."
	}

	return UpdateScheduledJobResult{
		ToolResult: ToolResult{Status: "updated", Message: msg},
	}, nil
}

// --- Tool constructors ---

// NewScheduleJobTool creates the schedule_job tool.
func NewScheduleJobTool() (tool.Tool, error) {
	return functiontool.New(functiontool.Config{
		Name: "schedule_job",
		Description: `Create a scheduled job on a cron schedule. Uses 5-field cron syntax.

CRITICAL RULES:
- NEVER change the mode from what the user requested. If user says "routine", use "routine". If the flow doesn't exist yet, use distill_flow to create it FIRST, then come back to scheduling.
- NEVER silently switch from "routine" to "adaptive" or vice versa. If there's a problem with the chosen mode, EXPLAIN the issue to the user and ask how to proceed.
- NEVER call this tool without completing ALL workflow steps below.
- Default scope is "personal" (uses the user's personal credentials). Use scope="team" only for shared team automation that should use team/service credentials. Do NOT ask the user to publish personal credentials to the team just to schedule a job — use personal scope instead.

WORKFLOW — Follow these steps IN ORDER, one at a time:

1. MODE SELECTION — Present BOTH options and wait for the user's explicit choice:
   - "routine": Runs a saved flow with fixed parameters. Best for repeatable, deterministic tasks. Requires an existing flow (use distill_flow to create one if needed).
   - "adaptive": An AI agent executes free-form instructions each run. Best for tasks needing web searches, analysis, or dynamic reasoning.
   You MUST wait for the user to explicitly choose. Do NOT pre-select or suggest a default.

2. SCHEDULE — Ask when and how often. Confirm the cron expression and timezone with the user before proceeding.

3. DELIVERY — You MUST ask about delivery preferences:
   - "Who should receive the results?" → delivery_mode: "owner" (just you), "team" (everyone), or "members" (specific people)
   - Personal-scope jobs only support delivery_mode "owner".
   - "Via which channel(s)?" → delivery_channels: ["telegram"], ["email"], etc. If not specified, all linked channels are used.
   - If "members" mode: call list_team_members first to show available members, then ask which should receive results.
   Do NOT skip this step. Do NOT assume "owner" without asking.

4. TEST — ALWAYS set test_first=true on the first creation. This runs the job immediately as a dry-run without enabling the schedule. Show the test results to the user and ask if the output looks good.

5. CONFIRM — After the user approves the test output, call update_scheduled_job to enable it.

Each step requires a separate user interaction. Never combine multiple steps into one message.`,
	}, scheduleJob)
}

// NewListScheduledJobsTool creates the list_scheduled_jobs tool.
func NewListScheduledJobsTool() (tool.Tool, error) {
	return functiontool.New(functiontool.Config{
		Name:        "list_scheduled_jobs",
		Description: "List all scheduled jobs with their status, next run time, and configuration.",
	}, listScheduledJobs)
}

// NewRemoveScheduledJobTool creates the remove_scheduled_job tool.
func NewRemoveScheduledJobTool() (tool.Tool, error) {
	return functiontool.New(functiontool.Config{
		Name:        "remove_scheduled_job",
		Description: "Remove a scheduled job by ID or name. The job will no longer execute.",
	}, removeScheduledJob)
}

// NewUpdateScheduledJobTool creates the update_scheduled_job tool.
func NewUpdateScheduledJobTool() (tool.Tool, error) {
	return functiontool.New(functiontool.Config{
		Name:        "update_scheduled_job",
		Description: "Update a scheduled job. Can enable/disable, change schedule, timezone, or name. Works for all job modes (routine, adaptive, fleet_poll). For fleet_poll jobs, schedule changes should ideally be done by deactivating and reactivating the fleet plan.",
	}, updateScheduledJob)
}

// GetSchedulerTools returns all scheduler tools. These should be appended to the
// tool list in the chat factory when the scheduler is available.
func GetSchedulerTools() ([]tool.Tool, error) {
	var tools []tool.Tool

	schedTool, err := NewScheduleJobTool()
	if err != nil {
		return nil, fmt.Errorf("schedule_job: %w", err)
	}
	tools = append(tools, schedTool)

	listTool, err := NewListScheduledJobsTool()
	if err != nil {
		return nil, fmt.Errorf("list_scheduled_jobs: %w", err)
	}
	tools = append(tools, listTool)

	removeTool, err := NewRemoveScheduledJobTool()
	if err != nil {
		return nil, fmt.Errorf("remove_scheduled_job: %w", err)
	}
	tools = append(tools, removeTool)

	updateTool, err := NewUpdateScheduledJobTool()
	if err != nil {
		return nil, fmt.Errorf("update_scheduled_job: %w", err)
	}
	tools = append(tools, updateTool)

	membersTool, err := NewListTeamMembersTool()
	if err != nil {
		return nil, fmt.Errorf("list_team_members: %w", err)
	}
	tools = append(tools, membersTool)

	return tools, nil
}

// timezoneLabel returns a human-friendly timezone label.
func timezoneLabel(tz string) string {
	if tz == "" {
		return "local timezone"
	}
	return tz
}

// SchedulerHTTPAccess implements SchedulerAccess by talking to the daemon's
// HTTP API. Used by the console/chat when running outside the daemon process.
type SchedulerHTTPAccess struct {
	BaseURL string // e.g., "http://localhost:9393"
}

func (s *SchedulerHTTPAccess) AddJob(job *SchedulerJob) error {
	data, err := json.Marshal(job)
	if err != nil {
		return fmt.Errorf("failed to marshal job: %w", err)
	}
	resp, err := http.Post(s.BaseURL+"/api/scheduler/jobs", "application/json", strings.NewReader(string(data)))
	if err != nil {
		return fmt.Errorf("failed to contact daemon: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("daemon returned status %d", resp.StatusCode)
	}
	// Read back the created job to get the ID
	var created SchedulerJob
	if err := json.NewDecoder(resp.Body).Decode(&created); err == nil && created.ID != "" {
		job.ID = created.ID
	}
	return nil
}

func (s *SchedulerHTTPAccess) ListJobs() []*SchedulerJob {
	resp, err := http.Get(s.BaseURL + "/api/scheduler/jobs")
	if err != nil {
		return nil
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil
	}

	var result struct {
		Jobs []*SchedulerJob `json:"jobs"`
	}
	json.NewDecoder(resp.Body).Decode(&result)
	return result.Jobs
}

func (s *SchedulerHTTPAccess) RemoveJob(id string) error {
	req, err := http.NewRequest(http.MethodDelete, s.BaseURL+"/api/scheduler/jobs/"+id, nil)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("daemon returned status %d", resp.StatusCode)
	}
	return nil
}

func (s *SchedulerHTTPAccess) UpdateJob(job *SchedulerJob) error {
	data, err := json.Marshal(job)
	if err != nil {
		return fmt.Errorf("failed to marshal job: %w", err)
	}
	req, err := http.NewRequest(http.MethodPut, s.BaseURL+"/api/scheduler/jobs/"+job.ID, strings.NewReader(string(data)))
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("daemon returned status %d", resp.StatusCode)
	}
	return nil
}

func (s *SchedulerHTTPAccess) RunNow(ctx context.Context, jobID string) (string, error) {
	resp, err := http.Post(s.BaseURL+"/api/scheduler/jobs/"+jobID+"/run", "application/json", nil)
	if err != nil {
		return "", fmt.Errorf("failed to contact daemon: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("daemon returned HTTP %d", resp.StatusCode)
	}

	var result struct {
		Result string `json:"result"`
		Error  string `json:"error"`
	}
	json.NewDecoder(resp.Body).Decode(&result)
	if result.Error != "" {
		return result.Result, fmt.Errorf("%s", result.Error)
	}
	return result.Result, nil
}

func (s *SchedulerHTTPAccess) GetJobByName(name string) *SchedulerJob {
	jobs := s.ListJobs()
	for _, j := range jobs {
		if strings.EqualFold(j.Name, name) {
			return j
		}
	}
	return nil
}

func (s *SchedulerHTTPAccess) ValidateCron(expr string) error {
	// Validate locally — no need to call daemon for this
	return validateCronExpr(expr)
}

// validateCronExpr validates a 5-field cron expression locally.
func validateCronExpr(expr string) error {
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return fmt.Errorf("expected 5 fields (minute hour day month weekday), got %d", len(fields))
	}
	// Basic validation — the full validation happens server-side via robfig/cron
	return nil
}
