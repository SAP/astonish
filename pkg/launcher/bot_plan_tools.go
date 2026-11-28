package launcher

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/google/uuid"

	"github.com/SAP/astonish/pkg/bot"
	"google.golang.org/adk/tool/functiontool"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/store"
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
	DeliveryIntent    store.BotDeliveryIntent    `json:"delivery_intent,omitempty"`
	DeliverableFormat store.BotDeliverableFormat `json:"deliverable_format,omitempty"`
	ExpectedRevision  int64                      `json:"expected_revision"`
	Kind              bot.BotWorkChangeKind      `json:"kind"`
	Item              *botWorkItemArgs           `json:"item,omitempty"`
	Notes             string                     `json:"notes,omitempty"`
	Disposition       store.AgentWorkDisposition `json:"disposition,omitempty"`
	Remaining         []botWorkRemainingArgs     `json:"remaining,omitempty"`
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
	// check or task. Failed attempts still return explicit feedback below. These
	// services cannot dispatch external actions. Bind an observation to the live
	// run instead of asking the journal to trust a bare Verified flag.
	if identity, ok := execution.BotRunIdentityFromContext(ctx); ok {
		if actor, parseErr := uuid.Parse(identity.ActorID); parseErr == nil {
			bot.RecordToolOutcome(ctx, ctx.FunctionCallID(), bot.ToolOutcome{Verified: true, Unchanged: []store.BotEvidence{{
				Kind: store.BotEvidenceObservation, Summary: "Scoped local work bookkeeping attempt returned; no content correctness, task success or external effect inferred",
				Source: "bot-work://" + identity.BotID.String() + "/" + identity.RunID.String() + "/" + ctx.FunctionCallID(), ActorID: actor, ObservedAt: time.Now().UTC(),
			}}})
		}
	}
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
	update, err := functiontool.New(functiontool.Config{Name: botWorkUpdateTool, Description: "Optionally update agent-reported progress using expected_revision and kind upsert_item, cancel_item, replace_notes, set_disposition, set_remaining or set_delivery. Ordinary information requests default to chat_only and return the complete useful answer directly in chat; no update is required to finish. Select delivery_intent=file_deliverable only for an explicitly requested artifact, editable source, diagram, binary, or an already pinned file-delivery contract, with deliverable_format=markdown or binary as appropriate. Prose length or an assistant calling its answer a report does not require a file. File delivery cannot be downgraded within a run; when selected, save and publish actual files and return a short chat summary with committed references. Omit new item IDs. Completion is agent judgment, not verification or permission."}, func(c tool.Context, a botWorkUpdateArgs) (map[string]any, error) {
		s, err := service(c, true)
		if err != nil {
			return nil, err
		}
		out, err := s.Update(c, bot.BotWorkChangeRequest{DeliveryIntent: a.DeliveryIntent, DeliverableFormat: a.DeliverableFormat, CallID: c.FunctionCallID(), ExpectedRevision: a.ExpectedRevision, Kind: a.Kind, Item: a.item(), Notes: a.Notes, Disposition: a.Disposition, Remaining: a.remaining()})
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
		switch t.Name() {
		case botWorkUpdateTool, "bot_task_select", "bot_task_capture", "bot_task_update", "bot_task_asset", "bot_task_state", "bot_task_execute":
			continue
		}
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
