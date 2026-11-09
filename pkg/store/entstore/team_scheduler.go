package entstore

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/google/uuid"

	teament "github.com/SAP/astonish/ent/team"
	"github.com/SAP/astonish/ent/team/scheduledjob"
	"github.com/SAP/astonish/pkg/store"
)

// teamSchedulerStore implements store.SchedulerStore using the Ent team client.
type teamSchedulerStore struct {
	client *teament.Client
}

var _ store.SchedulerStore = (*teamSchedulerStore)(nil)

func (s *teamSchedulerStore) List(ctx context.Context) []*store.ScheduledJob {
	jobs, err := s.client.ScheduledJob.Query().
		Order(scheduledjob.ByName()).
		All(ctx)
	if err != nil {
		slog.Error("list team scheduled jobs failed", "error", err)
		return nil
	}

	result := make([]*store.ScheduledJob, len(jobs))
	for i, j := range jobs {
		result[i] = entScheduledJobToStore(j)
	}
	return result
}

func (s *teamSchedulerStore) Get(ctx context.Context, id string) *store.ScheduledJob {
	uid, err := uuid.Parse(id)
	if err != nil {
		return nil
	}
	j, err := s.client.ScheduledJob.Get(ctx, uid)
	if err != nil {
		return nil
	}
	return entScheduledJobToStore(j)
}

func (s *teamSchedulerStore) GetByName(ctx context.Context, name string) *store.ScheduledJob {
	j, err := s.client.ScheduledJob.Query().
		Where(scheduledjob.NameEQ(name)).
		Only(ctx)
	if err != nil {
		return nil
	}
	return entScheduledJobToStore(j)
}

func (s *teamSchedulerStore) Add(ctx context.Context, job *store.ScheduledJob) error {
	create := s.client.ScheduledJob.Create().
		SetName(job.Name).
		SetSchedule(job.Schedule.Cron).
		SetMode(scheduledjob.Mode(job.Mode)).
		SetStatus(scheduledjob.StatusActive)

	if job.ID != "" {
		if id, err := uuid.Parse(job.ID); err == nil {
			create.SetID(id)
		}
	}

	payload := jobToPayloadMap(job)
	create.SetPayload(payload)

	if job.OwnerID != "" {
		if uid, err := uuid.Parse(job.OwnerID); err == nil {
			create.SetCreatedBy(uid)
		}
	}

	if job.NextRun != nil {
		create.SetNextRunAt(*job.NextRun)
	}

	created, err := create.Save(ctx)
	if err != nil {
		return fmt.Errorf("entstore: SchedulerStore.Add: %w", err)
	}
	job.ID = created.ID.String()
	job.CreatedAt = created.CreatedAt
	return nil
}

func (s *teamSchedulerStore) Update(ctx context.Context, job *store.ScheduledJob) error {
	uid, err := uuid.Parse(job.ID)
	if err != nil {
		return fmt.Errorf("entstore: SchedulerStore.Update: invalid ID: %w", err)
	}

	update := s.client.ScheduledJob.UpdateOneID(uid).
		SetName(job.Name).
		SetSchedule(job.Schedule.Cron).
		SetMode(scheduledjob.Mode(job.Mode)).
		SetLastStatus(job.LastStatus).
		SetLastError(job.LastError).
		SetConsecutiveFailures(job.ConsecutiveFailures)

	if job.Enabled {
		update.SetStatus(scheduledjob.StatusActive)
	} else {
		update.SetStatus(scheduledjob.StatusPaused)
	}

	payload := jobToPayloadMap(job)
	update.SetPayload(payload)

	if job.LastRun != nil {
		update.SetLastRunAt(*job.LastRun)
	} else {
		update.ClearLastRunAt()
	}

	if job.NextRun != nil {
		update.SetNextRunAt(*job.NextRun)
	} else {
		update.ClearNextRunAt()
	}

	return update.Exec(ctx)
}

func (s *teamSchedulerStore) Remove(ctx context.Context, id string) error {
	uid, err := uuid.Parse(id)
	if err != nil {
		return fmt.Errorf("entstore: SchedulerStore.Remove: invalid ID: %w", err)
	}
	return s.client.ScheduledJob.DeleteOneID(uid).Exec(ctx)
}

// --- Helpers ---

func entScheduledJobToStore(j *teament.ScheduledJob) *store.ScheduledJob {
	job := &store.ScheduledJob{
		ID:                  j.ID.String(),
		Name:                j.Name,
		Mode:                string(j.Mode),
		Enabled:             j.Status == scheduledjob.StatusActive,
		CreatedAt:           j.CreatedAt,
		LastStatus:          j.LastStatus,
		LastError:           j.LastError,
		ConsecutiveFailures: j.ConsecutiveFailures,
		LastRun:             j.LastRunAt,
		NextRun:             j.NextRunAt,
	}
	if j.CreatedBy != nil {
		job.OwnerID = j.CreatedBy.String()
	}

	job.Schedule = store.JobSchedule{Cron: j.Schedule}
	if tz, ok := j.Payload["timezone"].(string); ok {
		job.Schedule.Timezone = tz
	}
	if teamSlug, ok := j.Payload["team_slug"].(string); ok {
		job.TeamSlug = teamSlug
	}

	// Extract payload fields.
	job.Payload = payloadMapToJobPayload(j.Payload)
	job.Delivery = payloadMapToJobDelivery(j.Payload)
	job.Scope = store.JobScopeTeam

	return job
}

