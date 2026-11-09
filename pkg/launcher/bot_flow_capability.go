package launcher

import (
	"context"
	"fmt"
	"sync"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/credentials"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/tools"
	adkagent "google.golang.org/adk/agent"
	"google.golang.org/adk/model"
	"google.golang.org/adk/runner"
	"google.golang.org/adk/session"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
)

// BotRunFlowTool reuses the actual Chat flow engine, not its host-initializing
// launcher. Configuration is completed privately by newBotWiredChatAgent only
// after the model and every child tool have been bound to the run budget/policy.
func BotRunFlowTool() (tool.Tool, error) {
	f := &botFlowTool{}
	schema, err := functiontool.New(functiontool.Config{Name: "run_flow", Description: "Execute an owner-scoped saved flow; supply input-node parameters to resume"}, f.run)
	if err != nil {
		return nil, err
	}
	f.botRunnableTool = schema.(botRunnableTool)
	return f, nil
}

type botFlowTool struct {
	botRunnableTool
	mu       sync.Mutex
	llm      model.LLM
	tools    []tool.Tool
	redactor *credentials.Redactor
	engine   InteractiveFlowRunner
}

func (f *botFlowTool) configure(llm model.LLM, ts []tool.Tool, r *credentials.Redactor) {
	f.llm, f.redactor = llm, r
	for _, t := range ts {
		if t.Name() != "run_flow" {
			f.tools = append(f.tools, t)
		}
	}
}
func (f *botFlowTool) cleanup() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.engine.sessions.Range(func(k, v any) bool { f.engine.CleanupSession(k.(string)); return true })
}
func (f *botFlowTool) run(ctx tool.Context, args tools.RunFlowArgs) (*tools.FlowRunResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if ctx == nil || f.llm == nil || args.FlowName == "" {
		return nil, store.ErrBotUnsupported
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	fs := store.FlowStoreFromContext(ctx)
	// Use the existing merged store semantics, never filesystem fallback.
	if team := store.TeamFlowStoreFromContext(ctx); team != nil {
		fs = store.NewCompositeFlowStore(fs, team)
	}
	if fs == nil {
		return nil, store.ErrBotUnsupported
	}
	key := fmt.Sprintf("%s:%s", ctx.SessionID(), args.FlowName)
	if paused := f.engine.GetPausedNode(key); paused != "" {
		response := args.Parameters[paused]
		options := f.engine.GetPausedOptions(key)
		if response == "" {
			return &tools.FlowRunResult{Status: "waiting_for_input", InputNode: paused, InputOptions: options}, nil
		}
		if len(options) > 0 {
			valid := false
			for _, o := range options {
				if o == response {
					valid = true
				}
			}
			if !valid {
				return nil, store.ErrBotInvalid
			}
		}
		result, err := f.engine.resumeFlow(ctx, key, response)
		if result != nil && (result.Status == "completed" || result.Status == "error") {
			f.engine.CleanupSession(key)
		}
		return result, err
	}
	yaml, err := fs.GetFlow(ctx, args.FlowName)
	if err != nil {
		return nil, err
	}
	return f.executeYAML(ctx, key, yaml, args.Parameters)
}

// executeYAML shares the host-free engine for saved flows and distillation tests.
// Callers hold f.mu and supply the already bound owner context.
func (f *botFlowTool) executeYAML(ctx context.Context, key, yaml string, params map[string]string) (*tools.FlowRunResult, error) {
	cfg, err := config.LoadAgentFromBytes([]byte(yaml))
	if err != nil {
		return nil, err
	}
	if cfg.Type == "drill" || cfg.Type == "drill_suite" {
		return nil, fmt.Errorf("%w: use the drill runtime for drills", store.ErrBotUnsupported)
	}
	ss := session.InMemoryService()
	a := agent.NewAstonishAgent(cfg, f.llm, f.tools)
	a.AutoApprove = true // child tools still enforce the exact live Bot policy
	a.IsWebMode = true
	a.SessionService = ss
	a.Redactor = f.redactor
	// Child bound tools own substitution after authorization; never resolve flow
	// placeholders before the policy sees the saved symbolic arguments.
	ag, err := adkagent.New(adkagent.Config{Name: "bot_flow", Description: cfg.Description, Run: a.Run})
	if err != nil {
		return nil, err
	}
	r, err := runner.New(runner.Config{AppName: "astonish", Agent: ag, SessionService: ss})
	if err != nil {
		return nil, err
	}
	created, err := ss.Create(ctx, &session.CreateRequest{AppName: "astonish", UserID: store.UserIDFromContext(ctx)})
	if err != nil {
		return nil, err
	}
	sess := &flowSession{runner: r, sessionService: ss, sessionID: created.Session.ID(), userID: created.Session.UserID(), appName: "astonish", agentConfig: cfg}
	f.engine.sessions.Store(key, sess)
	result, err := f.engine.executeFlowTurn(ctx, sess, nil, params)
	if err != nil || result != nil && (result.Status == "completed" || result.Status == "error") {
		f.engine.CleanupSession(key)
	}
	return result, err
}

var _ context.Context = botPinnedSessionContext{}
