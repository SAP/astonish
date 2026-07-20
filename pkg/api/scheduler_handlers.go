package api

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/SAP/astonish/pkg/scheduler"
	"github.com/SAP/astonish/pkg/store"
)

// executorInstance holds a reference to the active scheduler Executor.
// Set by the daemon during startup via SetExecutor.
// In platform mode this is used for RunNow (manual trigger) and test execution.
// CRUD operations go through the request-scoped store.SchedulerStore.
var (
	executorMu       sync.RWMutex
	executorInstance *scheduler.Executor
)

// SetExecutor registers the active scheduler executor for API/tool access.
// Called by the daemon run loop after scheduler initialization.
func SetExecutor(e *scheduler.Executor) {
	executorMu.Lock()
	defer executorMu.Unlock()
	executorInstance = e
}

// GetExecutor returns the active scheduler executor, or nil if not set.
func GetExecutor() *scheduler.Executor {
	executorMu.RLock()
	defer executorMu.RUnlock()
	return executorInstance
}

// runHeadlessFunc holds the headless runner function for routine job execution.
// Set by the daemon at startup (all modes) via SetRunHeadlessFunc.
// Used by the RunJobFunc in chat_handlers.go to construct a local executor on
// API pods that don't have the global scheduler Executor.
var (
	runHeadlessMu sync.RWMutex
	runHeadlessFn scheduler.RunHeadlessFunc
)

// SetRunHeadlessFunc registers the headless runner function.
// Called by the daemon at startup for all modes (api, worker, default).
func SetRunHeadlessFunc(fn scheduler.RunHeadlessFunc) {
	runHeadlessMu.Lock()
	defer runHeadlessMu.Unlock()
	runHeadlessFn = fn
}

// GetRunHeadlessFunc returns the registered headless runner, or nil if not set.
func GetRunHeadlessFunc() scheduler.RunHeadlessFunc {
	runHeadlessMu.RLock()
	defer runHeadlessMu.RUnlock()
	return runHeadlessFn
}

// Legacy globals — kept for personal mode backward compatibility where
// the single-instance Scheduler engine is still used for the tick loop.
var (
	schedulerMu       sync.RWMutex
	schedulerInstance *scheduler.Scheduler
)

// SetScheduler registers the active scheduler for personal mode tick loop.
func SetScheduler(s *scheduler.Scheduler) {
	schedulerMu.Lock()
	defer schedulerMu.Unlock()
	schedulerInstance = s
}

// GetScheduler returns the active scheduler, or nil if not set.
func GetScheduler() *scheduler.Scheduler {
	schedulerMu.RLock()
	defer schedulerMu.RUnlock()
	return schedulerInstance
}

