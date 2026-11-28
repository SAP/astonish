package daemon

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/SAP/astonish/pkg/sandbox/openshell"
	"github.com/SAP/astonish/pkg/scheduler"
	"github.com/SAP/astonish/pkg/store"
)

// platformNetworkPolicyProvider is implemented by entstore.Store.
type platformNetworkPolicyProvider interface {
	PlatformNetworkPolicies() store.NetworkPolicyStore
}

// MultiTenantScheduler manages scheduled job execution across all organizations
// and teams in platform mode. It replaces the single-instance scheduler.Scheduler
// for platform deployments, iterating all orgs → all teams on every tick, and
// also ticking each org member's personal jobs.
//
// Two lanes:
//   - Team jobs: team schema, team credentials only, headless identity.
//   - Personal jobs: personal schema, MergedCredentialStore(personal, team),
//     run as OwnerID. TeamSlug on the job selects team fallback context.
type MultiTenantScheduler struct {
	backend    store.PlatformBackend
	executor   *scheduler.Executor
	deliver    scheduler.DeliverFunc
	logger     *log.Logger
	gatewayCfg *openshell.GRPCClientConfig

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	// running tracks in-flight job IDs globally to prevent double dispatch.
	// Key format: "orgSlug/teamSlug/jobID" for team jobs,
	// "orgSlug/personal/userID/jobID" for personal jobs.
	running map[string]struct{}
	runMu   sync.Mutex
}

// NewMultiTenantScheduler creates a new multi-tenant scheduler for platform mode.
func NewMultiTenantScheduler(
	backend store.PlatformBackend,
	executor *scheduler.Executor,
	deliver scheduler.DeliverFunc,
	logger *log.Logger,
) *MultiTenantScheduler {
	if logger == nil {
		logger = log.Default()
	}
	return &MultiTenantScheduler{
		backend:  backend,
		executor: executor,
		deliver:  scheduler.WithoutBotTriggerDelivery(deliver),
		logger:   logger,
		running:  make(map[string]struct{}),
	}
}

// SetGatewayConfig sets the OpenShell gateway config used to pre-seed and
// auto-approve PolicyAllow network endpoints during adaptive job runs.
func (mts *MultiTenantScheduler) SetGatewayConfig(cfg openshell.GRPCClientConfig) {
	mts.gatewayCfg = &cfg
	if mts.executor != nil {
		mts.executor.GatewayConfig = &cfg
	}
}

// Start begins the multi-tenant scheduler tick loop.
func (mts *MultiTenantScheduler) Start(ctx context.Context) {
	mts.ctx, mts.cancel = context.WithCancel(ctx)

	// Refresh NextRun for all jobs across all tenants on startup
	mts.refreshAllNextRuns()

	mts.wg.Add(1)
	go mts.loop()

	mts.logger.Printf("[scheduler] Multi-tenant scheduler started")
}

// Stop gracefully shuts down the scheduler and waits for in-flight jobs.
func (mts *MultiTenantScheduler) Stop() {
	if mts.cancel != nil {
		mts.cancel()
	}
	mts.wg.Wait()
	mts.logger.Printf("[scheduler] Multi-tenant scheduler stopped")
}

// loop is the main tick loop. It checks for due jobs every 30 seconds.
func (mts *MultiTenantScheduler) loop() {
	defer mts.wg.Done()

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-mts.ctx.Done():
			return
		case <-ticker.C:
			mts.tick()
		}
	}
}

// tick iterates all orgs → team jobs + personal jobs per org member.
func (mts *MultiTenantScheduler) tick() {
	ctx := mts.ctx

	orgs, err := mts.backend.Organizations().List(ctx)
	if err != nil {
		mts.logger.Printf("[scheduler] Failed to list organizations: %v", err)
		return
	}

	for _, org := range orgs {
		if org.Status != "active" {
			continue
		}
		mts.tickOrg(ctx, org)
	}
}

