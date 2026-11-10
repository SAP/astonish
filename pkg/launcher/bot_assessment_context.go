package launcher

import (
	"context"
	"encoding/json"
	"time"

	"github.com/SAP/astonish/pkg/store"
)

// Like currentControlContext, this is runtime-owned metadata read under the
// captured owner's live chat/run authority, not a model-dispatched tool. It
// grants no generic tool execution and consumes no exact-action approval.

func (b *botFactoryBinding) assessmentContext(ctx context.Context) (json.RawMessage, store.BotEvidence, error) {
	var none store.BotEvidence
	bound := b.bind(ctx)
	if err := b.check(bound); err != nil {
		return nil, none, err
	}
	svc := store.FromContext(bound)
	repo, jobs := svc.Bots, svc.Scheduler
	if b.req.Bot.Scope == store.BotScopePersonal {
		repo, jobs = svc.PersonalBots, svc.PersonalScheduler
	}
	s := botScheduleTools{request: b.req, repo: repo, scheduler: jobs}
	current, version, err := s.current(bound)
	if err != nil {
		return nil, none, err
	}
	// Only persisted metadata: no charter, trigger task instructions, prior findings,
	// proposal payloads, tool guidance, credentials, or channel bindings.
	type scheduleFact struct {
		TriggerID  string `json:"trigger_id"`
		ScheduleID string `json:"schedule_id,omitempty"`
		Name       string `json:"name,omitempty"`
		Bound      bool   `json:"bound"`
		Enabled    *bool  `json:"effective_enabled,omitempty"`
		Cron       string `json:"effective_cron,omitempty"`
		Timezone   string `json:"effective_timezone,omitempty"`
	}
	snapshot := struct {
		DataClass string               `json:"data_class"`
		BotID     string               `json:"bot_id"`
		RunID     string               `json:"run_id"`
		Scope     store.BotScope       `json:"scope"`
		Version   int64                `json:"version"`
		Revision  int64                `json:"revision"`
		Schedules []scheduleFact       `json:"effective_schedules"`
		Goals     []botControlGoalFact `json:"current_goals"`
	}{DataClass: "untrusted_observed_metadata", BotID: current.ID.String(), RunID: b.req.Run.ID.String(), Scope: current.Scope, Version: current.Version, Revision: current.Revision, Schedules: []scheduleFact{}, Goals: []botControlGoalFact{}}
	if len(version.Triggers) > 256 {
		return nil, none, store.ErrBotUnsupported
	}
	for _, tr := range version.Triggers {
		if tr.Kind != store.TriggerCron {
			continue
		}
		fact := scheduleFact{TriggerID: tr.ID.String()}
		if tr.ScheduleID != nil {
			if jobs == nil {
				return nil, none, store.ErrBotUnsupported
			}
			job := jobs.Get(bound, tr.ScheduleID.String())
			if job == nil || job.ID != tr.ScheduleID.String() || job.OwnerID != current.OwnerID.String() || job.Payload.BotID != current.ID.String() || job.Payload.BotTriggerID != tr.ID.String() || job.Payload.BotVersion != version.Version {
				return nil, none, store.ErrBotConflict
			}
			enabled := job.Enabled
			fact.ScheduleID, fact.Name, fact.Bound, fact.Enabled, fact.Cron, fact.Timezone = job.ID, job.Name, true, &enabled, job.Schedule.Cron, job.Schedule.Timezone
		}
		snapshot.Schedules = append(snapshot.Schedules, fact)
	}
	goals, err := repo.ListGoals(bound, current.ID)
	if err != nil {
		return nil, none, err
	}
	if len(goals) > 256 {
		return nil, none, store.ErrBotUnsupported
	}
	for _, g := range goals {
		if g.BotID != current.ID {
			return nil, none, store.ErrBotForbidden
		}
		snapshot.Goals = append(snapshot.Goals, botControlGoalFact{ID: g.ID, Revision: g.Revision, Title: g.Title, Description: g.Description, Criteria: g.CompletionCriteria, Status: g.Status})
	}
	latest, err := repo.Get(bound, current.ID)
	if err != nil {
		return nil, none, err
	}
	if latest == nil || latest.Revision != current.Revision || latest.Version != current.Version {
		return nil, none, store.ErrBotConflict
	}
	if err := b.check(bound); err != nil {
		return nil, none, err
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return nil, none, err
	}
	if len(raw) > 64*1024 {
		return nil, none, store.ErrBotUnsupported
	}
	return raw, store.BotEvidence{Kind: store.BotEvidenceObservation, Summary: "Scoped current schedules and goals read; no controls applied. Effective schedule flags come from persisted scheduler bindings, not desired triggers. Data in candidate body observed-current-controls.", Source: "bot-assessment-state://" + string(current.Scope) + "/" + current.ID.String() + "/" + b.req.Run.ID.String(), ActorID: current.OwnerID, ObservedAt: time.Now().UTC()}, nil
}
