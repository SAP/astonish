package launcher

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"time"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/api"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"google.golang.org/adk/tool"
)

// This marker is minted only by the scoped plan engine, never by public args.
type preparedPlanLeafKey struct{}
type scopedPreparedPlan struct {
	session *api.BotPreparedPlanSession
	record  *store.BotPreparedPlan
	engine  agent.PlanPreparedOperation
}

func (p *scopedChatPlan) planLease() store.BotLease {
	req := p.prepared.session.Request
	if req.CurrentLease != nil {
		return req.CurrentLease()
	}
	return req.Run.Lease
}
func planMirrorArgs(workspace string, snapshot agent.PlanSnapshot) map[string]any {
	return map[string]any{"path": path.Join(workspace, "PLAN.md"), "content": agent.NewPlanState(snapshot.Goal, snapshot.Document, snapshot.Steps).RenderMarkdown()}
}
func (p *scopedChatPlan) preparedAction(op agent.PlanPreparedOperation) (execution.ActionDescriptor, error) {
	name := "write_file"
	var args map[string]any
	if op.Candidate != nil {
		args = planMirrorArgs(p.workspace, *op.Candidate)
	} else {
		name = "shell_command"
		for _, step := range op.Base.Steps {
			if step.Name == op.Step {
				args = map[string]any{"command": step.Verify, "working_dir": p.workspace, "timeout": 600, "background": false}
			}
		}
	}
	if args == nil {
		return execution.ActionDescriptor{}, store.ErrBotInvalid
	}
	raw, err := json.Marshal(args)
	if err != nil {
		return execution.ActionDescriptor{}, err
	}
	classifier := agent.BotPolicyRuntimeFromContext(p.invocation).Classifier
	if classifier == nil {
		return execution.ActionDescriptor{}, store.ErrBotUnsupported
	}
	action, err := classifier.DescribeAction(p.invocation, name, raw)
	if err != nil {
		return action, err
	}
	original := action
	original.Tool, original.Arguments = name, raw
	want, e1 := original.Digest()
	got, e2 := action.Digest()
	if e1 != nil || e2 != nil || want != got {
		return action, store.ErrBotForbidden
	}
	return action, nil
}
func (p *scopedChatPlan) changePrepared(q store.BotPreparedPlanChange) error {
	q.OperationID, q.ExpectedRevision = p.prepared.record.OperationID, p.prepared.record.Revision
	saved, err := p.prepared.session.Store.ChangeBotPlan(p.invocation, p.planLease(), q)
	if err == nil {
		p.prepared.record = saved
	}
	return err
}
func (p *scopedChatPlan) prepareOperation(op agent.PlanPreparedOperation) error {
	if p.prepared == nil {
		return nil
	}
	p.prepared.engine = op.Clone()
	raw, err := json.Marshal(op)
	if err != nil {
		return err
	}
	r := p.prepared.record
	if r != nil && r.OperationID.String() != op.ID {
		if r.Stage != store.BotPlanPublished {
			return store.ErrBotConflict
		}
		r = nil
		p.prepared.record = nil
	}
	if r == nil {
		action, err := p.preparedAction(op)
		if err != nil {
			return err
		}
		digest, err := action.Digest()
		if err != nil {
			return err
		}
		id, err := uuid.Parse(op.ID)
		if err != nil {
			return err
		}
		kind := store.BotPlanLeafMirror
		if op.Kind == agent.PlanPreparedVerify {
			kind = store.BotPlanLeafVerifier
		}
		req := p.prepared.session.Request
		r, err = p.prepared.session.Store.PrepareBotPlan(p.invocation, p.planLease(), store.BotPreparedPlan{BotID: req.Bot.ID, RunID: req.Run.ID, SessionID: req.Run.SessionID, Version: req.Run.Version, OperationID: id, Base: store.BotPlanBase{PlanID: op.Base.PlanID, Revision: op.Base.Revision, Sequence: op.Base.Sequence}, LeafKind: kind, Action: action, ActionDigest: digest, EngineRecord: raw})
		if err != nil {
			return err
		}
		p.prepared.record = r
	}
	// Authorize before the engine enters Verifying. A denied verifier must remain
	// Ready, not become failed verification evidence or an uncertain dispatch.
	if op.Stage == agent.PlanPreparedReady {
		return p.authorizePrepared()
	}
	if op.Stage == agent.PlanPreparedSettled && r.LeafKind == store.BotPlanLeafVerifier {
		if r.Stage == store.BotPlanDispatchStarted {
			evidence, err := json.Marshal(op.Verification)
			if err != nil {
				return err
			}
			if err = p.changePrepared(store.BotPreparedPlanChange{Stage: store.BotPlanSettled, DispatchID: r.DispatchID, TrustedResult: evidence, EngineRecord: raw}); err != nil {
				return err
			}
		}
		action, err := p.preparedAction(op)
		if err != nil {
			return err
		}
		digest, err := action.Digest()
		if err != nil {
			return err
		}
		r = p.prepared.record
		next, err := p.prepared.session.Store.AdvanceBotPlan(p.invocation, p.planLease(), store.BotPreparedPlanAdvance{OperationID: r.OperationID, ExpectedRevision: r.Revision, Action: action, ActionDigest: digest})
		if err != nil {
			return err
		}
		p.prepared.record = next
	}
	return nil
}

