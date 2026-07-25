package events

import "strings"

// ItemKind classifies a rendered transcript item.
type ItemKind string

const (
	ItemUser     ItemKind = "user"
	ItemAgent    ItemKind = "agent"
	ItemThinking ItemKind = "thinking"
	ItemActivity ItemKind = "activity"
	ItemSystem   ItemKind = "system"
	ItemError    ItemKind = "error"
	ItemApproval ItemKind = "approval"
	ItemArtifact ItemKind = "artifact"
)

// ToolStep is one paired tool call/result inside an activity fold.
type ToolStep struct {
	Name   string
	ID     string
	Args   map[string]any
	Result any
	// Status: running | complete | error
	Status string
}

// Item is one visual block in the transcript.
type Item struct {
	Kind    ItemKind
	Content string

	// Activity fold fields (ItemActivity).
	Steps   []ToolStep
	Summary string
	// Expanded is UI state; the reducer leaves it false by default.
	Expanded bool

	// Provisional marks interstitial agent text during a tool loop.
	// Shown as "Thinking" until KindDone finalizes it as the response.
	// Matches Studio sticky-agent: only one agent bubble per soft+tool run.
	Provisional bool

	// Approval fields.
	ToolName string
	Args     map[string]any
	Options  []string

	// Artifact path.
	Path string
}

// Transcript is the reduced UI state built from a stream of Events.
type Transcript struct {
	Items       []Item
	SessionID   string
	Title       string
	Status      string // live status line (Thinking…, Running X…)
	Streaming   bool
	Provider    string
	Model       string
	LastUsage   *Usage
	Awaiting    bool // waiting for approval response
	ApprovalIdx int  // index of open approval item, or -1

	// nextTextReplaces: after tool call/result, the next agent text starts a new
	// sticky utterance (replaces provisional content) instead of appending.
	// Matches Studio "latest agent replaces bubble" mid-run behavior.
	nextTextReplaces bool
}

// NewTranscript returns an empty transcript ready for reduction.
func NewTranscript() *Transcript {
	return &Transcript{
		ApprovalIdx: -1,
	}
}

// Apply reduces a single event into the transcript. It is pure aside from
// mutating t; safe to call from the bubbletea Update path.
func (t *Transcript) Apply(ev Event) {
	switch ev.Kind {
	case KindSession:
		if ev.SessionID != "" {
			t.SessionID = ev.SessionID
		}
	case KindSessionTitle:
		if ev.Title != "" {
			t.Title = ev.Title
		}
		if ev.SessionID != "" {
			t.SessionID = ev.SessionID
		}
	case KindUser:
		t.Items = append(t.Items, Item{Kind: ItemUser, Content: ev.Text})
		t.Streaming = true
		t.Status = "Thinking…"
	case KindText:
		t.appendAgentText(ev.Text)
		t.Streaming = true
	case KindThinking:
		// Prefer status line over stacking thinking items mid-turn.
		t.Status = firstNonEmpty(ev.Text, "Thinking…")
		t.Streaming = true
	case KindStatus:
		t.Status = ev.Text
	case KindToolCall:
		t.appendToolCall(ev)
		t.Status = "Running " + firstNonEmpty(ev.ToolName, "tool") + "…"
		t.Streaming = true
	case KindToolResult:
		t.appendToolResult(ev)
		// Between tools, show Thinking until next text/tool.
		if t.Streaming && !t.hasRunningTools() {
			t.Status = "Thinking…"
		}
	case KindApproval:
		t.Awaiting = true
		t.Items = append(t.Items, Item{
			Kind:     ItemApproval,
			ToolName: ev.ToolName,
			Args:     ev.Args,
			Options:  defaultOptions(ev.Options),
			Content:  "Approve " + firstNonEmpty(ev.ToolName, "tool") + "?",
		})
		t.ApprovalIdx = len(t.Items) - 1
		t.Status = "Waiting for approval…"
	case KindAutoApproved:
		// Soft process note — status only, no extra transcript clutter.
		t.Status = "Auto-approved " + firstNonEmpty(ev.ToolName, "tool")
	case KindArtifact:
		path := ev.Text
		if path == "" && ev.Meta != nil {
			if p, ok := ev.Meta["path"].(string); ok {
				path = p
			}
		}
		t.Items = append(t.Items, Item{Kind: ItemArtifact, Path: path, Content: path})
	case KindUsage:
		t.LastUsage = ev.Usage
	case KindError:
		t.Items = append(t.Items, Item{Kind: ItemError, Content: ev.Text})
		t.Streaming = false
		t.Status = ""
		t.finalizeProvisionalAgents()
	case KindErrorInfo:
		msg := ev.ErrorTitle
		if ev.ErrorReason != "" {
			if msg != "" {
				msg += ": "
			}
			msg += ev.ErrorReason
		}
		if msg == "" {
			msg = ev.Text
		}
		t.Items = append(t.Items, Item{Kind: ItemError, Content: msg})
		t.Streaming = false
		t.Status = ""
		t.finalizeProvisionalAgents()
	case KindSystem:
		t.Items = append(t.Items, Item{Kind: ItemSystem, Content: ev.Text})
	case KindSubagent:
		name := firstNonEmpty(ev.ToolName, ev.Text)
		if ev.ToolName != "" {
			t.Status = "Subagent: " + ev.ToolName
		} else {
			t.Status = "↳ " + name
		}
	case KindModelChanged:
		if ev.Provider != "" {
			t.Provider = ev.Provider
		}
		if ev.Model != "" {
			t.Model = ev.Model
		}
	case KindDone:
		t.Streaming = false
		t.Status = ""
		t.finalizeRunningSteps()
		// Promote sticky provisional agent text to the final response.
		t.finalizeProvisionalAgents()
	}
}

