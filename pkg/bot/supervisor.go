package bot

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
)

// RunPreparation is supplied by trusted composition, not a dispatch request.
// Ready must verify the wired Runtime enforces action policy, fresh authorization,
// fencing, sandbox readiness and shared usage accounting. A policy alone is not
// an execution guard. No production readiness adapter is installed here.
type RunPreparation struct {
	Mandate       *MandateExecution
	Authorization execution.BotTurnAuthorizer
	Policy        *execution.RunPolicy
	Budget        execution.RunBudget
}
type RunReadiness func(context.Context, store.BotRunInput) (RunPreparation, error)

type SupervisorConfig struct {
	Store   store.BotStore
	Runtime Runtime
	Homes   HomeManager
	Ready   RunReadiness
	// Events must persist before publication. A viewer connection is not an
	// appropriate sink: its disconnection must not control the worker lifetime.
	Events        EventSink
	WorkerID      string
	LeaseDuration time.Duration
	RenewInterval time.Duration
}

// Supervisor executes one already admitted manual/scheduled run or explicitly requeued exact
// approval checkpoint. It does not poll, schedule, retry effects, or expose HTTP.
// Execute's context must be daemon-owned and authenticated as the Bot owner;
// service-principal delegation remains a separate, unimplemented boundary.
type Supervisor struct {
	config SupervisorConfig
	inputs store.BotRuntimeStore
}

func NewSupervisor(c SupervisorConfig) (*Supervisor, error) {
	if c.Store == nil || c.Runtime == nil || c.Homes == nil || c.Ready == nil || c.Events == nil {
		return nil, fmt.Errorf("%w: supervisor dependencies are not ready", store.ErrBotUnsupported)
	}
	inputs, ok := c.Store.(store.BotRuntimeStore)
	if !ok {
		return nil, store.ErrBotUnsupported
	}
	if strings.TrimSpace(c.WorkerID) == "" {
		return nil, store.ErrBotInvalid
	}
	if c.LeaseDuration == 0 {
		c.LeaseDuration = time.Minute
	}
	if c.RenewInterval == 0 {
		c.RenewInterval = c.LeaseDuration / 3
	}
	if c.LeaseDuration <= 0 || c.LeaseDuration > 5*time.Minute || c.RenewInterval <= 0 || c.RenewInterval >= c.LeaseDuration {
		return nil, store.ErrBotInvalid
	}
	return &Supervisor{config: c, inputs: inputs}, nil
}