// SchedulerJobsHandler handles listing and creating scheduled jobs.
//
// GET  /api/scheduler/jobs[?scope=personal|team] — list jobs (default: both)
// POST /api/scheduler/jobs[?scope=personal|team] — create a job (default: personal)
func SchedulerJobsHandler(w http.ResponseWriter, r *http.Request) {
	svc := store.FromRequest(r)
	if svc == nil || (svc.Scheduler == nil && svc.PersonalScheduler == nil) {
		respondError(w, http.StatusServiceUnavailable, "scheduler not available")
		return
	}

	switch r.Method {
	case http.MethodGet:
		handleListJobs(w, r, svc)
	case http.MethodPost:
		handleCreateJob(w, r, svc)
	default:
		respondError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// SchedulerJobHandler handles operations on a single job.
//
// GET    /api/scheduler/jobs/{id} — get job details
// PUT    /api/scheduler/jobs/{id} — update job
// DELETE /api/scheduler/jobs/{id} — remove job
func SchedulerJobHandler(w http.ResponseWriter, r *http.Request) {
	svc := store.FromRequest(r)
	if svc == nil || (svc.Scheduler == nil && svc.PersonalScheduler == nil) {
		respondError(w, http.StatusServiceUnavailable, "scheduler not available")
		return
	}

	parts := splitPath(r.URL.Path)
	if len(parts) < 4 {
		respondError(w, http.StatusBadRequest, "missing job ID")
		return
	}
	jobID := parts[len(parts)-1]

	existing, ss, jobScope := findSchedulerJob(r.Context(), svc, jobID)
	if existing == nil || ss == nil {
		respondError(w, http.StatusNotFound, "job not found")
		return
	}

	switch r.Method {
	case http.MethodGet:
		existing.Scope = jobScope
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(storeJobToAPIJob(existing))

	case http.MethodPut:
		if !authorizeSchedulerMutate(w, r, jobScope, existing) {
			return
		}
		var update struct {
			Name           string              `json:"name"`
			Mode           string              `json:"mode"`
			Cron           string              `json:"cron"`
			Timezone       string              `json:"timezone"`
			Flow           string              `json:"flow"`
			Params         map[string]string   `json:"params"`
			Instructions   string              `json:"instructions"`
			Channel        string              `json:"channel"`
			Target         string              `json:"target"`
			Enabled        *bool               `json:"enabled"`
			Delivery       *store.JobDelivery  `json:"delivery"`
			ChannelFilter  []string            `json:"channel_filter"`
			MemberChannels map[string][]string `json:"member_channels"`
		}
		if err := json.NewDecoder(r.Body).Decode(&update); err != nil {
			respondError(w, http.StatusBadRequest, "invalid request body")
			return
		}

		if update.Name != "" {
			existing.Name = update.Name
		}
		if update.Mode != "" {
			existing.Mode = update.Mode
		}
		if update.Cron != "" {
			existing.Schedule.Cron = update.Cron
		}
		if update.Timezone != "" {
			existing.Schedule.Timezone = update.Timezone
		}
		if update.Flow != "" {
			existing.Payload.Flow = update.Flow
		}
		if update.Params != nil {
			existing.Payload.Params = update.Params
		}
		if update.Instructions != "" {
			existing.Payload.Instructions = update.Instructions
		}
		if update.Channel != "" {
			existing.Delivery.Channel = update.Channel
		}
		if update.Target != "" {
			existing.Delivery.Target = update.Target
		}
		if update.Enabled != nil {
			existing.Enabled = *update.Enabled
		}
		if update.Delivery != nil {
			if jobScope == store.JobScopePersonal {
				if update.Delivery.Mode == "team" || update.Delivery.Mode == "members" {
					respondError(w, http.StatusBadRequest, "personal jobs only support owner delivery")
					return
				}
				update.Delivery.Mode = "owner"
				update.Delivery.MemberIDs = nil
			}
			existing.Delivery = *update.Delivery
		}
		if update.ChannelFilter != nil {
			existing.Delivery.ChannelFilter = update.ChannelFilter
		}
		if update.MemberChannels != nil {
			existing.Delivery.MemberChannels = update.MemberChannels
		}

		if err := ss.Update(r.Context(), existing); err != nil {
			respondError(w, http.StatusInternalServerError, err.Error())
			return
		}

		if existing.Enabled {
			existing.NextRun = scheduler.ComputeNextRun(existing.Schedule.Cron, existing.Schedule.Timezone)
			_ = ss.Update(r.Context(), existing)
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "updated"})

	case http.MethodDelete:
		if !authorizeSchedulerMutate(w, r, jobScope, existing) {
			return
		}
		if err := ss.Remove(r.Context(), jobID); err != nil {
			respondError(w, http.StatusNotFound, err.Error())
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"status": "removed"})

	default:
		respondError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// SchedulerJobRunHandler triggers immediate execution of a job.
//
// POST /api/scheduler/jobs/{id}/run
func SchedulerJobRunHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		respondError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	svc := store.FromRequest(r)
	if svc == nil || (svc.Scheduler == nil && svc.PersonalScheduler == nil) {
		respondError(w, http.StatusServiceUnavailable, "scheduler not available")
		return
	}

	exec := GetExecutor()
	if exec == nil {
		respondError(w, http.StatusServiceUnavailable, "scheduler executor not available")
		return
	}

	parts := splitPath(r.URL.Path)
	if len(parts) < 5 {
		respondError(w, http.StatusBadRequest, "missing job ID")
		return
	}
	jobID := parts[len(parts)-2]

	storeJob, ss, jobScope := findSchedulerJob(r.Context(), svc, jobID)
	if storeJob == nil || ss == nil {
		respondError(w, http.StatusNotFound, "job not found")
		return
	}
	if !authorizeSchedulerMutate(w, r, jobScope, storeJob) {
		return
	}

	storeJob.Scope = jobScope
	job := storeJobToSchedulerJob(storeJob)
	execCtx := buildSchedulerExecContext(r.Context(), svc, jobScope, storeJob)

	result, err := exec.Execute(execCtx, job)

	now := time.Now()
	storeJob.LastRun = &now
	if err != nil {
		storeJob.LastStatus = "failed"
		storeJob.LastError = err.Error()
		storeJob.ConsecutiveFailures++
	} else {
		storeJob.LastStatus = "success"
		storeJob.LastError = ""
		storeJob.ConsecutiveFailures = 0
	}
	storeJob.NextRun = scheduler.ComputeNextRun(storeJob.Schedule.Cron, storeJob.Schedule.Timezone)
	_ = ss.Update(r.Context(), storeJob)

	resp := map[string]any{
		"job_id": jobID,
		"scope":  jobScope,
		"result": result,
	}
	if err != nil {
		resp["error"] = err.Error()
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

// handleListJobs returns scheduled jobs for the requested scope (or both).
func handleListJobs(w http.ResponseWriter, r *http.Request, svc *store.Services) {
	scope := r.URL.Query().Get("scope")
	jobs := make([]apiJob, 0)
	ctx := r.Context()

	includePersonal := scope == "" || scope == store.JobScopePersonal
	includeTeam := scope == "" || scope == store.JobScopeTeam

	if includePersonal && svc.PersonalScheduler != nil {
		for _, sj := range svc.PersonalScheduler.List(ctx) {
			sj.Scope = store.JobScopePersonal
			jobs = append(jobs, storeJobToAPIJob(sj))
		}
	}
	if includeTeam && svc.Scheduler != nil {
		for _, sj := range svc.Scheduler.List(ctx) {
			sj.Scope = store.JobScopeTeam
			jobs = append(jobs, storeJobToAPIJob(sj))
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"jobs":          jobs,
		"is_team_admin": IsTeamAdmin(r),
	})
}

// SchedulerJobPublishHandler moves a personal scheduled job into the team store.
// POST /api/scheduler/jobs/publish
//
// Requires team admin. Move semantics (not copy) so the job only cron-fires once.
func SchedulerJobPublishHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		respondError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !isPlatformMode(r) {
		respondError(w, http.StatusNotFound, "Publish is only available in platform mode")
		return
	}
	if !RequireTeamAdmin(w, r) {
		return
	}

	svc := store.FromRequest(r)
	if svc == nil || svc.PersonalScheduler == nil || svc.Scheduler == nil {
		respondError(w, http.StatusServiceUnavailable, "scheduler not available")
		return
	}

	var req struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.ID == "" && req.Name == "" {
		respondError(w, http.StatusBadRequest, "job id or name is required")
		return
	}

	ctx := r.Context()
	var personalJob *store.ScheduledJob
	if req.ID != "" {
		personalJob = svc.PersonalScheduler.Get(ctx, req.ID)
	}
	if personalJob == nil && req.Name != "" {
		personalJob = svc.PersonalScheduler.GetByName(ctx, req.Name)
	}
	if personalJob == nil {
		respondError(w, http.StatusNotFound, "personal job not found")
		return
	}

	if existing := svc.Scheduler.GetByName(ctx, personalJob.Name); existing != nil {
		respondError(w, http.StatusConflict, "a team job with this name already exists")
		return
	}

	// Move: new team row (fresh ID), then remove personal.
	teamJob := *personalJob
	teamJob.ID = ""
	teamJob.Scope = store.JobScopeTeam
	teamJob.TeamSlug = "" // implicit for team-store jobs

	if err := svc.Scheduler.Add(ctx, &teamJob); err != nil {
		respondError(w, http.StatusInternalServerError, "failed to publish job: "+err.Error())
		return
	}
	if teamJob.Enabled {
		teamJob.NextRun = scheduler.ComputeNextRun(teamJob.Schedule.Cron, teamJob.Schedule.Timezone)
		_ = svc.Scheduler.Update(ctx, &teamJob)
	}

	if err := svc.PersonalScheduler.Remove(ctx, personalJob.ID); err != nil {
		respondError(w, http.StatusInternalServerError, "job published but failed to remove personal copy: "+err.Error())
		return
	}

	teamJob.Scope = store.JobScopeTeam
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"status":    "ok",
		"published": teamJob.Name,
		"job":       storeJobToAPIJob(&teamJob),
	})
}

