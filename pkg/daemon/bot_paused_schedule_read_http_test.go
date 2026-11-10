package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

// Controlled decisions only: the factory, scoped list adapter, operation capture
// and worker are real. Resolve a listed trigger to its current schedule ID instead
// of injecting the expected target from the test fixture.
type pausedScheduleReadModel struct {
	t         *testing.T
	step      int
	bridge    bool
	inspected func()
	target    string
}

func (m *pausedScheduleReadModel) GenerateContent(_ context.Context, r *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		if r.Tools["list_scheduled_jobs"] == nil {
			m.t.Error("paused factory omitted authorized native listing")
		}
		for _, name := range []string{"shell_command", "http_request", "web_fetch", "schedule_job", "update_scheduled_job", "remove_scheduled_job", "list_scheduled_jobs_future"} {
			if r.Tools[name] != nil {
				m.t.Errorf("paused factory exposed %s", name)
			}
		}
		var call *genai.FunctionCall
		switch m.step {
		case 0:
			call = &genai.FunctionCall{ID: "paused-list", Name: "list_scheduled_jobs", Args: map[string]any{"filter": "daily report"}}
		case 1:
			response := cadenceResponse(r, "paused-list")
			jobs, _ := response["jobs"].([]any)
			if len(jobs) != 2 {
				yield(nil, fmt.Errorf("paused listing missing: %v", response))
				return
			}
			// The first report is the one whose cadence the owner changed earlier.
			trigger := ""
			for _, value := range jobs {
				row := value.(map[string]any)
				if enabled, _ := row["enabled"].(bool); enabled {
					m.t.Fatal("read re-enabled a schedule")
				}
				if row["schedule"] == "*/2 * * * *" {
					trigger, _ = row["id"].(string)
				}
			}
			if trigger == "" {
				yield(nil, fmt.Errorf("requested task not found in scoped listing"))
				return
			}
			var facts struct {
				Status   store.BotStatus `json:"lifecycle_status"`
				Triggers []struct {
					ID         string `json:"id"`
					ScheduleID string `json:"schedule_id"`
				} `json:"triggers"`
			}
			for _, part := range r.Config.SystemInstruction.Parts {
				section := strings.Index(part.Text, "## Current Bot control state\n")
				if section < 0 {
					continue
				}
				// The current-goal fields precede lifecycle_status; JSON field order
				// is not the fixture's contract.
				start := strings.Index(part.Text[section:], "\n{")
				if start >= 0 {
					start += section
					if err := json.NewDecoder(strings.NewReader(part.Text[start:])).Decode(&facts); err != nil {
						m.t.Fatal(err)
					}
				}
			}
			if facts.Status != store.BotPaused {
				yield(nil, fmt.Errorf("not a paused conversation: %s", facts.Status))
				return
			}
			for _, tr := range facts.Triggers {
				if tr.ID == trigger {
					m.target = tr.ScheduleID
				}
			}
			if m.target == "" {
				yield(nil, fmt.Errorf("no current schedule binding for listed trigger"))
				return
			}
			m.inspected()
			call = &genai.FunctionCall{ID: "paused-resume", Name: "propose_bot_control", Args: map[string]any{"payload": map[string]any{"operation": store.BotControlResumeTasks, "reason": "Owner requested only the first report", "resume_targets": map[string]any{"schedule_ids": []string{m.target}}}}}
		case 2:
			response := cadenceResponse(r, "paused-resume")
			if fmt.Sprint(response["status"]) != string(store.BotOperationPending) {
				yield(nil, fmt.Errorf("resume not pending: %v", response))
				return
			}
			m.inspected()
			m.step++
			yield(&model.LLMResponse{Content: genai.NewContentFromText("Selected report resume is pending.", "model"), TurnComplete: true}, nil)
			return
		default:
			yield(nil, fmt.Errorf("unexpected paused schedule retry"))
			return
		}
		if m.bridge && call.Name == "list_scheduled_jobs" {
			call = &genai.FunctionCall{ID: call.ID, Name: "execute_tool", Args: map[string]any{"name": call.Name, "arguments": call.Args}}
		}
		m.step++
		yield(&model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: call}}}}, nil)
	}
}

func assertPausedReadMandate(t *testing.T, m store.BotMandate) {
	t.Helper()
	if m.Purpose != execution.BotMandatePurposeOperation {
		t.Fatal("paused listing got task authority")
	}
	found := false
	for _, c := range m.Constraints {
		for _, r := range c.Resources {
			if c.Action == execution.CapabilityToolExecute && r == "list_scheduled_jobs" {
				found = true
			}
		}
	}
	if !found {
		t.Fatal("admitted paused mandate lost exact list capability")
	}
}
