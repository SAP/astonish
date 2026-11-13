package launcher

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/SAP/astonish/pkg/bot"
	"google.golang.org/adk/tool/functiontool"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/tools"
	"google.golang.org/adk/session"
	"google.golang.org/adk/tool"
)

const (
	botWorkReadTool     = "bot_work_read"
	botWorkUpdateTool   = "bot_work_update"
	botEvidenceReadTool = "bot_evidence_read"
	botCheckWorkTool    = "bot_check_work"
)

type botWorkReadArgs struct {
	Cursor string `json:"cursor,omitempty"`
	Limit  int    `json:"limit,omitempty"`
}
type botWorkUpdateArgs struct {
	ExpectedRevision int64                      `json:"expected_revision"`
	Kind             bot.BotWorkChangeKind      `json:"kind"`
	Item             *botWorkItemArgs           `json:"item,omitempty"`
	Notes            string                     `json:"notes,omitempty"`
	Disposition      store.AgentWorkDisposition `json:"disposition,omitempty"`
	Remaining        []botWorkRemainingArgs     `json:"remaining,omitempty"`
}

// IDs are optional only for new items. The scoped service derives their actual
// identity from the function call and validates existing IDs before updating.
type botWorkItemArgs struct {
	ID           store.RunID             `json:"id,omitempty"`
	Summary      string                  `json:"summary"`
	Status       store.BotWorkItemStatus `json:"status"`
	Reason       string                  `json:"reason,omitempty"`
	EvidenceRefs []string                `json:"evidence_refs,omitempty"`
}

func (a botWorkUpdateArgs) item() *store.BotWorkItem {
	if a.Item == nil {
		return nil
	}
	return &store.BotWorkItem{ID: a.Item.ID, Summary: a.Item.Summary, Status: a.Item.Status, Reason: a.Item.Reason, EvidenceRefs: a.Item.EvidenceRefs}
}

// Only agent-reported content is accepted. Obligation IDs, source run and any
// continuation authority are supplied/validated by the scoped service.
type botWorkRemainingArgs struct {
	WorkItemID     *store.RunID                  `json:"work_item_id,omitempty"`
	Summary        string                        `json:"summary"`
	Reason         string                        `json:"reason"`
	Continuation   store.BotWorkContinuationKind `json:"continuation"`
	ContinuationID *store.RunID                  `json:"continuation_id,omitempty"`
}

func (a botWorkUpdateArgs) remaining() []store.BotWorkObligation {
	out := make([]store.BotWorkObligation, len(a.Remaining))
	for i, r := range a.Remaining {
		out[i] = store.BotWorkObligation{WorkItemID: r.WorkItemID, Summary: r.Summary, Reason: r.Reason, Continuation: r.Continuation, ContinuationID: r.ContinuationID}
	}
	return out
}

type botEvidenceReadArgs struct {
	CallID string `json:"call_id"`
	Offset int    `json:"offset,omitempty"`
	Limit  int    `json:"limit,omitempty"`
}
type botCheckWorkArgs struct {
	Question     string   `json:"question"`
	DraftText    string   `json:"draft_text"`
	EvidenceRefs []string `json:"evidence_refs,omitempty"`
}

func botWorkToolResult(ctx tool.Context, value any, err error) (map[string]any, error) {
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil && !errors.Is(err, store.ErrBotInvalid) && !errors.Is(err, store.ErrBotForbidden) && !errors.Is(err, store.ErrBotConflict) && !errors.Is(err, store.ErrBotUnsupported) && !errors.Is(err, store.ErrBotNotFound) {
		return nil, err
	}
	// Only the completed local bookkeeping attempt is recorded, not a successful
	// check or task. Failed attempts still return explicit feedback below.
	bot.RecordToolOutcome(ctx, ctx.FunctionCallID(), bot.ToolOutcome{Verified: true})
	if err != nil {
		code, message := "unavailable", "Work bookkeeping or evidence is unavailable. Continue with available information; do not infer success or absence from this failure."
		if errors.Is(err, store.ErrBotConflict) {
			code, message = "conflict", "Work changed. Read current work and retry only the intended update with its revision."
		}
		if errors.Is(err, store.ErrBotInvalid) || errors.Is(err, store.ErrBotForbidden) {
			code, message = "invalid_request", "Use known item IDs and captured evidence references, valid statuses and bounded content. This request did not update work."
		}
		return map[string]any{"status": code, "message": message, "current": value}, nil
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var result any
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, err
	}
	return map[string]any{"status": "ok", "result": result}, nil
}