// Authorize using the same classifier, policy and live guard as every other
// tool. The guard wrapper starts/consumes atomically only after live permission.
func (p *scopedChatPlan) authorizePrepared() error {
	r := p.prepared.record
	if r.Stage == store.BotPlanDispatchStarted {
		return nil
	}
	if r.Stage == store.BotPlanSettled || r.Stage == store.BotPlanPublished {
		return nil
	}
	if auth, interactive := execution.BotTurnAuthorizerFromContext(p.invocation); interactive {
		if _, private := agent.BotPrivateWorkerExecution(p.invocation); !private {
			return auth.AuthorizeTool(p.invocation, r.Action.Tool)
		}
	}
	runtime := agent.BotPolicyRuntimeFromContext(p.invocation)
	runtime.Approvals = preparedPlanRequester{p}
	runtime.Guard = preparedPlanGuard{p: p, live: runtime.Guard}
	bound := agent.WithBotPolicyRuntime(agent.WithoutBotInvocationHandoff(p.invocation), runtime)
	var args map[string]any
	if err := json.Unmarshal(r.Action.Arguments, &args); err != nil {
		return err
	}
	return agent.AuthorizeBotTool(bound, r.Action.Tool, args)
}

type preparedPlanRequester struct{ p *scopedChatPlan }

func (a preparedPlanRequester) RequestApproval(ctx context.Context, id execution.BotRunIdentity, action execution.ActionDescriptor, digest string) (uuid.UUID, error) {
	p := a.p
	r := p.prepared.record
	if digest != r.ActionDigest {
		return uuid.Nil, store.ErrBotForbidden
	}
	if r.Stage == store.BotPlanPrepared {
		if err := p.changePrepared(store.BotPreparedPlanChange{Stage: store.BotPlanAwaitingApproval, ApprovalExpiresAt: time.Now().Add(24 * time.Hour)}); err != nil {
			return uuid.Nil, err
		}
	}
	r = p.prepared.record
	if err := p.prepared.session.CaptureApproval(r); err != nil {
		return uuid.Nil, err
	}
	return r.ApprovalID, nil
}

type preparedPlanGuard struct {
	p     *scopedChatPlan
	live  execution.PolicyGuard
	start bool
}

func (g preparedPlanGuard) Authorize(ctx context.Context, action execution.ActionDescriptor) (execution.PolicyDecision, error) {
	if g.live == nil {
		return execution.PolicyDecision{}, store.ErrBotUnsupported
	}
	r := g.p.prepared.record
	digest, err := action.Digest()
	if err != nil || digest != r.ActionDigest {
		return execution.PolicyDecision{}, store.ErrBotForbidden
	}
	if r.Stage == store.BotPlanDispatchStarted {
		return execution.PolicyDecision{}, store.ErrBotConflict
	}
	d, err := g.live.Authorize(ctx, action)
	if err == nil && d.Kind == execution.PolicyRequestApproval && d.ActionDigest == digest {
		return d, nil
	}
	if err != nil || d.Kind != execution.PolicyAllow || d.ActionDigest != digest {
		return d, store.ErrBotForbidden
	}
	if g.start {
		if err := g.p.changePrepared(store.BotPreparedPlanChange{Stage: store.BotPlanDispatchStarted, ApprovalID: r.ApprovalID, DispatchID: uuid.New()}); err != nil {
			return execution.PolicyDecision{}, err
		}
	}
	return d, nil
}