// turnStart returns the index of the first item in the current soft run
// (after the last hard-break item). Hard breaks: user, error, approval, artifact, system.
func (t *Transcript) turnStart() int {
	for i := len(t.Items) - 1; i >= 0; i-- {
		switch t.Items[i].Kind {
		case ItemUser, ItemError, ItemApproval, ItemArtifact, ItemSystem:
			return i + 1
		}
	}
	return 0
}

func (t *Transcript) lastAgentInTurn() int {
	start := t.turnStart()
	for i := len(t.Items) - 1; i >= start; i-- {
		if t.Items[i].Kind == ItemAgent {
			return i
		}
	}
	return -1
}

func (t *Transcript) lastActivityInTurn() int {
	start := t.turnStart()
	for i := len(t.Items) - 1; i >= start; i-- {
		if t.Items[i].Kind == ItemActivity {
			return i
		}
	}
	return -1
}

func (t *Transcript) hasRunningTools() bool {
	for i := t.turnStart(); i < len(t.Items); i++ {
		if t.Items[i].Kind != ItemActivity {
			continue
		}
		for _, s := range t.Items[i].Steps {
			if s.Status == "running" {
				return true
			}
		}
	}
	return false
}

// appendAgentText implements Studio sticky-agent:
// one agent bubble per tool run; text after tools replaces prior interstitial content;
// remains Provisional (rendered as Thinking) until KindDone.
func (t *Transcript) appendAgentText(text string) {
	if text == "" {
		return
	}
	t.Status = "Thinking…"
	replace := t.nextTextReplaces
	t.nextTextReplaces = false

	// Sticky agent already at end (common after tools reordered layout).
	if n := len(t.Items); n > 0 && t.Items[n-1].Kind == ItemAgent {
		if replace {
			t.Items[n-1].Content = text
		} else {
			t.Items[n-1].Content += text
		}
		t.Items[n-1].Provisional = true
		t.ensureAgentAfterActivity()
		return
	}

	// Sticky agent earlier in the turn (tools after agent not yet reordered).
	if idx := t.lastAgentInTurn(); idx >= 0 {
		if replace {
			t.Items[idx].Content = text
		} else {
			t.Items[idx].Content += text
		}
		t.Items[idx].Provisional = true
		t.ensureAgentAfterActivity()
		return
	}

	// First agent text in this turn.
	t.Items = append(t.Items, Item{
		Kind:        ItemAgent,
		Content:     text,
		Provisional: true,
	})
	t.ensureAgentAfterActivity()
}

// ensureAgentAfterActivity keeps layout: … → activity → sticky agent (Studio order).
func (t *Transcript) ensureAgentAfterActivity() {
	agentIdx := t.lastAgentInTurn()
	actIdx := t.lastActivityInTurn()
	if agentIdx < 0 || actIdx < 0 {
		return
	}
	if agentIdx > actIdx {
		return // already after activity
	}
	// Move agent to end of turn (after activity).
	agent := t.Items[agentIdx]
	// Remove agentIdx
	t.Items = append(t.Items[:agentIdx], t.Items[agentIdx+1:]...)
	// actIdx may have shifted if agent was before it
	if agentIdx < actIdx {
		actIdx--
	}
	// Insert after activity
	insertAt := actIdx + 1
	if insertAt > len(t.Items) {
		insertAt = len(t.Items)
	}
	t.Items = append(t.Items[:insertAt], append([]Item{agent}, t.Items[insertAt:]...)...)
}

func (t *Transcript) appendToolCall(ev Event) {
	t.nextTextReplaces = true
	step := ToolStep{
		Name:   ev.ToolName,
		ID:     ev.ToolID,
		Args:   ev.Args,
		Status: "running",
	}

	// Prefer a single activity fold for the whole soft+tool run.
	if actIdx := t.lastActivityInTurn(); actIdx >= 0 {
		t.Items[actIdx].Steps = append(t.Items[actIdx].Steps, step)
		t.Items[actIdx].Summary = summarizeSteps(t.Items[actIdx].Steps)
		t.ensureAgentAfterActivity()
		return
	}

	// No activity yet. If sticky agent exists, insert activity before it.
	if agentIdx := t.lastAgentInTurn(); agentIdx >= 0 {
		act := Item{
			Kind:    ItemActivity,
			Steps:   []ToolStep{step},
			Summary: summarizeSteps([]ToolStep{step}),
		}
		// insert at agentIdx (push agent right)
		t.Items = append(t.Items[:agentIdx], append([]Item{act}, t.Items[agentIdx:]...)...)
		return
	}

	t.Items = append(t.Items, Item{
		Kind:    ItemActivity,
		Steps:   []ToolStep{step},
		Summary: summarizeSteps([]ToolStep{step}),
	})
}

