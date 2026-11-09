package launcher

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/scheduler"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/tools"
	"github.com/google/uuid"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
)

const botScheduleReviewRequired = "review_required"

// botScheduleReview is an exact, revision-bound request for the existing owner
// configuration API, not an authorization token. Neither tool approval nor plan
// completion applies it. ChangeTrigger independently requires a human context,
// live ownership, no active run, and the expected revision. Saving never enables
// recurrence; that is a separate reviewed request after inspecting the save.
type botScheduleReview struct {
	Method string                 `json:"method"`
	Path   string                 `json:"path"`
	Body   botScheduleReviewBody  `json:"body"`
	Digest string                 `json:"digest"`
	Action store.BotTriggerAction `json:"action"`
}

type botScheduleReviewBody struct {
	ExpectedRevision int64              `json:"expected_revision"`
	Reason           string             `json:"reason"`
	Definition       *store.TriggerSpec `json:"definition,omitempty"`
}

type botScheduleChangeResult struct {
	tools.ToolResult
	JobID       string                     `json:"job_id,omitempty"`
	OperationID string                     `json:"operation_id,omitempty"`
	Receipt     *store.BotOperationReceipt `json:"receipt,omitempty"`
	Review      botScheduleReview          `json:"review,omitempty"`
}

// BotScheduleTools reuses Chat's scheduling names with Bot-specific create/update
// schemas, but never exposes a generic SchedulerStore or mutates it in a Bot run.
// The returned adapters MUST be wrapped by the Bot factory's live policy guard.
func BotScheduleTools(r bot.RunRequest, scope *BotChatScope) ([]tool.Tool, error) {
	if scope == nil || scope.Services == nil {
		return nil, store.ErrBotUnsupported
	}
	repo := scope.Services.Bots
	switch r.Bot.Scope {
	case store.BotScopePersonal:
		repo = scope.Services.PersonalBots
	case store.BotScopeTeam:
	default:
		return nil, store.ErrBotInvalid
	}
	if repo == nil {
		return nil, store.ErrBotUnsupported
	}
	jobs := scope.Services.Scheduler
	if r.Bot.Scope == store.BotScopePersonal {
		jobs = scope.Services.PersonalScheduler
	}
	s := botScheduleTools{request: r, repo: repo, services: scope.Services, scheduler: jobs}
	list, err := functiontool.New(functiontool.Config{Name: "list_scheduled_jobs", Description: "List only this Bot's persisted cron triggers, including disabled schedules. Filter words match the job name, trigger ID, schedule ID, mode and task instructions (all words must match, case-insensitively). Each entry's id is the legacy Bot trigger ID, NOT a resume target. schedule_id and job_id are the canonical scheduler job ID; copy schedule_id into resume_targets.schedule_ids. Unbound entries have no schedule_id and cannot be resumed."}, s.list)
	if err != nil {
		return nil, err
	}
	add, err := functiontool.New(functiontool.Config{Name: "schedule_job", InputSchema: botScheduleInputSchema(), Description: "Capture an authorized Bot cron schedule as a durable pending operation. For an explicit owner request within the current bounded mandate, call this tool with the given task and cadence; do not ask for redundant frequency confirmation, even every minute. The runtime enforces the minimum interval and budgets; honor actual ambiguity, policy denials and exact-operation authorization requirements. Enabled by default after the source turn ends and the worker commits. Results appear in this Bot's home chat; do not ask for channels or recipients. Never claim applied from a pending receipt."}, func(ctx tool.Context, args tools.ScheduleJobArgs) (map[string]any, error) {
		return botScheduleResultMap(s.add(ctx, args))
	})
	if err != nil {
		return nil, err
	}
	update, err := functiontool.New(functiontool.Config{Name: "update_scheduled_job", InputSchema: botScheduleUpdateInputSchema(), Description: "Capture an authorized cadence or recurrence-state change to this Bot's schedule as a durable pending operation. Supply job_id plus at least one top-level patch field: enabled, schedule or timezone. Omitted fields are preserved. This tool does not change task instructions or job names; never send a nested patch or a job_id-only call. For a preference-only memory request, save memory without rewriting, replacing or removing schedules. Only the worker's committed receipt establishes success."}, func(ctx tool.Context, args tools.UpdateScheduledJobArgs) (map[string]any, error) {
		return botScheduleResultMap(s.update(ctx, args))
	})
	if err != nil {
		return nil, err
	}
	remove, err := functiontool.New(functiontool.Config{Name: "remove_scheduled_job", Description: "Capture authorized removal of only this Bot's schedule as a durable pending operation. Pending does not mean deleted."}, func(ctx tool.Context, args tools.RemoveScheduledJobArgs) (map[string]any, error) {
		return botScheduleResultMap(s.remove(ctx, args))
	})
	if err != nil {
		return nil, err
	}
	return []tool.Tool{list, add, update, remove}, nil
}