// tickOrg processes all teams and personal jobs in a single organization.
func (mts *MultiTenantScheduler) tickOrg(ctx context.Context, org *store.Organization) {
	orgStore, err := mts.backend.ForOrg(org.Slug)
	if err != nil {
		mts.logger.Printf("[scheduler] Failed to resolve org %q: %v", org.Slug, err)
		return
	}

	teams, err := orgStore.Teams().ListTeams(ctx)
	if err != nil {
		mts.logger.Printf("[scheduler] Failed to list teams for org %q: %v", org.Slug, err)
		return
	}

	for _, team := range teams {
		mts.tickTeam(ctx, org.Slug, orgStore, team.Slug)
	}

	// Personal lane: one pass per org member (deduped across teams).
	members, err := mts.backend.Organizations().ListMembers(ctx, org.ID)
	if err != nil {
		mts.logger.Printf("[scheduler] Failed to list members for org %q: %v", org.Slug, err)
		return
	}
	for _, member := range members {
		mts.tickPersonal(ctx, org.Slug, orgStore, member.ID)
	}
}

// tickTeam processes all due team-scoped jobs for a single team.
func (mts *MultiTenantScheduler) tickTeam(ctx context.Context, orgSlug string, orgStore store.OrgDataStore, teamSlug string) {
	teamStore := orgStore.ForTeam(teamSlug)
	if teamStore == nil {
		return
	}
	schedulerStore := teamStore.ScheduledJobs()
	mts.dispatchDueJobs(ctx, schedulerStore.List(ctx), func(job *store.ScheduledJob) string {
		return orgSlug + "/" + teamSlug + "/" + job.ID
	}, func(job *store.ScheduledJob, runKey string) {
		mts.wg.Add(1)
		go func(sj *store.ScheduledJob, key string) {
			defer mts.wg.Done()
			defer mts.clearRunning(key)
			mts.executeTeamJob(ctx, sj, orgSlug, teamSlug, orgStore, teamStore, schedulerStore)
		}(job, runKey)
	})
}

// tickPersonal processes all due personal jobs for a single user.
func (mts *MultiTenantScheduler) tickPersonal(ctx context.Context, orgSlug string, orgStore store.OrgDataStore, userID string) {
	personalStore := orgStore.ForUser(userID)
	if personalStore == nil {
		return
	}
	schedulerStore := personalStore.ScheduledJobs()
	mts.dispatchDueJobs(ctx, schedulerStore.List(ctx), func(job *store.ScheduledJob) string {
		return orgSlug + "/personal/" + userID + "/" + job.ID
	}, func(job *store.ScheduledJob, runKey string) {
		mts.wg.Add(1)
		go func(sj *store.ScheduledJob, key string) {
			defer mts.wg.Done()
			defer mts.clearRunning(key)
			mts.executePersonalJob(ctx, sj, orgSlug, orgStore, personalStore, schedulerStore, userID)
		}(job, runKey)
	})
}

func (mts *MultiTenantScheduler) dispatchDueJobs(
	ctx context.Context,
	jobs []*store.ScheduledJob,
	runKeyFn func(*store.ScheduledJob) string,
	dispatch func(*store.ScheduledJob, string),
) {
	now := time.Now()
	for _, job := range jobs {
		if !job.Enabled || job.NextRun == nil || now.Before(*job.NextRun) {
			continue
		}
		if job.ConsecutiveFailures > 0 && job.LastRun != nil {
			if now.Before(job.LastRun.Add(backoffDuration(job.ConsecutiveFailures))) {
				continue
			}
		}

		runKey := runKeyFn(job)
		mts.runMu.Lock()
		if _, alreadyRunning := mts.running[runKey]; alreadyRunning {
			mts.runMu.Unlock()
			mts.logger.Printf("[scheduler] Job %q still running, skipping tick", job.Name)
			continue
		}
		mts.running[runKey] = struct{}{}
		mts.runMu.Unlock()

		dispatch(job, runKey)
	}
}

func (mts *MultiTenantScheduler) clearRunning(key string) {
	mts.runMu.Lock()
	delete(mts.running, key)
	mts.runMu.Unlock()
}