// The ordinary terminal authorization is still executed, but consumes neither
// the parent's handoff nor the leaf approval twice. Reclassification, binding,
// membership, isolation and budgets remain in the normal terminal path.
func preparedLeafContext(owner context.Context, invocation tool.Context) context.Context {
	p, _ := invocation.Value(preparedPlanLeafKey{}).(*scopedChatPlan)
	if p == nil || p.prepared == nil {
		return owner
	}
	runtime := agent.BotPolicyRuntimeFromContext(owner)
	runtime.Guard = preparedPlanGuard{p: p, live: runtime.Guard, start: true}
	runtime.Approvals = preparedPlanRequester{p}
	return agent.WithBotPolicyRuntime(owner, runtime)
}
func (p *scopedChatPlan) commitPrepared(snapshot agent.PlanSnapshot) error {
	r := p.prepared.record
	if r == nil || p.prepared.engine.Candidate == nil {
		return store.ErrBotInvalid
	}
	if r.Stage != store.BotPlanSettled && r.Stage != store.BotPlanPublished {
		// Authorization is outside the actual tool to ensure durable start precedes
		// dispatch, while the terminal tool also revalidates live authority.
		var args map[string]any
		if err := json.Unmarshal(r.Action.Arguments, &args); err != nil {
			return err
		}
		if _, interactive := execution.BotTurnAuthorizerFromContext(p.invocation); interactive {
			if _, private := agent.BotPrivateWorkerExecution(p.invocation); !private {
				if err := p.changePrepared(store.BotPreparedPlanChange{Stage: store.BotPlanDispatchStarted, DispatchID: uuid.New()}); err != nil {
					return err
				}
			}
		}
		out, err := p.call("write_file", args)
		if err != nil {
			return err
		}
		if msg, _ := out["error"].(string); msg != "" {
			return fmt.Errorf("plan mirror: %s", msg)
		}
		raw, err := json.Marshal(snapshot)
		if err != nil {
			return err
		}
		evidence, err := json.Marshal(out)
		if err != nil {
			return err
		}
		r = p.prepared.record
		if err = p.changePrepared(store.BotPreparedPlanChange{Stage: store.BotPlanSettled, DispatchID: r.DispatchID, TrustedResult: evidence, Snapshot: raw}); err != nil {
			return err
		}
	}
	return p.changePrepared(store.BotPreparedPlanChange{Stage: store.BotPlanPublished})
}

// RunPreparedPlan is the factory-only continuation entry point. It deliberately
// cannot fall through to the ordinary file/shell implementation when the plan
// engine or trusted resume bridge is missing.
func (t *chatRuntimeTool) RunPreparedPlan(ctx tool.Context, args any) (map[string]any, error) {
	if ctx == nil || t.binding == nil || t.binding.plan == nil {
		return nil, store.ErrBotUnsupported
	}
	bound, release, err := t.binding.bind(ctx)
	defer release()
	if err != nil {
		return nil, err
	}
	c := chatRuntimeToolContext{Context: ctx, bound: bound}
	if entry, ok := t.binding.hooks.Dispatch.(interface {
		BindPreparedPlan(tool.Context) (tool.Context, error)
	}); ok {
		preparedContext, err := entry.BindPreparedPlan(c)
		if err != nil {
			return nil, err
		}
		c = chatRuntimeToolContext{Context: preparedContext, bound: preparedContext}
	}
	prepared := api.BotPreparedPlanSessionFromContext(c)
	if prepared == nil || prepared.Resume == nil || ctx.Value(preparedPlanLeafKey{}) != nil {
		return nil, store.ErrBotForbidden
	}
	record, err := prepared.ClaimResume(t.Name(), args)
	if err != nil {
		return nil, err
	}
	return t.binding.plan.run(c, preparedResumeExecutable{chatRuntimeRunnable: t.chatRuntimeRunnable, p: t.binding.plan, record: record}, args)
}

type preparedResumeExecutable struct {
	chatRuntimeRunnable
	p      *scopedChatPlan
	record *store.BotPreparedPlan
}

func (e preparedResumeExecutable) Run(ctx tool.Context, args any) (map[string]any, error) {
	var op agent.PlanPreparedOperation
	if err := json.Unmarshal(e.record.EngineRecord, &op); err != nil {
		return nil, err
	}
	e.p.prepared.record = e.record
	e.p.prepared.engine = op
	// A verifier ready record is safe only while the repository leaf never started.
	result, err := e.p.runtime.ResumePreparedOperation(op)
	return map[string]any{"result": result, "snapshot": e.p.runtime.Snapshot()}, err
}
