package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/SAP/astonish/pkg/credentials"
	persistentsession "github.com/SAP/astonish/pkg/session"
	"github.com/google/uuid"
	adksession "google.golang.org/adk/session"
	"google.golang.org/adk/tool"
)

const (
	DelegationHandoffVersion  = 1
	DelegationHandoffMaxBytes = 256 * 1024
	DelegationHandoffStateKey = "astonish_delegation_handoff"
)

type DelegationPlanStatus string

const (
	DelegationPlanMatched     DelegationPlanStatus = "matched"
	DelegationPlanMissing     DelegationPlanStatus = "no_scoped_plan"
	DelegationPlanStepMissing DelegationPlanStatus = "no_plan_step"
)

// DelegationPlanContext is definition-only: no sibling phases, mutable status,
// parent transcript, system prompt, or pending revision is copied to the child.
type DelegationPlanContext struct {
	Status   DelegationPlanStatus    `json:"status"`
	PlanID   string                  `json:"plan_id,omitempty"`
	Revision int64                   `json:"revision,omitempty"`
	Sequence int64                   `json:"sequence,omitempty"`
	Goal     string                  `json:"goal,omitempty"`
	Document *PlanDocumentDefinition `json:"document,omitempty"`
	Step     *PlanStepDefinition     `json:"step,omitempty"`
	// SourceMirror is a label, never an instruction to read mutable plan contents.
	SourceMirror string `json:"source_mirror,omitempty"`
}

// DelegationHandoff is the versioned per-attempt record. The exact serialized
// record is both model input and a state-only event in the child session.
type DelegationHandoff struct {
	Version               int                   `json:"version"`
	ParentSessionID       string                `json:"parent_session_id"`
	BatchID               string                `json:"batch_id"`
	TaskID                string                `json:"task_id"`
	ChildSessionID        string                `json:"child_session_id"`
	Attempt               int                   `json:"attempt"`
	Name                  string                `json:"name"`
	OriginalTask          string                `json:"original_task"`
	EffectiveTask         string                `json:"effective_task"`
	OriginalInstructions  string                `json:"original_instructions"`
	EffectiveInstructions string                `json:"effective_instructions"`
	PlanStep              string                `json:"plan_step"`
	Plan                  DelegationPlanContext `json:"plan"`
	WorkDir               string                `json:"workdir"`
	PreferredTools        []string              `json:"preferred_tools"`
	InitialToolNames      []string              `json:"initial_tool_names"`
}

type delegationPlanSourceKey struct{}

// WithDelegationPlanSource labels the session's known plan mirror. It never
// reads that path or discovers a repository-root PLAN.md. Launchers may opt in.
func WithDelegationPlanSource(ctx context.Context, path string) context.Context {
	return context.WithValue(ctx, delegationPlanSourceKey{}, path)
}

// DelegationPlanCapture is an immutable batch capture with no runtime pointer.
// Capture once before dispatch so all tasks see the same revision and sequence.
type DelegationPlanCapture struct {
	snapshot     PlanSnapshot
	sourceMirror string
}

func CaptureDelegationPlan(ctx context.Context) DelegationPlanCapture {
	runtime, _ := PlanRuntimeFromContext(ctx)
	capture := DelegationPlanCapture{snapshot: runtime.Snapshot()}
	if ctx != nil {
		capture.sourceMirror, _ = ctx.Value(delegationPlanSourceKey{}).(string)
	}
	return capture
}

// DelegationHandoffInput is an opaque, immutable task-specific seed. Copies of a
// task retain this seed across outer retries; they never recapture the plan.
type DelegationHandoffInput struct {
	originalTask         string
	originalInstructions string
	planStep             string
	plan                 DelegationPlanContext
}

// ForTask matches ONLY an explicit exact step name. Missing scope/step is
// recorded, not inferred from task names or a global runtime or plan file.
func (c DelegationPlanCapture) ForTask(description, instructions, planStep string) (*DelegationHandoffInput, error) {
	p := DelegationPlanContext{Status: DelegationPlanMissing, SourceMirror: c.sourceMirror}
	if len(c.snapshot.Steps) > 0 {
		p.Status = DelegationPlanStepMissing
		if planStep != "" {
			names := make([]string, 0, len(c.snapshot.Steps))
			for _, step := range c.snapshot.Steps {
				names = append(names, step.Name)
				if step.Name == planStep {
					definition, doc := step.Definition(), c.snapshot.Document.Definition()
					p.Status, p.PlanID, p.Revision, p.Sequence = DelegationPlanMatched, c.snapshot.PlanID, c.snapshot.Revision, c.snapshot.Sequence
					p.Goal, p.Document, p.Step = c.snapshot.Goal, &doc, &definition
				}
			}
			if p.Status != DelegationPlanMatched {
				return nil, fmt.Errorf("unknown plan_step %q; available steps: %s", planStep, strings.Join(names, ", "))
			}
		}
	}
	return &DelegationHandoffInput{originalTask: description, originalInstructions: instructions, planStep: planStep, plan: p}, nil
}