// SchedulerJobForkHandler copies a team scheduled job into the user's personal store.
// POST /api/scheduler/jobs/fork
//
// Requires team admin (same gate as credentials fork). Copy semantics — both jobs remain.
func SchedulerJobForkHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		respondError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if !isPlatformMode(r) {
		respondError(w, http.StatusNotFound, "Fork is only available in platform mode")
		return
	}
	if !RequireTeamAdmin(w, r) {
		return
	}

	svc := store.FromRequest(r)
	if svc == nil || svc.PersonalScheduler == nil || svc.Scheduler == nil {
		respondError(w, http.StatusServiceUnavailable, "scheduler not available")
		return
	}

	var req struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.ID == "" && req.Name == "" {
		respondError(w, http.StatusBadRequest, "job id or name is required")
		return
	}

	ctx := r.Context()
	var teamJob *store.ScheduledJob
	if req.ID != "" {
		teamJob = svc.Scheduler.Get(ctx, req.ID)
	}
	if teamJob == nil && req.Name != "" {
		teamJob = svc.Scheduler.GetByName(ctx, req.Name)
	}
	if teamJob == nil {
		respondError(w, http.StatusNotFound, "team job not found")
		return
	}

	if existing := svc.PersonalScheduler.GetByName(ctx, teamJob.Name); existing != nil {
		respondError(w, http.StatusConflict, "a personal job with this name already exists")
		return
	}

	personalJob := *teamJob
	personalJob.ID = ""
	personalJob.Scope = store.JobScopePersonal
	personalJob.Delivery.Mode = "owner"
	personalJob.Delivery.MemberIDs = nil
	if uid := store.UserIDFromContext(ctx); uid != "" {
		personalJob.OwnerID = uid
	} else if user := GetPlatformUser(r); user != nil {
		personalJob.OwnerID = user.ID
	}
	if tc := store.TenantContextFrom(ctx); tc != nil {
		personalJob.TeamSlug = tc.TeamSlug
	}

	if err := svc.PersonalScheduler.Add(ctx, &personalJob); err != nil {
		respondError(w, http.StatusInternalServerError, "failed to fork job: "+err.Error())
		return
	}
	if personalJob.Enabled {
		personalJob.NextRun = scheduler.ComputeNextRun(personalJob.Schedule.Cron, personalJob.Schedule.Timezone)
		_ = svc.PersonalScheduler.Update(ctx, &personalJob)
	}

	personalJob.Scope = store.JobScopePersonal
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"status": "ok",
		"forked": personalJob.Name,
		"job":    storeJobToAPIJob(&personalJob),
	})
}

