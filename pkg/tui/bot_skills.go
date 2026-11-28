package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/tui/backend"
	"github.com/SAP/astonish/pkg/tui/events"
	"github.com/google/uuid"
)

type botSkillsMode uint8

const (
	botSkillsList botSkillsMode = iota
	botSkillsHistory
	botSkillsRuns
)

type botSkillsState struct {
	mode        botSkillsMode
	items       []store.BotSkill
	revisions   []store.BotSkillRevision
	runs        []store.BotRun
	selected    uuid.UUID
	after, next string
	archived    bool
	scroll      int
}
type botSkillsLoadedMsg struct {
	generation uint64
	requestID  uuid.UUID
	state      botSkillsState
	err        error
}

func (m model) openBotSkills() (tea.Model, tea.Cmd) {
	if _, ok := m.backend.(backend.BotSkillBackend); !ok || !m.botSelected() {
		m.tr.Apply(events.NewSystem("Select a remote Bot first; /bots skills inspects committed knowledge. /skills remains local."))
		m.refreshViewport()
		return m, nil
	}
	m.bots = botsState{open: true, skills: &botSkillsState{}}
	m.slash = slashCompletion{}
	return m.loadBotSkills()
}
func (m model) loadBotSkills() (tea.Model, tea.Cmd) {
	cap, ok := m.backend.(backend.BotSkillBackend)
	if !ok {
		return m, nil
	}
	state := *m.bots.skills
	m.bots.loading = true
	m.bots.err = ""
	m.bots.requestID = uuid.New()
	gen, request := m.botGeneration, m.bots.requestID
	return m, func() tea.Msg {
		out := botSkillsLoadedMsg{generation: gen, requestID: request, state: state}
		page := store.BotSkillPage{Limit: 50, After: state.after}
		switch state.mode {
		case botSkillsList:
			page.IncludeArchived = state.archived
			data, err := cap.BotSkills(m.ctx, page)
			out.err = err
			if err == nil && data != nil {
				out.state.items, out.state.next = data.Skills, data.Next
			}
		case botSkillsHistory:
			data, err := cap.BotSkillRevisions(m.ctx, state.selected, page)
			out.err = err
			if err == nil && data != nil {
				out.state.revisions, out.state.next = data.Revisions, data.Next
			}
		case botSkillsRuns:
			data, err := cap.BotSkillRuns(m.ctx, state.selected, page)
			out.err = err
			if err == nil && data != nil {
				out.state.runs, out.state.next = data.Runs, data.Next
			}
		}
		return out
	}
}
func (m model) applyBotSkills(v botSkillsLoadedMsg) (tea.Model, tea.Cmd) {
	if v.generation != m.botGeneration || !m.bots.open || m.bots.skills == nil || v.requestID != m.bots.requestID {
		return m, nil
	}
	m.bots.loading = false
	if v.err != nil {
		m.bots.err = "Unavailable or stale: " + v.err.Error()
		return m, nil
	}
	m.bots.skills = &v.state
	m.bots.cursor = 0
	m.bots.skills.scroll = 0
	return m, nil
}
func (m model) handleBotSkillsKey(key string) (tea.Model, tea.Cmd) {
	state := *m.bots.skills
	m.bots.skills = &state
	switch key {
	case "esc", "ctrl+c":
		if state.mode != botSkillsList {
			state.mode = botSkillsList
			state.after = ""
			state.next = ""
			m.bots.err = ""
			return m.loadBotSkills()
		}
		m.bots = botsState{}
		return m, nil
	case "r":
		return m.loadBotSkills()
	}
	if m.bots.loading {
		return m, nil
	}
	switch key {
	case "a":
		if state.mode == botSkillsList {
			state.archived = !state.archived
			state.after = ""
			return m.loadBotSkills()
		}
	case "n":
		if state.next != "" && m.bots.err == "" {
			state.after = state.next
			return m.loadBotSkills()
		}
	case "p":
		state.after = ""
		return m.loadBotSkills()
	case "up", "k":
		if state.mode == botSkillsList {
			m.bots.cursor = max(0, m.bots.cursor-1)
		} else {
			state.scroll = max(0, state.scroll-1)
		}
	case "down", "j":
		if state.mode == botSkillsList {
			m.bots.cursor = min(max(0, len(state.items)-1), m.bots.cursor+1)
		} else {
			state.scroll++
		}
	case "enter":
		if state.mode == botSkillsList && len(state.items) > m.bots.cursor && m.bots.err == "" {
			state.selected = state.items[m.bots.cursor].ID
			state.mode = botSkillsHistory
			state.after = ""
			state.next = ""
			return m.loadBotSkills()
		}
	case "h", "u":
		if state.mode != botSkillsList {
			state.mode = botSkillsHistory
			if key == "u" {
				state.mode = botSkillsRuns
			}
			state.after = ""
			state.next = ""
			return m.loadBotSkills()
		}
	}
	return m, nil
}
func skillRevisionText(v store.BotSkillRevision) string {
	if v.SchemaVersion != store.BotSkillSchemaVersion {
		return fmt.Sprintf("Unsupported skill schema %d; content unavailable.\n", v.SchemaVersion)
	}
	var s strings.Builder
	fmt.Fprintf(&s, "Revision %d · digest %s · parent %d\nResponsibility: %s\n", v.Revision, v.Digest, v.ParentRevision, v.Responsibility)
	for _, x := range v.Expectations {
		fmt.Fprintf(&s, "Expectation: %s [%s] run=%s event=%s quote=%s\n", x.Text, x.Provenance.Level, x.Provenance.RunID, x.Provenance.EventID, x.Provenance.Quote)
	}
	for _, x := range v.Method {
		fmt.Fprintf(&s, "Method: %s\n", x)
	}
	for _, x := range v.Sources {
		fmt.Fprintf(&s, "Source: %s · %s\nAccess: %s · fallback: %s · limitation: %s [%s] · observed: %v\n", x.URL, x.Purpose, x.AccessMethod, x.Fallback, x.Limitation, x.Provenance.Level, x.LastObservedAt)
		if observation := x.Observation; observation != nil {
			fmt.Fprintf(&s, "Access observation: %s · HTTP %d · truncated: %t · call %s\n", observation.Status, observation.HTTPStatus, observation.Truncated, observation.CallID)
		}
		s.WriteString("Source usefulness is agent judgment, not an access guarantee.\n")
	}
	fmt.Fprintf(&s, "Output format: %s\nEvaluation rules: %s\nChange reason: %s\n", v.OutputFormat, strings.Join(v.EvaluationRules, "; "), v.ChangeReason)
	if v.RevertsRevision != nil {
		fmt.Fprintf(&s, "Reverts revision %d\n", *v.RevertsRevision)
	}
	for _, p := range v.Provenance {
		fmt.Fprintf(&s, "Provenance: %s · recorded %v · run %s · event %s · evidence %s · quote %s\n", p.Level, p.RecordedAt, p.RunID, p.EventID, strings.Join(p.EvidenceRefs, ", "), p.Quote)
	}
	return s.String()
}
func (m model) renderBotSkills() string {
	state := m.bots.skills
	var s strings.Builder
	s.WriteString("Bot skills & history — read-only committed knowledge\nNot authorization; /skills is separate local knowledge.\n")
	if m.bots.loading {
		s.WriteString("Loading skills…\n")
	}
	if m.bots.err != "" {
		s.WriteString(m.bots.err + "\n")
	}
	switch state.mode {
	case botSkillsList:
		if !m.bots.loading && m.bots.err == "" && len(state.items) == 0 {
			s.WriteString("No committed skills. Legacy schedules remain unchanged; no import on read.\n")
		}
		for i := max(0, m.bots.cursor-3); i < len(state.items) && i < max(0, m.bots.cursor-3)+8; i++ {
			x := state.items[i]
			marker := "  "
			if i == m.bots.cursor {
				marker = "> "
			}
			fmt.Fprintf(&s, "%s%s [%s] revision %d\n  %s\n", marker, x.Title, x.Status, x.HeadRevision, x.HeadDigest)
		}
	case botSkillsHistory:
		s.WriteString("Committed revisions\n")
		if !m.bots.loading && m.bots.err == "" && len(state.revisions) == 0 {
			s.WriteString("No committed revisions.\n")
		}
		for _, x := range state.revisions {
			s.WriteString(skillRevisionText(x))
		}
	case botSkillsRuns:
		s.WriteString("Run receipts\n")
		if !m.bots.loading && m.bots.err == "" && len(state.runs) == 0 {
			s.WriteString("No linked runs.\n")
		}
		for _, x := range state.runs {
			fmt.Fprintf(&s, "Run %s [%s]\n", x.ID, x.Status)
			r := x.Report.Skill
			if r == nil || r.SchemaVersion != store.BotSkillSchemaVersion {
				s.WriteString("Skill receipt unavailable or unsupported.\n")
				continue
			}
			fmt.Fprintf(&s, "%s · used revision %d · committed revision %d\n%s · %s\n", r.Kind, r.UsedRevision, r.CommittedRevision, r.Digest, r.Reason)
		}
	}
	lines := strings.Split(s.String(), "\n")
	height := max(5, m.height-10)
	offset := min(state.scroll, max(0, len(lines)-height))
	body := strings.Join(lines[offset:min(len(lines), offset+height)], "\n")
	return body + "\n\n↑↓ scroll/select · Enter history · u runs · h revisions\nr retry · a archived · n next page · p first · Esc back (work continues)"
}