func (t SubAgentTask) hasDelegationHandoff() bool { return t.Handoff != nil && !t.CustomPrompt }

// normalizeHandoffValue operates on string values, not serialized JSON: redacted
// values containing quotes/newlines cannot corrupt the stored record.
func normalizeHandoffValue(value any, redactor *credentials.Redactor) any {
	switch v := value.(type) {
	case string:
		v = strings.ToValidUTF8(v, "\ufffd")
		if redactor != nil {
			v = redactor.Redact(v)
		}
		return v
	case []any:
		for i := range v {
			v[i] = normalizeHandoffValue(v[i], redactor)
		}
	case map[string]any:
		for k := range v {
			v[k] = normalizeHandoffValue(v[k], redactor)
		}
	}
	return value
}

func (m *SubAgentManager) freezeDelegationHandoff(task SubAgentTask, parentID string, attempt int, tools []tool.Tool) (string, error) {
	names := make([]string, 0, len(tools))
	seen := map[string]bool{}
	for _, t := range tools {
		if !seen[t.Name()] {
			names = append(names, t.Name())
			seen[t.Name()] = true
		}
	}
	h := DelegationHandoff{
		Version: DelegationHandoffVersion, ParentSessionID: parentID,
		BatchID: task.BatchID, TaskID: task.TaskID, ChildSessionID: task.ChildSessionID, Attempt: attempt,
		Name: task.Name, OriginalTask: task.Handoff.originalTask, EffectiveTask: task.Description,
		OriginalInstructions: task.Handoff.originalInstructions, EffectiveInstructions: task.Instructions,
		PlanStep: task.Handoff.planStep, Plan: task.Handoff.plan, WorkDir: m.WorkDir,
		PreferredTools: task.ToolFilter, InitialToolNames: names,
	}
	raw, err := json.Marshal(h)
	if err != nil {
		return "", fmt.Errorf("serialize delegation handoff: %w", err)
	}
	var value any
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber() // Preserve int64 plan revisions/sequences above 2^53.
	if err := decoder.Decode(&value); err != nil {
		return "", err
	}
	normalized, err := json.MarshalIndent(normalizeHandoffValue(value, m.Redactor), "", "  ")
	if err != nil {
		return "", fmt.Errorf("serialize delegation handoff: %w", err)
	}
	if len(normalized) > DelegationHandoffMaxBytes {
		return "", fmt.Errorf("delegation handoff exceeds %d byte limit (%d bytes); narrow task instructions or phase context", DelegationHandoffMaxBytes, len(normalized))
	}
	return string(normalized), nil
}

func reservedDelegationStateKey(key string) bool {
	switch key {
	case DelegationHandoffStateKey, persistentsession.StateKeyParentID, "batch_id", "task_id", "child_session_id", "attempt":
		return true
	}
	return false
}

func appendDelegationHandoff(ctx context.Context, svc adksession.Service, sess adksession.Session, text string) error {
	var handoff DelegationHandoff
	if err := json.Unmarshal([]byte(text), &handoff); err != nil {
		return fmt.Errorf("decode delegation handoff for persistence: %w", err)
	}
	// FileStore does not persist CreateRequest.State. Keep the frozen record
	// and its attempt identity in the same durable, state-only event.
	event := &adksession.Event{ID: uuid.NewString(), Author: "system", Timestamp: time.Now(), Actions: adksession.EventActions{StateDelta: map[string]any{
		DelegationHandoffStateKey:          text,
		persistentsession.StateKeyParentID: handoff.ParentSessionID,
		"batch_id":                         handoff.BatchID, "task_id": handoff.TaskID,
		"attempt": handoff.Attempt, "child_session_id": handoff.ChildSessionID,
	}}}
	if err := svc.AppendEvent(ctx, sess, event); err != nil {
		return fmt.Errorf("persist delegation handoff: %w", err)
	}
	return nil
}
