package api

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/SAP/astonish/pkg/credentials"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/session"
)

// Serializes admission/decisions within the authenticated tenant, never by a
// process-global active plan. The runner registry excludes overlapping turns.
var planSessionLocks sync.Map

func lockPlanSession(p execution.Principal, app, id string) func() {
	key := struct{ Org, Team, Subject, App, ID string }{p.OrgSlug, p.TeamSlug, p.Subject, app, id}
	value, _ := planSessionLocks.LoadOrStore(key, &sync.Mutex{})
	mu := value.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

func validatePlanChatRequest(req StudioChatRequest) error {
	if req.PlanRevisionDecision == nil && !req.ApprovedPlanExecution {
		return nil
	}
	if req.SessionID == "" || req.PlanMode || (req.PlanRevisionDecision != nil && req.ApprovedPlanExecution) {
		return fmt.Errorf("plan decisions require an existing session and Normal mode")
	}
	if strings.HasPrefix(strings.TrimSpace(req.Message), "/") {
		return fmt.Errorf("plan decisions cannot accompany slash commands")
	}
	if d := req.PlanRevisionDecision; d != nil {
		if strings.TrimSpace(d.PlanID) == "" || strings.TrimSpace(d.ProposalID) == "" || d.BaseRevision < 1 {
			return fmt.Errorf("plan decision identity and positive base revision are required")
		}
		switch d.Decision {
		case agent.PlanRevisionApprove, agent.PlanRevisionReject, agent.PlanRevisionRequestChanges:
		default:
			return fmt.Errorf("invalid plan revision decision")
		}
	}
	return nil
}

// Initial execution consent never approves a revision or reopens a completed plan.
func approveInitialSessionPlan(runtime *agent.PlanRuntime) error {
	s := runtime.Snapshot()
	code, message := "", ""
	switch {
	case len(s.Steps) == 0:
		code, message = agent.PlanRuntimeNoPlan, "no active plan"
	case s.Document.Results != "" || s.Document.Lifecycle == agent.PlanLifecycleCompleted:
		code, message = agent.PlanRuntimeCompleted, "plan is completed"
	case s.Document.Lifecycle == agent.PlanLifecycleApproved || s.Document.Lifecycle == agent.PlanLifecycleExecuting:
		return nil // idempotent initial consent, not a lifecycle rollback
	}
	if code != "" {
		return &agent.PlanRuntimeError{Code: code, Message: message}
	}
	return runtime.SetApproved()
}

// Scrub a JSON-shaped copy, including nested proposal definitions and evidence.
// Never redact the authoritative snapshot used for compare-and-swap decisions.
func redactPlanSnapshot(s agent.PlanSnapshot, redactor *credentials.Redactor) agent.PlanSnapshot {
	if redactor == nil {
		return s
	}
	raw, _ := json.Marshal(s)
	var value map[string]any
	_ = json.Unmarshal(raw, &value)
	raw, _ = json.Marshal(redactor.RedactMap(value))
	var clean agent.PlanSnapshot
	_ = json.Unmarshal(raw, &clean)
	return clean
}

func decodePlanSnapshot(value any) (agent.PlanSnapshot, bool) {
	var s agent.PlanSnapshot
	raw, err := json.Marshal(value)
	if err != nil || json.Unmarshal(raw, &s) != nil || s.PlanID == "" || len(s.Steps) == 0 {
		return s, false
	}
	return s, true
}

func planSnapshotData(s agent.PlanSnapshot) map[string]any {
	return map[string]any{"event_type": "plan_snapshot", "snapshot": s, "plan_id": s.PlanID, "revision": s.Revision, "sequence": s.Sequence}
}

func overlayPlanSnapshot(messages []StudioMessage, s agent.PlanSnapshot) []StudioMessage {
	if len(s.Steps) == 0 {
		return messages
	}
	msg := StudioMessage{Type: "plan", Goal: s.Goal, Snapshot: &s}
	for _, step := range s.Steps {
		msg.Steps = append(msg.Steps, PlanStepMsg{Name: step.Name, Description: step.Description, Status: step.Status})
	}
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Type != "plan" {
			continue
		}
		old := messages[i].Snapshot
		if old != nil && old.PlanID != "" && old.PlanID != s.PlanID {
			continue
		}
		if old != nil && old.Sequence > s.Sequence {
			return messages
		}
		msg.InvocationID, msg.ToolID = messages[i].InvocationID, messages[i].ToolID
		messages[i] = msg
		return messages
	}
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Type == "plan_pending" {
			msg.InvocationID, msg.ToolID = messages[i].InvocationID, messages[i].ToolID
			messages[i] = msg
			return messages
		}
	}
	return append(messages, msg)
}