// executeTeamJob runs a team-scoped job with team credentials only.
func (mts *MultiTenantScheduler) executeTeamJob(
	ctx context.Context,
	storeJob *store.ScheduledJob,
	orgSlug, teamSlug string,
	orgStore store.OrgDataStore,
	teamStore store.TeamDataStore,
	schedulerStore store.SchedulerStore,
) {
	mts.logger.Printf("[scheduler] Executing team job %q (mode: %s)", storeJob.Name, storeJob.Mode)

	execCtx := withBotSchedulerScope(ctx, BotSchedulerScope{OrgSlug: orgSlug, TeamSlug: teamSlug})
	if scheduler.JobMode(storeJob.Mode) != scheduler.ModeBotTrigger {
		execCtx = mts.buildTeamExecContext(ctx, orgStore, teamStore)
	}
	job := storeJobToSchedulerJob(storeJob)
	job.Scope = store.JobScopeTeam

	execCtx, ack := scheduler.WithBotAcknowledgement(execCtx)
	result, execErr := mts.executor.Execute(execCtx, job)
	if job.Mode != scheduler.ModeBotTrigger || *ack != scheduler.BotScheduleAcknowledged {
		mts.updateJobState(ctx, schedulerStore, storeJob, result, execErr)
	}

	if mts.deliver != nil && storeJob.Mode != "fleet_poll" {
		deliverCtx := scheduler.WithDeliveryContext(ctx, &scheduler.DeliveryContext{
			OrgSlug:  orgSlug,
			TeamSlug: teamSlug,
		})
		if deliverErr := mts.deliver(deliverCtx, job, result, execErr); deliverErr != nil {
			mts.logger.Printf("[scheduler] Delivery failed for job %q: %v", storeJob.Name, deliverErr)
		} else {
			mts.logger.Printf("[scheduler] Delivered job %q", storeJob.Name)
		}
	}
}

// executePersonalJob runs a personal job with merged credentials as the owner.
func (mts *MultiTenantScheduler) executePersonalJob(
	ctx context.Context,
	storeJob *store.ScheduledJob,
	orgSlug string,
	orgStore store.OrgDataStore,
	personalStore store.PersonalDataStore,
	schedulerStore store.SchedulerStore,
	userID string,
) {
	mts.logger.Printf("[scheduler] Executing personal job %q (mode: %s, owner: %s)", storeJob.Name, storeJob.Mode, userID)

	teamSlug := storeJob.TeamSlug
	var teamStore store.TeamDataStore
	if teamSlug != "" {
		teamStore = orgStore.ForTeam(teamSlug)
	}

	execCtx := withBotSchedulerScope(ctx, BotSchedulerScope{OrgSlug: orgSlug, TeamSlug: teamSlug, UserID: userID})
	if scheduler.JobMode(storeJob.Mode) != scheduler.ModeBotTrigger {
		execCtx = mts.buildPersonalExecContext(ctx, orgStore, teamStore, personalStore, userID)
	}
	job := storeJobToSchedulerJob(storeJob)
	job.Scope = store.JobScopePersonal
	if job.OwnerID == "" {
		job.OwnerID = userID
	}

	execCtx, ack := scheduler.WithBotAcknowledgement(execCtx)
	result, execErr := mts.executor.Execute(execCtx, job)
	if job.Mode != scheduler.ModeBotTrigger || *ack != scheduler.BotScheduleAcknowledged {
		mts.updateJobState(ctx, schedulerStore, storeJob, result, execErr)
	}

	if mts.deliver != nil && storeJob.Mode != "fleet_poll" {
		// Personal jobs only deliver to the owner.
		deliverJob := *job
		if deliverJob.Delivery.Mode == "" ||
			deliverJob.Delivery.Mode == scheduler.DeliveryModeTeam ||
			deliverJob.Delivery.Mode == scheduler.DeliveryModeMembers {
			deliverJob.Delivery.Mode = scheduler.DeliveryModeOwner
			deliverJob.Delivery.MemberIDs = nil
		}
		deliverCtx := scheduler.WithDeliveryContext(ctx, &scheduler.DeliveryContext{
			OrgSlug:  orgSlug,
			TeamSlug: teamSlug,
		})
		if deliverErr := mts.deliver(deliverCtx, &deliverJob, result, execErr); deliverErr != nil {
			mts.logger.Printf("[scheduler] Delivery failed for personal job %q: %v", storeJob.Name, deliverErr)
		} else {
			mts.logger.Printf("[scheduler] Delivered personal job %q", storeJob.Name)
		}
	}
}

