package entstore

import (
	"context"
	"encoding/json"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"strings"
)

// Private issuer records, never scheduler payload or model parameters.
type scheduledTaskBinding struct {
	Ref          store.BotMandateRef
	Parent       store.BotMandateRef
	OperationID  uuid.UUID
	TriggerID    uuid.UUID
	JobID        uuid.UUID
	Instructions string
}

func scheduledTaskKey(id uuid.UUID) string  { return "scheduled-task:" + id.String() }
func scheduledTaskHead(id uuid.UUID) string { return "scheduled-task-head:" + id.String() }
func (s *botAuthorizedOperationRuntime) issueScheduledTask(ctx context.Context, tx botDraftDB, rec *botStoredRecord, op store.BotOperation, tr store.TriggerSpec, parent store.BotMandateRecord) error {
	if op.Payload.Operation != store.BotControlScheduleCreate && op.Payload.Operation != store.BotControlScheduleEdit {
		return nil
	}
	m := parent.Mandate.Clone()
	m.Ref.ID = uuid.NewSHA1(op.ID, []byte("scheduled-task"))
	// The lifetime and resource ceiling are inherited, never renewed by application.
	m.Purpose = execution.BotMandatePurposeTask
	// A scheduled task cannot issue further self-control operations.
	for i := range m.Constraints {
		if m.Constraints[i].Action == execution.CapabilityToolExecute {
			resources := []string{}
			for _, resource := range m.Constraints[i].Resources {
				if resource != "schedule_job" && resource != "update_scheduled_job" && resource != "remove_scheduled_job" && resource != "list_scheduled_jobs" && resource != "propose_bot_control" && !strings.HasPrefix(resource, "bot_control:") {
					resources = append(resources, resource)
				}
			}
			m.Constraints[i].Resources = resources
		}
	}
	filtered := m.Constraints[:0]
	for _, c := range m.Constraints {
		if len(c.Resources) > 0 {
			filtered = append(filtered, c)
		}
	}
	m.Constraints = filtered
	record := parent.Clone()
	record.Mandate = m
	if err := record.Validate(s.repo.clock()); err != nil {
		return err
	}
	binding := scheduledTaskBinding{Ref: m.Ref, Parent: op.MandateRef, OperationID: op.ID, TriggerID: tr.ID, JobID: *tr.ScheduleID, Instructions: tr.Instructions}
	if err := appendMandateRecord(ctx, tx, rec, mandateKey(m.Ref), record, s.repo.clock()); err != nil {
		return err
	}
	if err := appendMandateRecord(ctx, tx, rec, scheduledTaskKey(m.Ref.ID), binding, s.repo.clock()); err != nil {
		return err
	}
	if rec.Metadata == nil {
		rec.Metadata = map[string]any{}
	}
	rec.Metadata[scheduledTaskHead(tr.ID)] = m.Ref
	return saveMandateMetadata(ctx, tx, rec)
}
func readScheduledTask(ctx context.Context, tx botDraftDB, ref store.BotMandateRef) (*scheduledTaskBinding, error) {
	a, err := tx.FindActivity(ctx, ref.BotID, scheduledTaskKey(ref.ID))
	if err != nil {
		return nil, err
	}
	var b scheduledTaskBinding
	if a.Kind != mandateRecordKind || json.Unmarshal(a.Payload, &b) != nil || b.Ref != ref || b.Parent == ref || b.TriggerID == uuid.Nil || b.JobID == uuid.Nil {
		return nil, store.ErrBotForbidden
	}
	return &b, nil
}
func (s *botMandateRuntime) verifyScheduledTask(ctx context.Context, tx botDraftDB, rec *botStoredRecord, m *store.BotMandateRecord, b *scheduledTaskBinding) error {
	parent, _, err := s.verify(ctx, tx, b.Parent, execution.BotMandatePurposeTask)
	if err != nil {
		return err
	}
	if parent.Provenance != m.Provenance || m.Mandate.ExpiresAt.After(parent.Mandate.ExpiresAt) {
		return store.ErrBotForbidden
	}
	if err := checkMandatePolicy(m.Mandate, store.BotMandatePolicy{Scopes: parent.Mandate.Scopes, Constraints: parent.Mandate.Constraints, Limits: parent.Mandate.Limits}); err != nil {
		return err
	}
	op, err := readMandateOperation(ctx, tx, rec, b.Parent, b.OperationID)
	if err != nil {
		return err
	}
	if op.Status != store.BotOperationApplied || op.Receipt == nil || op.Receipt.Schedule == nil || op.Receipt.Schedule.TriggerID != b.TriggerID || op.Receipt.Schedule.ScheduleID != b.JobID {
		return store.ErrBotForbidden
	}
	return verifyAuthorizedGrant(ctx, tx, *op)
}
func (r *botRepository) attachScheduledTask(ctx context.Context, tx botDraftDB, rec *botStoredRecord, run *store.BotRun, req store.BotRunRequest) error {
	raw, exists := rec.Metadata[scheduledTaskHead(req.TriggerID)]
	if !exists {
		return nil
	} // legacy schedule
	data, err := json.Marshal(raw)
	if err != nil {
		return err
	}
	var ref store.BotMandateRef
	if json.Unmarshal(data, &ref) != nil {
		return store.ErrBotForbidden
	}
	policy, ok := store.BotMandateAdmissionFromContext(ctx)
	if !ok {
		return store.ErrBotForbidden
	}
	rt := &botMandateRuntime{repo: r, policy: policy}
	if _, _, err = rt.verify(ctx, tx, ref, execution.BotMandatePurposeTask); err != nil {
		return err
	}
	binding, err := readScheduledTask(ctx, tx, ref)
	if err != nil {
		return err
	}
	if binding.TriggerID != req.TriggerID || binding.Instructions != req.Instructions {
		return store.ErrBotForbidden
	}
	v, err := tx.ReadVersion(ctx, req.BotID, req.Version)
	if err != nil {
		return err
	}
	matched := false
	for _, tr := range v.Triggers {
		if tr.ID == binding.TriggerID && tr.ScheduleID != nil && *tr.ScheduleID == binding.JobID && tr.Instructions == binding.Instructions {
			matched = true
		}
	}
	if !matched {
		return store.ErrBotForbidden
	}
	run.MandateRef = &ref
	return nil
}