// Execute returns the runtime result and any execution/commit error. Only a nil
// error proves a committed successful orchestration; a nonnil error may accompany
// a durably recorded failed/partial result. Cancellation or lease loss never
// commits the late result: existing recovery must fence/reconcile that attempt.
func (s *Supervisor) Execute(ctx context.Context, botID store.BotID, runID store.RunID) (out RunResult, outErr error) {
	var zero RunResult
	p, ok := execution.PrincipalFromContext(ctx)
	if !ok || p.Validate() != nil || p.Kind != execution.PrincipalKindUser {
		return zero, store.ErrBotForbidden
	}
	input, err := s.inputs.LoadRunInput(ctx, botID, runID)
	if err != nil {
		return zero, err
	}
	if input == nil || input.Bot.ID != botID || input.Run.ID != runID || input.Run.BotID != botID || input.Bot.OwnerID.String() != p.Subject {
		return zero, store.ErrBotForbidden
	}
	if !input.Run.Intent.AllowsStatus(input.Bot.Status) || input.Run.Status != store.RunQueued || input.Run.Version != input.Bot.Version || input.Version.BotID != botID || input.Version.Version != input.Run.Version || input.Run.SessionID == uuid.Nil || strings.TrimSpace(input.Instructions) == "" {
		return zero, store.ErrBotConflict
	}
	// Trigger and instructions come exclusively from the owner-authenticated
	// durable input binding. HTTP RequestRun remains manual-only; scheduler
	// admission validates its saved enabled job before this worker can claim it.
	switch input.Run.Trigger {
	case store.TriggerManual, store.TriggerCron, store.TriggerGoalDeadline:
	case store.TriggerHook, store.TriggerWebhook, store.TriggerChannel:
		if !input.ExternalAdmissionValidated || input.Run.TriggerID == uuid.Nil {
			return zero, store.ErrBotForbidden
		}
	default:
		return zero, store.ErrBotUnsupported
	}
	if err := ValidateVersion(input.Version); err != nil {
		return zero, err
	}
	if input.Version.Budget.MaxRuntimeSeconds > int64((1<<63-1)/int64(time.Second)) {
		return zero, store.ErrBotInvalid
	}
	identity := execution.BotRunIdentity{BotID: botID, RunID: runID, Version: input.Run.Version, ActorID: p.Subject, OrgSlug: p.OrgSlug, TeamSlug: p.TeamSlug}
	if err := identity.Validate(); err != nil {
		return zero, store.ErrBotForbidden
	}
	ctx = execution.WithBotRunIdentity(ctx, identity)
	ctx = WithApprovalStore(ctx, s.config.Store)
	prepared, err := s.config.Ready(ctx, *input)
	if err != nil {
		var denied *RunError
		if errors.As(err, &denied) && denied.Class == ErrorPermission {
			// Terminalize denied queued attempts without touching a home/model/tool.
			now := time.Now().UTC()
			lease, claimErr := s.config.Store.ClaimRun(ctx, store.BotClaimRequest{BotID: botID, RunID: runID, WorkerID: s.config.WorkerID, Now: now, ExpiresAt: now.Add(s.config.LeaseDuration)})
			if claimErr != nil {
				return zero, errors.Join(err, claimErr)
			}
			result := supervisorOutcome(zero, err)
			commitErr := s.config.Store.CommitRun(ctx, store.BotRunCommit{Lease: lease, Status: result.Status, Report: result.Report})
			return result, errors.Join(err, commitErr)
		}
		return zero, err
	}
	if release, ok := prepared.Budget.(interface{ Release(context.Context) error }); ok {
		defer func() { outErr = errors.Join(outErr, release.Release(ctx)) }()
	}
	if prepared.Mandate != nil {
		if input.Run.MandateRef == nil || *input.Run.MandateRef != prepared.Mandate.Authorization.Ref() {
			return zero, store.ErrBotForbidden
		}
		if err := prepared.Mandate.Check(ctx); err != nil {
			return zero, err
		}
		p = prepared.Mandate.Authorization.Principal(execution.Surface("daemon"))
		ctx, err = execution.WithPrincipal(ctx, p)
		if err != nil {
			return zero, err
		}
	}
	if prepared.Budget == nil || !input.Run.ExecutionMode.Valid() {
		return zero, store.ErrBotUnsupported
	}
	if prepared.Mandate == nil && input.Run.IsInteractiveChat() {
		if prepared.Authorization == nil || prepared.Policy != nil || input.Run.CallerCeiling == nil {
			return zero, store.ErrBotForbidden
		}
		p = input.Run.CallerCeiling.Principal
		if p.Subject != identity.ActorID || p.OrgSlug != identity.OrgSlug || p.TeamSlug != identity.TeamSlug {
			return zero, store.ErrBotForbidden
		}
		ctx, err = execution.WithPrincipal(ctx, p)
		if err != nil {
			return zero, err
		}
		ctx = execution.WithBotTurnAuthorizer(ctx, prepared.Authorization)
		if err := prepared.Authorization.Authorize(ctx, execution.CapabilityChat); err != nil {
			return zero, err
		}
	} else {
		if prepared.Policy == nil {
			return zero, store.ErrBotUnsupported
		}
		if prepared.Policy.Identity() != identity || prepared.Authorization != nil {
			return zero, store.ErrBotForbidden
		}
		ctx = execution.WithRunPolicy(ctx, prepared.Policy)
	}
	if err := prepared.Budget.Check(ctx); err != nil {
		return zero, err
	}
	// Storage must already be durably registered. Ensure may recover that exact
	// binding but cannot silently replace the home's identity, revision or scope.
	home := input.Bot.Home
	if input.Run.RequiresPhysicalHome() {
		if home.ID == uuid.Nil || home.BotID != botID || home.Scope != input.Bot.Scope || home.Revision < 1 || home.Backend == "" || home.StorageKey == "" {
			return zero, store.ErrBotUnsupported
		}
		actual, err := s.config.Homes.Ensure(ctx, HomeRequest{BotID: botID, Scope: input.Bot.Scope, Template: input.Version.SandboxTemplate})
		if err != nil {
			return zero, err
		}
		if actual != home {
			return zero, store.ErrBotConflict
		}
	}
	now := time.Now().UTC()
	lease, err := s.config.Store.ClaimRun(ctx, store.BotClaimRequest{BotID: botID, RunID: runID, WorkerID: s.config.WorkerID, Now: now, ExpiresAt: now.Add(s.config.LeaseDuration)})
	if err != nil {
		return zero, err
	}
	deadline := time.Now().Add(time.Duration(input.Version.Budget.MaxRuntimeSeconds) * time.Second)
	if prepared.Mandate == nil && input.Run.IsInteractiveChat() && input.Run.CallerCeiling.ExpiresAt.Before(deadline) {
		deadline = input.Run.CallerCeiling.ExpiresAt
	}
	if prepared.Mandate != nil {
		// Preparation may narrow runtime below the saved version budget. Enforce
		// that actual deadline for home binding, tools and in-flight models alike.
		if bounded, ok := prepared.Budget.(interface{ Deadline() time.Time }); ok && bounded.Deadline().Before(deadline) {
			deadline = bounded.Deadline()
		}
		if expiry := prepared.Mandate.Authorization.Snapshot().ExpiresAt; expiry.Before(deadline) {
			deadline = expiry
		}
	}
	runCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	// Renewal covers home binding as well as Start. A lost lease cancels the
	// context immediately and invokes Runtime.Cancel with a bounded cleanup ctx.
	var mu sync.Mutex
	latest := lease
	var renewalErr error
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer func() {
			if runCtx.Err() == nil {
				return
			}
			mu.Lock()
			renewalErr = errors.Join(renewalErr, runCtx.Err())
			l := latest
			mu.Unlock()
			cleanup, release := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			cancelErr := s.config.Runtime.Cancel(cleanup, l)
			release()
			mu.Lock()
			renewalErr = errors.Join(renewalErr, cancelErr)
			mu.Unlock()
		}()
		ticker := time.NewTicker(s.config.RenewInterval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-runCtx.Done():
				return
			case <-ticker.C:
				mu.Lock()
				l := latest
				mu.Unlock()
				renewCtx, release := context.WithDeadline(runCtx, l.ExpiresAt)
				var next store.BotLease
				var renewErr error
				if prepared.Authorization != nil {
					renewErr = prepared.Authorization.Authorize(renewCtx, execution.CapabilityChat)
				}
				if prepared.Mandate != nil {
					renewErr = prepared.Mandate.Check(renewCtx)
				}
				if renewErr == nil {
					renewErr = prepared.Budget.Check(renewCtx)
				}
				if renewErr == nil {
					next, renewErr = s.config.Store.RenewLease(renewCtx, l, time.Now().UTC().Add(s.config.LeaseDuration))
				}
				release()
				mu.Lock()
				if renewErr != nil {
					renewalErr = renewErr
				} else {
					latest = next
				}
				mu.Unlock()
				if renewErr != nil {
					cancel()
				}
			}
		}
	}()
	input.Run.Lease = lease
	input.Run.Status = store.RunRunning
	input.Run.StartedAt = &now
	request := RunRequest{Work: input.Work.Clone(), Mandate: prepared.Mandate, Bot: input.Bot, Version: input.Version, Run: input.Run, Principal: p, Authorization: prepared.Authorization, Interactive: prepared.Mandate == nil && input.Run.IsInteractiveChat(), Policy: prepared.Policy, Budget: prepared.Budget, Goals: input.Goals, Instructions: input.Instructions, Attachments: input.Attachments, Now: now}
	result := zero
	if workStore, ok := s.config.Store.(store.BotWorkStore); ok {
		request.Work, err = workStore.LoadWork(runCtx, lease)
	}
	if err == nil && input.Run.RequiresPhysicalHome() {
		err = s.config.Homes.BindRun(runCtx, HomeRunBinding{Home: home, PhysicalSessionID: input.Run.SessionID, Lease: lease})
	}
	if err == nil {
		err = runCtx.Err()
	}
	if err == nil {
		if len(request.Run.Checkpoint) > 0 {
			result, err = s.config.Runtime.Resume(runCtx, request, s.config.Events)
		} else {
			result, err = s.config.Runtime.Start(runCtx, request, s.config.Events)
		}
		if err == nil && result.Failure != nil {
			err = result.Failure
		}
	}
	// If cancellation races with Start returning, wait for the cancellation path
	// rather than letting a successful late result escape the fence.
	if runCtx.Err() == nil {
		close(stop)
	}
	<-done
	mu.Lock()
	lease = latest
	lost := renewalErr
	mu.Unlock()
	if lost != nil || runCtx.Err() != nil {
		return result, errors.Join(err, lost, runCtx.Err())
	}
	result = supervisorOutcome(result, err)
	if transitionErr := ValidateRunTransition(store.RunRunning, result.Status, RunTransitionGuard{}); transitionErr != nil {
		return result, errors.Join(err, transitionErr)
	}
	commitErr := s.config.Store.CommitRun(ctx, store.BotRunCommit{Lease: lease, Status: result.Status, Report: result.Report, Usage: result.Usage, Todos: result.Todos, Checkpoint: result.Checkpoint})
	return result, errors.Join(err, commitErr)
}