func (mts *MultiTenantScheduler) buildTeamExecContext(
	ctx context.Context,
	orgStore store.OrgDataStore,
	teamStore store.TeamDataStore,
) context.Context {
	execCtx := ctx
	execCtx = store.WithCredentialStore(execCtx, teamStore.Credentials())
	execCtx = store.WithFlowStore(execCtx, teamStore.Flows())
	execCtx = store.WithDrillReportStore(execCtx, teamStore.DrillReports())
	execCtx = store.WithSkillStores(execCtx, &store.SkillStores{
		Team: teamStore.Skills(),
	})
	execCtx = store.WithMCPServerStores(execCtx, &store.MCPServerStores{
		Platform: mts.backend.PlatformMCPServers(),
		Org:      orgStore.OrgMCPServers(),
		Team:     teamStore.MCPServers(),
	})
	execCtx = store.WithMemoryStore(execCtx, teamStore.Memories())
	execCtx = store.WithFleetTemplateStore(execCtx, teamStore.FleetTemplates())
	execCtx = store.WithFleetPlanStore(execCtx, teamStore.FleetPlans())
	execCtx = mts.withNetworkPolicyStores(execCtx, orgStore, teamStore)

	if mts.executor.ChatAgent != nil && mts.executor.ChatAgent.PlatformReflector != nil {
		execCtx = store.WithMemorySaveOrMerge(execCtx, mts.executor.ChatAgent.PlatformReflector.MemorySaveOrMergeFunc())
	}
	if ts := teamStore.Settings(); ts != nil {
		if settings, err := ts.Get(ctx); err == nil && len(settings.DisabledTools) > 0 {
			execCtx = store.WithDisabledTools(execCtx, settings.DisabledTools)
		}
	}
	return execCtx
}

func (mts *MultiTenantScheduler) buildPersonalExecContext(
	ctx context.Context,
	orgStore store.OrgDataStore,
	teamStore store.TeamDataStore,
	personalStore store.PersonalDataStore,
	userID string,
) context.Context {
	execCtx := store.WithUserID(ctx, userID)

	var teamCreds store.CredentialStore
	var teamFlows store.FlowStore
	if teamStore != nil {
		teamCreds = teamStore.Credentials()
		teamFlows = teamStore.Flows()
		execCtx = store.WithDrillReportStore(execCtx, teamStore.DrillReports())
		execCtx = store.WithSkillStores(execCtx, &store.SkillStores{
			Team: teamStore.Skills(),
		})
		execCtx = store.WithMCPServerStores(execCtx, &store.MCPServerStores{
			Platform: mts.backend.PlatformMCPServers(),
			Org:      orgStore.OrgMCPServers(),
			Team:     teamStore.MCPServers(),
		})
		execCtx = store.WithMemoryStore(execCtx, teamStore.Memories())
		execCtx = store.WithFleetTemplateStore(execCtx, teamStore.FleetTemplates())
		execCtx = store.WithFleetPlanStore(execCtx, teamStore.FleetPlans())
		if ts := teamStore.Settings(); ts != nil {
			if settings, err := ts.Get(ctx); err == nil && len(settings.DisabledTools) > 0 {
				execCtx = store.WithDisabledTools(execCtx, settings.DisabledTools)
			}
		}
	} else {
		execCtx = store.WithMCPServerStores(execCtx, &store.MCPServerStores{
			Platform: mts.backend.PlatformMCPServers(),
			Org:      orgStore.OrgMCPServers(),
		})
	}

	// Same as Studio chat: personal-first, team fallback; writes go personal.
	execCtx = store.WithCredentialStore(execCtx, store.NewMergedCredentialStore(personalStore.Credentials(), teamCreds))
	execCtx = store.WithFlowStore(execCtx, store.NewCompositeFlowStore(personalStore.Flows(), teamFlows))
	execCtx = mts.withNetworkPolicyStores(execCtx, orgStore, teamStore)

	if mts.executor.ChatAgent != nil && mts.executor.ChatAgent.PlatformReflector != nil {
		execCtx = store.WithMemorySaveOrMerge(execCtx, mts.executor.ChatAgent.PlatformReflector.MemorySaveOrMergeFunc())
	}
	return execCtx
}

func (mts *MultiTenantScheduler) withNetworkPolicyStores(
	ctx context.Context,
	orgStore store.OrgDataStore,
	teamStore store.TeamDataStore,
) context.Context {
	nps := &store.NetworkPolicyStores{
		Org: orgStore.OrgNetworkPolicies(),
	}
	if p, ok := mts.backend.(platformNetworkPolicyProvider); ok {
		nps.Platform = p.PlatformNetworkPolicies()
	}
	if teamStore != nil {
		nps.Team = teamStore.NetworkPolicies()
	}
	return store.WithNetworkPolicyStores(ctx, nps)
}

