package api

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/session"
	"google.golang.org/adk/tool"
)

// BotPreparedPlanSession is a trusted, attempt-local bridge. Only the runtime
// adapter installs it; tool arguments and transcript state cannot create it.
type BotPreparedPlanSession struct {
	Request bot.RunRequest
	Store   store.BotPreparedPlanStore
	Resume  *store.BotPreparedPlan
	mu      sync.Mutex
	claimed bool
	capture *botApprovalCapture
}
type botPreparedPlanSessionKey struct{}
type botPreparedPlanResumeKey struct{}

func BotPreparedPlanSessionFromContext(ctx context.Context) *BotPreparedPlanSession {
	p, _ := ctx.Value(botPreparedPlanSessionKey{}).(*BotPreparedPlanSession)
	return p
}
func WithBotPreparedPlanSessionFromContext(dst, src context.Context) context.Context {
	if p := BotPreparedPlanSessionFromContext(src); p != nil {
		return context.WithValue(dst, botPreparedPlanSessionKey{}, p)
	}
	return dst
}

// ClaimResume checks the exact saved leaf before granting entry to the prepared
// engine, not permission to execute the leaf. Each leaf still needs live policy.
func (p *BotPreparedPlanSession) ClaimResume(name string, args any) (*store.BotPreparedPlan, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.Resume == nil || p.claimed {
		return nil, store.ErrBotForbidden
	}
	raw, err := json.Marshal(args)
	if err != nil {
		return nil, err
	}
	action := p.Resume.Action
	action.Tool, action.Arguments = name, raw
	digest, err := action.Digest()
	if err != nil || digest != p.Resume.ActionDigest {
		return nil, store.ErrBotForbidden
	}
	p.claimed = true
	return p.Resume, nil
}

// CaptureApproval records the repository-created exact leaf approval using the
// existing checkpoint format. It never requests a second, unbound approval.
func (p *BotPreparedPlanSession) CaptureApproval(record *store.BotPreparedPlan) error {
	if p.capture == nil || record == nil || record.ApprovalID == [16]byte{} {
		return store.ErrBotUnsupported
	}
	c := p.capture
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.approval != nil {
		c.conflict = true
		return store.ErrBotConflict
	}
	c.approval = &store.BotApproval{ID: record.ApprovalID, BotID: record.BotID, RunID: record.RunID, Version: record.Version, Action: record.Action, ActionDigest: record.ActionDigest, Status: store.ApprovalPending}
	return nil
}

// The synthetic invocation has no model/progressive preauthorization. Only the
// factory's exact private continuation can claim it; its leaves use real tools.
type preparedPlanToolContext struct {
	*mcpToolContext
	user  string
	state session.State
}

func (c *preparedPlanToolContext) UserID() string                       { return c.user }
func (c *preparedPlanToolContext) State() session.State                 { return c.state }
func (c *preparedPlanToolContext) ReadonlyState() session.ReadonlyState { return c.state }
func executePreparedPlan(ctx context.Context, req bot.RunRequest, resources *BotChatResources, capture *botApprovalCapture, state session.State, record *store.BotPreparedPlan) (result bot.RunResult, runErr error) {
	// This no-model continuation bypasses the ChatRunner completion finalizer.
	// Its waiting and settled reports still need the same trusted, bound envelope
	// before the supervisor can commit them and release the lease.
	defer func() {
		result.Report.Work = store.BuildBotWorkProjection(req.Work)
		// Failure constructors may already have bound an envelope before the
		// work projection was attached; regenerate it from the final facts.
		message := bot.EmployeeMessageFallback(result.Status, result.Report, runErr)
		result.Report.EmployeeMessage = &message
	}()
	var args map[string]any
	if err := json.Unmarshal(record.Action.Arguments, &args); err != nil {
		return botChatFailure(bot.ErrorBlocked, err)
	}
	invocation := &preparedPlanToolContext{mcpToolContext: &mcpToolContext{Context: ctx, sessionID: req.Run.SessionID.String()}, user: req.Bot.OwnerID.String(), state: state}
	// A generic runnable leaf is not a prepared continuation. Require the
	// scoped factory's explicit engine entry point before any effect can run.
	var target interface {
		RunPreparedPlan(tool.Context, any) (map[string]any, error)
	}
	if resources.Agent.ToolIndex != nil {
		if entry := resources.Agent.ToolIndex.FirstPartyToolEntry(record.Action.Tool); entry != nil {
			target, _ = entry.Tool.(interface {
				RunPreparedPlan(tool.Context, any) (map[string]any, error)
			})
		}
	}
	if target == nil {
		return botChatFailure(bot.ErrorBlocked, store.ErrBotUnsupported)
	}
	_, err := target.RunPreparedPlan(invocation, args)
	if capture != nil {
		if result, ok := capture.checkpoint(); ok {
			return result, nil
		}
	}
	if err != nil {
		return botChatFailure(bot.ErrorBlocked, fmt.Errorf("prepared execution: %w", err))
	}
	bridge := BotPreparedPlanSessionFromContext(ctx)
	if bridge == nil {
		return botChatFailure(bot.ErrorBlocked, store.ErrBotForbidden)
	}
	bridge.mu.Lock()
	claimed := bridge.claimed
	bridge.mu.Unlock()
	if !claimed {
		return botChatFailure(bot.ErrorBlocked, store.ErrBotForbidden)
	}
	lease := req.Run.Lease
	if req.CurrentLease != nil {
		lease = req.CurrentLease()
	}
	settled, loadErr := bridge.Store.LoadBotPlan(ctx, lease, record.OperationID)
	if loadErr != nil {
		return botChatFailure(bot.ErrorBlocked, loadErr)
	}
	if settled.Stage != store.BotPlanPublished {
		return botChatFailure(bot.ErrorBlocked, store.ErrBotConflict)
	}
	if req.PlanMode && settled.LeafKind == store.BotPlanLeafMirror {
		var snapshot agent.PlanSnapshot
		if json.Unmarshal(settled.Snapshot, &snapshot) != nil {
			return botChatFailure(bot.ErrorBlocked, store.ErrBotInvalid)
		}
		if err := bridge.CaptureDefinition(ctx, snapshot); err != nil {
			return botChatFailure(bot.ErrorBlocked, err)
		}
		if waiting, ok := capture.checkpoint(); ok {
			return waiting, nil
		}
	}
	status := store.RunSucceeded
	report := store.BotReport{Summary: "Prepared plan continuation published its saved snapshot."}
	if req.Work != nil {
		// Snapshot publication is not a captured/reviewed task answer. Do not
		// bypass the work protocol merely because this exact leaf completed.
		status = store.RunPartial
		report.Unverified = []string{"The saved plan operation was published; the broader task outcome was not assessed by this exact continuation."}
		report.EffectState = store.BotReportEffectsConfirmed
	}
	return bot.RunResult{Status: status, Report: report}, nil
}
