package launcher

import (
	"context"
	"fmt"
	"sync"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/tools"
	"google.golang.org/adk/session"
	"google.golang.org/adk/tool"
)

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