// State is authoritative after compaction. Accepted result replay is the only
// fallback; a model's requested status is never execution evidence.
func sessionPlanSnapshot(sess session.Session) (agent.PlanSnapshot, bool) {
	if value, err := sess.State().Get(agent.PlanSnapshotStateKey); err == nil && value != nil {
		return decodePlanSnapshot(value)
	}
	messages := eventsToMessages(sess.Events(), nil)
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Snapshot != nil {
			return messages[i].Snapshot.Clone(), true
		}
	}
	return agent.PlanSnapshot{}, false
}

func overlaySessionPlan(messages []StudioMessage, sess session.Session, redactor *credentials.Redactor) []StudioMessage {
	if s, ok := sessionPlanSnapshot(sess); ok {
		return overlayPlanSnapshot(messages, redactPlanSnapshot(s, redactor))
	}
	return messages
}

// The verifier can only use the session's sandbox tool client. A missing pool
// fails closed, even if the singleton agent has a host PlanVerify callback.
func scopedPlanVerifier(ctx context.Context, pool sandbox.ToolNodePool, id string) func(string) (int, string, error) {
	return func(command string) (int, string, error) {
		principal, ok := execution.PrincipalFromContext(ctx)
		if !ok {
			return -1, "", fmt.Errorf("authenticated verification principal required")
		}
		if _, err := execution.NewService(nil).Authorize(ctx, principal, execution.CapabilityChat); err != nil {
			return -1, "", err
		}
		if slices.Contains(store.DisabledToolsFromContext(ctx), "shell_command") {
			return -1, "", fmt.Errorf("shell_command is disabled for this session")
		}
		if strings.TrimSpace(id) == "" {
			return -1, "", fmt.Errorf("session sandbox target required")
		}
		if pool == nil {
			return -1, "", fmt.Errorf("session sandbox verifier unavailable; host fallback forbidden")
		}
		client := pool.GetOrCreateWithImage(id, store.SandboxTemplateFromContext(ctx), store.SandboxLayerChainFromContext(ctx), store.SandboxImageFromContext(ctx))
		if err := sandbox.EnsureToolClientReady(ctx, client, id); err != nil {
			return -1, "", err
		}
		raw, err := client.Call(id, "shell_command", map[string]any{"command": command, "timeout": 600})
		if err != nil {
			return -1, "", err
		}
		var result struct {
			ExitCode *int   `json:"exit_code"`
			Stdout   string `json:"stdout"`
			Error    string `json:"error"`
			Waiting  bool   `json:"waiting_for_input"`
		}
		if err := json.Unmarshal(raw, &result); err != nil {
			return -1, "", err
		}
		if result.ExitCode == nil || result.Waiting || result.Error != "" {
			return -1, result.Stdout, fmt.Errorf("verification did not finish: %s", result.Error)
		}
		return *result.ExitCode, result.Stdout, nil
	}
}

func newSessionPlanRuntime(ctx context.Context, svc session.Service, user, app, id string, verify func(string) (int, string, error), emit func(string, map[string]any)) (*agent.PlanRuntime, error) {
	req := &session.GetRequest{AppName: app, UserID: user, SessionID: id}
	resp, err := svc.Get(ctx, req)
	if err != nil {
		return nil, err
	}
	if resp == nil || resp.Session == nil {
		return nil, fmt.Errorf("session not found")
	}
	if value, stateErr := resp.Session.State().Get(agent.PlanSnapshotStateKey); stateErr == nil && value != nil {
		if _, valid := decodePlanSnapshot(value); !valid {
			return nil, fmt.Errorf("invalid durable plan snapshot")
		}
	}
	options := agent.PlanRuntimeOptions{Workspace: "/workspace", Verify: verify, CommitSnapshot: func(s agent.PlanSnapshot) error {
		current, err := svc.Get(ctx, req)
		if err != nil {
			return err
		}
		raw, err := json.Marshal(s)
		if err != nil {
			return err
		}
		var value map[string]any
		if err = json.Unmarshal(raw, &value); err != nil {
			return err
		}
		event := &session.Event{ID: fmt.Sprintf("plan-%s-%d", s.PlanID, s.Sequence), Author: "system", Timestamp: time.Now(), Actions: session.EventActions{StateDelta: map[string]any{agent.PlanSnapshotStateKey: value}}}
		if err = svc.AppendEvent(ctx, current.Session, event); err != nil {
			return err
		}
		if emit != nil {
			emit("subtask_progress", planSnapshotData(redactPlanSnapshot(s, credentials.RedactorFromContext(ctx))))
		}
		return nil
	}}
	if s, ok := sessionPlanSnapshot(resp.Session); ok {
		runtime := agent.NewPlanRuntimeFromSnapshot(s, options)
		if err := runtime.Bootstrap(); err != nil {
			return nil, err
		}
		return runtime, nil
	}
	return agent.NewPlanRuntime(nil, options), nil
}
