package launcher

import "github.com/google/jsonschema-go/jsonschema"

// Bot schedules intentionally expose no generic recipient/channel schema.
// External delivery belongs to a separate reviewed Bot configuration operation.
func botScheduleInputSchema() *jsonschema.Schema {
	text := func(description string) *jsonschema.Schema {
		return &jsonschema.Schema{Type: "string", Description: description}
	}
	return &jsonschema.Schema{Type: "object", AdditionalProperties: &jsonschema.Schema{Not: &jsonschema.Schema{}}, Required: []string{"name", "mode", "schedule"}, AnyOf: []*jsonschema.Schema{{Required: []string{"instructions"}}, {Required: []string{"invocation"}}}, Properties: map[string]*jsonschema.Schema{
		"skill_id":     {Type: "string", Format: "uuid", Description: "Optional internal skill of this Bot. Omit to use the persisted current task; never ask the owner for an internal ID or copy its procedure"},
		"invocation":   text("Short task intent for the selected skill. Method is resolved at execution, never stored in the schedule."),
		"name":         text("Short name for the reviewed schedule"),
		"mode":         {Type: "string", Enum: []any{"adaptive"}, Description: "Bot schedules use adaptive mode; no mode-selection question needed"},
		"schedule":     text("Five-field cron; every minute is * * * * *"),
		"timezone":     text("IANA timezone; defaults to the Bot timezone or UTC"),
		"instructions": text("Task and output format from this conversation. Results appear in this Bot's home chat by default."),
		"scope":        text("Omit; ownership is bound to this Bot"),
	}}
}

// Expose only patches implemented by the Bot adapter, not Chat's mutable name.
func botScheduleUpdateInputSchema() *jsonschema.Schema {
	return &jsonschema.Schema{
		Type:                 "object",
		AdditionalProperties: &jsonschema.Schema{Not: &jsonschema.Schema{}},
		Required:             []string{"job_id"},
		Properties: map[string]*jsonschema.Schema{
			"job_id":   {Type: "string", MinLength: new(1), Description: "Existing Bot trigger ID returned by list_scheduled_jobs"},
			"enabled":  {Type: "boolean", Description: "Explicitly enable or pause recurrence; omit to preserve current state"},
			"schedule": {Type: "string", MinLength: new(1), Description: "New five-field cron expression; omit to preserve cadence"},
			"timezone": {Type: "string", MinLength: new(1), Description: "New IANA timezone; omit to preserve timezone"},
		},
		AnyOf: []*jsonschema.Schema{
			{Required: []string{"enabled"}},
			{Required: []string{"schedule"}},
			{Required: []string{"timezone"}},
		},
	}
}
