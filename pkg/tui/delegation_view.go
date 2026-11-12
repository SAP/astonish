package tui

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/SAP/astonish/pkg/tui/events"
	"github.com/SAP/astonish/pkg/tui/render"
)

type delegationDetailPollMsg struct{ request *int }

func (m model) delegationDetailActive() bool {
	d := m.delegationDetail
	if d.itemIdx < 0 || d.itemIdx >= len(m.tr.Items) || d.taskIdx < 0 || d.taskIdx >= len(m.tr.Items[d.itemIdx].DelegationTasks) {
		return false
	}
	switch m.tr.Items[d.itemIdx].DelegationTasks[d.taskIdx].Status {
	case "queued", "running", "waiting_on_model", "evaluating", "retrying":
		return true
	}
	return false
}

type delegationDetailTab uint8

const (
	delegationActivityTab delegationDetailTab = iota
	delegationHandoffTab
)

// Terminal wire DTO: deliberately allowlisted, with no agent/runtime dependency.
// Unknown fields (including prompts/history) can never become rendered content.
type delegationHandoff struct {
	Version               int      `json:"version"`
	TaskID                string   `json:"task_id"`
	ChildSessionID        string   `json:"child_session_id"`
	Attempt               int      `json:"attempt"`
	OriginalTask          string   `json:"original_task"`
	EffectiveTask         string   `json:"effective_task"`
	OriginalInstructions  string   `json:"original_instructions"`
	EffectiveInstructions string   `json:"effective_instructions"`
	WorkDir               string   `json:"workdir"`
	PreferredTools        []string `json:"preferred_tools"`
	InitialToolNames      []string `json:"initial_tool_names"`
	PlanStep              string   `json:"plan_step"`
	Plan                  struct {
		Status       string `json:"status"`
		PlanID       string `json:"plan_id"`
		Revision     int64  `json:"revision"`
		Sequence     int64  `json:"sequence"`
		Goal         string `json:"goal"`
		SourceMirror string `json:"source_mirror"`
		Document     *struct {
			Context      string `json:"context"`
			WhatNotToDo  string `json:"what_not_to_do"`
			Verification string `json:"verification"`
		} `json:"document"`
		Step *struct {
			Name          string `json:"name"`
			Description   string `json:"description"`
			Details       string `json:"details"`
			Summary       string `json:"summary"`
			Outcome       string `json:"outcome"`
			Verify        string `json:"verify"`
			VerifyKind    string `json:"verify_kind"`
			ParallelGroup string `json:"parallel_group"`
			Files         []struct {
				Path string `json:"path"`
				Kind string `json:"kind"`
			} `json:"files"`
		} `json:"step"`
	} `json:"plan"`
}