// handleCreateJob creates a scheduled job in the personal or team store.
func handleCreateJob(w http.ResponseWriter, r *http.Request, svc *store.Services) {
	scope := r.URL.Query().Get("scope")
	if scope == "" {
		// Body may also carry scope; decode into a peek buffer via flat struct below.
		scope = store.JobScopePersonal
	}

	var flat struct {
		ID           string            `json:"id"`
		Name         string            `json:"name"`
		Mode         string            `json:"mode"`
		Cron         string            `json:"cron"`
		Timezone     string            `json:"timezone"`
		Flow         string            `json:"flow"`
		Params       map[string]string `json:"params"`
		Instructions string            `json:"instructions"`
		Channel      string            `json:"channel"`
		Target       string            `json:"target"`
		Enabled      bool              `json:"enabled"`
		Scope        string            `json:"scope"`
		DeliveryMode string            `json:"delivery_mode"`
	}
	if err := json.NewDecoder(r.Body).Decode(&flat); err != nil {
		respondError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if flat.Scope != "" {
		scope = flat.Scope
	}
	if scope != store.JobScopePersonal && scope != store.JobScopeTeam {
		respondError(w, http.StatusBadRequest, "scope must be 'personal' or 'team'")
		return
	}

	if scope == store.JobScopeTeam {
		if !RequireTeamAdmin(w, r) {
			return
		}
	}

	ss := svc.PersonalScheduler
	if scope == store.JobScopeTeam {
		ss = svc.Scheduler
	}
	if ss == nil {
		respondError(w, http.StatusServiceUnavailable, "scheduler not available for scope "+scope)
		return
	}

	deliveryMode := flat.DeliveryMode
	if deliveryMode == "" {
		deliveryMode = "owner"
	}
	if scope == store.JobScopePersonal {
		if deliveryMode == "team" || deliveryMode == "members" {
			respondError(w, http.StatusBadRequest, "personal jobs only support owner delivery")
			return
		}
		deliveryMode = "owner"
	}

	job := &store.ScheduledJob{
		ID:   flat.ID,
		Name: flat.Name,
		Mode: flat.Mode,
		Schedule: store.JobSchedule{
			Cron:     flat.Cron,
			Timezone: flat.Timezone,
		},
		Payload: store.JobPayload{
			Flow:         flat.Flow,
			Params:       flat.Params,
			Instructions: flat.Instructions,
		},
		Delivery: store.JobDelivery{
			Channel: flat.Channel,
			Target:  flat.Target,
			Mode:    deliveryMode,
		},
		Enabled:    flat.Enabled,
		CreatedAt:  time.Now(),
		LastStatus: "pending",
		Scope:      scope,
	}

	if uid := store.UserIDFromContext(r.Context()); uid != "" {
		job.OwnerID = uid
	} else if user := GetPlatformUser(r); user != nil {
		job.OwnerID = user.ID
	}
	if scope == store.JobScopePersonal {
		if tc := store.TenantContextFrom(r.Context()); tc != nil {
			job.TeamSlug = tc.TeamSlug
		}
	}

	if err := ss.Add(r.Context(), job); err != nil {
		respondError(w, http.StatusInternalServerError, err.Error())
		return
	}

	if job.Enabled {
		job.NextRun = scheduler.ComputeNextRun(job.Schedule.Cron, job.Schedule.Timezone)
		_ = ss.Update(r.Context(), job)
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(storeJobToAPIJob(job))
}

// findSchedulerJob looks up a job in personal then team stores.
func findSchedulerJob(ctx context.Context, svc *store.Services, jobID string) (*store.ScheduledJob, store.SchedulerStore, string) {
	if svc.PersonalScheduler != nil {
		if j := svc.PersonalScheduler.Get(ctx, jobID); j != nil {
			return j, svc.PersonalScheduler, store.JobScopePersonal
		}
	}
	if svc.Scheduler != nil {
		if j := svc.Scheduler.Get(ctx, jobID); j != nil {
			return j, svc.Scheduler, store.JobScopeTeam
		}
	}
	return nil, nil, ""
}

// authorizeSchedulerMutate enforces personal-owner or team-admin for mutations.
func authorizeSchedulerMutate(w http.ResponseWriter, r *http.Request, scope string, job *store.ScheduledJob) bool {
	if scope == store.JobScopeTeam {
		return RequireTeamAdmin(w, r)
	}
	// Personal jobs: owner only (or team admin acting on behalf is NOT allowed —
	// personal vault isolation).
	user := GetPlatformUser(r)
	if user == nil {
		if !isPlatformMode(r) {
			return true
		}
		respondError(w, http.StatusUnauthorized, "authentication required")
		return false
	}
	if job.OwnerID != "" && job.OwnerID != user.ID {
		respondError(w, http.StatusForbidden, "only the job owner can manage this personal job")
		return false
	}
	return true
}

// buildSchedulerExecContext mirrors cron credential and network-policy injection
// for RunNow/test_first so adaptive jobs get the same OpenShell PolicyAllow path.
func buildSchedulerExecContext(ctx context.Context, svc *store.Services, scope string, job *store.ScheduledJob) context.Context {
	execCtx := ctx
	if scope == store.JobScopePersonal {
		ownerID := job.OwnerID
		if ownerID == "" {
			ownerID = store.UserIDFromContext(ctx)
		}
		if ownerID != "" {
			execCtx = store.WithUserID(execCtx, ownerID)
		}
		execCtx = store.WithCredentialStore(execCtx, store.NewMergedCredentialStore(svc.PersonalCredentials, svc.Credentials))
		execCtx = store.WithFlowStore(execCtx, store.NewCompositeFlowStore(svc.PersonalFlows, svc.Flows))
	} else {
		if svc.Credentials != nil {
			execCtx = store.WithCredentialStore(execCtx, svc.Credentials)
		}
		if svc.Flows != nil {
			execCtx = store.WithFlowStore(execCtx, svc.Flows)
		}
	}
	if svc.DrillReports != nil {
		execCtx = store.WithDrillReportStore(execCtx, svc.DrillReports)
	}
	if svc.Skills != nil || svc.TeamSkills != nil {
		execCtx = store.WithSkillStores(execCtx, &store.SkillStores{
			Org:  svc.Skills,
			Team: svc.TeamSkills,
		})
	}
	if svc.Memory != nil {
		execCtx = store.WithMemoryStore(execCtx, svc.Memory)
	}
	if svc.PlatformNetworkPolicies != nil || svc.NetworkPolicies != nil || svc.TeamNetworkPolicies != nil {
		execCtx = store.WithNetworkPolicyStores(execCtx, &store.NetworkPolicyStores{
			Platform: svc.PlatformNetworkPolicies,
			Org:      svc.NetworkPolicies,
			Team:     svc.TeamNetworkPolicies,
		})
	}
	return execCtx
}

// --- Type conversion helpers ---

// apiJob is the JSON representation returned to the frontend.
// It uses the flat format expected by SchedulerSettings.tsx.
type apiJob struct {
	ID                  string            `json:"id"`
	Name                string            `json:"name"`
	Mode                string            `json:"mode"`
	Scope               string            `json:"scope,omitempty"`
	TeamSlug            string            `json:"team_slug,omitempty"`
	OwnerID             string            `json:"owner_id,omitempty"`
	Schedule            store.JobSchedule `json:"schedule"`
	Payload             store.JobPayload  `json:"payload"`
	Delivery            store.JobDelivery `json:"delivery"`
	Enabled             bool              `json:"enabled"`
	CreatedAt           time.Time         `json:"created_at"`
	LastRun             *time.Time        `json:"last_run,omitempty"`
	LastStatus          string            `json:"last_status"`
	LastError           string            `json:"last_error,omitempty"`
	NextRun             *time.Time        `json:"next_run,omitempty"`
	ConsecutiveFailures int               `json:"consecutive_failures"`
}

func storeJobToAPIJob(sj *store.ScheduledJob) apiJob {
	return apiJob{
		ID:                  sj.ID,
		Name:                sj.Name,
		Mode:                sj.Mode,
		Scope:               sj.Scope,
		TeamSlug:            sj.TeamSlug,
		OwnerID:             sj.OwnerID,
		Schedule:            sj.Schedule,
		Payload:             sj.Payload,
		Delivery:            sj.Delivery,
		Enabled:             sj.Enabled,
		CreatedAt:           sj.CreatedAt,
		LastRun:             sj.LastRun,
		LastStatus:          sj.LastStatus,
		LastError:           sj.LastError,
		NextRun:             sj.NextRun,
		ConsecutiveFailures: sj.ConsecutiveFailures,
	}
}

// storeJobToSchedulerJob converts a store.ScheduledJob to a scheduler.Job
// for use with the executor's Execute method.
func storeJobToSchedulerJob(sj *store.ScheduledJob) *scheduler.Job {
	return &scheduler.Job{
		ID:   sj.ID,
		Name: sj.Name,
		Mode: scheduler.JobMode(sj.Mode),
		Schedule: scheduler.JobSchedule{
			Cron:     sj.Schedule.Cron,
			Timezone: sj.Schedule.Timezone,
		},
		Payload: scheduler.JobPayload{
			Flow:         sj.Payload.Flow,
			Params:       sj.Payload.Params,
			Instructions: sj.Payload.Instructions,
		},
		Delivery: scheduler.JobDelivery{
			Channel:        sj.Delivery.Channel,
			Target:         sj.Delivery.Target,
			Mode:           scheduler.DeliveryMode(sj.Delivery.Mode),
			MemberIDs:      sj.Delivery.MemberIDs,
			ChannelFilter:  sj.Delivery.ChannelFilter,
			MemberChannels: sj.Delivery.MemberChannels,
		},
		Enabled:             sj.Enabled,
		OwnerID:             sj.OwnerID,
		Scope:               sj.Scope,
		TeamSlug:            sj.TeamSlug,
		CreatedAt:           sj.CreatedAt,
		LastRun:             sj.LastRun,
		LastStatus:          scheduler.JobStatus(sj.LastStatus),
		LastError:           sj.LastError,
		NextRun:             sj.NextRun,
		ConsecutiveFailures: sj.ConsecutiveFailures,
	}
}

// splitPath splits a URL path into segments, filtering empty strings.
func splitPath(path string) []string {
	var parts []string
	for _, p := range split(path, '/') {
		if p != "" {
			parts = append(parts, p)
		}
	}
	return parts
}

// split is a simple byte-based string splitter.
func split(s string, sep byte) []string {
	var result []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == sep {
			result = append(result, s[start:i])
			start = i + 1
		}
	}
	result = append(result, s[start:])
	return result
}

// --- Team members with channels endpoint ---

// TeamMemberChannelsHandler handles GET /api/team/members/channels.
// Returns all team members with their linked (enabled+verified) channel types.
// Used by the Scheduler Settings UI to populate the member picker and
// per-member channel selection.
func TeamMemberChannelsHandler(pa *PlatformAuth) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			respondError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}

		user := GetPlatformUser(r)
		if user == nil {
			respondError(w, http.StatusUnauthorized, "authentication required")
			return
		}

		ctx := r.Context()
		orgDataStore, err := pa.pgStore.ForOrg(user.OrgSlug)
		if err != nil {
			respondError(w, http.StatusInternalServerError, "failed to access organization")
			return
		}

		teamSlug := effectiveTeamSlug(r)
		if teamSlug == "" {
			respondError(w, http.StatusBadRequest, "team context required")
			return
		}

		team, err := orgDataStore.Teams().GetTeamBySlug(ctx, teamSlug)
		if err != nil || team == nil {
			respondError(w, http.StatusNotFound, "team not found")
			return
		}

		members, err := orgDataStore.Teams().ListMembers(ctx, team.ID)
		if err != nil {
			respondError(w, http.StatusInternalServerError, "failed to list members")
			return
		}

		// Enrich with user details and channel links
		type memberChannelInfo struct {
			UserID         string   `json:"user_id"`
			DisplayName    string   `json:"display_name"`
			Email          string   `json:"email"`
			Role           string   `json:"role"`
			LinkedChannels []string `json:"linked_channels"`
		}

		userStore := pa.pgStore.Users()
		channelStore := pa.pgStore.UserChannels()
		result := make([]memberChannelInfo, 0, len(members))

		for _, m := range members {
			info := memberChannelInfo{
				UserID: m.UserID,
				Role:   m.Role,
				Email:  m.Email,
			}

			// Get display name from platform users table
			if u, err := userStore.GetByID(ctx, m.UserID); err == nil && u != nil {
				info.DisplayName = u.DisplayName
				if info.Email == "" {
					info.Email = u.Email
				}
			}

			// Get linked channels (only enabled+verified)
			if links, err := channelStore.ListByUser(ctx, m.UserID); err == nil {
				for _, link := range links {
					if link.Enabled && link.Verified {
						info.LinkedChannels = append(info.LinkedChannels, link.ChannelType)
					}
				}
			}

			result = append(result, info)
		}

		respondJSON(w, http.StatusOK, map[string]any{
			"members": result,
			"count":   len(result),
		})
	}
}