func supervisorOutcome(result RunResult, err error) (out RunResult) {
	source := result.Report.EmployeeMessageSourceDigest(result.Status)
	valid := result.Report.ValidateEmployeeMessageIntegrity(result.Status) == nil
	defer func() {
		// Do not repair forged runner envelopes. Commit must reject them, even
		// when an execution error also changes the outcome.
		if out.Report.EmployeeMessage != nil && !valid {
			return
		}
		if out.Report.EmployeeMessage == nil || source != out.Report.EmployeeMessageSourceDigest(out.Status) {
			cause := err
			if cause == nil && out.Failure != nil {
				cause = out.Failure
			}
			message := EmployeeMessageFallback(out.Status, out.Report, cause)
			out.Report.EmployeeMessage = &message
		}
	}()
	failure := result.Failure
	if failure == nil {
		_ = errors.As(err, &failure)
	}
	if err == nil && failure == nil {
		return result
	}
	// Unknown errors are not safe to retry. Preserve evidence/checkpoints/usage,
	// but never persist success after an execution error or uncertain effect.
	result.Status = store.RunFailed
	if failure != nil {
		result.Failure = failure
		if failure.UncertainEffect {
			result.Status = store.RunPartial
		} else if failure.Class == ErrorInterrupted {
			result.Status = store.RunInterrupted
		}
	}
	if strings.TrimSpace(result.Report.Summary) == "" {
		result.Report.Summary = "Run did not complete; execution requires review."
	}
	return result
}