// These tools are distinct from Code-style plans and never certify completion.
// Child sessions may inspect/check but cannot mutate the parent's work.
func newBotWorkTools(binding *botFactoryBinding) ([]tool.Tool, error) {
	if binding == nil || binding.owner == nil || bot.BotWorkServicesFromContext(binding.owner) == nil {
		return nil, nil
	}
	service := func(ctx tool.Context, mutation bool) (bot.BotWorkServices, error) {
		if ctx == nil || mutation && ctx.SessionID() != binding.req.Run.SessionID.String() {
			return nil, store.ErrBotForbidden
		}
		bound := binding.bind(ctx)
		if err := binding.check(bound); err != nil {
			return nil, err
		}
		capability := bot.BotWorkServicesFromContext(bound)
		if capability == nil {
			return nil, store.ErrBotUnsupported
		}
		return capability, nil
	}
	read, err := functiontool.New(functiontool.Config{Name: botWorkReadTool, Description: "Optionally read committed work and paginated evidence references. No checklist is required."}, func(c tool.Context, a botWorkReadArgs) (map[string]any, error) {
		s, err := service(c, false)
		if err != nil {
			return nil, err
		}
		out, err := s.Read(c, bot.BotWorkReadRequest{Cursor: a.Cursor, Limit: a.Limit})
		return botWorkToolResult(c, out, err)
	})
	if err != nil {
		return nil, err
	}
	update, err := functiontool.New(functiontool.Config{Name: botWorkUpdateTool, Description: "Optionally update agent-reported progress using expected_revision and kind upsert_item, cancel_item, replace_notes, set_disposition or set_remaining. Omit new item IDs. Completion is agent judgment, not verification or permission."}, func(c tool.Context, a botWorkUpdateArgs) (map[string]any, error) {
		s, err := service(c, true)
		if err != nil {
			return nil, err
		}
		out, err := s.Update(c, bot.BotWorkChangeRequest{CallID: c.FunctionCallID(), ExpectedRevision: a.ExpectedRevision, Kind: a.Kind, Item: a.item(), Notes: a.Notes, Disposition: a.Disposition, Remaining: a.remaining()})
		return botWorkToolResult(c, out, err)
	})
	if err != nil {
		return nil, err
	}
	evidence, err := functiontool.New(functiontool.Config{Name: botEvidenceReadTool, Description: "Read a bounded excerpt of an earlier captured tool response in this run. A failed lookup is not evidence that a fact does not exist."}, func(c tool.Context, a botEvidenceReadArgs) (map[string]any, error) {
		s, err := service(c, false)
		if err != nil {
			return nil, err
		}
		out, err := s.Evidence(c, bot.BotEvidenceReadRequest{CallID: a.CallID, Offset: a.Offset, Limit: a.Limit})
		return botWorkToolResult(c, out, err)
	})
	if err != nil {
		return nil, err
	}
	check, err := functiontool.New(functiontool.Config{Name: botCheckWorkTool, Description: "Optionally request advisory feedback on a draft using selected captured evidence. Costs shared model budget. Feedback and failures are information, never a publication gate."}, func(c tool.Context, a botCheckWorkArgs) (map[string]any, error) {
		s, err := service(c, false)
		if err != nil {
			return nil, err
		}
		out, err := s.Check(c, bot.BotWorkCheckRequest{CallID: c.FunctionCallID(), Question: a.Question, DraftText: a.DraftText, EvidenceRefs: a.EvidenceRefs})
		return botWorkToolResult(c, out, err)
	})
	if err != nil {
		return nil, err
	}
	out := []tool.Tool{read, update, evidence, check}
	for i, t := range out {
		out[i], err = newBotOptionalSchemaTool(t, binding, t.Name() == botWorkUpdateTool)
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// botChildWorkTools keeps optional work mutation out of every child catalogue.
// The service's session check remains a second guard against retained handles.
func botChildWorkTools(all []tool.Tool) []tool.Tool {
	out := make([]tool.Tool, 0, len(all))
	for _, t := range all {
		if t.Name() != botWorkUpdateTool {
			out = append(out, t)
		}
	}
	return out
}

func botChildWorkGroups(groups map[string]*agent.ToolGroup) map[string]*agent.ToolGroup {
	out := make(map[string]*agent.ToolGroup, len(groups))
	for name, g := range groups {
		copy := *g
		copy.Tools = botChildWorkTools(g.Tools)
		out[name] = &copy
	}
	return out
}

const botPlanStateKey = "bot:execution_plan"

type botPlanRuntime struct {
	mu           sync.Mutex
	chat         *agent.ChatAgent
	shell, write botRunnableTool
	workspace    string
}
type botPlanTool struct {
	botRunnableTool
	runtime *botPlanRuntime
}

func (p *botPlanTool) Run(ctx tool.Context, args any) (map[string]any, error) {
	r := p.runtime
	r.mu.Lock()
	defer r.mu.Unlock()
	// Every verify goes back through the guarded sandbox tool, not Chat's host
	// DefaultPlanVerify. The closure is call-local under the plan mutex.
	r.chat.PlanVerify = func(command string) (int, string, error) {
		if r.shell == nil {
			return -1, "", store.ErrBotUnsupported
		}
		out, err := r.shell.Run(ctx, map[string]any{"command": command, "working_dir": r.workspace, "timeout": 600, "background": false})
		if err != nil {
			return -1, "", err
		}
		code, ok := out["exit_code"].(float64)
		if !ok {
			if n, yes := out["exit_code"].(int); yes {
				code = float64(n)
				ok = true
			}
		}
		if !ok {
			return -1, "", fmt.Errorf("verify returned no exit code")
		}
		text, _ := out["stdout"].(string)
		return int(code), text, nil
	}
	defer func() {
		r.chat.PlanVerify = func(string) (int, string, error) { return -1, "", store.ErrBotUnsupported }
	}()
	callbacks := tools.PlanCallbacks{
		State: func(goal string, doc agent.PlanDocumentInfo, steps []agent.PlanStepInfo) bool {
			if !r.chat.TrySetActivePlan(agent.NewPlanState(goal, doc, steps)) {
				return false
			}
			// The Bot's published run policy authorizes planning, not the effects of
			// its verification commands; those still pass the bound tool guard.
			r.chat.MarkActivePlanApproved()
			return true
		},
		Update: r.chat.ApplyPlanStepUpdate, Complete: r.chat.AnnounceCompletion,
		KnownSteps: func() []string {
			if plan := r.chat.GetActivePlan(); plan != nil {
				return plan.StepNames()
			}
			return nil
		},
		Progress: func(e agent.SubTaskProgressEvent) {
			e.SessionID = ctx.SessionID()
			if r.chat.SubTaskProgressCallback != nil {
				r.chat.SubTaskProgressCallback(e)
			}
		},
	}
	local := botBoundToolContext{Context: ctx, owner: tools.WithPlanCallbacks(ctx, callbacks)}
	out, err := p.botRunnableTool.Run(local, args)
	if err != nil {
		return nil, err
	}
	if plan := r.chat.GetActivePlan(); plan != nil {
		markdown := plan.RenderMarkdown()
		if err := ctx.State().Set(botPlanStateKey, markdown); err != nil {
			return nil, err
		}
		if r.write != nil {
			if _, err := r.write.Run(ctx, map[string]any{"path": r.workspace + "/PLAN.md", "content": markdown}); err != nil {
				return nil, err
			}
		}
	}
	return out, nil
}
func newBotPlanTools(chat *agent.ChatAgent, native []tool.Tool, workspace string) ([]tool.Tool, error) {
	r := &botPlanRuntime{chat: chat, workspace: workspace}
	for _, t := range native {
		if t.Name() == "shell_command" {
			r.shell, _ = t.(botRunnableTool)
		}
		if t.Name() == "write_file" {
			r.write, _ = t.(botRunnableTool)
		}
	}
	chat.PlanVerify = func(string) (int, string, error) { return -1, "", store.ErrBotUnsupported }
	constructors := []func() (tool.Tool, error){tools.NewAnnouncePlanTool, tools.NewUpdatePlanTool, tools.NewAnnounceCompletionTool}
	var result []tool.Tool
	for _, ctor := range constructors {
		t, err := ctor()
		if err != nil {
			return nil, err
		}
		result = append(result, &botPlanTool{botRunnableTool: t.(botRunnableTool), runtime: r})
	}
	return result, nil
}
func restoreBotPlan(ctx context.Context, chat *agent.ChatAgent, scope *BotChatScope, sessionID, userID string) error {
	// Session state is authoritative across attempts; no daemon-host PLAN.md path.
	s, err := scope.Sessions.Get(ctx, &session.GetRequest{AppName: "astonish", UserID: userID, SessionID: sessionID})
	if err != nil {
		return err
	}
	value, err := s.Session.State().Get(botPlanStateKey)
	if err != nil {
		return nil
	}
	markdown, ok := value.(string)
	if !ok || markdown == "" {
		return nil
	}
	doc, goal, steps, err := agent.ParsePlanDocument(markdown)
	if err != nil {
		return err
	}
	chat.SetActivePlan(agent.NewPlanState(goal, doc, steps))
	if agent.NormalizePlanLifecycle(doc.Lifecycle) != "" {
		chat.MarkActivePlanApproved()
	}
	return nil
}
