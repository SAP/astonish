package agent

import (
	"fmt"
	"net/url"
	"reflect"
	"time"

	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/common"
	"github.com/SAP/astonish/pkg/execution"
	"google.golang.org/adk/model"
	"google.golang.org/adk/tool"
)

// botEmployeeChatTool is internal orchestration, not an executable-action grant.
// Only trusted composition may seal its local functiontool and live owner binding.
type botEmployeeChatTool struct {
	proposalCaptureRunnable
	bind func(tool.Context) (tool.Context, error)
}

// NewBotEmployeeChatTool seals the exact local Employee service adapter. bind must
// reject worker/control/title modes and check live owner, lease, revocation,
// capability, deadline, isolation and the original shared budget on every call.
func NewBotEmployeeChatTool(t tool.Tool, bind func(tool.Context) (tool.Context, error)) (tool.Tool, error) {
	r, ok := t.(proposalCaptureRunnable)
	if !ok || bind == nil || t.Name() != "employee_chat" {
		return nil, fmt.Errorf("invalid Bot Employee Chat capability")
	}
	return &botEmployeeChatTool{proposalCaptureRunnable: r, bind: bind}, nil
}

func (t *botEmployeeChatTool) Run(ctx tool.Context, args any) (map[string]any, error) {
	if ctx == nil {
		return nil, botPolicyDenied("Employee Chat requires live invocation")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	id, ok := execution.BotRunIdentityFromContext(ctx)
	_, interactive := execution.BotTurnAuthorizerFromContext(ctx)
	if !ok || (execution.RunPolicyFromContext(ctx) == nil && !interactive) || (interactive && execution.RunPolicyFromContext(ctx) != nil) {
		return nil, botPolicyDenied("Employee Chat requires scoped Bot authority")
	}
	input, ok := args.(map[string]any)
	if !ok {
		return nil, botPolicyDenied("Employee Chat requires structured arguments")
	}
	if err := common.ValidateToolArguments(t, input); err != nil {
		return nil, err
	}
	bound, err := t.bind(ctx)
	if err != nil {
		return nil, err
	}
	if bound == nil {
		return nil, botPolicyDenied("Employee Chat binding unavailable")
	}
	if err := bound.Err(); err != nil {
		return nil, err
	}
	boundID, ok := execution.BotRunIdentityFromContext(bound)
	if !ok || !reflect.DeepEqual(id, boundID) || ctx.SessionID() != bound.SessionID() || bot.EmployeeChatServiceFromContext(bound) == nil {
		return nil, botPolicyDenied("Employee Chat binding mismatch")
	}
	out, err := t.proposalCaptureRunnable.Run(bound, args)
	// Attest only that the internal service returned, never that worker prose is
	// true or an external effect succeeded. Worker outcomes are aggregated separately.
	if employeeChatServiceReturned(out, err) {
		if actor, parseErr := uuid.Parse(boundID.ActorID); parseErr == nil {
			bot.RecordToolOutcome(bound, ctx.FunctionCallID(), bot.ToolOutcome{Verified: true, Unchanged: []store.BotEvidence{{Kind: store.BotEvidenceObservation, Summary: "Private Employee Chat service returned; no external action attested", Source: "bot-employee-chat://" + boundID.RunID.String() + "/" + url.PathEscape(ctx.FunctionCallID()), ObservedAt: time.Now().UTC(), ActorID: actor}}})
		}
	}
	return out, err
}

// employeeChatServiceReturned validates the typed local service result projected
// by functiontool. A failed/cancelled worker still returned a definite service
// response; this is not ToolResultSucceeded and never certifies worker effects.
func employeeChatServiceReturned(out map[string]any, err error) bool {
	if err != nil || out == nil {
		return false
	}
	if result, wrapped := out["result"]; wrapped {
		// functiontool wraps structs containing artifact slices instead of projecting
		// their fields directly. This is the sealed local service result only.
		typed, ok := result.(*bot.EmployeeChatResult)
		if !ok || typed == nil {
			return false
		}
		out = map[string]any{"worker_id": typed.WorkerID, "status": string(typed.Status)}
	}
	workerID, ok := out["worker_id"].(string)
	if !ok {
		return false
	}
	worker, err := uuid.Parse(workerID)
	if err != nil || worker == uuid.Nil {
		return false
	}
	status, ok := out["status"].(string)
	if !ok {
		return false
	}
	switch bot.EmployeeChatStatus(status) {
	case bot.EmployeeChatCompleted, bot.EmployeeChatPartial, bot.EmployeeChatNeedsInput, bot.EmployeeChatFailed, bot.EmployeeChatCancelled:
		return true
	default:
		return false
	}
}

func (t *botEmployeeChatTool) ProcessRequest(ctx tool.Context, req *model.LLMRequest) error {
	if err := t.proposalCaptureRunnable.ProcessRequest(ctx, req); err != nil {
		return err
	}
	if req != nil && req.Tools != nil {
		req.Tools[t.Name()] = t
	}
	return nil
}