func (m model) renderDelegationHandoff(task events.DelegationTaskState, width int) string {
	var h delegationHandoff
	found := false
	for _, a := range task.Activity {
		if a.Type != "handoff" {
			continue
		}
		found = true
		if json.Unmarshal([]byte(a.Text), &h) != nil || h.Version != 1 {
			return wrapPlain("Handoff unavailable: invalid or unsupported captured record. Press r to Retry.", width)
		}
		break
	}
	if !found {
		return wrapPlain("Handoff unavailable for this attempt. Older sessions may not have a captured handoff; no context has been inferred from the current plan or parent history.", width)
	}
	if (h.TaskID != "" && task.TaskID != "" && h.TaskID != task.TaskID) || (h.ChildSessionID != "" && task.ChildSessionID != "" && h.ChildSessionID != task.ChildSessionID) || (h.Attempt > 0 && task.Attempt > 0 && h.Attempt != task.Attempt) {
		return wrapPlain("Handoff unavailable: captured identity does not match the selected attempt.", width)
	}
	var b strings.Builder
	section := func(title, value string) {
		if strings.TrimSpace(value) == "" {
			value = "Not recorded"
		}
		b.WriteString(m.theme.Text.Bold(true).Render(title) + "\n")
		b.WriteString(wrapPlain(value, width) + "\n\n")
	}
	section("Immutable handoff", fmt.Sprintf("Captured for attempt %d. This is the supplied task context, not the child system prompt or parent conversation.", h.Attempt))
	section("Task", h.EffectiveTask)
	section("Instructions", h.EffectiveInstructions)
	if h.OriginalTask != h.EffectiveTask {
		section("Original task", h.OriginalTask)
	}
	if h.OriginalInstructions != h.EffectiveInstructions {
		section("Original instructions", h.OriginalInstructions)
	}
	section("Working directory", h.WorkDir)
	preferred := strings.Join(h.PreferredTools, ", ")
	if preferred == "" {
		preferred = "None specified (preference, not an authorization boundary)"
	}
	section("Preferred tools", preferred)
	section("Initially available tools", strings.Join(h.InitialToolNames, ", "))
	section("Captured plan", fmt.Sprintf("Status: %s\nPlan ID: %s\nRevision: %d · sequence: %d\nAssigned phase: %s", h.Plan.Status, h.Plan.PlanID, h.Plan.Revision, h.Plan.Sequence, h.PlanStep))
	section("Source mirror (provenance only; not read)", h.Plan.SourceMirror)
	if h.Plan.Goal != "" {
		section("Plan goal", h.Plan.Goal)
	}
	if d := h.Plan.Document; d != nil {
		section("Definition context", d.Context)
		section("Exclusions / what not to do", d.WhatNotToDo)
		section("Plan verification", d.Verification)
	}
	if s := h.Plan.Step; s != nil {
		section("Phase", s.Name+" — "+s.Description)
		section("Phase summary", s.Summary)
		section("Full phase definition", s.Details)
		section("Expected outcome", s.Outcome)
		var files []string
		for _, f := range s.Files {
			files = append(files, f.Kind+" "+f.Path)
		}
		section("Phase files", strings.Join(files, "\n"))
		section("Phase verification ("+s.VerifyKind+")", s.Verify)
		if s.ParallelGroup != "" {
			section("Parallel group", s.ParallelGroup)
		}
	}
	return wrapPlain(b.String(), width)
}

// Merge a persisted snapshot with live rows that may have arrived during its
// read. Multiplicity is retained for identical text chunks and ID-less tools.
func mergeDelegationDetailActivity(snapshot, live []events.DelegationActivity) []events.DelegationActivity {
	out := append([]events.DelegationActivity(nil), snapshot...)
	key := func(a events.DelegationActivity) string {
		if a.Type == "handoff" {
			return "handoff"
		}
		if a.CallID != "" {
			return a.Type + ":" + a.CallID
		}
		a.Sequence = 0
		raw, _ := json.Marshal(a)
		return string(raw)
	}
	counts := make(map[string]int)
	for _, a := range snapshot {
		counts[key(a)]++
	}
	for _, a := range live {
		k := key(a)
		if counts[k] > 0 {
			counts[k]--
			continue
		}
		out = append(out, a)
	}
	return out
}

type delegationActivityRow struct {
	text string
	step *render.ToolStep
}

// One ordered projection pairs by call identity, including results delivered
// before calls. Calls retain their position among text; results do not duplicate
// them. Legacy ID-less records pair only when adjacent and unambiguous.
func projectDelegationActivity(activity []events.DelegationActivity) []delegationActivityRow {
	pairs := make(map[int]int)
	consumed := make(map[int]bool)
	results := make(map[string][]int)
	for i, a := range activity {
		if a.Type == "tool_result" && a.CallID != "" {
			results[a.CallID] = append(results[a.CallID], i)
		}
	}
	for i, a := range activity {
		if a.Type != "tool_call" {
			continue
		}
		if a.CallID != "" {
			if matches := results[a.CallID]; len(matches) > 0 {
				pairs[i] = matches[0]
				consumed[matches[0]] = true
				results[a.CallID] = matches[1:]
			}
		} else if i+1 < len(activity) {
			r := activity[i+1]
			if r.Type == "tool_result" && r.CallID == "" && r.ToolName == a.ToolName {
				pairs[i] = i + 1
				consumed[i+1] = true
			}
		}
	}
	var rows []delegationActivityRow
	for i, a := range activity {
		switch a.Type {
		case "text":
			rows = append(rows, delegationActivityRow{text: a.Text})
		case "tool_call", "tool_result":
			if consumed[i] {
				continue
			}
			step := render.ToolStep{Name: a.ToolName, Args: a.Args, Status: "running"}
			if a.Type == "tool_result" {
				step.Result = a.Result
				step.Status = delegationResultStatus(a.Result)
			}
			if j, ok := pairs[i]; ok {
				step.Result = activity[j].Result
				step.Status = delegationResultStatus(step.Result)
			}
			step.Result = normalizeDelegationResult(step.Result)
			step = unwrapDelegationTool(step)
			rows = append(rows, delegationActivityRow{step: &step})
		}
	}
	return rows
}