func (mts *MultiTenantScheduler) updateJobState(
	ctx context.Context,
	schedulerStore store.SchedulerStore,
	storeJob *store.ScheduledJob,
	result string,
	execErr error,
) {
	now := time.Now()
	stored := schedulerStore.Get(ctx, storeJob.ID)
	if stored == nil {
		return
	}

	stored.LastRun = &now
	if execErr != nil {
		stored.LastStatus = "failed"
		stored.LastError = execErr.Error()
		stored.ConsecutiveFailures++
		mts.logger.Printf("[scheduler] Job %q failed (%d consecutive): %v",
			stored.Name, stored.ConsecutiveFailures, execErr)
	} else {
		stored.LastStatus = "success"
		stored.LastError = ""
		stored.ConsecutiveFailures = 0
		mts.logger.Printf("[scheduler] Job %q completed successfully (%d chars)",
			stored.Name, len(result))
	}

	if scheduler.JobMode(stored.Mode) != scheduler.ModeBotTrigger || execErr == nil {
		stored.NextRun = scheduler.ComputeNextRun(stored.Schedule.Cron, stored.Schedule.Timezone)
	}
	if err := schedulerStore.Update(ctx, stored); err != nil {
		mts.logger.Printf("[scheduler] Failed to update job state for %q: %v", stored.Name, err)
	}
}

// refreshAllNextRuns computes and sets NextRun for all enabled jobs across all tenants.
func (mts *MultiTenantScheduler) refreshAllNextRuns() {
	ctx := context.Background()

	orgs, err := mts.backend.Organizations().List(ctx)
	if err != nil {
		mts.logger.Printf("[scheduler] Failed to list orgs for NextRun refresh: %v", err)
		return
	}

	var totalJobs int
	for _, org := range orgs {
		if org.Status != "active" {
			continue
		}
		orgStore, err := mts.backend.ForOrg(org.Slug)
		if err != nil {
			continue
		}
		teams, err := orgStore.Teams().ListTeams(ctx)
		if err != nil {
			continue
		}
		for _, team := range teams {
			teamStore := orgStore.ForTeam(team.Slug)
			if teamStore == nil {
				continue
			}
			totalJobs += mts.refreshStoreNextRuns(ctx, teamStore.ScheduledJobs())
		}

		members, err := mts.backend.Organizations().ListMembers(ctx, org.ID)
		if err != nil {
			continue
		}
		for _, member := range members {
			personalStore := orgStore.ForUser(member.ID)
			if personalStore == nil {
				continue
			}
			totalJobs += mts.refreshStoreNextRuns(ctx, personalStore.ScheduledJobs())
		}
	}

	if totalJobs > 0 {
		mts.logger.Printf("[scheduler] Refreshed NextRun for %d jobs across all tenants", totalJobs)
	}
}

func (mts *MultiTenantScheduler) refreshStoreNextRuns(ctx context.Context, ss store.SchedulerStore) int {
	var n int
	for _, job := range ss.List(ctx) {
		if !job.Enabled {
			continue
		}
		if scheduler.JobMode(job.Mode) == scheduler.ModeBotTrigger && job.NextRun != nil {
			continue
		}
		nextRun := scheduler.ComputeNextRun(job.Schedule.Cron, job.Schedule.Timezone)
		if nextRun != nil && (job.NextRun == nil || !nextRun.Equal(*job.NextRun)) {
			job.NextRun = nextRun
			_ = ss.Update(ctx, job)
			n++
		}
	}
	return n
}

