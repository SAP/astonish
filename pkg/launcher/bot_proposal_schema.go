package launcher

import (
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/jsonschema-go/jsonschema"
)

// botProposalInputSchema describes JSON wire values, not Go storage types.
// In particular uuid.UUID is a JSON string, NOT its underlying [16]byte.
// Branches are optional here; BotControlPayload.Validate enforces the closed
// operation union and the repository validates operation-specific semantics.
func botProposalInputSchema() *jsonschema.Schema {
	scalar := func(kind string) *jsonschema.Schema { return &jsonschema.Schema{Type: kind} }
	text := func() *jsonschema.Schema { return scalar("string") }
	id := func() *jsonschema.Schema { return &jsonschema.Schema{Type: "string", Format: "uuid"} }
	timestamp := func() *jsonschema.Schema {
		return &jsonschema.Schema{Types: []string{"string", "null"}, Format: "date-time"}
	}
	nullable := func(s *jsonschema.Schema) *jsonschema.Schema {
		s.Types = []string{s.Type, "null"}
		s.Type = ""
		return s
	}
	object := func(p map[string]*jsonschema.Schema, required ...string) *jsonschema.Schema {
		return &jsonschema.Schema{Type: "object", Properties: p, Required: required, AdditionalProperties: &jsonschema.Schema{Not: &jsonschema.Schema{}}}
	}
	array := func(items *jsonschema.Schema) *jsonschema.Schema {
		return &jsonschema.Schema{Types: []string{"array", "null"}, Items: items}
	}
	binding := func() *jsonschema.Schema {
		return object(map[string]*jsonschema.Schema{"id": id(), "channel": text(), "chat_id": text(), "thread_id": text()}, "id", "channel", "chat_id")
	}
	persona := object(map[string]*jsonschema.Schema{"avatar": text(), "language": text(), "timezone": text(), "soul": text(), "identity": text(), "user": text()})
	autonomy := object(map[string]*jsonschema.Schema{
		"level": text(), "allowed_tools": array(text()), "denied_tools": array(text()), "allowed_paths": array(text()), "allowed_hosts": array(text()), "can_contact_people": scalar("boolean"), "can_publish": scalar("boolean"), "spend_currency": text(), "max_spend_per_run_minor": scalar("integer"), "max_spend_per_month_minor": scalar("integer"),
	}, "level")
	budget := object(map[string]*jsonschema.Schema{"max_runtime_seconds": scalar("integer"), "max_tool_calls": nullable(scalar("integer")), "max_tokens": nullable(scalar("integer")), "max_runs_per_day": nullable(scalar("integer")), "max_model_cost_micros": nullable(scalar("integer"))}, "max_runtime_seconds")
	delivery := object(map[string]*jsonschema.Schema{"notify": text(), "preferences": text(), "bindings": array(binding())})
	configuration := object(map[string]*jsonschema.Schema{"pause": scalar("boolean"), "autonomy": autonomy, "budget": budget, "delivery": delivery}, "autonomy", "budget", "delivery")
	evidence := object(map[string]*jsonschema.Schema{"kind": text(), "summary": text(), "source": text(), "observed_at": {Type: "string", Format: "date-time"}, "actor_id": id()}, "kind", "summary", "source", "observed_at", "actor_id")
	goal := object(map[string]*jsonschema.Schema{"id": id(), "bot_id": id(), "revision": scalar("integer"), "title": text(), "description": text(), "completion_criteria": text(), "status": text(), "due_at": timestamp(), "workspace_path": text(), "completed_at": timestamp(), "evidence": array(evidence)})
	goal.Required = []string{"title", "completion_criteria"}
	goal.Properties["title"].Pattern = `\S`
	goal.Properties["completion_criteria"].Pattern = `\S`
	goal.Properties["completion_criteria"].Description = "Required non-empty success condition for goal_create and goal_edit, grounded in the owner's requested outcome. Description does not substitute for this field. Do not invent broader obligations; ask for clarification if the outcome is unknown."
	goal.Properties["id"].Description = "Omit for goal_create; existing goal UUID for goal_edit."
	trigger := object(map[string]*jsonschema.Schema{
		"id": id(), "kind": text(), "schedule_id": nullable(id()), "goal_id": nullable(id()), "instructions": text(), "cron": text(), "timezone": text(), "enabled": scalar("boolean"), "hook_status": text(), "credential_ref": text(), "fingerprint": text(), "catch_up_once": scalar("boolean"), "poll": nullable(object(map[string]*jsonschema.Schema{"url": text(), "allowed_host": text()}, "url", "allowed_host")), "hook_watermark": text(), "channel_binding": nullable(binding()),
	})
	trigger.Properties["id"].Description = "Omit for schedule_create (server assigns UUID); required existing trigger UUID for schedule_edit, schedule_pause and schedule_remove."
	trigger.Properties["kind"].Description = "Use cron for scheduled checks."
	trigger.Properties["channel_binding"].Description = "Omit for schedules. Results appear in this Bot's own home chat; external delivery requires a separate explicit configuration review."
	schedule := object(map[string]*jsonschema.Schema{"trigger": trigger, "expected_revision": scalar("integer")}, "trigger", "expected_revision")
	schedule.Description = "Always provide current Bot revision as expected_revision. For create provide cron trigger instructions, cron, timezone and enabled; omit id and schedule_id. For edit provide existing trigger id and schedule_id plus replacement settings; pause/remove require existing id and schedule_id. Never guess IDs or revision."
	memory := object(map[string]*jsonschema.Schema{"expected_fingerprint": text(), "memory_id": text(), "content": text(), "category": text(), "expected_revision": scalar("integer")}, "expected_revision")
	memory.Description = "Always provide current Bot revision as expected_revision for memory_remember, memory_correct and memory_forget. Never guess or default revision; obtain current context before retrying."
	operation := text()
	operation.Enum = []any{store.BotControlResponsibility, store.BotControlPersona, store.BotControlConfigurationOperation, store.BotControlGoalCreate, store.BotControlGoalEdit, store.BotControlMemoryRemember, store.BotControlMemoryCorrect, store.BotControlMemoryForget, store.BotControlScheduleCreate, store.BotControlScheduleEdit, store.BotControlSchedulePause, store.BotControlScheduleRemove, store.BotControlPriorityPause, store.BotControlResumeTasks, store.BotControlTaskOnceOperation, store.BotControlActivate, store.BotControlResume}
	payload := object(map[string]*jsonschema.Schema{"operation": operation, "reason": text(), "responsibility": nullable(text()), "persona": nullable(persona), "configuration": nullable(configuration), "goal": nullable(object(map[string]*jsonschema.Schema{"goal": goal, "expected_revision": scalar("integer")}, "goal")), "memory": nullable(memory), "schedule": nullable(schedule), "resume_targets": nullable(object(map[string]*jsonschema.Schema{"schedule_ids": array(id())}, "schedule_ids")), "task_once": nullable(object(map[string]*jsonschema.Schema{"instructions": text()}, "instructions"))}, "operation", "reason")
	payload.Properties["goal"].Description = "Goal fields MUST be nested under payload.goal.goal, not directly under payload.goal. Example: {\"goal\":{\"title\":\"Clear updates\",\"completion_criteria\":\"Each update is easy to understand\"}}. For goal_edit, expected_revision is mandatory and must be the current goal revision (not Bot revision). For goal_create omit expected_revision or provide zero. Never guess a revision."
	payload.AllOf = []*jsonschema.Schema{{
		If:   &jsonschema.Schema{Properties: map[string]*jsonschema.Schema{"operation": {Enum: []any{store.BotControlGoalEdit}}}, Required: []string{"operation"}},
		Then: &jsonschema.Schema{Properties: map[string]*jsonschema.Schema{"goal": {Type: "object", Required: []string{"expected_revision"}}}, Required: []string{"goal"}},
	}}
	resumeIDs := payload.Properties["resume_targets"].Properties["schedule_ids"]
	resumeIDs.Description = "Canonical scheduler job UUIDs from current triggers[].schedule_id or list_scheduled_jobs jobs[].schedule_id (same as job_id). Do not use triggers[].id or jobs[].id: those are Bot trigger IDs, not scheduler job IDs."
	resumeIDs.Items.Description = "Existing scheduler job UUID copied from schedule_id, never the trigger id."
	payload.Properties["resume_targets"].Description = "resume_tasks requires 1-32 unique existing canonical scheduler job IDs from the current Bot, copied from schedule_id, NOT trigger id. Only these tasks resume under their original unexpired grants; never guess IDs. priority_pause has no payload branch and immediately fences work."
	payload.Description = "Provide exactly the one payload matching operation; omit all other branches. Mandated routine controls return operation status and decision, not a human-review proposal. Pending is not applied. activate/resume have no payload branch. Never supply authority, owner, scope, grants or proposal status."
	return object(map[string]*jsonschema.Schema{"payload": payload}, "payload")
}