func (t *Transcript) appendToolResult(ev Event) {
	t.nextTextReplaces = true
	// Find matching running step in current turn activity (by ID, then name FIFO).
	start := t.turnStart()
	for i := len(t.Items) - 1; i >= start; i-- {
		if t.Items[i].Kind != ItemActivity {
			continue
		}
		steps := t.Items[i].Steps
		for j := range steps {
			if steps[j].Status != "running" {
				continue
			}
			if ev.ToolID != "" && steps[j].ID != "" && steps[j].ID != ev.ToolID {
				continue
			}
			if ev.ToolName != "" && steps[j].Name != "" && steps[j].Name != ev.ToolName {
				continue
			}
			steps[j].Result = ev.Result
			steps[j].Status = "complete"
			if isResultError(ev.Result) {
				steps[j].Status = "error"
			}
			t.Items[i].Steps = steps
			t.Items[i].Summary = summarizeSteps(steps)
			return
		}
	}
	// Orphan result — attach to turn activity or create one.
	status := "complete"
	if isResultError(ev.Result) {
		status = "error"
	}
	step := ToolStep{Name: ev.ToolName, ID: ev.ToolID, Result: ev.Result, Status: status}
	if actIdx := t.lastActivityInTurn(); actIdx >= 0 {
		t.Items[actIdx].Steps = append(t.Items[actIdx].Steps, step)
		t.Items[actIdx].Summary = summarizeSteps(t.Items[actIdx].Steps)
		return
	}
	t.Items = append(t.Items, Item{
		Kind:    ItemActivity,
		Steps:   []ToolStep{step},
		Summary: summarizeSteps([]ToolStep{step}),
	})
}

func (t *Transcript) finalizeRunningSteps() {
	for i := range t.Items {
		if t.Items[i].Kind != ItemActivity {
			continue
		}
		for j := range t.Items[i].Steps {
			if t.Items[i].Steps[j].Status == "running" {
				t.Items[i].Steps[j].Status = "complete"
			}
		}
		t.Items[i].Summary = summarizeSteps(t.Items[i].Steps)
	}
}

func (t *Transcript) finalizeProvisionalAgents() {
	for i := range t.Items {
		if t.Items[i].Kind == ItemAgent {
			t.Items[i].Provisional = false
		}
	}
}

// ToggleExpand flips Expanded on the item at idx for kinds that support it
// (activity folds, long user bubbles).
func (t *Transcript) ToggleExpand(idx int) {
	if idx < 0 || idx >= len(t.Items) {
		return
	}
	switch t.Items[idx].Kind {
	case ItemActivity, ItemUser:
		t.Items[idx].Expanded = !t.Items[idx].Expanded
	}
}

// ToggleLastActivity expands/collapses the most recent activity item.
func (t *Transcript) ToggleLastActivity() {
	for i := len(t.Items) - 1; i >= 0; i-- {
		if t.Items[i].Kind == ItemActivity {
			t.Items[i].Expanded = !t.Items[i].Expanded
			return
		}
	}
}

// ToggleLastUser expands/collapses the most recent user bubble.
func (t *Transcript) ToggleLastUser() {
	for i := len(t.Items) - 1; i >= 0; i-- {
		if t.Items[i].Kind == ItemUser {
			t.Items[i].Expanded = !t.Items[i].Expanded
			return
		}
	}
}

// ClearApproval marks approval as resolved (caller sends the response).
func (t *Transcript) ClearApproval() {
	t.Awaiting = false
	t.ApprovalIdx = -1
}

func summarizeSteps(steps []ToolStep) string {
	if len(steps) == 0 {
		return "Tools"
	}
	running := false
	names := make([]string, 0, len(steps))
	for _, s := range steps {
		if s.Status == "running" {
			running = true
		}
		if s.Name != "" {
			names = append(names, s.Name)
		}
	}
	if running && len(steps) == 1 {
		return "Running " + firstNonEmpty(steps[0].Name, "tool") + "…"
	}
	if len(names) == 1 {
		return names[0]
	}
	return strings.Join(uniquePreserve(names), ", ")
}

func uniquePreserve(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

func isResultError(result any) bool {
	if result == nil {
		return false
	}
	if s, ok := result.(string); ok {
		head := strings.ToLower(s)
		if len(head) > 300 {
			head = head[:300]
		}
		return strings.HasPrefix(head, "error:") || strings.HasPrefix(head, "error ") ||
			strings.Contains(head, "\nerror:")
	}
	if m, ok := result.(map[string]any); ok {
		if v, ok := m["success"].(bool); ok && !v {
			return true
		}
		if v, ok := m["ok"].(bool); ok && !v {
			return true
		}
		if e, ok := m["error"]; ok && e != nil && e != "" {
			return true
		}
	}
	return false
}

func defaultOptions(opts []string) []string {
	if len(opts) == 0 {
		return []string{"Yes", "No"}
	}
	return opts
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