// RunNow executes a team job immediately with team-scoped context.
func (mts *MultiTenantScheduler) RunNow(ctx context.Context, schedulerStore store.SchedulerStore, teamStore store.TeamDataStore, jobID string) (string, error) {
	storeJob := schedulerStore.Get(ctx, jobID)
	if storeJob == nil {
		return "", nil
	}

	orgStore, _ := mts.orgStoreFromTeam(ctx, teamStore)
	execCtx := ctx
	if tc := store.TenantContextFrom(ctx); tc != nil {
		execCtx = withBotSchedulerScope(ctx, BotSchedulerScope{OrgSlug: tc.OrgSlug, TeamSlug: tc.TeamSlug})
	}
	if orgStore != nil && scheduler.JobMode(storeJob.Mode) != scheduler.ModeBotTrigger {
		execCtx = mts.buildTeamExecContext(ctx, orgStore, teamStore)
	} else if scheduler.JobMode(storeJob.Mode) != scheduler.ModeBotTrigger {
		execCtx = ctx
		execCtx = store.WithCredentialStore(execCtx, teamStore.Credentials())
		execCtx = store.WithFlowStore(execCtx, teamStore.Flows())
	}

	job := storeJobToSchedulerJob(storeJob)
	job.Scope = store.JobScopeTeam
	result, execErr := mts.executor.Execute(execCtx, job)

	now := time.Now()
	storeJob.LastRun = &now
	if execErr != nil {
		storeJob.LastStatus = "failed"
		storeJob.LastError = execErr.Error()
		storeJob.ConsecutiveFailures++
	} else {
		storeJob.LastStatus = "success"
		storeJob.LastError = ""
		storeJob.ConsecutiveFailures = 0
	}
	if scheduler.JobMode(storeJob.Mode) != scheduler.ModeBotTrigger || execErr == nil {
		storeJob.NextRun = scheduler.ComputeNextRun(storeJob.Schedule.Cron, storeJob.Schedule.Timezone)
	}
	_ = schedulerStore.Update(ctx, storeJob)

	return result, execErr
}

// RunNowPersonal executes a personal job immediately with merged credentials.
func (mts *MultiTenantScheduler) RunNowPersonal(
	ctx context.Context,
	schedulerStore store.SchedulerStore,
	orgStore store.OrgDataStore,
	teamStore store.TeamDataStore,
	personalStore store.PersonalDataStore,
	userID, jobID string,
) (string, error) {
	storeJob := schedulerStore.Get(ctx, jobID)
	if storeJob == nil {
		return "", nil
	}

	execCtx := ctx
	if tc := store.TenantContextFrom(ctx); tc != nil {
		execCtx = withBotSchedulerScope(ctx, BotSchedulerScope{OrgSlug: tc.OrgSlug, TeamSlug: tc.TeamSlug, UserID: userID})
	}
	if scheduler.JobMode(storeJob.Mode) != scheduler.ModeBotTrigger {
		execCtx = mts.buildPersonalExecContext(ctx, orgStore, teamStore, personalStore, userID)
	}
	job := storeJobToSchedulerJob(storeJob)
	job.Scope = store.JobScopePersonal
	if job.OwnerID == "" {
		job.OwnerID = userID
	}
	result, execErr := mts.executor.Execute(execCtx, job)

	now := time.Now()
	storeJob.LastRun = &now
	if execErr != nil {
		storeJob.LastStatus = "failed"
		storeJob.LastError = execErr.Error()
		storeJob.ConsecutiveFailures++
	} else {
		storeJob.LastStatus = "success"
		storeJob.LastError = ""
		storeJob.ConsecutiveFailures = 0
	}
	if scheduler.JobMode(storeJob.Mode) != scheduler.ModeBotTrigger || execErr == nil {
		storeJob.NextRun = scheduler.ComputeNextRun(storeJob.Schedule.Cron, storeJob.Schedule.Timezone)
	}
	_ = schedulerStore.Update(ctx, storeJob)

	return result, execErr
}

// orgStoreFromTeam is a best-effort lookup; RunNow often already has stores in ctx.
func (mts *MultiTenantScheduler) orgStoreFromTeam(ctx context.Context, _ store.TeamDataStore) (store.OrgDataStore, error) {
	if tc := store.TenantContextFrom(ctx); tc != nil && tc.OrgSlug != "" {
		return mts.backend.ForOrg(tc.OrgSlug)
	}
	return nil, nil
}

// backoffSteps defines the error backoff delays for consecutive failures.
var backoffSteps = []time.Duration{
	30 * time.Second,
	1 * time.Minute,
	5 * time.Minute,
	15 * time.Minute,
	60 * time.Minute,
}

// backoffDuration returns the delay for a given failure count.
func backoffDuration(failures int) time.Duration {
	idx := failures - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(backoffSteps) {
		idx = len(backoffSteps) - 1
	}
	return backoffSteps[idx]
}