// A mandated turn must select exact tasks, not fall through to the legacy
// lifecycle-only resume proposal. No IDs or authority are inferred here.
func botMandatedProposalInputSchema() *jsonschema.Schema {
	schema := botProposalInputSchema()
	payload := schema.Properties["payload"]
	operation := payload.Properties["operation"]
	values := operation.Enum[:0]
	for _, value := range operation.Enum {
		if value != store.BotControlResume {
			values = append(values, value)
		}
	}
	operation.Enum = values
	operation.Description = "For explicit resumption of selected recurring tasks use resume_tasks, not legacy resume or activate."
	payload.Description = "Provide exactly the branch matching operation. Mandated routine controls return durable operation status and decision; pending is not applied. Resume only the explicitly requested tasks using resume_tasks and current schedule IDs; no separate lifecycle resume or owner review is needed within the mandate. Out-of-ceiling operations still require bounded authorization. Never supply authority, owner, scope, grants or proposal status."
	payload.AllOf = append(payload.AllOf, &jsonschema.Schema{
		If:   &jsonschema.Schema{Properties: map[string]*jsonschema.Schema{"operation": {Enum: []any{store.BotControlResumeTasks}}}, Required: []string{"operation"}},
		Then: &jsonschema.Schema{Properties: map[string]*jsonschema.Schema{"resume_targets": {Type: "object", Required: []string{"schedule_ids"}}}, Required: []string{"resume_targets"}},
	})
	return schema
}
