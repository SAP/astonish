package events

import (
	"path/filepath"
	"strings"
)

// ItemKind classifies a rendered transcript item.
type ItemKind string

const (
	ItemUser          ItemKind = "user"
	ItemAgent         ItemKind = "agent"
	ItemThinking      ItemKind = "thinking"
	ItemActivity      ItemKind = "activity"
	ItemFileDiff      ItemKind = "file_diff" // main-thread editor-style file change
	ItemSystem        ItemKind = "system"
	ItemError         ItemKind = "error"
	ItemApproval      ItemKind = "approval"
	ItemNetworkDenial ItemKind = "network_denial"
	ItemArtifact      ItemKind = "artifact"
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

	// Network denial fields.
	NetworkDenials []NetworkDenial
	SandboxName    string
	SessionID      string

	// Artifact / file-diff path.
	Path string
	// Artifacts holds one or more generated files shown as a compact file list.
	Artifacts []Artifact

	// FileDiff fields (ItemFileDiff) — dual-gutter editor view on the main thread.
	// DiffVerification is preferred (tool verification_context). Args hold
	// old_string/new_string/content for fallback while streaming or if context is empty.
	DiffVerification string
	// ToolName identifies edit_file vs write_file for fallback rendering.
}

// Transcript is the reduced UI state built from a stream of Events.
type Transcript struct {
	Items     []Item
	SessionID string
	Title     string
	Status    string // live status line (Thinking…, Running X…)
	Streaming bool
	Provider  string
	Model     string
	LastUsage *Usage
	// ContextTokens is the current context-window occupancy: the token count of
	// the most recent LLM request/response in the latest turn (input + output).
	// Unlike LastUsage (which accumulates across the whole session), this tracks
	// "how full is the context right now" — the number that matters when coding.
	ContextTokens int64
	Awaiting      bool // waiting for approval response
	ApprovalIdx   int  // index of open approval item, or -1

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
	case KindNetworkDenial:
		t.Awaiting = true
		t.Items = append(t.Items, Item{
			Kind:           ItemNetworkDenial,
			Content:        "Network access blocked",
			NetworkDenials: ev.NetworkDenials,
			SandboxName:    ev.SandboxName,
			SessionID:      ev.SessionID,
		})
		t.ApprovalIdx = len(t.Items) - 1
		t.Status = "Waiting for network authorization…"
	case KindAutoApproved:
		// Soft process note — status only, no extra transcript clutter.
		t.Status = "Auto-approved " + firstNonEmpty(ev.ToolName, "tool")
	case KindReportMarker:
		t.markArtifactReport(ev)
	case KindArtifact:
		artifact := Artifact{}
		if ev.Artifact != nil {
			artifact = *ev.Artifact
		}
		if artifact.Path == "" {
			artifact.Path = ev.Text
		}
		if artifact.Path == "" && ev.Meta != nil {
			if p, ok := ev.Meta["path"].(string); ok {
				artifact.Path = p
			}
		}
		if artifact.ToolName == "" && ev.Meta != nil {
			if tool, ok := ev.Meta["tool"].(string); ok {
				artifact.ToolName = tool
			}
		}
		if artifact.FileName == "" {
			artifact.FileName = artifactBaseName(artifact.Path)
		}
		if artifact.FileType == "" {
			artifact.FileType = artifactTypeFromPath(artifact.Path)
		}
		if artifact.Path == "" {
			return
		}
		t.appendArtifact(artifact)
	case KindUsage:
		t.addUsage(ev.Usage)
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

func (t *Transcript) addUsage(usage *Usage) {
	if usage == nil {
		return
	}
	if t.LastUsage == nil {
		t.LastUsage = &Usage{}
	}
	// Estimated readings represent the full current context (not a per-call
	// delta), so accumulating them into cumulative usage would over-count.
	// They only update the context-occupancy figure below.
	if !usage.Estimated {
		t.LastUsage.Input += usage.Input
		t.LastUsage.Output += usage.Output
		t.LastUsage.Total += usage.Total
	}

	// Context occupancy = this LLM call's input (prompt) + output tokens. In a
	// multi-call tool loop the prompt grows each call, so the largest reading
	// this turn reflects the current context-window fill. Tracking the max
	// avoids brief zero-usage events resetting the displayed value.
	ctx := usage.Total
	if ctx == 0 {
		ctx = usage.Input + usage.Output
	}
	if ctx > t.ContextTokens {
		t.ContextTokens = ctx
	}
}

// turnStart returns the index of the first item in the current soft run
// (after the last hard-break item). Hard breaks: user, error, approval, network denial, artifact, system.
// File diffs stay inside the soft turn (do not hard-break).
func (t *Transcript) turnStart() int {
	for i := len(t.Items) - 1; i >= 0; i-- {
		switch t.Items[i].Kind {
		case ItemUser, ItemError, ItemApproval, ItemNetworkDenial, ItemArtifact, ItemSystem:
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

// ensureAgentAfterActivity keeps layout: … → activity → file_diff(s) → sticky agent.
func (t *Transcript) ensureAgentAfterActivity() {
	agentIdx := t.lastAgentInTurn()
	actIdx := t.lastActivityInTurn()
	if agentIdx < 0 || actIdx < 0 {
		return
	}
	// Target: immediately after the last file_diff in the turn, else after activity.
	insertAt := actIdx + 1
	start := t.turnStart()
	for i := start; i < len(t.Items); i++ {
		if t.Items[i].Kind == ItemFileDiff {
			insertAt = i + 1
		}
	}
	if agentIdx >= insertAt {
		// Agent is already after activity / file diffs (or is the insert point).
		// If agentIdx == insertAt-1 only when insertAt points past agent wrongly —
		// require agent strictly after the last non-agent tool surface.
		if agentIdx > actIdx {
			// Still may sit between activity and a later file_diff; fix if any
			// file_diff is after the agent.
			for i := agentIdx + 1; i < len(t.Items); i++ {
				if t.Items[i].Kind == ItemFileDiff || t.Items[i].Kind == ItemActivity {
					goto move
				}
			}
			return
		}
	}
move:
	// Move agent after activity and any file diffs.
	agent := t.Items[agentIdx]
	t.Items = append(t.Items[:agentIdx], t.Items[agentIdx+1:]...)
	// Recompute insert after removal.
	insertAt = 0
	actIdx = t.lastActivityInTurn()
	if actIdx >= 0 {
		insertAt = actIdx + 1
	}
	start = t.turnStart()
	for i := start; i < len(t.Items); i++ {
		if t.Items[i].Kind == ItemFileDiff {
			insertAt = i + 1
		}
	}
	if insertAt > len(t.Items) {
		insertAt = len(t.Items)
	}
	t.Items = append(t.Items[:insertAt], append([]Item{agent}, t.Items[insertAt:]...)...)
}

func (t *Transcript) markArtifactReport(ev Event) {
	path := ev.Text
	if path == "" && ev.Artifact != nil {
		path = ev.Artifact.Path
	}
	if path == "" && ev.Meta != nil {
		if p, ok := ev.Meta["path"].(string); ok {
			path = p
		}
	}
	if path == "" {
		return
	}
	title := ""
	if ev.Artifact != nil {
		title = ev.Artifact.ReportTitle
	}
	if title == "" && ev.Meta != nil {
		if s, ok := ev.Meta["title"].(string); ok {
			title = s
		}
	}
	for i := range t.Items {
		if t.Items[i].Kind != ItemArtifact {
			continue
		}
		changed := false
		for j := range t.Items[i].Artifacts {
			if filepath.Clean(t.Items[i].Artifacts[j].Path) != filepath.Clean(path) {
				continue
			}
			t.Items[i].Artifacts[j].IsReport = true
			if title != "" {
				t.Items[i].Artifacts[j].ReportTitle = title
			}
			changed = true
		}
		if changed {
			t.Items[i].Content = artifactListContent(t.Items[i].Artifacts)
		}
	}
}

func (t *Transcript) appendArtifact(artifact Artifact) {
	if artifact.Path == "" {
		return
	}
	// Consecutive artifact events in the same turn render as one file list.
	if n := len(t.Items); n > 0 && t.Items[n-1].Kind == ItemArtifact {
		for _, existing := range t.Items[n-1].Artifacts {
			if existing.Path == artifact.Path {
				return
			}
		}
		t.Items[n-1].Artifacts = append(t.Items[n-1].Artifacts, artifact)
		t.Items[n-1].Path = t.Items[n-1].Artifacts[0].Path
		t.Items[n-1].Content = artifactListContent(t.Items[n-1].Artifacts)
		return
	}
	t.Items = append(t.Items, Item{
		Kind:      ItemArtifact,
		Path:      artifact.Path,
		Content:   artifactListContent([]Artifact{artifact}),
		Artifacts: []Artifact{artifact},
	})
}

func artifactListContent(artifacts []Artifact) string {
	paths := make([]string, 0, len(artifacts))
	for _, a := range artifacts {
		if a.Path != "" {
			paths = append(paths, a.Path)
		}
	}
	return strings.Join(paths, "\n")
}

func artifactBaseName(path string) string {
	base := filepath.Base(path)
	if base == "." || base == string(filepath.Separator) {
		return path
	}
	return base
}

func artifactTypeFromPath(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".md", ".markdown", ".mdown", ".mkd":
		return "Markdown"
	case ".go":
		return "Go"
	case ".js", ".jsx":
		return "JavaScript"
	case ".ts", ".tsx":
		return "TypeScript"
	case ".py":
		return "Python"
	case ".json":
		return "JSON"
	case ".yaml", ".yml":
		return "YAML"
	case ".html", ".htm":
		return "HTML"
	case ".css":
		return "CSS"
	case ".txt", ".log":
		return "Text"
	default:
		return "File"
	}
}

func (t *Transcript) appendToolCall(ev Event) {
	t.nextTextReplaces = true
	step := ToolStep{
		Name:   ev.ToolName,
		ID:     ev.ToolID,
		Args:   ev.Args,
		Status: "running",
	}

	// Reuse the current activity fold only when no file diff has been emitted
	// after it. Once a diff is shown, later tools must start a NEW fold so they
	// render chronologically *after* the diff — not merged back into the fold
	// that sits above it. Layout becomes: activity → file_diff → activity → …
	if actIdx := t.reusableActivityInTurn(); actIdx >= 0 {
		t.Items[actIdx].Steps = append(t.Items[actIdx].Steps, step)
		t.Items[actIdx].Summary = summarizeSteps(t.Items[actIdx].Steps)
		t.ensureAgentAfterActivity()
		return
	}

	act := Item{
		Kind:    ItemActivity,
		Steps:   []ToolStep{step},
		Summary: summarizeSteps([]ToolStep{step}),
	}
	// Insert the new fold after the last tool surface (file diff / activity) in
	// the turn, but before a trailing sticky agent so the agent stays last.
	insertAt := t.newActivityInsertIndex()
	if insertAt >= len(t.Items) {
		t.Items = append(t.Items, act)
	} else {
		t.Items = append(t.Items[:insertAt], append([]Item{act}, t.Items[insertAt:]...)...)
	}
	t.ensureAgentAfterActivity()
}

// reusableActivityInTurn returns the index of the current turn's last activity
// fold, but only when no ItemFileDiff appears after it. When a diff follows the
// last fold, -1 is returned so a fresh fold is started (keeping tools that run
// after a code change visually below that change).
func (t *Transcript) reusableActivityInTurn() int {
	start := t.turnStart()
	actIdx := -1
	diffAfterLastAct := false
	for i := start; i < len(t.Items); i++ {
		switch t.Items[i].Kind {
		case ItemActivity:
			actIdx = i
			// A fresh fold — any earlier diff is irrelevant now.
			diffAfterLastAct = false
		case ItemFileDiff:
			if actIdx >= 0 {
				diffAfterLastAct = true
			}
		}
	}
	if actIdx >= 0 && !diffAfterLastAct {
		return actIdx
	}
	return -1
}

// newActivityInsertIndex returns where a freshly started activity fold should be
// inserted: after the last tool surface (file diff or activity) in the turn,
// otherwise before a trailing sticky agent, otherwise at the end.
func (t *Transcript) newActivityInsertIndex() int {
	start := t.turnStart()
	lastSurface := -1
	agentIdx := -1
	for i := start; i < len(t.Items); i++ {
		switch t.Items[i].Kind {
		case ItemFileDiff, ItemActivity:
			lastSurface = i
		case ItemAgent:
			agentIdx = i
		}
	}
	if lastSurface >= 0 {
		return lastSurface + 1
	}
	if agentIdx >= 0 {
		return agentIdx
	}
	return len(t.Items)
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
			t.maybeAppendFileDiff(steps[j].Name, steps[j].Args, steps[j].Result, steps[j].Status)
			return
		}
	}
	// Orphan result — attach to the reusable turn activity or create one.
	status := "complete"
	if isResultError(ev.Result) {
		status = "error"
	}
	step := ToolStep{Name: ev.ToolName, ID: ev.ToolID, Args: ev.Args, Result: ev.Result, Status: status}
	if actIdx := t.reusableActivityInTurn(); actIdx >= 0 {
		t.Items[actIdx].Steps = append(t.Items[actIdx].Steps, step)
		t.Items[actIdx].Summary = summarizeSteps(t.Items[actIdx].Steps)
		t.maybeAppendFileDiff(step.Name, step.Args, step.Result, step.Status)
		return
	}
	act := Item{
		Kind:    ItemActivity,
		Steps:   []ToolStep{step},
		Summary: summarizeSteps([]ToolStep{step}),
	}
	insertAt := t.newActivityInsertIndex()
	if insertAt >= len(t.Items) {
		t.Items = append(t.Items, act)
	} else {
		t.Items = append(t.Items[:insertAt], append([]Item{act}, t.Items[insertAt:]...)...)
	}
	t.maybeAppendFileDiff(step.Name, step.Args, step.Result, step.Status)
}

// maybeAppendFileDiff promotes successful edit_file/write_file results to a
// main-thread ItemFileDiff (dual-gutter editor view). Failed writes/edits never
// produce a main-thread diff — only results that include verification_context
// (set only after a successful apply) are shown.
func (t *Transcript) maybeAppendFileDiff(toolName string, args map[string]any, result any, status string) {
	name := strings.ToLower(strings.TrimSpace(toolName))
	if name != "edit_file" && name != "write_file" {
		return
	}
	if status == "error" || isResultError(result) {
		return
	}
	// verification_context is only present after a successful disk write/edit.
	// Do not fall back to args alone — that would show a diff for failed tools
	// that still carry old_string/new_string/content in the call args.
	vc := toolResultString(result, "verification_context")
	if vc == "" {
		return
	}
	// edit_file also sets success=true; respect an explicit false.
	if m, ok := result.(map[string]any); ok {
		if v, ok := m["success"].(bool); ok && !v {
			return
		}
	}
	path := pathFromToolArgs(args)
	if path == "" {
		path = toolResultString(result, "path")
	}
	item := Item{
		Kind:             ItemFileDiff,
		Path:             path,
		ToolName:         name,
		Args:             args,
		DiffVerification: vc,
	}
	// Insert after the last file_diff in the turn, else after activity, else before agent.
	insertAt := t.fileDiffInsertIndex()
	if insertAt >= len(t.Items) {
		t.Items = append(t.Items, item)
		return
	}
	t.Items = append(t.Items[:insertAt], append([]Item{item}, t.Items[insertAt:]...)...)
}

// fileDiffInsertIndex returns where a new ItemFileDiff should land: immediately
// after the latest tool surface in the turn (whichever of the last activity fold
// or last file diff comes later), so diffs interleave chronologically with the
// activity folds that produced them. Falls back to before a trailing sticky
// agent, otherwise the end.
func (t *Transcript) fileDiffInsertIndex() int {
	start := t.turnStart()
	lastSurface := -1 // max(lastDiff, lastActivity)
	agentIdx := -1
	for i := start; i < len(t.Items); i++ {
		switch t.Items[i].Kind {
		case ItemFileDiff, ItemActivity:
			lastSurface = i
		case ItemAgent:
			agentIdx = i
		}
	}
	if lastSurface >= 0 {
		return lastSurface + 1
	}
	if agentIdx >= 0 {
		return agentIdx // insert before agent
	}
	return len(t.Items)
}

func pathFromToolArgs(args map[string]any) string {
	if args == nil {
		return ""
	}
	for _, k := range []string{"path", "file_path"} {
		if v, ok := args[k].(string); ok && strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func toolResultString(result any, key string) string {
	if result == nil {
		return ""
	}
	m, ok := result.(map[string]any)
	if !ok {
		return ""
	}
	s, _ := m[key].(string)
	return strings.TrimSpace(s)
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

// Reset clears transcript items and turn state (used for /new and session switch).
func (t *Transcript) Reset() {
	t.Items = nil
	t.Title = ""
	t.Status = ""
	t.Streaming = false
	t.LastUsage = nil
	t.ContextTokens = 0
	t.Awaiting = false
	t.ApprovalIdx = -1
	t.nextTextReplaces = false
}

// HistoryMsg is a finalized transcript entry loaded when resuming a session.
type HistoryMsg struct {
	Kind     string // user | agent | tool_call | tool_result | system | thinking | artifact
	Text     string
	ToolName string
	ToolID   string
	Args     map[string]any
	Result   any
	Artifact *Artifact
}

// LoadHistory applies session history using the same sticky-agent / tool-fold
// rules as live events, then freezes everything as non-provisional.
func (t *Transcript) LoadHistory(entries []HistoryMsg) {
	t.Reset()
	for _, e := range entries {
		switch e.Kind {
		case "user":
			t.Apply(NewUser(e.Text))
		case "agent":
			// Full historical agent messages replace sticky content within a turn.
			t.nextTextReplaces = true
			t.Apply(NewText(e.Text))
		case "thinking":
			t.Apply(Event{Kind: KindThinking, Text: e.Text})
		case "system":
			t.Apply(NewSystem(e.Text))
		case "tool_call":
			t.Apply(NewToolCall(e.ToolName, e.ToolID, e.Args))
		case "tool_result":
			t.Apply(NewToolResult(e.ToolName, e.ToolID, e.Result))
		case "artifact":
			t.Apply(Event{Kind: KindArtifact, Text: e.Text, Artifact: e.Artifact})
		}
	}
	t.finalizeRunningSteps()
	t.finalizeProvisionalAgents()
	t.Streaming = false
	t.Status = ""
	t.nextTextReplaces = false
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
		head := strings.ToLower(strings.TrimSpace(s))
		if len(head) > 300 {
			head = head[:300]
		}
		// Tool errors often surface as plain Go error strings, not "Error: …".
		return strings.HasPrefix(head, "error:") ||
			strings.HasPrefix(head, "error ") ||
			strings.Contains(head, "\nerror:") ||
			strings.HasPrefix(head, "failed ") ||
			strings.HasPrefix(head, "failed:") ||
			strings.Contains(head, "permission denied") ||
			strings.Contains(head, "access denied") ||
			strings.Contains(head, "not found") && strings.Contains(head, "failed")
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