func normalizeDelegationResult(result any) any {
	if s, ok := result.(string); ok {
		var decoded any
		if json.Unmarshal([]byte(s), &decoded) == nil {
			return decoded
		}
	}
	return result
}

func delegationResultStatus(result any) string {
	if s, ok := result.(string); ok {
		var decoded any
		if json.Unmarshal([]byte(s), &decoded) == nil {
			return delegationResultStatus(decoded)
		}
		lower := strings.ToLower(strings.TrimSpace(s))
		if strings.HasPrefix(lower, "error") || strings.HasPrefix(lower, "failed") {
			return "error"
		}
	}
	if r, ok := result.(map[string]any); ok {
		if status, _ := r["status"].(string); status == "pending_authorization" || status == "denied" || status == "cancelled" || status == "interrupted" {
			return "error"
		}
		if e := r["error"]; e != nil && e != "" && e != false {
			return "error"
		}
		if r["success"] == false || r["isError"] == true || r["status"] == "error" || r["status"] == "failed" {
			return "error"
		}
		for _, key := range []string{"result", "output"} {
			if nested, ok := r[key]; ok && delegationResultStatus(nested) == "error" {
				return "error"
			}
		}
	}
	return "complete"
}

func unwrapDelegationTool(s render.ToolStep) render.ToolStep {
	if s.Name != "execute_tool" {
		return s
	}
	name, _ := s.Args["name"].(string)
	if name == "" {
		return s
	}
	// Catalog references may be namespaced (functions.edit_file).
	if i := strings.LastIndex(name, "."); i >= 0 {
		name = name[i+1:]
	}
	args, _ := s.Args["arguments"].(map[string]any)
	if raw, ok := s.Args["arguments"].(string); ok {
		_ = json.Unmarshal([]byte(raw), &args)
	}
	s.Name, s.Args = name, args
	if r, ok := s.Result.(map[string]any); ok {
		for _, key := range []string{"result", "output"} {
			if nested, ok := r[key]; ok && render.ExtractVerificationContext(normalizeDelegationResult(nested)) != "" {
				s.Result = normalizeDelegationResult(nested)
				break
			}
		}
	}
	return s
}

func (m model) renderDelegationActivity(task events.DelegationTaskState, width int) string {
	var b strings.Builder
	rows := projectDelegationActivity(task.Activity)
	if len(rows) == 0 {
		return m.theme.Muted.Italic(true).Render("No activity yet…") + "\n"
	}
	for _, row := range rows {
		if row.step == nil {
			b.WriteString(render.Markdown(row.text, width, m.theme.RenderStyles()) + "\n\n")
			continue
		}
		s := *row.step
		label := render.ToolStatusLabel(s.Status) + "  " + render.ToolDisplayName(s.Name)
		if s.Status == "running" {
			label += " · pending"
		}
		b.WriteString(m.theme.Text.Render(label) + "\n")
		if detail := render.ToolDetailBody(s, width); detail != "" {
			b.WriteString(wrapPlain(detail, width) + "\n")
		}
		if s.Status == "complete" && (s.Name == "edit_file" || s.Name == "write_file") {
			b.WriteString(wrapPlain(render.DiffFromToolStep(s.Name, s.Args, s.Result, width, true, m.info.WorkingDir, m.theme.RenderStyles()), width) + "\n")
		}
		if preview := render.ToolResultPreview(s, width); preview != "" {
			b.WriteString(wrapPlain(preview, width) + "\n")
		}
		if render.ToolDetailBody(s, width) == "" {
			b.WriteString(render.ToolArgsPreview(s, width) + "\n")
		}
		b.WriteByte('\n')
	}
	return wrapPlain(b.String(), width)
}