func jobToPayloadMap(job *store.ScheduledJob) map[string]any {
	payload := make(map[string]any)
	if job.Payload.BotID != "" {
		payload["bot_id"] = job.Payload.BotID
	}
	if job.Payload.BotVersion != 0 {
		payload["bot_version"] = job.Payload.BotVersion
	}
	if job.Payload.BotTriggerID != "" {
		payload["bot_trigger_id"] = job.Payload.BotTriggerID
	}

	// Store schedule timezone in payload.
	if job.Schedule.Timezone != "" {
		payload["timezone"] = job.Schedule.Timezone
	}
	// Team context for personal jobs (credential/flow/MCP fallback at tick time).
	if job.TeamSlug != "" {
		payload["team_slug"] = job.TeamSlug
	}

	// Payload fields.
	if job.Payload.Flow != "" {
		payload["flow"] = job.Payload.Flow
	}
	if len(job.Payload.Params) > 0 {
		payload["params"] = job.Payload.Params
	}
	if job.Payload.Instructions != "" {
		payload["instructions"] = job.Payload.Instructions
	}

	// Delivery fields.
	hasDeliveryData := job.Delivery.Channel != "" || job.Delivery.Mode != "" ||
		job.Delivery.Target != "" || len(job.Delivery.MemberIDs) > 0 ||
		len(job.Delivery.ChannelFilter) > 0 || len(job.Delivery.MemberChannels) > 0
	if hasDeliveryData {
		delivery := map[string]any{
			"channel": job.Delivery.Channel,
			"target":  job.Delivery.Target,
		}
		if job.Delivery.Mode != "" {
			delivery["mode"] = job.Delivery.Mode
		}
		if len(job.Delivery.MemberIDs) > 0 {
			delivery["member_ids"] = job.Delivery.MemberIDs
		}
		if len(job.Delivery.ChannelFilter) > 0 {
			delivery["channel_filter"] = job.Delivery.ChannelFilter
		}
		if len(job.Delivery.MemberChannels) > 0 {
			delivery["member_channels"] = job.Delivery.MemberChannels
		}
		payload["delivery"] = delivery
	}

	return payload
}

func payloadMapToJobPayload(m map[string]any) store.JobPayload {
	var p store.JobPayload
	p.BotID, _ = m["bot_id"].(string)
	p.BotTriggerID, _ = m["bot_trigger_id"].(string)
	switch v := m["bot_version"].(type) {
	case int64:
		p.BotVersion = v
	case float64:
		if v == float64(int64(v)) {
			p.BotVersion = int64(v)
		}
	}
	if flow, ok := m["flow"].(string); ok {
		p.Flow = flow
	}
	if instructions, ok := m["instructions"].(string); ok {
		p.Instructions = instructions
	}
	if params, ok := m["params"].(map[string]any); ok {
		p.Params = make(map[string]string, len(params))
		for k, v := range params {
			p.Params[k] = fmt.Sprintf("%v", v)
		}
	}
	return p
}

func payloadMapToJobDelivery(m map[string]any) store.JobDelivery {
	var d store.JobDelivery
	deliveryRaw, ok := m["delivery"]
	if !ok {
		return d
	}
	deliveryMap, ok := deliveryRaw.(map[string]any)
	if !ok {
		return d
	}

	d.Channel, _ = deliveryMap["channel"].(string)
	d.Target, _ = deliveryMap["target"].(string)
	d.Mode, _ = deliveryMap["mode"].(string)

	if ids, ok := deliveryMap["member_ids"].([]any); ok {
		for _, id := range ids {
			if s, ok := id.(string); ok {
				d.MemberIDs = append(d.MemberIDs, s)
			}
		}
	}
	if cf, ok := deliveryMap["channel_filter"].([]any); ok {
		for _, c := range cf {
			if s, ok := c.(string); ok {
				d.ChannelFilter = append(d.ChannelFilter, s)
			}
		}
	}
	if mc, ok := deliveryMap["member_channels"].(map[string]any); ok {
		d.MemberChannels = make(map[string][]string, len(mc))
		for userID, channels := range mc {
			if chList, ok := channels.([]any); ok {
				for _, ch := range chList {
					if s, ok := ch.(string); ok {
						d.MemberChannels[userID] = append(d.MemberChannels[userID], s)
					}
				}
			}
		}
	}

	return d
}