func botScheduleResultMap(result botScheduleChangeResult, err error) (map[string]any, error) {
	if err != nil {
		return nil, err
	}
	data, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	err = json.Unmarshal(data, &out)
	if result.OperationID != "" {
		delete(out, "review")
	}
	return out, err
}

type botScheduleTools struct {
	request   bot.RunRequest
	repo      store.BotStore
	services  *store.Services
	scheduler store.SchedulerStore
}

func (s botScheduleTools) current(ctx context.Context) (*store.Bot, *store.BotVersion, error) {
	if ctx == nil {
		return nil, nil, store.ErrBotForbidden
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	id, ok := execution.BotRunIdentityFromContext(ctx)
	p, authenticated := execution.PrincipalFromContext(ctx)
	r := s.request
	if !ok || !authenticated || p.Validate() != nil || id.BotID != r.Bot.ID || id.RunID != r.Run.ID || id.Version != r.Version.Version || id.ActorID != r.Bot.OwnerID.String() || p.Subject != r.Bot.OwnerID.String() || id.OrgSlug != r.Principal.OrgSlug || id.TeamSlug != r.Principal.TeamSlug || p.OrgSlug != id.OrgSlug || p.TeamSlug != id.TeamSlug {
		return nil, nil, store.ErrBotForbidden
	}
	b, err := s.repo.Get(ctx, r.Bot.ID)
	if err != nil {
		return nil, nil, err
	}
	if b == nil || b.ID != r.Bot.ID || b.OwnerID != r.Bot.OwnerID || b.Scope != r.Bot.Scope || b.Version != r.Version.Version || b.Revision < 1 {
		return nil, nil, store.ErrBotConflict
	}
	v, err := s.repo.GetVersion(ctx, b.ID, b.Version)
	if err != nil {
		return nil, nil, err
	}
	if v == nil || v.BotID != b.ID || v.Version != b.Version {
		return nil, nil, store.ErrBotConflict
	}
	return b, v, nil
}

type botScheduleJobSummary struct {
	tools.JobSummary
	ScheduleID string `json:"schedule_id,omitempty" jsonschema:"Canonical scheduler job UUID; use in resume_targets.schedule_ids, never the legacy trigger id"`
	JobID      string `json:"job_id,omitempty" jsonschema:"Same canonical scheduler job UUID as schedule_id"`
}

type botScheduleListResult struct {
	Jobs    []botScheduleJobSummary `json:"jobs"`
	Count   int                     `json:"count"`
	Message string                  `json:"message"`
}

func (s botScheduleTools) list(ctx tool.Context, a tools.ListScheduledJobsArgs) (botScheduleListResult, error) {
	b, v, err := s.current(ctx)
	out := botScheduleListResult{Jobs: []botScheduleJobSummary{}}
	if err != nil {
		return out, err
	}
	filter := strings.Fields(strings.ToLower(a.Filter))
	for _, tr := range v.Triggers {
		if tr.Kind != store.TriggerCron {
			continue
		}
		name := tr.ID.String()
		row := botScheduleJobSummary{JobSummary: tools.JobSummary{ID: name, Name: name, Mode: "adaptive", Scope: string(b.Scope), Schedule: tr.Cron, Timezone: tr.Timezone, Status: "unbound"}}
		if tr.ScheduleID != nil {
			if s.scheduler == nil {
				return out, store.ErrBotUnsupported
			}
			job := s.scheduler.Get(ctx, tr.ScheduleID.String())
			if job == nil || job.Payload.BotID != b.ID.String() || job.Payload.BotTriggerID != tr.ID.String() || job.Payload.BotVersion != v.Version || job.OwnerID != b.OwnerID.String() {
				return out, store.ErrBotConflict
			}
			row.ScheduleID, row.JobID = tr.ScheduleID.String(), tr.ScheduleID.String()
			row.Name, row.Schedule, row.Timezone = job.Name, job.Schedule.Cron, job.Schedule.Timezone
			row.Enabled, row.Status, row.Failures = job.Enabled, job.LastStatus, job.ConsecutiveFailures
			if job.NextRun != nil {
				row.NextRun = job.NextRun.Format(time.RFC3339)
			}
		}
		// Generated scheduler names omit the user's descriptive schedule name.
		// Match only this already-authorized trigger's task, never another Bot or
		// the Bot-wide charter (which would match unrelated schedules).
		searchable := strings.ToLower(row.Name + " " + row.ID + " " + row.ScheduleID + " adaptive " + tr.Instructions)
		matches := true
		for _, term := range filter {
			if !strings.Contains(searchable, term) {
				matches = false
				break
			}
		}
		if !matches {
			continue
		}
		out.Jobs = append(out.Jobs, row)
	}
	out.Count = len(out.Jobs)
	out.Message = "Only this Bot's schedules, showing effective persisted scheduler state. Pending changes are not applied schedules. For resume_targets.schedule_ids use schedule_id (same as job_id), not id: id is the Bot trigger ID. Entries without schedule_id are unbound and cannot be resumed."
	if out.Count == 0 && len(filter) != 0 {
		out.Message += " No schedules matched this filter; this does not establish that a pending operation exists or that the Bot has no schedules."
	}
	if err := ctx.Err(); err != nil {
		return out, err
	}
	// Attest only after every scoped version/scheduler read and binding check
	// succeeded. Returned JSON cannot mint this server-only exact-call record.
	bot.RecordToolOutcome(ctx, ctx.FunctionCallID(), bot.ToolOutcome{Verified: true, Unchanged: []store.BotEvidence{{
		Kind:       store.BotEvidenceObservation,
		Summary:    fmt.Sprintf("Scoped schedule read completed: %d matching triggers observed; no schedule changed", out.Count),
		Source:     "bot-schedules://" + string(b.Scope) + "/" + b.ID.String() + "/" + s.request.Run.ID.String() + "/" + url.PathEscape(ctx.FunctionCallID()),
		ObservedAt: time.Now().UTC(), ActorID: b.OwnerID,
	}}})
	return out, nil
}

func validateBotCron(tr store.TriggerSpec) error {
	if len(strings.Fields(tr.Cron)) != 5 || scheduler.ValidateCron(tr.Cron) != nil || tr.Timezone == "" {
		return store.ErrBotInvalid
	}
	if _, err := time.LoadLocation(tr.Timezone); err != nil {
		return store.ErrBotInvalid
	}
	return nil
}

func (s botScheduleTools) add(ctx tool.Context, a tools.ScheduleJobArgs) (botScheduleChangeResult, error) {
	b, v, err := s.current(ctx)
	if err != nil {
		return botScheduleChangeResult{}, err
	}
	if a.Scope != "" && a.Scope != string(b.Scope) {
		return botScheduleChangeResult{}, store.ErrBotForbidden
	}
	// These fields cannot be represented by TriggerSpec. Never silently turn a
	// routine into an adaptive run or overwrite the reviewed delivery policy.
	if a.Mode != "adaptive" || a.Flow != "" || len(a.Params) != 0 || a.TestFirst || a.DeliveryMode != "" || len(a.DeliveryChannels) != 0 || len(a.DeliveryMembers) != 0 || a.Channel != "" || a.Target != "" {
		return botScheduleChangeResult{}, fmt.Errorf("%w: Bot schedules use adaptive instructions and the Bot's reviewed delivery; test runs require owner configuration", store.ErrBotUnsupported)
	}
	if strings.TrimSpace(a.Name) == "" || strings.TrimSpace(a.Instructions) == "" || len(a.Instructions) > 65536 {
		return botScheduleChangeResult{}, store.ErrBotInvalid
	}
	tz := a.Timezone
	if tz == "" {
		tz = v.Persona.Timezone
	}
	if tz == "" {
		tz = "UTC"
	}
	tr := store.TriggerSpec{ID: uuid.New(), Kind: store.TriggerCron, Cron: a.Schedule, Timezone: tz, Instructions: a.Instructions}
	if err := validateBotCron(tr); err != nil {
		return botScheduleChangeResult{}, err
	}
	if s.request.Mandate != nil {
		tr.ID, tr.Enabled = uuid.Nil, true
		return s.enqueue(ctx, b, store.BotControlScheduleCreate, "Schedule: "+a.Name, tr)
	}
	return s.propose(b, tr.ID, store.BotTriggerSave, "Review disabled schedule: "+a.Name, &tr), nil
}

func botCronByID(v *store.BotVersion, key string) (store.TriggerSpec, error) {
	id, err := uuid.Parse(key)
	if err != nil || id == uuid.Nil {
		return store.TriggerSpec{}, store.ErrBotNotFound
	}
	for _, tr := range v.Triggers {
		if tr.Kind == store.TriggerCron && (tr.ID == id || (tr.ScheduleID != nil && *tr.ScheduleID == id)) {
			return tr, nil
		}
	}
	// No lookup in another Bot, personal scheduler, team scheduler or global job
	// registry, even if the caller owns both Bots.
	return store.TriggerSpec{}, store.ErrBotNotFound
}

func (s botScheduleTools) update(ctx tool.Context, a tools.UpdateScheduledJobArgs) (botScheduleChangeResult, error) {
	b, v, err := s.current(ctx)
	if err != nil {
		return botScheduleChangeResult{}, err
	}
	tr, err := botCronByID(v, a.JobID)
	if err != nil {
		return botScheduleChangeResult{}, err
	}
	if a.Name != "" {
		return botScheduleChangeResult{}, fmt.Errorf("%w: Bot triggers are identified by ID, not mutable job names", store.ErrBotUnsupported)
	}
	if s.request.Mandate != nil {
		if a.Enabled == nil && a.Schedule == "" && a.Timezone == "" {
			return botScheduleChangeResult{}, store.ErrBotInvalid
		}
		// Preserve the effective state unless the owner explicitly requested enable/pause.
		if tr.ScheduleID != nil {
			if s.scheduler == nil {
				return botScheduleChangeResult{}, store.ErrBotUnsupported
			}
			job := s.scheduler.Get(ctx, tr.ScheduleID.String())
			if job == nil || job.Payload.BotID != b.ID.String() || job.Payload.BotTriggerID != tr.ID.String() || job.Payload.BotVersion != v.Version {
				return botScheduleChangeResult{}, store.ErrBotConflict
			}
			tr.Enabled = job.Enabled
		} else {
			tr.Enabled = false
		}
		if a.Schedule != "" {
			tr.Cron = a.Schedule
		}
		if a.Timezone != "" {
			tr.Timezone = a.Timezone
		}
		if a.Enabled != nil {
			tr.Enabled = *a.Enabled
		}
		if err := validateBotCron(tr); err != nil {
			return botScheduleChangeResult{}, err
		}
		op := store.BotControlScheduleEdit
		if a.Enabled != nil && !*a.Enabled && a.Schedule == "" && a.Timezone == "" {
			op = store.BotControlSchedulePause
		}
		return s.enqueue(ctx, b, op, "Update schedule", tr)
	}
	if a.Enabled != nil {
		if a.Schedule != "" || a.Timezone != "" {
			return botScheduleChangeResult{}, fmt.Errorf("%w: save changes disabled, then review recurrence separately", store.ErrBotInvalid)
		}
		action := store.BotTriggerDisable
		if *a.Enabled {
			action = store.BotTriggerEnable
		}
		return s.propose(b, tr.ID, action, "Review schedule "+string(action), nil), nil
	}
	if a.Schedule == "" && a.Timezone == "" {
		return botScheduleChangeResult{}, store.ErrBotInvalid
	}
	if a.Schedule != "" {
		tr.Cron = a.Schedule
	}
	if a.Timezone != "" {
		tr.Timezone = a.Timezone
	}
	// Runtime fields must never be submitted as configuration/activation proof.
	tr.Enabled, tr.ScheduleID, tr.Fingerprint = false, nil, ""
	if err := validateBotCron(tr); err != nil {
		return botScheduleChangeResult{}, err
	}
	return s.propose(b, tr.ID, store.BotTriggerSave, "Review changed schedule (saved disabled)", &tr), nil
}

func (s botScheduleTools) remove(ctx tool.Context, a tools.RemoveScheduledJobArgs) (botScheduleChangeResult, error) {
	b, v, err := s.current(ctx)
	if err != nil {
		return botScheduleChangeResult{}, err
	}
	key := a.JobID
	if key == "" {
		key = a.JobName
	}
	tr, err := botCronByID(v, key)
	if err != nil {
		return botScheduleChangeResult{}, err
	}
	if a.JobID != "" && a.JobName != "" && a.JobName != tr.ID.String() {
		return botScheduleChangeResult{}, store.ErrBotInvalid
	}
	if s.request.Mandate != nil {
		return s.enqueue(ctx, b, store.BotControlScheduleRemove, "Remove schedule", tr)
	}
	return s.propose(b, tr.ID, store.BotTriggerRemove, "Review schedule removal", nil), nil
}

func (s botScheduleTools) enqueue(ctx tool.Context, b *store.Bot, operation store.BotControlOperation, reason string, tr store.TriggerSpec) (botScheduleChangeResult, error) {
	if s.services == nil || s.services.BotAuthorizedOperationFactory == nil {
		return botScheduleChangeResult{}, store.ErrBotUnsupported
	}
	if s.request.Mandate == nil || ctx.FunctionCallID() == "" {
		return botScheduleChangeResult{}, store.ErrBotForbidden
	}
	runtime, err := s.services.BotAuthorizedOperationFactory(ctx, s.services, b.Scope)
	if err != nil {
		return botScheduleChangeResult{}, err
	}
	if runtime == nil {
		return botScheduleChangeResult{}, store.ErrBotUnsupported
	}
	ref := s.request.Mandate.Authorization.Ref()
	key := sha256.Sum256([]byte(s.request.Run.ID.String() + "\n" + ctx.FunctionCallID()))
	q := store.BotAuthorizedOperationRequest{Lease: s.request.Run.Lease, MandateRef: ref, IdempotencyKey: "schedule-tool:" + hex.EncodeToString(key[:]), Payload: store.BotControlPayload{Operation: operation, Reason: reason, Schedule: &store.BotControlSchedule{Trigger: tr, ExpectedRevision: b.Revision}}}
	op, err := runtime.EnqueueAuthorizedOperation(ctx, q)
	if err != nil {
		return botScheduleChangeResult{}, err
	}
	op, err = verifyScheduleCapture(ctx, runtime, q, op)
	if err != nil {
		return botScheduleChangeResult{}, err
	}
	capture := bot.ScheduleCapture{OperationID: op.ID, BotID: b.ID, SourceRunID: s.request.Run.ID, OwnerID: b.OwnerID, Operation: operation, Status: store.BotOperationPending, Revision: op.Revision, ObservedAt: time.Now().UTC()}
	bot.RecordToolOutcome(ctx, ctx.FunctionCallID(), bot.ToolOutcome{Verified: true, ScheduleCapture: &capture})
	return botScheduleChangeResult{ToolResult: tools.ToolResult{Status: string(store.BotOperationPending), Message: capture.Acknowledgment()}, OperationID: op.ID.String()}, nil
}

func (s botScheduleTools) propose(b *store.Bot, id uuid.UUID, action store.BotTriggerAction, reason string, definition *store.TriggerSpec) botScheduleChangeResult {
	path := fmt.Sprintf("/api/studio/bots/%s/triggers/%s", b.ID, id)
	method := http.MethodPost
	switch action {
	case store.BotTriggerSave:
		method = http.MethodPut
	case store.BotTriggerRemove:
		method = http.MethodDelete
	default:
		path += "/" + string(action)
	}
	path += "?scope=" + string(b.Scope)
	review := botScheduleReview{Method: method, Path: path, Action: action, Body: botScheduleReviewBody{ExpectedRevision: b.Revision, Reason: reason, Definition: definition}}
	body, _ := json.Marshal(review.Body)
	digest := sha256.Sum256([]byte(method + "\n" + path + "\n" + string(body)))
	review.Digest = hex.EncodeToString(digest[:])
	return botScheduleChangeResult{ToolResult: tools.ToolResult{Status: botScheduleReviewRequired, Message: "No schedule changed and no proposal or operation was persisted. This is only a configuration request for the owner to review and submit after the active run ends."}, Review: review}
}
