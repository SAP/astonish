package daemon

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/mux"
	"google.golang.org/adk/model"
	"google.golang.org/genai"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/api"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/launcher"
	"github.com/SAP/astonish/pkg/provider"
	"github.com/SAP/astonish/pkg/scheduler"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/entstore"
)

type authorizedScheduleQuotaAction uint8

const (
	authorizedScheduleQuotaReserve authorizedScheduleQuotaAction = iota
	authorizedScheduleQuotaConsume
	authorizedScheduleQuotaRelease
)

type authorizedScheduleHTTPModel struct {
	workerTestModel
	schedule        bool
	direct          bool
	pendingDone     bool
	control         map[string]any
	cadence         *scheduleCadenceModel
	pausedRead      *pausedScheduleReadModel
	clarify         bool
	clarified       bool
	proposal        bool
	timezone        string
	t               *testing.T
	currentBot      func() (*store.Bot, error)
	pendingCapture  func()
	questionCapture func(context.Context)
	// Only owner acknowledgments are chat-only; scheduled report work retains
	// its ordinary file-delivery contract.
	chatOnly        bool
	deliverySet     bool
	work            func() (*store.BotWorkState, error)
	synchronous     bool
	employeeStarted bool
	employeeRequest string
	controlOnly     bool
	finalCapture    func(*model.LLMRequest)
	finalText       string
	modelCalls      int
	pendingReviews  int
	denyPending     bool
	artifactPending bool
	artifactFixture bool
	artifactWorker  bool
}

func (m *authorizedScheduleHTTPModel) GenerateContent(ctx context.Context, r *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	m.modelCalls++
	if m.synchronous && r.Config != nil && r.Config.SystemInstruction != nil && len(r.Config.SystemInstruction.Parts) > 0 {
		prompt := r.Config.SystemInstruction.Parts[0].Text
		if strings.HasPrefix(prompt, "BOT_WORK_PENDING_REVIEW_V1") {
			m.pendingReviews++
			var input struct {
				store.BotWorkState
				Scope    string `json:"scope"`
				Controls []struct {
					Operation      store.BotControlOperation `json:"operation"`
					Acknowledgment string                    `json:"acknowledgment"`
					Payload        store.BotControlPayload   `json:"payload"`
				} `json:"trusted_pending_controls"`
			}
			if len(r.Contents) != 1 || json.Unmarshal([]byte(r.Contents[0].Parts[0].Text), &input) != nil || input.Candidate == nil || input.EmployeeReviewPolicy != store.BotEmployeeReviewEvidenceBoundV1 || input.Scope != "current_turn_pending_acknowledgment" || len(input.Controls) != 1 {
				m.t.Fatal("pending review did not receive the complete production evidence-bound contract")
			}
			control := input.Controls[0]
			if control.Payload.Validate() != nil || control.Payload.Operation != control.Operation || control.Payload.Schedule == nil || control.Payload.Schedule.Trigger.Cron != "0 9 * * *" || control.Payload.Schedule.Trigger.Timezone != "UTC" || !control.Payload.Schedule.Trigger.Enabled || input.Candidate.Text != control.Acknowledgment || len(input.Candidate.Observations) == 0 {
				m.t.Fatalf("pending review lacks exact queued payload and acknowledgment: %+v", input)
			}
			current := store.BotWorkReview{CandidateDigest: input.Candidate.Digest}
			deferred := store.BotWorkReview{CandidateDigest: input.Candidate.Digest}
			for _, criterion := range input.Criteria {
				if criterion.Excluded {
					continue
				}
				assessment := store.BotCriterionAssessment{CriterionID: criterion.ID, Verdict: store.BotCriterionSatisfied, Method: store.BotAssessmentModel, EvidenceRefs: []string{"candidate:" + input.Candidate.Digest}, Explanation: "Exact queued schedule payload matches the original owner request; no application claimed."}
				if m.denyPending {
					assessment.Verdict = store.BotCriterionFailed
					assessment.Explanation = "The exact pending payload does not satisfy the requested work."
				}
				current.Criteria = append(current.Criteria, assessment)
				assessment.Verdict = store.BotCriterionUnverified
				assessment.Explanation = "Application must await source commit and independent scheduler readback."
				deferred.Criteria = append(deferred.Criteria, assessment)
			}
			return func(yield func(*model.LLMResponse, error) bool) {
				raw, _ := json.Marshal(map[string]any{"current_turn": current, "deferred_application": deferred})
				yield(&model.LLMResponse{Content: genai.NewContentFromText(string(raw), "model"), TurnComplete: true}, nil)
			}
		} else if strings.HasPrefix(prompt, "BOT_EMPLOYEE_REVIEW_V1") {
			return func(yield func(*model.LLMResponse, error) bool) {
				yield(nil, errors.New("application cannot be reviewed before source commit; use the pending payload contract"))
			}
		}
	}
	if m.artifactFixture {
		if response, ok := workerWorkAssessment(r, func(w *store.BotWorkState) bool {
			return w.Candidate != nil && strings.Contains(w.Candidate.Text, "pending") && len(w.Candidate.Observations) > 0
		}); ok {
			return scheduleArtifactContentReview(r, response)
		}
	}
	if response, ok := workerWorkAssessment(r, nil); ok {
		return response
	}
	if m.artifactPending && m.artifactWorker {
		if _, employee := r.Tools["employee_chat"]; !employee {
			m.artifactPending = false
			return func(yield func(*model.LLMResponse, error) bool) {
				call := &genai.FunctionCall{ID: "crash-retained-artifact", Name: "publish_file", Args: map[string]any{"path": "outputs/crash-report.md", "title": "Retained report", "purpose": "Crash recovery fixture", "memory_summary": "Retained report at outputs/crash-report.md"}}
				if _, direct := r.Tools[call.Name]; !direct {
					call.Name, call.Args = "execute_tool", map[string]any{"name": "publish_file", "arguments": call.Args}
				}
				yield(&model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: call}}}}, nil)
			}
		}
	}
	if m.clarify && m.chatOnly && !m.deliverySet && r.Tools["employee_chat"] != nil {
		return func(yield func(*model.LLMResponse, error) bool) {
			work, err := m.work()
			if err != nil || work == nil {
				yield(nil, fmt.Errorf("load clarification delivery: %v", err))
				return
			}
			m.deliverySet = true
			yield(&model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{ID: "clarification-delivery", Name: "bot_work_update", Args: map[string]any{"expected_revision": work.Revision, "kind": "set_delivery", "delivery_intent": string(store.BotDeliveryChatOnly)}}}}}}, nil)
		}
	}
	if m.clarify && !m.clarified && r.Tools["employee_chat"] != nil {
		m.clarified = true
		return func(yield func(*model.LLMResponse, error) bool) {
			if m.questionCapture != nil {
				m.questionCapture(ctx)
			}
			yield(&model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{ID: "schedule-timezone", Name: "ask_user", Args: map[string]any{"kind": "text", "prompt": "Which timezone should the daily 9am briefing use?"}}}}}}, nil)
		}
	}
	if m.clarify && m.schedule && r.Tools["employee_chat"] != nil {
		requests, answers := 0, 0
		for _, content := range r.Contents {
			for _, part := range content.Parts {
				if content.Role == "user" && part.Text == "Schedule this briefing every day at 9am" {
					requests++
				}
				if content.Role == "user" && strings.Contains(part.Text, `"bot_owner_answer"`) {
					var answer struct {
						Response store.BotInteractionResponse `json:"response"`
					}
					if err := json.Unmarshal([]byte(part.Text), &answer); err != nil {
						m.t.Fatal(err)
					}
					if answer.Response.Answer != "America/New_York" {
						m.t.Fatalf("wrong restored answer: %+v", answer)
					}
					answers++
				}
			}
		}
		if requests != 1 || answers != 1 {
			m.t.Fatalf("lost/repeated source request or answer: requests=%d answers=%d", requests, answers)
		}
	}
	if _, employee := r.Tools["employee_chat"]; employee && !m.controlOnly && m.pausedRead == nil {
		return func(yield func(*model.LLMResponse, error) bool) {
			if !m.employeeStarted {
				m.employeeStarted = true
				message := m.employeeRequest
				if message == "" {
					message = "Schedule the daily report at 9am using the owner's request."
				}
				yield(&model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{ID: "employee-schedule-worker", Name: "employee_chat", Args: map[string]any{"message": message}}}}}}, nil)
				return
			}
			if m.artifactFixture && !m.artifactWorker {
				m.artifactWorker = true
				yield(&model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{ID: "employee-artifact-worker", Name: "employee_chat", Args: map[string]any{"message": "Publish the existing illustrative outputs/crash-report.md file."}}}}}}, nil)
				return
			}
			yield(&model.LLMResponse{Content: genai.NewContentFromText("The schedule request is pending; application is not yet verified.", "model"), TurnComplete: true}, nil)
		}
	}
	if r.Config != nil && r.Config.SystemInstruction != nil && len(r.Config.SystemInstruction.Parts) > 0 && strings.Contains(r.Config.SystemInstruction.Parts[0].Text, "BOT_EMPLOYEE_PRESENTATION_V1") {
		return func(yield func(*model.LLMResponse, error) bool) {
			if len(r.Tools) != 0 {
				m.t.Fatal("presentation exposes tools")
			}
			if m.finalCapture != nil {
				m.finalCapture(r)
			}
			var facts struct {
				SourceDigest string               `json:"source_digest"`
				Operations   []store.BotOperation `json:"operations"`
			}
			for _, content := range r.Contents {
				for _, part := range content.Parts {
					_ = json.Unmarshal([]byte(part.Text), &facts)
				}
			}
			finalText := m.finalText
			if finalText == "" {
				finalText = "Your daily report is scheduled at 9am."
				if !m.synchronous {
					finalText = "No schedule application is verified."
					for _, operation := range facts.Operations {
						finalText = fmt.Sprintf("The %s request is %s.", operation.Payload.Operation, operation.Status)
						if operation.Status == store.BotOperationApplied && operation.Receipt != nil && operation.Receipt.Schedule != nil {
							receipt := operation.Receipt.Schedule
							finalText = fmt.Sprintf("The schedule request is applied; verified enabled %t, removed %t.", receipt.Enabled, receipt.Removed)
							if operation.Payload.Schedule != nil {
								finalText += " Requested cron: " + operation.Payload.Schedule.Trigger.Cron + "."
							}
						}
					}
				}
			}
			text, _ := json.Marshal(map[string]any{"source_digest": facts.SourceDigest, "text": finalText})
			yield(&model.LLMResponse{Content: genai.NewContentFromText(string(text), "model"), TurnComplete: true}, nil)
		}
	}
	if m.controlOnly {
		return func(yield func(*model.LLMResponse, error) bool) {
			if m.control != nil {
				if _, available := r.Tools["propose_bot_control"]; !available {
					yield(nil, errors.New("paused control requires native propose_bot_control capability"))
					return
				}
				payload := m.control
				m.control = nil
				yield(&model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{ID: "paused-goal-control", Name: "propose_bot_control", Args: map[string]any{"payload": payload}}}}}}, nil)
				return
			}
			yield(&model.LLMResponse{Content: genai.NewContentFromText("The paused goal request is pending; application is not yet verified.", "model"), TurnComplete: true}, nil)
		}
	}
	if m.chatOnly && !m.deliverySet {
		return func(yield func(*model.LLMResponse, error) bool) {
			_, direct := r.Tools["bot_work_update"]
			if _, bridge := r.Tools["execute_tool"]; !direct && !bridge {
				yield(nil, fmt.Errorf("conversational fixture requires authorized bot_work_update or Chat dispatch bridge"))
				return
			}
			work, err := m.work()
			if err != nil || work == nil {
				yield(nil, fmt.Errorf("load conversational delivery revision: work=%v err=%v", work, err))
				return
			}
			m.deliverySet = true
			call := &genai.FunctionCall{ID: "schedule-chat-delivery", Name: "bot_work_update", Args: map[string]any{"expected_revision": work.Revision, "kind": "set_delivery", "delivery_intent": string(store.BotDeliveryChatOnly)}}
			if !direct {
				call.Name, call.Args = "execute_tool", map[string]any{"name": "bot_work_update", "arguments": call.Args}
			}
			yield(&model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: call}}}}, nil)
		}
	}
	if m.clarify && !m.clarified {
		m.clarified = true
		return func(yield func(*model.LLMResponse, error) bool) {
			if m.questionCapture != nil {
				m.questionCapture(ctx)
			}
			call := &genai.FunctionCall{ID: "schedule-timezone", Name: "ask_user", Args: map[string]any{"kind": "text", "prompt": "Which timezone should the daily 9am briefing use?"}}
			if _, available := r.Tools["ask_user"]; !available {
				call.Name, call.Args = "execute_tool", map[string]any{"name": "ask_user", "arguments": call.Args}
			}
			yield(&model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: call}}}}, nil)
		}
	}
	if m.pausedRead != nil {
		return m.pausedRead.GenerateContent(ctx, r, stream)
	}
	if m.cadence != nil {
		return m.cadence.GenerateContent(ctx, r, stream)
	}
	if m.control != nil {
		payload := m.control
		m.control = nil
		return func(yield func(*model.LLMResponse, error) bool) {
			yield(&model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{ID: uuid.NewString(), Name: "propose_bot_control", Args: map[string]any{"payload": payload}}}}}}, nil)
		}
	}
	if m.pendingDone {
		if m.pendingCapture != nil {
			m.pendingCapture()
		}
		m.pendingDone = false
		return func(yield func(*model.LLMResponse, error) bool) {
			// This agent-led producer owns the wording; no mandatory reviewer
			// rewrites an invented application claim into a pending receipt.
			yield(&model.LLMResponse{Content: genai.NewContentFromText("The schedule request is pending; application is not yet verified.", "model"), TurnComplete: true}, nil)
		}
	}
	if !m.schedule {
		return m.workerTestModel.GenerateContent(ctx, r, stream)
	}
	m.schedule = false
	m.pendingDone = true
	return func(yield func(*model.LLMResponse, error) bool) {
		args := map[string]any{"name": "Daily report", "schedule": "0 9 * * *", "timezone": "UTC", "instructions": "Read the daily report", "invocation": "Deliver the daily report", "mode": "adaptive"}
		if m.timezone != "" {
			args["timezone"] = m.timezone
		}
		call := &genai.FunctionCall{ID: uuid.NewString(), Name: "schedule_job", Args: args}
		if m.proposal {
			b, err := m.currentBot()
			if err != nil {
				m.t.Fatal(err)
			}
			call.Name, call.Args = "propose_bot_control", map[string]any{"payload": store.BotControlPayload{Operation: store.BotControlScheduleCreate, Reason: "Owner requested daily briefing", Schedule: &store.BotControlSchedule{ExpectedRevision: b.Revision, Trigger: store.TriggerSpec{Kind: store.TriggerCron, Cron: "0 9 * * *", Timezone: m.timezone, Instructions: "Read the daily report", Enabled: true}}}}
			// Proposal capture is a sealed native capability, not a catalogue
			// executable. Never route it through the generic progressive bridge.
		} else if _, available := r.Tools["schedule_job"]; !m.direct || !available {
			call.Name, call.Args = "execute_tool", map[string]any{"name": "schedule_job", "arguments": args}
		}
		yield(&model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: call}}}}, nil)
	}
}

// scheduleArtifactContentReview exempts only the exact illustrative file input;
// all execution and pending-control claims retain the shared evidence reviewer.
func scheduleArtifactContentReview(r *model.LLMRequest, responses iter.Seq2[*model.LLMResponse, error]) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		for response, err := range responses {
			if err != nil || response == nil {
				yield(response, err)
				return
			}
			var review map[string]any
			_ = json.Unmarshal([]byte(response.Content.Parts[0].Text), &review)
			if _, ok := review["content_assessments"]; ok {
				var input workerContentInput
				_ = json.Unmarshal([]byte(r.Contents[0].Parts[0].Text), &input)
				assessments := []workerContentAssessment{}
				for _, item := range input.Items {
					a := workerAssessContent(input, item)
					if item.Text == "# Retained report" || item.Text == "An illustrative recovery fixture." {
						a.ClaimType, a.Support = workerClaimNonfactual, store.BotCriterionSatisfied
						a.Explanation = "Exact controlled illustrative input, no external fact claimed."
					}
					assessments = append(assessments, a)
				}
				review["content_assessments"] = assessments
				raw, _ := json.Marshal(review)
				response.Content = genai.NewContentFromText(string(raw), "model")
			}
			if !yield(response, nil) {
				return
			}
		}
	}
}

// Exceed historical execution caps with genuine read-only dispatch before
// scheduling. Usage remains accounted even though execution is not budget-gated.
type cumulativeScheduleHTTPModel struct {
	*authorizedScheduleHTTPModel
	reads         int
	verifiedReads map[string]bool
}

func (m *cumulativeScheduleHTTPModel) GenerateContent(ctx context.Context, r *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	if m.verifiedReads == nil {
		m.verifiedReads = make(map[string]bool)
	}
	for _, content := range r.Contents {
		for _, part := range content.Parts {
			response := part.FunctionResponse
			if response == nil || !strings.HasPrefix(response.ID, "budget-read-") {
				continue
			}
			if err, denied := response.Response["error"]; denied {
				m.t.Fatalf("accounted read denied by historical execution cap: %s: %v", response.ID, err)
			}
			m.verifiedReads[response.ID] = true
		}
	}
	if _, employee := r.Tools["employee_chat"]; employee && m.reads < 3 {
		m.reads++
		return func(yield func(*model.LLMResponse, error) bool) {
			call := &genai.FunctionCall{ID: fmt.Sprintf("budget-read-%d", m.reads), Name: "bot_work_read", Args: map[string]any{"limit": 1}}
			yield(&model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: call}}}}, nil)
		}
	}
	// The base model owns pending evidence review, including denyPending.
	return m.authorizedScheduleHTTPModel.GenerateContent(ctx, r, stream)
}
func TestAuthorizedScheduleHTTPConversationalDelivery(t *testing.T) {
	m := &authorizedScheduleHTTPModel{chatOnly: true, pendingDone: true}
	revision := int64(7)
	m.work = func() (*store.BotWorkState, error) {
		return &store.BotWorkState{Revision: revision}, nil
	}
	request := &model.LLMRequest{Tools: map[string]any{"bot_work_update": struct{}{}}}
	for _, want := range []int64{7, 11} {
		// Prepare resets the declaration for each owner run; never reuse the
		// preceding run's revision or suppress its actual tool invocation.
		revision, m.deliverySet = want, false
		responses := 0
		for response, err := range m.GenerateContent(t.Context(), request, false) {
			if err != nil {
				t.Fatal(err)
			}
			responses++
			call := response.Content.Parts[0].FunctionCall
			if call == nil || call.Name != "bot_work_update" || call.Args["expected_revision"] != want || call.Args["kind"] != "set_delivery" || call.Args["delivery_intent"] != string(store.BotDeliveryChatOnly) {
				t.Fatalf("wrong delivery protocol: %+v", call)
			}
		}
		if responses != 1 || !m.deliverySet || !m.pendingDone {
			t.Fatalf("declaration consumed or repeated the producer: responses=%d model=%+v", responses, m)
		}
	}
	for response, err := range m.GenerateContent(t.Context(), request, false) {
		if err != nil || response.Content.Parts[0].FunctionCall != nil || !strings.Contains(response.Content.Parts[0].Text, "pending") {
			t.Fatalf("declaration did not resume pending acknowledgment: %+v %v", response, err)
		}
	}
	m.deliverySet = false
	for response, err := range m.GenerateContent(t.Context(), &model.LLMRequest{}, false) {
		if response != nil || err == nil || !strings.Contains(err.Error(), "authorized bot_work_update") {
			t.Fatalf("missing tool must remain a blocker: %+v %v", response, err)
		}
	}
	if m.deliverySet {
		t.Fatal("missing tool changed delivery state")
	}
	// An actual report task is not relabeled chat-only by this wrapper.
	m.chatOnly, m.pendingDone = false, true
	for response, err := range m.GenerateContent(t.Context(), request, false) {
		if err != nil || response.Content.Parts[0].FunctionCall != nil || m.deliverySet {
			t.Fatalf("nonconversational run acquired chat-only delivery: %+v %v", response, err)
		}
	}
}

func authorizedHTTPPolicy(_ context.Context, ref store.BotMandateRef) (store.BotMandatePolicy, error) {
	return store.BotMandatePolicy{TTL: 24 * time.Hour, Scopes: []string{string(execution.CapabilityChat), string(execution.CapabilityToolExecute)}, Constraints: []execution.BotActionResourceConstraint{{Action: execution.CapabilityChat, Resources: []string{ref.BotID.String()}}, {Action: execution.CapabilityToolExecute, Resources: []string{"schedule_job", "update_scheduled_job", "remove_scheduled_job", "read_file"}}}, Limits: store.BotMandateLimits{MinScheduleIntervalSeconds: 60, MaxSchedules: 4, MaxDailyRuns: 10, MaxConcurrentRuns: 1, MaxRuntimeSeconds: 300, MaxToolCalls: 20, MaxTokens: 20000, MaxTotalToolCalls: 100, MaxTotalTokens: 100000}}, nil
}

// Each independently checkable stage gets a bounded wall-clock budget. Race
// instrumentation makes SQLite migrations/reopen and the many authorization
// reads much slower; one shared 15s budget let earlier stages starve the final
// scheduled run. Keep real clocks, lease durations and synchronous polls intact.
const authorizedScheduleHTTPStageTimeout = 15 * time.Second

func authorizedScheduleHTTPStage(t *testing.T, parent context.Context, name string) (context.Context, func()) {
	t.Helper()
	ctx, cancel := context.WithTimeout(parent, authorizedScheduleHTTPStageTimeout)
	started := time.Now()
	t.Logf("starting stage %q", name)
	return ctx, func() {
		t.Helper()
		defer cancel()
		t.Logf("stage %q elapsed=%s context=%v", name, time.Since(started), ctx.Err())
		if err := ctx.Err(); err != nil {
			t.Errorf("stage %q: %v", name, err)
		}
	}
}

func TestAuthorizedScheduleHTTPStageBoundsAndIsolation(t *testing.T) {
	parent, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	parent, err := execution.WithPrincipal(parent, execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: uuid.NewString(), OrgSlug: "org", TeamSlug: "team", Authenticated: true})
	if err != nil {
		t.Fatal(err)
	}
	first, finishFirst := authorizedScheduleHTTPStage(t, parent, "first")
	deadline, ok := first.Deadline()
	if !ok || time.Until(deadline) > authorizedScheduleHTTPStageTimeout {
		t.Fatalf("stage lacks bounded deadline: %v %t", deadline, ok)
	}
	finishFirst()
	if first.Err() != context.Canceled || parent.Err() != nil {
		t.Fatalf("finishing stage must cancel only its own context: first=%v parent=%v", first.Err(), parent.Err())
	}
	second, finishSecond := authorizedScheduleHTTPStage(t, parent, "second")
	defer finishSecond()
	if second.Err() != nil {
		t.Fatalf("later stage inherited cancellation: %v", second.Err())
	}
	want, _ := execution.PrincipalFromContext(parent)
	got, ok := execution.PrincipalFromContext(second)
	if !ok || got.Subject != want.Subject || got.OrgSlug != want.OrgSlug || got.TeamSlug != want.TeamSlug {
		t.Fatalf("stage lost authenticated identity: %+v", got)
	}
}

func TestAuthorizedScheduleHTTPStageHonorsParentDeadline(t *testing.T) {
	parent, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	ctx, finish := authorizedScheduleHTTPStage(t, parent, "short parent")
	defer finish()
	want, _ := parent.Deadline()
	got, ok := ctx.Deadline()
	if !ok || !got.Equal(want) {
		t.Fatalf("stage extended parent deadline: got %v want %v", got, want)
	}
}

// A real HTTP owner admission invokes the actual scheduling tool and worker. The
// model and isolated compute are controlled; no operation or run pin is forged.
func TestBotAuthorizedScheduleHTTPAcceptance(t *testing.T) {
	testBotAuthorizedScheduleHTTPAcceptance(t, false, false)
}

func TestBotClarifiedScheduleHTTPAcceptance(t *testing.T) {
	for _, proposal := range []bool{false, true} {
		t.Run(fmt.Sprintf("proposal=%t", proposal), func(t *testing.T) { testBotAuthorizedScheduleHTTPAcceptance(t, true, proposal) })
	}
}

func TestEmployeeSynchronousScheduleHTTPAcceptance(t *testing.T) {
	t.Run("typed_readiness", func(t *testing.T) {
		cause := errors.New("private backend diagnostic")
		err := &botScheduleReadinessError{cause: cause}
		if !errors.Is(err, cause) || !errors.Is(err, store.ErrBotUnsupported) || strings.Contains(err.Error(), cause.Error()) {
			t.Fatal("readiness category leaked or lost private cause")
		}
	})
	for _, scenario := range []string{"readiness_denied", "permission_denied", "cancelled", "original_expiry", "cumulative_tools", "issuer_denied"} {
		t.Run(scenario, func(t *testing.T) {
			testBotAuthorizedScheduleHTTPScenario(t, false, false, true, scenario)
		})
	}
	for _, scenario := range []string{"crash_private_capture", "crash_before_coordinate", "crash_applied_before_presentation", "crash_schedule_committed", "crash_presentation_started", "crash_presentation_settled"} {
		t.Run(scenario, func(t *testing.T) {
			testBotAuthorizedScheduleHTTPScenario(t, false, false, true, scenario)
		})
	}
	for _, proposal := range []bool{false, true} {
		t.Run(fmt.Sprintf("proposal=%t", proposal), func(t *testing.T) { testBotAuthorizedScheduleHTTPAcceptance(t, false, proposal, true) })
	}
	t.Run("progressive_schedule_job", func(t *testing.T) {
		testBotAuthorizedScheduleHTTPScenario(t, false, false, true, "progressive")
	})
}

func testBotAuthorizedScheduleHTTPAcceptance(t *testing.T, clarify, proposal bool, synchronous ...bool) {
	testBotAuthorizedScheduleHTTPScenario(t, clarify, proposal, len(synchronous) > 0 && synchronous[0], "")
}

func authorizedScheduleHTTPRequestContext(auth, request context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(auth, timeout)
	stop := context.AfterFunc(request, cancel)
	return ctx, func() {
		stop()
		cancel()
	}
}

func TestEmployeeScheduleArtifactCrashRecoveryHTTP(t *testing.T) {
	for _, scenario := range []string{"crash_before_coordinate", "crash_applied_before_presentation", "crash_schedule_committed", "crash_presentation_started", "crash_presentation_settled"} {
		t.Run(scenario, func(t *testing.T) { testBotAuthorizedScheduleHTTPScenario(t, false, false, true, scenario+"_artifact") })
	}
}

func TestEmployeeScheduleArtifactChangedJobExpiryHTTP(t *testing.T) {
	for _, boundary := range []string{"crash_applied_before_presentation", "crash_schedule_committed"} {
		for _, mutation := range []string{"next_run_advanced", "owner_disabled_updated"} {
			t.Run(boundary+"/"+mutation, func(t *testing.T) {
				testBotAuthorizedScheduleHTTPScenario(t, false, false, true, boundary+"_"+mutation+"_artifact")
			})
		}
	}
}

func testBotAuthorizedScheduleHTTPScenario(t *testing.T, clarify, proposal, synchronous bool, scenario string, realOptions ...*realScheduleHTTPOptions) {
	artifactFixture := strings.HasSuffix(scenario, "_artifact")
	scenario = strings.TrimSuffix(scenario, "_artifact")
	var jobMutation string
	for _, mutation := range []string{"next_run_advanced", "owner_disabled_updated"} {
		if strings.HasSuffix(scenario, "_"+mutation) {
			jobMutation = mutation
			scenario = strings.TrimSuffix(scenario, "_"+mutation)
		}
	}
	var real *realScheduleHTTPOptions
	if len(realOptions) > 0 {
		real = realOptions[0]
	}
	scopes := []store.BotScope{store.BotScopePersonal, store.BotScopeTeam}
	stage := authorizedScheduleHTTPStage
	if real != nil {
		stage = realScheduleHTTPStage
	}
	for _, scope := range scopes {
		t.Run(string(scope), func(t *testing.T) {
			if real != nil {
				real.model = &realScheduleHTTPModel{LLM: real.model.LLM, t: t}
			}
			ctx, finishStage := authorizedScheduleHTTPStage(t, t.Context(), "provision")
			defer func() { finishStage() }()
			dir := t.TempDir()
			cfg := entstore.Config{DSN: filepath.Join(dir, "platform.db"), DataDir: dir}
			_, db, err := entstore.NewPlatformServices(ctx, cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { db.Close() }()
			org := &store.Organization{ID: uuid.NewString(), Slug: "scheduling", Name: "Scheduling", Status: "active"}
			if err = db.Organizations().Create(ctx, org); err != nil {
				t.Fatal(err)
			}
			if err = db.ProvisionOrg(ctx, org.ID, org.Slug); err != nil {
				t.Fatal(err)
			}
			ods, err := db.ForOrg(org.Slug)
			if err != nil {
				t.Fatal(err)
			}
			team := &store.Team{ID: uuid.NewString(), Slug: "team", Name: "Team", SchemaName: "team_team"}
			if err = ods.Teams().CreateTeam(ctx, team); err != nil {
				t.Fatal(err)
			}
			owner := uuid.New()
			if err = db.Users().Create(ctx, &store.User{ID: owner.String(), Email: owner.String() + "@test.invalid", DisplayName: "Owner", Status: "active"}); err != nil {
				t.Fatal(err)
			}
			if err = db.Organizations().AddMember(ctx, owner.String(), org.ID, "member"); err != nil {
				t.Fatal(err)
			}
			if err = ods.Teams().AddMember(ctx, &store.TeamMembership{UserID: owner.String(), TeamID: team.ID, Role: "member"}); err != nil {
				t.Fatal(err)
			}
			repo := ods.ForTeam(team.Slug).Bots()
			ss := ods.ForTeam(team.Slug).Sessions()
			jobs := ods.ForTeam(team.Slug).ScheduledJobs()
			pin := ods.ForTeam(team.Slug).SetSessionPin
			if scope == store.BotScopePersonal {
				repo = ods.ForUser(owner.String()).Bots()
				ss = ods.ForUser(owner.String()).Sessions()
				jobs = ods.ForUser(owner.String()).ScheduledJobs()
				pin = ods.ForUser(owner.String()).SetSessionPin
			}
			var botIDForClarification uuid.UUID
			var sourceRunForBoundary uuid.UUID
			llm := &authorizedScheduleHTTPModel{schedule: true, direct: scope == store.BotScopePersonal, clarify: clarify, proposal: proposal, t: t, synchronous: synchronous, denyPending: synchronous}
			if scenario == "progressive" {
				llm.direct = false
				scenario = ""
			}
			if synchronous {
				llm.timezone = "UTC"
			}
			if clarify {
				llm.timezone = "America/New_York"
			}
			llm.currentBot = func() (*store.Bot, error) { return repo.Get(ctx, botIDForClarification) }
			registry := execution.NewBotTurnAuthorizationRegistry(10)
			policyConfig := &config.AppConfig{}
			if scenario == "cumulative_tools" {
				limits := config.DefaultBotMandateConfig()
				// Retain historical metadata to prove it no longer gates work.
				limits.MaxToolCalls, limits.MaxTotalToolCalls = 2, 2
				limits.MaxTokens, limits.MaxTotalTokens = 1, 1
				policyConfig.BotMandates = &limits
			}
			if clarify {
				// Tighten only daily admissions, never bypass the production budget.
				// A successful same-run resume must fit the one already charged run.
				limits := config.DefaultBotMandateConfig()
				limits.MaxDailyRuns = 1
				policyConfig.BotMandates = &limits
			}
			policy := func(ctx context.Context, ref store.BotMandateRef) (store.BotMandatePolicy, error) {
				resolved, err := newProductionBotMandatePolicy(db, policyConfig)(ctx, ref)
				if scenario == "original_expiry" || strings.HasPrefix(scenario, "crash_") {
					// Isolated fixture's ORIGINAL persisted authority; never renew it.
					resolved.TTL = 4 * time.Second
					if scenario == "original_expiry" {
						resolved.TTL = 2 * time.Second
					}
				}
				return resolved, err
			}
			backend := newRegressionHomeBackend(t)
			if err := db.InstallBotContentHomeBackend(backend); err != nil {
				t.Fatal(err)
			}
			var abortBoundary context.CancelFunc
			var workerForBoundary *BotWorker
			budgetModel := &cumulativeScheduleHTTPModel{authorizedScheduleHTTPModel: llm}
			workerConfig := BotWorkerConfig{Backend: db, MandatePolicy: policy, TurnRegistry: registry, Sandbox: backend, Pool: workerTestPool{}, WorkerID: "schedule-http", ContextWindow: 8192, ModelOptions: provider.BudgetLLMOptions{MaxOutputTokens: 100, SupportsOutputLimit: true, EstimateInput: func(context.Context, *model.LLMRequest) (int64, error) { return 100, nil }}, ResolveModel: func(c context.Context, _ execution.Principal, _ store.BotScope, _ string, _ string) (model.LLM, error) {
				if scenario == "crash_schedule_committed" {
					current, e := repo.GetRun(c, botIDForClarification, sourceRunForBoundary)
					if e == nil && current.Status.Terminal() {
						rt, err := workerForBoundary.AuthorizedOperations(repo)
						if err != nil {
							return nil, err
						}
						ops, err := rt.ListAuthorizedOperations(c, *current.MandateRef)
						if err == nil && len(ops) == 1 && ops[0].Status == store.BotOperationApplied {
							abortBoundary()
							return nil, context.Canceled
						}
					}
				}
				if scenario == "cumulative_tools" {
					return budgetModel, nil
				}
				if real != nil {
					return real.model, nil
				}
				if identity, ok := execution.BotRunIdentityFromContext(c); ok {
					if run, err := repo.GetRun(c, identity.BotID, identity.RunID); err == nil && run.Trigger == store.TriggerCron {
						return mediateDaemonModel(&workerTestModel{}), nil
					}
				}
				return llm, nil
			}, Prepare: func(c context.Context, q bot.RunRequest, s *launcher.BotChatScope) (*launcher.BotChatSafeResources, error) {
				if synchronous && q.Run.IsInteractiveChat() {
					work, e := repo.(store.BotWorkStore).LoadWork(c, q.Run.Lease)
					if e != nil || work.EmployeeReviewPolicy != store.BotEmployeeReviewEvidenceBoundV1 {
						t.Fatalf("production work missing evidence-bound policy: %+v %v", work, e)
					}
				}
				llm.chatOnly, llm.deliverySet = q.Run.IsInteractiveChat() && !synchronous, false
				llm.work = func() (*store.BotWorkState, error) {
					return repo.(store.BotWorkStore).LoadWork(c, q.Run.Lease)
				}
				s.Services.BotFilesFactory = func(c context.Context, svc *store.Services, scope store.BotScope) (store.BotFiles, error) {
					return productionBotFiles(c, backend, svc, scope)
				}
				return &launcher.BotChatSafeResources{ChatConfig: &config.AppConfig{}, Catalog: &launcher.ChatCatalogResources{Pool: &titleSkillPool{}}, WorkspaceDir: "/workspace", PolicyRuntime: agent.BotPolicyRuntime{Guard: botLiveGuard{repo: repo, request: q}, Classifier: launcher.BotCatalogueClassifier{}}, VerifyIsolation: func(context.Context) error { return nil }, Cleanup: func() {}}, nil
			}}
			if scenario == "permission_denied" || scenario == "issuer_denied" || scenario == "original_expiry" || strings.HasPrefix(scenario, "crash_") {
				// The original claim persists this fixture TTL; no database expiry edits.
				workerConfig.LeaseTTL = 4 * time.Second
			}
			if scenario == "crash_before_coordinate" || scenario == "crash_applied_before_presentation" {
				workerConfig.ObserveExecution = func(timing entstore.BotExecutionTiming) {
					if timing.Stage == entstore.BotStageCommit && timing.Result == entstore.BotStageSucceeded {
						abortBoundary()
					}
				}
			}
			if real != nil {
				workerConfig.ContextWindow = 200000
				workerConfig.ModelOptions = provider.BudgetLLMOptions{MaxOutputTokens: 8192, SupportsOutputLimit: true}
			}
			worker, err := NewBotWorker(workerConfig)
			if err != nil {
				t.Fatal(err)
			}
			workerForBoundary = worker
			expectedLeaseTTL := workerConfig.LeaseTTL
			if expectedLeaseTTL == 0 {
				expectedLeaseTTL = 5 * time.Minute
			}
			if worker.config.LeaseTTL != expectedLeaseTTL {
				t.Fatalf("constructor changed configured/default lease: got=%s want=%s", worker.config.LeaseTTL, expectedLeaseTTL)
			}
			defer func() { worker.Stop() }()
			worker.schedulerReady.Store(true)
			auth, err := execution.WithPrincipal(t.Context(), execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: owner.String(), OrgSlug: org.Slug, TeamSlug: team.Slug, Authenticated: true})
			if err != nil {
				t.Fatal(err)
			}
			auth, err = execution.WithAuthenticationEvidence(auth, execution.AuthenticationEvidence{Method: execution.AuthMethodPlatformJWT, ExpiresAt: time.Now().Add(time.Hour)})
			if err != nil {
				t.Fatal(err)
			}
			// Keep identity/evidence on a stable parent, not a completed stage.
			// Every subsequent store read and worker call uses the new stage context.
			authParent := auth
			nextStage := func(name string) {
				t.Helper()
				finishStage()
				ctx, finishStage = stage(t, authParent, name)
				auth = ctx
			}
			nextStage("source pending acknowledgment and applied receipt")
			v := store.DefaultBotVersion()
			v.Charter = "Read the daily report"
			if real != nil {
				v.Charter = "Deliver a concise useful briefing. The briefing is: Scheduling health is normal; pending actions await verified receipts. When asked to schedule delivery, use exactly one private Chat worker and native progressive discovery of schedule_job (search_tools, describe_tools, execute_tool). Tell that worker to call search_tools first to discover scheduler references, then describe_tools to inspect the returned schedule_job schema, then execute_tool to create the requested schedule once, and return a concise honest pending-capture acknowledgment immediately. Never substitute propose_bot_control, call undeclared tools directly, or launch another worker to check pending state: the synchronous coordinator commits and independently reads back only after the source turn ends. Repeat this briefing on the requested cadence in this Bot Home, UTC. End the source turn after capture without claiming enabled. Only the later tools-disabled Employee presentation may claim enabled after a trusted applied receipt. In that final reply, use the supplied operation status and verified schedule receipt as the current outcome; an applied enabled receipt supersedes the earlier private pending acknowledgment. Confirm the five-minute cadence plainly when the receipt verifies enablement."
			}
			v.Budget.MaxTokens = nil
			b, err := repo.Create(auth, store.BotCreateRequest{Name: "Reporter", OwnerID: owner, Version: v, IdempotencyKey: "draft"})
			if err != nil {
				t.Fatal(err)
			}
			b, err = worker.provisionHome(auth, repo, b.ID, b.Revision)
			if err != nil {
				t.Fatal(err)
			}
			if err = pin(auth, b.HomeSessionID.String(), "controlled", "model"); err != nil {
				t.Fatal(err)
			}
			botIDForClarification = b.ID
			const crashArtifactBody = "# Retained report\nAn illustrative recovery fixture.\n"
			if artifactFixture {
				// Existing Home fixture input; publication still runs through the native
				// tool, live mandate, lease barrier and immutable snapshot receipt.
				path := filepath.Join(backend.documentsRoot, b.ID.String(), "outputs", "crash-report.md")
				if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(crashArtifactBody), 0600); err != nil {
					t.Fatal(err)
				}
				llm.artifactPending, llm.artifactFixture, llm.denyPending = true, true, false
			}
			svc := &store.Services{Bots: repo, Sessions: ss, BotMandatePolicy: policy, BotTurnRegistry: registry, BotInteractiveReady: true}
			if scope == store.BotScopePersonal {
				svc.PersonalBots = repo
				svc.PersonalSessions = ss
			}
			svc.BotFilesFactory = func(c context.Context, services *store.Services, requested store.BotScope) (store.BotFiles, error) {
				return productionBotFiles(c, backend, services, requested)
			}
			httpCtx := store.WithServices(store.WithTenantContext(authParent, &store.TenantContext{UserID: owner.String(), OrgSlug: org.Slug, TeamSlug: team.Slug}), svc)
			router := mux.NewRouter()
			router.HandleFunc("/bots/{botID}/runs/{runID}/cancel", api.BotCancelRunHandler)
			router.HandleFunc("/bots/{botID}/runs/{runID}/reports/{reportSequence}/artifacts/{artifactID}/download", api.BotFileDownloadHandler)
			router.HandleFunc("/bots/{botID}/messages", api.BotMessageHandler)
			router.HandleFunc("/bots/{botID}/approvals/{approvalID}/approve-and-continue", api.BotApproveContinueHandler)
			if real != nil {
				router.HandleFunc("/bots/{botID}/sessions/{sessionID}/stream", api.BotHistoryStreamHandler)
				router.HandleFunc("/bots/{botID}/sessions/{sessionID}/events", api.BotEventsHandler)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				timeout := authorizedScheduleHTTPStageTimeout
				if real != nil {
					timeout = 2 * time.Minute
				}
				requestCtx, cancel := authorizedScheduleHTTPRequestContext(httpCtx, r.Context(), timeout)
				defer cancel()
				router.ServeHTTP(w, r.WithContext(requestCtx))
			}))
			defer server.Close()
			post := func(body string) *http.Response {
				t.Helper()
				request, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/bots/"+b.ID.String()+"/messages?scope="+string(scope), strings.NewReader(body))
				if err != nil {
					t.Fatal(err)
				}
				request.Header.Set("Content-Type", "application/json")
				response, err := server.Client().Do(request)
				if err != nil {
					t.Fatal(err)
				}
				return response
			}
			body := fmt.Sprintf(`{"version":%d,"idempotency_key":"owner-schedule","instructions":"Every day at nine, read the daily report."}`, b.Version)
			if clarify {
				body = fmt.Sprintf(`{"version":%d,"idempotency_key":"owner-schedule","instructions":"Schedule this briefing every day at 9am"}`, b.Version)
			}
			if real != nil {
				body = fmt.Sprintf(`{"version":%d,"idempotency_key":"owner-schedule","instructions":"Schedule to deliver it every 5 minutes"}`, b.Version)
			}
			resp := post(body)
			raw, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			if resp.StatusCode != 202 {
				t.Fatalf("admit %d: %s", resp.StatusCode, raw)
			}
			var source store.BotMessageReceipt
			// Observe the real runtime before ask_user, not a forged waiting run.
			// Quota activity is durable and independent of the in-memory registry.
			quotaActions := func(c context.Context) (reserves, releases int, tools, tokens int64) {
				t.Helper()
				activity, e := repo.ListActivity(c, b.ID, 0, 200)
				if e != nil {
					t.Fatal(e)
				}
				for _, event := range activity {
					var charge struct {
						RunID  string                         `json:"run_id"`
						Action *authorizedScheduleQuotaAction `json:"action"`
						Tools  int64                          `json:"tools"`
						Tokens int64                          `json:"tokens"`
					}
					if json.Unmarshal(event.Payload, &charge) != nil || charge.RunID != source.Run.ID.String() || charge.Action == nil {
						continue
					}
					switch *charge.Action {
					case authorizedScheduleQuotaReserve: // charges one logical daily run
						reserves++
					case authorizedScheduleQuotaRelease: // frees concurrency, not charged usage
						releases++
					}
					tools += charge.Tools
					tokens += charge.Tokens
				}
				return
			}
			questionReserved := false
			if clarify {
				llm.questionCapture = func(c context.Context) {
					reserves, releases, tools, tokens := quotaActions(c)
					if reserves != 1 || releases != 0 || tools < 1 || tokens < 1 {
						t.Fatalf("question did not hold a real charged reservation: reserves=%d releases=%d tools=%d tokens=%d", reserves, releases, tools, tokens)
					}
					questionReserved = true
				}
			}
			if err = json.Unmarshal(raw, &source); err != nil {
				t.Fatal(err)
			}
			finalObserved := false
			if strings.HasPrefix(scenario, "crash_") {
				sourceRunForBoundary = source.Run.ID
				boundaryCtx, cancelBoundary := context.WithCancel(ctx)
				abortBoundary = cancelBoundary
				defer cancelBoundary()
				if scenario == "crash_private_capture" {
					llm.pendingCapture = cancelBoundary
				} else if scenario == "crash_presentation_settled" {
					llm.finalCapture = func(*model.LLMRequest) { finalObserved = true; cancelBoundary() }
				} else if scenario == "crash_presentation_started" {
					llm.finalCapture = func(*model.LLMRequest) {
						finalObserved = true
						if e := db.Organizations().RemoveMember(auth, owner.String(), org.ID); e != nil {
							t.Fatal(e)
						}
						cancelBoundary()
					}
				}
				_ = worker.Poll(boundaryCtx)
				// Provider settlement deliberately ignores cancellation. Revoking live
				// membership at the real generation callback makes that settlement
				// fail, retaining the actual started reservation; restore directory
				// access only after unwinding and closing the original caller grant.
				if scenario == "crash_presentation_started" {
					registry.Close()
					if e := db.Organizations().AddMember(auth, owner.String(), org.ID, "member"); e != nil {
						t.Fatal(e)
					}
				}
				before, e := repo.GetRun(auth, b.ID, source.Run.ID)
				if e != nil || before.MandateRef == nil {
					t.Fatalf("boundary lacks genuine private execution: %+v %v", before, e)
				}
				runtime, e := worker.AuthorizedOperations(repo)
				if e != nil {
					t.Fatal(e)
				}
				operations, e := runtime.ListAuthorizedOperations(auth, *before.MandateRef)
				if e != nil || len(operations) != 1 || operations[0].Provenance == nil || operations[0].Provenance.SourceRunID != source.Run.ID {
					t.Fatalf("boundary lost exact captured source: %+v %v", operations, e)
				}
				wantJobs := 1
				if scenario == "crash_private_capture" {
					wantJobs = 0
					if before.Status != store.RunRunning || operations[0].Status != store.BotOperationPending || operations[0].Receipt != nil {
						t.Fatalf("not before commit: %+v %+v", before, operations)
					}
				} else if scenario == "crash_before_coordinate" || scenario == "crash_applied_before_presentation" {
					if !before.Status.Terminal() || operations[0].Status != store.BotOperationPending || operations[0].Receipt != nil || len(jobs.List(auth)) != 0 {
						t.Fatal("not before coordinator", before, operations)
					}
				} else if !before.Status.Terminal() || !before.Lease.ExpiresAt.IsZero() || operations[0].Status != store.BotOperationApplied || operations[0].Receipt == nil || operations[0].Receipt.Schedule == nil {
					t.Fatalf("not after real schedule commit: %+v %+v", before, operations)
				}
				if scenario != "crash_before_coordinate" && scenario != "crash_applied_before_presentation" && len(jobs.List(auth)) != wantJobs {
					t.Fatal("wrong jobs at boundary")
				}
				claim := store.BotEmployeePresentationRequest{BotID: b.ID, RunID: source.Run.ID, ClaimToken: "employee-final:" + source.Run.ID.String()}
				var beforePresentation *store.BotEmployeePresentation
				if wantJobs == 1 && scenario != "crash_before_coordinate" && scenario != "crash_applied_before_presentation" {
					beforePresentation, e = repo.(store.BotEmployeePresentationStore).ReadEmployeePresentation(auth, claim)
					if e != nil || beforePresentation.ClaimToken != claim.ClaimToken || beforePresentation.RunID != source.Run.ID || len(beforePresentation.Operations) != 1 || beforePresentation.Operations[0].OperationID != operations[0].ID || beforePresentation.Operations[0].Revision != operations[0].Revision {
						t.Fatalf("exact final claim/receipt binding missing: %+v %v", beforePresentation, e)
					}
					operationBytes, e := json.Marshal(operations[0])
					if e != nil || beforePresentation.Operations[0].ReceiptDigest != fmt.Sprintf("%x", sha256.Sum256(operationBytes)) || beforePresentation.ReportDigest != before.Report.EmployeeMessageSourceDigest(before.Status) || beforePresentation.SourceSessionID != before.SessionID || beforePresentation.UserEventID != before.UserEventID || beforePresentation.Provider != before.Provider || beforePresentation.Model != before.Model {
						t.Fatal("presentation not bound to exact receipt/source/model")
					}
					if scenario == "crash_presentation_started" && (!finalObserved || len(beforePresentation.Turns) != 1 || beforePresentation.Turns[0].State != store.BotEmployeeModelTurnStarted) {
						t.Fatalf("missing unsettled paid dispatch: %+v", beforePresentation)
					}
					if scenario == "crash_presentation_settled" && (!finalObserved || len(beforePresentation.Turns) != 1 || beforePresentation.Turns[0].State != store.BotEmployeeModelTurnSettled) {
						t.Fatal("missing settled crash window", beforePresentation)
					}
					if scenario == "crash_schedule_committed" && len(beforePresentation.Turns) != 0 {
						t.Fatal("model ran before pre-presentation boundary")
					}
				}
				if scenario == "crash_applied_before_presentation" {
					coordinator := runtime.(interface {
						CoordinateEmployeeSchedules(context.Context, store.BotMandateRef, uuid.UUID) ([]store.BotOperation, error)
					})
					ops, err := coordinator.CoordinateEmployeeSchedules(auth, *before.MandateRef, before.ID)
					if err != nil || len(ops) != 1 || ops[0].Status != store.BotOperationApplied {
						t.Fatal("applied boundary", ops, err)
					}
					if _, err := repo.(store.BotEmployeePresentationStore).ReadEmployeePresentation(auth, claim); err == nil {
						t.Fatal("presentation existed before boundary")
					}
				}
				var changedJob *store.ScheduledJob
				if jobMutation != "" {
					operations, e = runtime.ListAuthorizedOperations(auth, *before.MandateRef)
					if e != nil || len(operations) != 1 || operations[0].Receipt == nil || operations[0].Receipt.Schedule == nil {
						t.Fatal("changed-job fixture lacks historical applied receipt", operations, e)
					}
					job := jobs.Get(auth, operations[0].Receipt.Schedule.ScheduleID.String())
					if job == nil || job.NextRun == nil {
						t.Fatal("changed-job fixture lacks committed scheduler job")
					}
					if jobMutation == "next_run_advanced" {
						next := job.NextRun.Add(24 * time.Hour)
						job.NextRun = &next
					} else {
						job.Enabled = false
						job.Payload.BotVersion++
					}
					if e := jobs.Update(auth, job); e != nil {
						t.Fatal(e)
					}
					changedJob = jobs.Get(auth, job.ID)
					coordinator := runtime.(interface {
						CoordinateEmployeeSchedules(context.Context, store.BotMandateRef, uuid.UUID) ([]store.BotOperation, error)
					})
					ops, err := coordinator.CoordinateEmployeeSchedules(auth, *before.MandateRef, before.ID)
					if !errors.Is(err, store.ErrBotConflict) || len(ops) != 0 {
						t.Fatal("mutable job mismatch must still fail coordinator verification", ops, err)
					}
				}
				mandates, e := entstore.NewBotMandateRuntime(repo, policy)
				if e != nil {
					t.Fatal(e)
				}
				original, e := mandates.ReadBotMandate(auth, *before.MandateRef)
				if e != nil {
					t.Fatal(e)
				}
				calls := llm.modelCalls
				reservesBefore, releasesBefore, toolsBefore, tokensBefore := quotaActions(auth)
				worker.Stop()
				registry.Close()
				nextStage("scoped SQL crash boundary reopen")
				if e = db.Close(); e != nil {
					t.Fatal(e)
				}
				_, db, e = entstore.NewPlatformServices(ctx, cfg)
				if e != nil {
					t.Fatal(e)
				}
				if e = db.InstallBotContentHomeBackend(backend); e != nil {
					t.Fatal(e)
				}
				ods, e = db.ForOrg(org.Slug)
				if e != nil {
					t.Fatal(e)
				}
				repo, ss, jobs = ods.ForTeam(team.Slug).Bots(), ods.ForTeam(team.Slug).Sessions(), ods.ForTeam(team.Slug).ScheduledJobs()
				if scope == store.BotScopePersonal {
					repo, ss, jobs = ods.ForUser(owner.String()).Bots(), ods.ForUser(owner.String()).Sessions(), ods.ForUser(owner.String()).ScheduledJobs()
				}
				// Route subsequent HTTP reads to the reopened SQL repositories.
				svc.Bots, svc.Sessions = repo, ss
				if scope == store.BotScopePersonal {
					svc.PersonalBots, svc.PersonalSessions = repo, ss
				}
				workerConfig.Backend, workerConfig.TurnRegistry = db, nil
				worker, e = NewBotWorker(workerConfig)
				if e != nil {
					t.Fatal(e)
				}
				worker.schedulerReady.Store(true)
				if wantJobs == 0 {
					wait := time.Until(before.Lease.ExpiresAt) + 20*time.Millisecond
					if wait <= 0 || wait > workerConfig.LeaseTTL+time.Second {
						t.Fatalf("invalid genuine expiry wait: %s", wait)
					}
					timer := time.NewTimer(wait)
					select {
					case <-timer.C:
					case <-ctx.Done():
						timer.Stop()
						t.Fatal(ctx.Err())
					}
				}
				if scenario == "crash_applied_before_presentation" {
					if wait := time.Until(original.Mandate.ExpiresAt) + 20*time.Millisecond; wait > 0 {
						time.Sleep(wait)
					}
				}
				if e = worker.Poll(ctx); e != nil {
					if wantJobs != 1 || !errors.Is(e, store.ErrBotConflict) {
						t.Fatal(e)
					}
					t.Logf("restart publication pending: %v; no automatic final-stage redispatch", e)
				}
				if scenario == "crash_before_coordinate" || scenario == "crash_applied_before_presentation" {
					beforePresentation, e = repo.(store.BotEmployeePresentationStore).ReadEmployeePresentation(auth, claim)
					if e != nil || len(beforePresentation.Turns) != 0 || len(beforePresentation.Operations) != 1 {
						t.Fatal("coordinate recovery did not prepare", beforePresentation, e)
					}
				}
				after, e := repo.GetRun(auth, b.ID, source.Run.ID)
				if e != nil || after.MandateRef == nil || *after.MandateRef != *before.MandateRef || after.Usage != before.Usage || llm.modelCalls != calls || len(jobs.List(auth)) != wantJobs {
					t.Fatalf("restart renewed usage/authority or replayed execution: %+v %v calls=%d/%d", after, e, llm.modelCalls, calls)
				}
				reservesAfter, releasesAfter, toolsAfter, tokensAfter := quotaActions(auth)
				if reservesAfter != reservesBefore || toolsAfter != toolsBefore || tokensAfter != tokensBefore || (wantJobs == 1 && releasesAfter != releasesBefore) {
					t.Fatal("restart recharged admission or reset cumulative quota")
				}
				if wantJobs == 1 && after.Status != before.Status {
					t.Fatal("restart rewrote terminal source status")
				}
				if wantJobs == 0 && after.Status != store.RunInterrupted {
					t.Fatalf("expired private attempt not interrupted: %s", after.Status)
				}
				mandates, e = entstore.NewBotMandateRuntime(repo, policy)
				if e != nil {
					t.Fatal(e)
				}
				reopened, e := mandates.ReadBotMandate(auth, *after.MandateRef)
				if e != nil || !reopened.Mandate.ExpiresAt.Equal(original.Mandate.ExpiresAt) || reopened.Mandate.Limits != original.Mandate.Limits {
					t.Fatalf("restart renewed expiry/limits: %+v %v", reopened, e)
				}
				if wantJobs == 1 {
					p, e := repo.(store.BotEmployeePresentationStore).ReadEmployeePresentation(auth, claim)
					if e != nil || p.ReportDigest != beforePresentation.ReportDigest || p.Operations[0] != beforePresentation.Operations[0] || !p.Deadline.Equal(beforePresentation.Deadline) || !p.AuthorityExpiresAt.Equal(beforePresentation.AuthorityExpiresAt) {
						t.Fatalf("restart replaced immutable presentation: %+v %v", p, e)
					}
					// Explicitly retry the production final entrypoint: an unsettled
					// paid dispatch must fail closed, never call the model again.
					if scenario == "crash_presentation_started" {
						_ = worker.completeEmployeeSchedule(auth, repo, b.ID, source.Run.ID)
						if llm.modelCalls != calls {
							t.Fatal("unsettled final redispatched model")
						}
					}
				}
				if wantJobs == 1 {
					// Original persisted fixture authority; no replacement lease/grant.
					if wait := time.Until(original.Mandate.ExpiresAt) + 20*time.Millisecond; wait > 0 {
						time.Sleep(wait)
					}
					if e = worker.Poll(ctx); e != nil && !errors.Is(e, store.ErrBotConflict) {
						t.Fatalf("expiry Poll: %v source artifacts=%+v work=%+v", e, before.Report.Artifacts, before.Work)
					}
					publication, e := repo.(store.BotPublicationReader).ReadBotPublication(auth, b.ID, source.Run.ID, after.StateSequence)
					if e != nil || publication == nil {
						t.Fatalf("expired terminal publication missing after Poll: %+v %v", publication, e)
					}
					p, e := repo.(store.BotEmployeePresentationStore).ReadEmployeePresentation(auth, claim)
					if e != nil || p.State != store.BotEmployeePresentationUnavailable || (scenario != "crash_before_coordinate" && scenario != "crash_applied_before_presentation" && !reflect.DeepEqual(p.Turns, beforePresentation.Turns)) || llm.modelCalls != calls {
						t.Fatalf("expired terminal presentation did not converge without replay: %+v %v", p, e)
					}
					if artifactFixture {
						if publication.EmployeeSpeech || publication.Text != "" || len(publication.Artifacts) != 1 || !reflect.DeepEqual(publication.Artifacts, before.Report.Artifacts) {
							t.Fatalf("expired recovery lost verified artifact or fabricated speech: %+v source=%+v", publication, before.Report.Artifacts)
						}
						artifact := publication.Artifacts[0]
						if artifact.ContentRef == nil || artifact.ContentRef.Validate() != nil || artifact.ContentRef.SHA256 != fmt.Sprintf("%x", sha256.Sum256([]byte(crashArtifactBody))) {
							t.Fatal("artifact lacks actual immutable receipt", artifact)
						}
						// Remove the mutable input: download must resolve retained immutable bytes.
						if e := os.Remove(filepath.Join(backend.documentsRoot, b.ID.String(), artifact.RelativePath)); e != nil {
							t.Fatal(e)
						}
						request, e := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/bots/%s/runs/%s/reports/%d/artifacts/%s/download?scope=%s", server.URL, b.ID, source.Run.ID, publication.ReportSequence, artifact.ID, scope), nil)
						if e != nil {
							t.Fatal(e)
						}
						response, e := server.Client().Do(request)
						if e != nil {
							t.Fatal(e)
						}
						body, e := io.ReadAll(response.Body)
						response.Body.Close()
						if e != nil || response.StatusCode != http.StatusOK || string(body) != crashArtifactBody {
							t.Fatalf("retained HTTP download after expiry: status=%d body=%q err=%v", response.StatusCode, body, e)
						}
					}
					if jobMutation != "" {
						rt, err := worker.AuthorizedOperations(repo)
						if err != nil {
							t.Fatal(err)
						}
						op, err := rt.ReadAuthorizedOperation(auth, *before.MandateRef, operations[0].ID)
						if err != nil || !reflect.DeepEqual(*op, operations[0]) || !reflect.DeepEqual(jobs.Get(auth, changedJob.ID), changedJob) {
							t.Fatal("expiry recovery rewrote historical receipt or reapplied changed job", op, err)
						}
						current, err := repo.GetRun(auth, b.ID, source.Run.ID)
						if err != nil || current.Usage != before.Usage || current.MandateRef == nil || *current.MandateRef != *before.MandateRef {
							t.Fatal("expiry recovery reset usage or authority", current, err)
						}
						r, releases, tools, tokens := quotaActions(auth)
						if r != reservesBefore || releases != releasesBefore || tools != toolsBefore || tokens != tokensBefore {
							t.Fatal("expiry recovery reset quota or grants")
						}
					}
					publicationAgain, e := repo.(store.BotPublicationStore).PublishReport(auth, store.BotPublicationRequest{BotID: b.ID, RunID: source.Run.ID})
					if e != nil || !reflect.DeepEqual(publication, publicationAgain) {
						t.Fatalf("terminal publication retry changed: %+v %v", publicationAgain, e)
					}
					activities, e := repo.ListActivity(auth, b.ID, 0, 200)
					if e != nil {
						t.Fatal(e)
					}
					published := 0
					for _, activity := range activities {
						var receipt store.BotPublication
						if json.Unmarshal(activity.Payload, &receipt) == nil && receipt.EventID == publication.EventID {
							published++
						}
					}
					if published != 1 {
						t.Fatalf("expected one durable terminal publication, got %d", published)
					}
				}
				events, e := ss.ReadTranscriptEvents(auth, "astonish", owner.String(), b.HomeSessionID.String())
				if e != nil {
					t.Fatal(e)
				}
				for _, event := range events {
					encoded, _ := json.Marshal(event)
					if strings.Contains(string(encoded), "Your daily report is scheduled") || strings.Contains(string(encoded), "The schedule request is pending") {
						t.Fatalf("crashed final fabricated Home speech: %s", encoded)
					}
				}
				t.Logf("%s SQL reopen: exact operation=%s source=%s, jobs=%d, model calls unchanged=%d", scenario, operations[0].ID, after.Status, wantJobs, calls)
				return
			}
			if scenario != "" && scenario != "cumulative_tools" {
				llm.finalText = "I could not enable the requested daily schedule."
				captureObserved := false
				llm.pendingCapture = func() {
					captureObserved = true
					current, e := repo.GetRun(auth, b.ID, source.Run.ID)
					if e != nil || current.MandateRef == nil {
						t.Fatalf("private capture source: %+v %v", current, e)
					}
					runtime, e := worker.AuthorizedOperations(repo)
					if e != nil {
						t.Fatal(e)
					}
					operations, e := runtime.ListAuthorizedOperations(auth, *current.MandateRef)
					if e != nil || len(operations) != 1 || operations[0].Status != store.BotOperationPending || len(jobs.List(auth)) != 0 {
						t.Fatalf("failure boundary lacks real private capture: %+v %v", operations, e)
					}
					switch scenario {
					case "readiness_denied":
						worker.schedulerReady.Store(false)
					case "permission_denied", "issuer_denied":
						if e := db.Organizations().RemoveMember(auth, owner.String(), org.ID); e != nil {
							t.Fatal(e)
						}
					case "cancelled":
						request, e := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/bots/"+b.ID.String()+"/runs/"+source.Run.ID.String()+"/cancel?scope="+string(scope), nil)
						if e != nil {
							t.Fatal(e)
						}
						response, e := server.Client().Do(request)
						if e != nil {
							t.Fatal(e)
						}
						response.Body.Close()
						if response.StatusCode != http.StatusAccepted {
							t.Fatalf("cancel status=%d", response.StatusCode)
						}
					case "original_expiry":
						mandates, e := entstore.NewBotMandateRuntime(repo, policy)
						if e != nil {
							t.Fatal(e)
						}
						original, e := mandates.ReadBotMandate(auth, *current.MandateRef)
						if e != nil || !original.Mandate.ExpiresAt.Before(current.Lease.ExpiresAt) {
							t.Fatalf("authority must expire before the original lease: %+v %v", original, e)
						}
						wait := time.Until(original.Mandate.ExpiresAt) + 20*time.Millisecond
						if wait <= 0 || wait > 2*time.Second+time.Second {
							t.Fatalf("invalid original authority expiry wait: %s", wait)
						}
						timer := time.NewTimer(wait)
						select {
						case <-timer.C:
						case <-ctx.Done():
							timer.Stop()
							t.Fatal(ctx.Err())
						}
						if time.Now().Before(original.Mandate.ExpiresAt) {
							t.Fatal("original mandate authority did not expire naturally")
						}
						afterExpiry, e := mandates.ReadBotMandate(auth, *current.MandateRef)
						if e != nil || !afterExpiry.Mandate.ExpiresAt.Equal(original.Mandate.ExpiresAt) || afterExpiry.Mandate.Limits != original.Mandate.Limits {
							t.Fatalf("waiting renewed original authority: %+v %v", afterExpiry, e)
						}
					}
				}
				llm.finalCapture = func(request *model.LLMRequest) {
					finalObserved = true
					if scenario == "readiness_denied" {
						failed, e := repo.GetRun(auth, b.ID, source.Run.ID)
						if e != nil || failed.Report.EffectState == store.BotReportEffectsUncertain {
							t.Fatalf("readiness scenario was preempted by uncertain source journal: %+v %v", failed, e)
						}
						runtime, e := worker.AuthorizedOperations(repo)
						if e != nil {
							t.Fatal(e)
						}
						operations, e := runtime.ListAuthorizedOperations(auth, *failed.MandateRef)
						if e != nil || len(operations) != 1 || operations[0].Status != store.BotOperationCancelled || operations[0].Receipt == nil || operations[0].Receipt.Reason != "synchronous_application_failed" {
							t.Fatalf("readiness denial lacks durable definitive receipt: %+v %v", operations, e)
						}
					}
					encoded, _ := json.Marshal(request.Contents)
					if strings.Contains(string(encoded), `"enabled":true`) {
						t.Fatalf("failed final received successful facts: %s", encoded)
					}
				}
				_ = worker.Poll(ctx) // Denials are expected; durable readback is the assertion.
				if scenario == "permission_denied" || scenario == "issuer_denied" {
					// Restore directory access only for maintenance readback; discard
					// the original live caller grant first. No execution is reauthorized.
					registry.Close()
					if e := db.Organizations().AddMember(auth, owner.String(), org.ID, "member"); e != nil {
						t.Fatal(e)
					}
				}
				failed, e := repo.GetRun(auth, b.ID, source.Run.ID)
				if scenario == "permission_denied" || scenario == "issuer_denied" || scenario == "original_expiry" {
					if e != nil || failed.Status != store.RunRunning || failed.Lease.ExpiresAt.IsZero() || finalObserved {
						t.Fatalf("denied commit must preserve original running lease without final speech: %+v %v", failed, e)
					}
					deniedLease := failed.Lease
					if e := repo.(interface {
						RecoverRun(context.Context, store.BotLease) error
					}).RecoverRun(auth, deniedLease); !errors.Is(e, store.ErrBotConflict) {
						t.Fatalf("recovery accepted an unexpired authoritative lease: %v", e)
					}
					// Permission revocation occurs before the first lease heartbeat.
					// Expiry work may heartbeat the lease, but never renew its mandate.
					if (scenario == "permission_denied" || scenario == "issuer_denied") && (failed.StartedAt == nil || deniedLease.ExpiresAt.Sub(*failed.StartedAt) > workerConfig.LeaseTTL || deniedLease.ExpiresAt.Sub(*failed.StartedAt) < workerConfig.LeaseTTL-10*time.Millisecond) {
						t.Fatalf("original claim did not persist fixture TTL: start=%v lease=%+v ttl=%s", failed.StartedAt, deniedLease, workerConfig.LeaseTTL)
					}
					deniedRef, deniedUsage, deniedCalls := *failed.MandateRef, failed.Usage, llm.modelCalls
					nextStage("real original lease expiry and terminal recovery")
					wait := time.Until(deniedLease.ExpiresAt) + 20*time.Millisecond
					if wait <= 0 || wait > workerConfig.LeaseTTL+time.Second {
						t.Fatalf("original persisted lease wait out of bounds: %s", wait)
					}
					timer := time.NewTimer(wait)
					select {
					case <-timer.C:
					case <-ctx.Done():
						timer.Stop()
						t.Fatal(ctx.Err())
					}
					if e := repo.(interface {
						RecoverRun(context.Context, store.BotLease) error
					}).RecoverRun(auth, deniedLease); e != nil {
						t.Fatalf("real expired-lease recovery: %v", e)
					}
					failed, e = repo.GetRun(auth, b.ID, source.Run.ID)
					if e != nil || failed.Status != store.RunInterrupted || !failed.Lease.ExpiresAt.IsZero() || failed.MandateRef == nil || *failed.MandateRef != deniedRef || failed.Usage != deniedUsage || llm.modelCalls != deniedCalls || finalObserved {
						t.Fatalf("recovery renewed authority/usage or redispatched model: %+v %v calls=%d/%d", failed, e, llm.modelCalls, deniedCalls)
					}
					t.Logf("original fixture lease=%s expired naturally at %s; premature recovery denied, terminal=%s, same authority/usage and model calls=%d", workerConfig.LeaseTTL, deniedLease.ExpiresAt.Format(time.RFC3339Nano), failed.Status, deniedCalls)
				}
				if scenario != "permission_denied" && scenario != "issuer_denied" && scenario != "original_expiry" && (e != nil || failed.MandateRef == nil || (!failed.Status.Terminal() && failed.Status != store.RunInterrupted) || !failed.Lease.ExpiresAt.IsZero()) {
					t.Fatalf("failed coordinator did not seal source: %+v %v", failed, e)
				}
				if !captureObserved {
					t.Fatal("failure was not exercised after real private capture")
				}
				if scenario == "issuer_denied" && (failed.Status != store.RunInterrupted || llm.pendingReviews != 0) {
					t.Fatalf("revoked issuer did not preserve denial: status=%s reviews=%d", failed.Status, llm.pendingReviews)
				}
				if len(jobs.List(auth)) != 0 {
					t.Fatal("failed request activated a schedule")
				}
				originalRef, originalUsage, originalLease, originalStatus := *failed.MandateRef, failed.Usage, failed.Lease, failed.Status
				originalModelCalls := llm.modelCalls
				mandates, e := entstore.NewBotMandateRuntime(repo, policy)
				if e != nil {
					t.Fatal(e)
				}
				original, e := mandates.ReadBotMandate(auth, originalRef)
				if e != nil {
					t.Fatal(e)
				}
				worker.Stop()
				registry.Close()
				if e := db.Close(); e != nil {
					t.Fatal(e)
				}
				_, db, e = entstore.NewPlatformServices(ctx, cfg)
				if e != nil {
					t.Fatal(e)
				}
				if e := db.InstallBotContentHomeBackend(backend); e != nil {
					t.Fatal(e)
				}
				ods, e = db.ForOrg(org.Slug)
				if e != nil {
					t.Fatal(e)
				}
				repo, ss, jobs = ods.ForTeam(team.Slug).Bots(), ods.ForTeam(team.Slug).Sessions(), ods.ForTeam(team.Slug).ScheduledJobs()
				if scope == store.BotScopePersonal {
					repo, ss, jobs = ods.ForUser(owner.String()).Bots(), ods.ForUser(owner.String()).Sessions(), ods.ForUser(owner.String()).ScheduledJobs()
				}
				workerConfig.Backend, workerConfig.TurnRegistry = db, nil
				worker, e = NewBotWorker(workerConfig)
				if e != nil {
					t.Fatal(e)
				}
				worker.schedulerReady.Store(true)
				if e := worker.Poll(ctx); e != nil {
					t.Fatal(e)
				}
				resumed, e := repo.GetRun(auth, b.ID, source.Run.ID)
				if e != nil || resumed.MandateRef == nil || *resumed.MandateRef != originalRef || resumed.Usage != originalUsage || resumed.Status != originalStatus || resumed.Lease != originalLease || llm.modelCalls != originalModelCalls || len(jobs.List(auth)) != 0 {
					t.Fatalf("restart renewed authority, reset usage or activated late: %+v %v", resumed, e)
				}
				mandates, e = entstore.NewBotMandateRuntime(repo, policy)
				if e != nil {
					t.Fatal(e)
				}
				reopened, e := mandates.ReadBotMandate(auth, originalRef)
				if e != nil || !reopened.Mandate.ExpiresAt.Equal(original.Mandate.ExpiresAt) || reopened.Mandate.Limits != original.Mandate.Limits {
					t.Fatalf("restart extended original expiry/limits: %+v %v", reopened, e)
				}
				events, e := ss.ReadTranscriptEvents(auth, "astonish", owner.String(), b.HomeSessionID.String())
				if e != nil {
					t.Fatal(e)
				}
				for _, event := range events {
					encoded, _ := json.Marshal(event)
					if strings.Contains(string(encoded), "Your daily report is scheduled") || strings.Contains(string(encoded), "The schedule request is pending") {
						t.Fatalf("failure published success or pending worker prose: %s", encoded)
					}
				}
				if scenario == "readiness_denied" && !finalObserved {
					t.Fatal("readiness denial was not returned to tools-disabled Employee")
				}
				return
			}
			if real != nil {
				finishLive, liveErr := realScheduleLiveCapture(ctx, server.Client(), server.URL+"/bots/"+b.ID.String()+"/sessions/"+b.HomeSessionID.String()+"/events?scope="+string(scope))
				if liveErr != nil {
					t.Fatal(liveErr)
				}
				defer finishLive("")
				real.model.beforeFinal = func(request *model.LLMRequest) {
					terminal, e := repo.GetRun(auth, b.ID, source.Run.ID)
					if e != nil || terminal == nil || terminal.Status != store.RunSucceeded || !terminal.Lease.ExpiresAt.IsZero() || terminal.MandateRef == nil {
						t.Fatalf("final before successful source commit: status=%s report=%+v error=%v", terminal.Status, terminal.Report, e)
					}
					runtime, e := worker.AuthorizedOperations(repo)
					if e != nil {
						t.Fatal(e)
					}
					ops, e := runtime.ListAuthorizedOperations(auth, *terminal.MandateRef)
					if e != nil || len(ops) != 1 || ops[0].Status != store.BotOperationApplied || ops[0].Receipt == nil || ops[0].Receipt.Schedule == nil || ops[0].Payload.Schedule == nil || ops[0].Payload.Schedule.Trigger.Cron != "*/5 * * * *" {
						t.Fatal("final lacks exact five-minute applied receipt")
					}
					r := ops[0].Receipt.Schedule
					job := jobs.Get(auth, r.ScheduleID.String())
					if job == nil || !job.Enabled || job.Schedule.Cron != "*/5 * * * *" || job.NextRun == nil || r.NextRun == nil || !job.NextRun.Equal(*r.NextRun) {
						t.Fatal("independent scheduler readback mismatch")
					}
					encoded, _ := json.Marshal(request.Contents)
					if !strings.Contains(string(encoded), ops[0].ID.String()) {
						t.Fatal("final did not receive operation identity")
					}
					var finalFacts struct {
						Operations []store.BotOperation `json:"operations"`
					}
					for _, content := range request.Contents {
						for _, part := range content.Parts {
							if part.Text != "" {
								_ = json.Unmarshal([]byte(part.Text), &finalFacts)
							}
						}
					}
					if len(finalFacts.Operations) != 1 || finalFacts.Operations[0].Status != store.BotOperationApplied || finalFacts.Operations[0].Receipt == nil || finalFacts.Operations[0].Receipt.Schedule == nil {
						t.Fatalf("presentation input omitted applied schedule receipt: operations=%+v", finalFacts.Operations)
					}
					finalObserved = true
				}
				if err = worker.Poll(ctx); err != nil {
					t.Fatal(err)
				}
				saved, e := repo.GetRun(auth, b.ID, source.Run.ID)
				if e != nil || saved == nil || saved.Status != store.RunSucceeded || !finalObserved {
					t.Fatalf("real schedule failed: run=%+v error=%v final=%t", saved, e, finalObserved)
				}
				presentation, e := repo.(store.BotEmployeePresentationStore).ReadEmployeePresentation(auth, store.BotEmployeePresentationRequest{BotID: b.ID, RunID: saved.ID, ClaimToken: "employee-final:" + saved.ID.String()})
				if e != nil || presentation.State != store.BotEmployeePresentationCompleted || presentation.FinalText == "" {
					t.Fatal("missing completed Employee reply")
				}
				if !realScheduleFinalReportsApplied(presentation.FinalText) {
					t.Fatalf("final did not report the applied five-minute schedule: %q", presentation.FinalText)
				}
				getHistory := func() []byte {
					t.Helper()
					response, e := server.Client().Get(server.URL + "/bots/" + b.ID.String() + "/sessions/" + b.HomeSessionID.String() + "/stream?scope=" + string(scope))
					if e != nil {
						t.Fatal(e)
					}
					defer response.Body.Close()
					data, e := io.ReadAll(response.Body)
					if e != nil || response.StatusCode != 200 {
						t.Fatalf("history %d %v", response.StatusCode, e)
					}
					return data
				}
				history := getHistory()
				id := realSchedulePublicationID(t, history, presentation.FinalText)
				live := finishLive(presentation.FinalText)
				if realSchedulePublicationID(t, live, presentation.FinalText) != id {
					t.Fatal("live/history reply mismatch")
				}
				calls := real.model.calls
				duplicate := post(body)
				duplicateRaw, _ := io.ReadAll(duplicate.Body)
				duplicate.Body.Close()
				var receipt store.BotMessageReceipt
				if duplicate.StatusCode != 202 || json.Unmarshal(duplicateRaw, &receipt) != nil || receipt.Run.ID != source.Run.ID {
					t.Fatal("duplicate changed source")
				}
				if err = worker.Poll(ctx); err != nil {
					t.Fatal(err)
				}
				if realSchedulePublicationID(t, getHistory(), presentation.FinalText) != id || real.model.calls != calls || len(jobs.List(auth)) != 1 {
					t.Fatal("reconcile duplicated effects or Employee reply")
				}
				real.model.assert(t)
				t.Logf("REAL ACCEPTANCE cron=*/5 enabled=true next_run independently matched; one Employee reply id=%s after applied receipt; duplicate/reconcile unchanged; calls=%d final=%q; fake Home transport, no physical sandbox", id, calls, presentation.FinalText)
				return
			}
			if llm.synchronous {
				llm.timezone = "UTC"
				llm.finalCapture = func(request *model.LLMRequest) {
					terminal, e := repo.GetRun(auth, b.ID, source.Run.ID)
					if e != nil || !terminal.Status.Terminal() || !terminal.Lease.ExpiresAt.IsZero() {
						t.Fatalf("final before terminal lease release: %+v %v", terminal, e)
					}
					if terminal.Report.Work == nil || terminal.Report.Work.Protocol != store.BotWorkProtocolAgentLedV1 || llm.pendingReviews != 0 {
						t.Fatalf("source invoked redundant model review: work=%+v pending_reviews=%d", terminal.Report.Work, llm.pendingReviews)
					}
					runtime, e := worker.AuthorizedOperations(repo)
					if e != nil {
						t.Fatal(e)
					}
					operations, e := runtime.ListAuthorizedOperations(auth, *terminal.MandateRef)
					if e != nil || len(operations) != 1 || operations[0].Status != store.BotOperationApplied || operations[0].Receipt == nil || operations[0].Receipt.Schedule == nil {
						diagnostic, _ := json.Marshal(terminal.Report)
						t.Fatalf("final lacks real applied receipt: %+v %v source=%s effects=%s report=%s", operations, e, terminal.Status, terminal.Report.EffectState, diagnostic)
					}
					r := operations[0].Receipt.Schedule
					job := jobs.Get(auth, r.ScheduleID.String())
					if job == nil || !job.Enabled || job.NextRun == nil || r.NextRun == nil || !job.NextRun.Equal(*r.NextRun) || r.Version != job.Payload.BotVersion {
						t.Fatalf("independent enabled job mismatch: %+v receipt=%+v", job, r)
					}
					encoded, _ := json.Marshal(request.Contents)
					if !strings.Contains(string(encoded), operations[0].ID.String()) {
						t.Fatalf("final did not receive exact operation: %s", encoded)
					}
					claim := store.BotEmployeePresentationRequest{BotID: b.ID, RunID: terminal.ID, ClaimToken: "employee-final:" + terminal.ID.String()}
					// A concurrent owner poll and losing direct caller must neither
					// abandon this live reservation nor replay its model.
					if e := worker.Poll(ctx); e != nil && !errors.Is(e, store.ErrBotConflict) {
						t.Fatal(e)
					}
					_ = worker.completeEmployeeSchedule(auth, repo, b.ID, terminal.ID)
					live, e := repo.(store.BotEmployeePresentationStore).ReadEmployeePresentation(auth, claim)
					if e != nil || live.State != store.BotEmployeePresentationClaimed || len(live.Turns) != 1 || live.Turns[0].State != store.BotEmployeeModelTurnStarted {
						t.Fatal("poll terminalized live final", live, e)
					}
					finalObserved = true
				}
			}
			if err = worker.Poll(ctx); err != nil {
				t.Fatal(err)
			}
			saved, err := repo.GetRun(auth, b.ID, source.Run.ID)
			if scenario == "cumulative_tools" {
				reserves, releases, chargedTools, chargedTokens := quotaActions(auth)
				if err != nil || saved.Status != store.RunSucceeded || saved.Usage.ToolCalls <= 2 || chargedTools != saved.Usage.ToolCalls || chargedTokens <= 1 || len(budgetModel.verifiedReads) != 3 || reserves != 1 || releases != 1 {
					t.Fatalf("historical execution caps blocked work or accounting was lost: run=%+v error=%v reserves=%d releases=%d charged_tools=%d charged_tokens=%d reads=%d", saved, err, reserves, releases, chargedTools, chargedTokens, len(budgetModel.verifiedReads))
				}
				t.Logf("historical tool cap=2 exceeded: usage=%d charged=%d; historical token cap=1 exceeded: charged=%d; all three real reads verified", saved.Usage.ToolCalls, chargedTools, chargedTokens)
			}
			if clarify {
				if err != nil || saved.Status != store.RunWaitingApproval || saved.MandateRef == nil || len(jobs.List(auth)) != 0 {
					t.Fatalf("not a genuine persisted question before scheduling: %+v %v", saved, err)
				}
				if !questionReserved || saved.Usage.ToolCalls < 2 || saved.Usage.InputTokens+saved.Usage.OutputTokens == 0 || saved.TitleConfirmation != nil {
					t.Fatalf("ordinary timezone question lost real pre-answer usage: reserved=%t run=%+v", questionReserved, saved)
				}
				beforeAnswerUsage := saved.Usage
				reserves, releases, chargedTools, chargedTokens := quotaActions(auth)
				if reserves != 1 || releases != 1 || chargedTools < 2 || chargedTokens == 0 {
					t.Fatalf("question cleanup did not release the real reservation: reserves=%d releases=%d tools=%d tokens=%d", reserves, releases, chargedTools, chargedTokens)
				}
				t.Logf("real question reserved once then released: tools=%d tokens=%d usage=%+v", chargedTools, chargedTokens, beforeAnswerUsage)
				questions, e := repo.ListApprovals(auth, b.ID, store.BotPage{})
				if e != nil || len(questions) != 1 || questions[0].Interaction == nil {
					t.Fatalf("questions: %+v %v", questions, e)
				}
				question := questions[0]
				originalRef := *saved.MandateRef
				mandateRuntime, e := entstore.NewBotMandateRuntime(repo, policy)
				if e != nil {
					t.Fatal(e)
				}
				mandateRecord, e := mandateRuntime.ReadBotMandate(auth, originalRef)
				if e != nil {
					t.Fatal(e)
				}
				t.Logf("original scheduling mandate constraints: %+v", mandateRecord.Mandate.Constraints)
				nextStage("question storage reopen without registry")
				worker.Stop()
				registry.Close()
				if err = db.Close(); err != nil {
					t.Fatal(err)
				}
				_, db, err = entstore.NewPlatformServices(ctx, cfg)
				if err != nil {
					t.Fatal(err)
				}
				if err = db.InstallBotContentHomeBackend(backend); err != nil {
					t.Fatal(err)
				}
				ods, err = db.ForOrg(org.Slug)
				if err != nil {
					t.Fatal(err)
				}
				repo, ss, jobs = ods.ForTeam(team.Slug).Bots(), ods.ForTeam(team.Slug).Sessions(), ods.ForTeam(team.Slug).ScheduledJobs()
				if scope == store.BotScopePersonal {
					repo, ss, jobs = ods.ForUser(owner.String()).Bots(), ods.ForUser(owner.String()).Sessions(), ods.ForUser(owner.String()).ScheduledJobs()
				}
				svc.Bots, svc.Sessions, svc.BotTurnRegistry = repo, ss, nil
				if scope == store.BotScopePersonal {
					svc.PersonalBots, svc.PersonalSessions = repo, ss
				}
				workerConfig.Backend, workerConfig.TurnRegistry = db, nil
				worker, err = NewBotWorker(workerConfig)
				if err != nil {
					t.Fatal(err)
				}
				worker.schedulerReady.Store(true)
				nextStage("single answer and same run continuation")
				answer := func(value string, want int) {
					t.Helper()
					encoded, _ := json.Marshal(map[string]any{"action_digest": question.ActionDigest, "response": store.BotInteractionResponse{QuestionID: question.Interaction.QuestionID, Answer: value}})
					request, e := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/bots/"+b.ID.String()+"/approvals/"+question.ID.String()+"/approve-and-continue?scope="+string(scope), strings.NewReader(string(encoded)))
					if e != nil {
						t.Fatal(e)
					}
					response, e := server.Client().Do(request)
					if e != nil {
						t.Fatal(e)
					}
					data, _ := io.ReadAll(response.Body)
					response.Body.Close()
					if response.StatusCode != want {
						t.Fatalf("answer status=%d want=%d: %s", response.StatusCode, want, data)
					}
				}
				llm.pendingCapture = func() {
					runtime, e := worker.AuthorizedOperations(repo)
					if e != nil {
						t.Fatal(e)
					}
					operations, e := runtime.ListAuthorizedOperations(auth, originalRef)
					if e != nil || len(operations) != 1 || operations[0].Status != store.BotOperationPending || operations[0].Receipt != nil || operations[0].Provenance.SourceRunID != saved.ID || len(jobs.List(auth)) != 0 {
						t.Fatalf("capture claimed application or lost source: operations=%+v err=%v", operations, e)
					}
				}
				answer("America/New_York", http.StatusAccepted)
				answer("America/New_York", http.StatusAccepted)
				answer("UTC", http.StatusConflict)
				queued, e := repo.GetRun(auth, b.ID, saved.ID)
				if e != nil || queued.Status != store.RunQueued || queued.SessionID != source.Run.SessionID || queued.MandateRef == nil || *queued.MandateRef != originalRef {
					t.Fatalf("answer replaced run/session/authority: %+v %v", queued, e)
				}
				for _, sid := range []uuid.UUID{queued.SessionID, b.HomeSessionID} {
					events, e := ss.ReadTranscriptEvents(auth, "astonish", owner.String(), sid.String())
					if e != nil {
						t.Fatal(e)
					}
					echoes := 0
					for _, event := range events {
						if event.ID == "bot-owner-answer:"+question.ID.String() {
							echoes++
						}
					}
					if echoes != 1 {
						t.Fatalf("answer echoes=%d session=%s", echoes, sid)
					}
				}
				// Use the dispatch entrypoint so a startup ErrBotConflict cannot be
				// silently swallowed by Poll's maintenance scan and mistaken for success.
				hint := botDispatchHint{key: botDispatchKey{botID: b.ID, runID: queued.ID}, route: BotSchedulerScope{OrgSlug: org.Slug, TeamSlug: team.Slug, UserID: owner.String()}, scope: scope, sequence: queued.StateSequence, lane: dispatchLane(*queued)}
				if e = worker.executeHint(ctx, hint); e != nil {
					t.Fatalf("released ordinary timezone question cannot resume through worker startup: %v", e)
				}
				saved, err = repo.GetRun(auth, b.ID, source.Run.ID)
				if err != nil || saved.Status != store.RunSucceeded || saved.Usage.ToolCalls <= beforeAnswerUsage.ToolCalls || saved.Usage.InputTokens < beforeAnswerUsage.InputTokens || saved.Usage.OutputTokens < beforeAnswerUsage.OutputTokens || saved.Usage.RuntimeNanos < beforeAnswerUsage.RuntimeNanos {
					t.Fatalf("continuation reset cumulative usage or failed: before=%+v after=%+v err=%v", beforeAnswerUsage, saved, err)
				}
				// Two occupancy reservations are expected, but the production daily
				// ceiling is one: completion proves reacquisition did not recharge it.
				reserves, releases, toolsAfter, tokensAfter := quotaActions(auth)
				if reserves != 2 || releases != 2 || toolsAfter <= chargedTools || tokensAfter <= chargedTokens {
					t.Fatalf("same logical run recharged daily admission or reset charged work: reserves=%d releases=%d tools=%d->%d tokens=%d->%d", reserves, releases, chargedTools, toolsAfter, chargedTokens, tokensAfter)
				}
			}
			if llm.synchronous {
				if err != nil || saved.Status != store.RunSucceeded || !finalObserved {
					t.Fatalf("missing synchronous Employee final: %+v %v observed=%t", saved, err, finalObserved)
				}
				approvals, e := repo.ListApprovals(auth, b.ID, store.BotPage{})
				if e != nil || len(approvals) != 0 {
					t.Fatalf("extra approval: %+v %v", approvals, e)
				}
				events, e := ss.ReadTranscriptEvents(auth, "astonish", owner.String(), b.HomeSessionID.String())
				if e != nil {
					t.Fatal(e)
				}
				replies := 0
				for _, event := range events {
					encoded, _ := json.Marshal(event)
					if strings.Contains(string(encoded), "Your daily report is scheduled at 9am.") {
						replies++
					}
				}
				if replies != 1 || len(jobs.List(auth)) != 1 {
					t.Fatalf("reply/job count: %d/%d", replies, len(jobs.List(auth)))
				}
				duplicate := post(body)
				duplicateRaw, _ := io.ReadAll(duplicate.Body)
				duplicate.Body.Close()
				var duplicateSource store.BotMessageReceipt
				if duplicate.StatusCode != http.StatusAccepted || json.Unmarshal(duplicateRaw, &duplicateSource) != nil || duplicateSource.Run.ID != source.Run.ID {
					t.Fatalf("duplicate changed original source: status=%d body=%s", duplicate.StatusCode, duplicateRaw)
				}
				originalUsage, originalRef := saved.Usage, *saved.MandateRef
				originalCalls, originalStatus := llm.modelCalls, saved.Status
				originalPresentation, e := repo.(store.BotEmployeePresentationStore).ReadEmployeePresentation(auth, store.BotEmployeePresentationRequest{BotID: b.ID, RunID: saved.ID, ClaimToken: "employee-final:" + saved.ID.String()})
				if e != nil || originalPresentation.State != store.BotEmployeePresentationCompleted {
					t.Fatalf("missing completed presentation: %+v %v", originalPresentation, e)
				}
				worker.Stop()
				registry.Close()
				nextStage("completed Employee presentation SQL reopen")
				if e = db.Close(); e != nil {
					t.Fatal(e)
				}
				_, db, e = entstore.NewPlatformServices(ctx, cfg)
				if e != nil {
					t.Fatal(e)
				}
				if e = db.InstallBotContentHomeBackend(backend); e != nil {
					t.Fatal(e)
				}
				ods, e = db.ForOrg(org.Slug)
				if e != nil {
					t.Fatal(e)
				}
				repo, ss, jobs = ods.ForTeam(team.Slug).Bots(), ods.ForTeam(team.Slug).Sessions(), ods.ForTeam(team.Slug).ScheduledJobs()
				if scope == store.BotScopePersonal {
					repo, ss, jobs = ods.ForUser(owner.String()).Bots(), ods.ForUser(owner.String()).Sessions(), ods.ForUser(owner.String()).ScheduledJobs()
				}
				workerConfig.Backend, workerConfig.TurnRegistry = db, nil
				worker, e = NewBotWorker(workerConfig)
				if e != nil {
					t.Fatal(e)
				}
				worker.schedulerReady.Store(true)
				if e := worker.Poll(ctx); e != nil {
					t.Fatal(e)
				}
				restarted, e := repo.GetRun(auth, b.ID, source.Run.ID)
				if e != nil || restarted.MandateRef == nil || *restarted.MandateRef != originalRef || restarted.Usage != originalUsage || restarted.Status != originalStatus || llm.modelCalls != originalCalls {
					t.Fatalf("duplicate/restart reset original source authority or usage: %+v %v", restarted, e)
				}
				restoredPresentation, e := repo.(store.BotEmployeePresentationStore).ReadEmployeePresentation(auth, store.BotEmployeePresentationRequest{BotID: b.ID, RunID: saved.ID, ClaimToken: "employee-final:" + saved.ID.String()})
				if e != nil || restoredPresentation.State != originalPresentation.State || restoredPresentation.FinalTextDigest != originalPresentation.FinalTextDigest || !restoredPresentation.Deadline.Equal(originalPresentation.Deadline) || !restoredPresentation.AuthorityExpiresAt.Equal(originalPresentation.AuthorityExpiresAt) || restoredPresentation.Usage != originalPresentation.Usage || restoredPresentation.Operations[0] != originalPresentation.Operations[0] {
					t.Fatalf("SQL reopen changed completed presentation claim/receipt/budget: %+v %v", restoredPresentation, e)
				}
				replayedEvents, e := ss.ReadTranscriptEvents(auth, "astonish", owner.String(), b.HomeSessionID.String())
				if e != nil {
					t.Fatal(e)
				}
				replayedReplies := 0
				for _, event := range replayedEvents {
					encoded, _ := json.Marshal(event)
					if strings.Contains(string(encoded), "Your daily report is scheduled at 9am.") {
						replayedReplies++
					}
				}
				if replayedReplies != 1 {
					t.Fatalf("duplicate/restart republished Employee final: %d", replayedReplies)
				}
				if len(jobs.List(auth)) != 1 {
					t.Fatal("duplicate activated another job")
				}
				return
			}
			if err != nil || saved.Status != store.RunSucceeded || len(saved.Report.Unverified) != 0 || len(saved.Report.Changed) != 0 || saved.Report.NextRunAt != nil || strings.Contains(saved.Report.Summary, "Done") || !strings.Contains(saved.Report.Summary, "pending") {
				t.Fatalf("source must acknowledge enqueue, not application: %+v %v", saved, err)
			}
			rt, err := worker.AuthorizedOperations(repo)
			if err != nil {
				t.Fatal(err)
			}
			ops, err := rt.ListAuthorizedOperations(auth, *saved.MandateRef)
			if err != nil || len(ops) != 1 {
				t.Fatalf("operations %+v %v", ops, err)
			}
			if ops[0].Status != store.BotOperationApplied || ops[0].Receipt.Schedule.NextRun == nil || !ops[0].Receipt.Schedule.Enabled {
				t.Fatalf("not applied %+v", ops[0])
			}
			wantBookkeeping := 1
			if clarify {
				wantBookkeeping = 2 // Owner clarification and private worker delivery.
			}
			var bookkeeping, pending, serviceReturns int
			for _, evidence := range saved.Report.Unchanged {
				switch {
				case strings.HasPrefix(evidence.Source, "bot-work://"+b.ID.String()+"/"+saved.ID.String()+"/") && (strings.HasSuffix(evidence.Source, "/schedule-chat-delivery") || clarify && strings.HasSuffix(evidence.Source, "/clarification-delivery")):
					bookkeeping++
				case strings.HasPrefix(evidence.Source, "bot-employee-chat://"+saved.ID.String()+"/"):
					serviceReturns++
				case strings.HasPrefix(evidence.Source, "bot-operation://") && strings.HasSuffix(evidence.Source, "/"+ops[0].ID.String()):
					pending++
				default:
					t.Fatalf("unexpected source observation: %+v", evidence)
				}
			}
			if saved.FinishedAt == nil || ops[0].Receipt.ObservedAt.Before(*saved.FinishedAt) || bookkeeping != wantBookkeeping || pending != 1 || serviceReturns != 1 {
				t.Fatalf("applied receipt must follow terminal source with per-attempt bookkeeping and service returns: %+v", saved.Report.Unchanged)
			}
			approvals, err := repo.ListApprovals(auth, b.ID, store.BotPage{})
			if clarify {
				if err != nil || len(approvals) != 1 || approvals[0].Status != store.ApprovalConsumed || approvals[0].Response == nil || approvals[0].Response.Answer != "America/New_York" {
					t.Fatalf("answer not consumed once: %+v %v", approvals, err)
				}
			} else if err != nil || len(approvals) != 0 {
				t.Fatalf("unexpected approvals: %+v %v", approvals, err)
			}
			if len(jobs.List(auth)) != 1 {
				t.Fatal("expected exactly one persisted job")
			}
			currentBot, err := repo.Get(auth, b.ID)
			if err != nil {
				t.Fatal(err)
			}
			currentVersion, err := repo.GetVersion(auth, b.ID, currentBot.Version)
			if err != nil || len(currentVersion.Triggers) != 1 {
				t.Fatalf("missing persisted task schedule: %+v %v", currentVersion, err)
			}
			trigger := currentVersion.Triggers[0]
			if trigger.SkillID != nil || trigger.Invocation != "" || trigger.Instructions != "Read the daily report" {
				t.Fatalf("unbound invocation must not require or invent a skill: %+v", trigger)
			}
			receipt := ops[0].Receipt.Schedule
			if clarify {
				job := jobs.Get(auth, receipt.ScheduleID.String())
				if job == nil || !job.Enabled || job.Schedule.Cron != "0 9 * * *" || job.NextRun == nil || trigger.Timezone != "America/New_York" {
					t.Fatalf("wrong enabled 09:00 schedule: job=%+v trigger=%+v", job, trigger)
				}
				if _, e := rt.ApplyAuthorizedOperation(auth, *saved.MandateRef, ops[0].ID); e != nil {
					t.Fatal(e)
				}
				if err = worker.Poll(ctx); err != nil {
					t.Fatal(err)
				}
				events, e := ss.ReadTranscriptEvents(auth, "astonish", owner.String(), b.HomeSessionID.String())
				if e != nil {
					t.Fatal(e)
				}
				receipts := 0
				for _, event := range events {
					data, _ := json.Marshal(event.Actions.StateDelta[store.BotOperationKey])
					var view store.BotOperationView
					if json.Unmarshal(data, &view) == nil && view.OperationID == ops[0].ID && view.Status == store.BotOperationApplied {
						receipts++
					}
				}
				if receipts != 1 || len(jobs.List(auth)) != 1 {
					t.Fatalf("duplicate worker application: receipts=%d jobs=%d", receipts, len(jobs.List(auth)))
				}
				t.Log("PASS owner HTTP request -> real ask_user -> SQLite reopen/no registry -> one saved answer -> same-run authorized schedule -> exactly one enabled 09:00 job and Home receipt")
				return
			}
			// Compose owner controls over real HTTP, without forged operations or pins.
			ownerTurn := func(text string, payload map[string]any) store.BotOperation {
				t.Helper()
				// Script state belongs to this owner turn, not the shared model's lifetime.
				llm.employeeStarted, llm.deliverySet = false, false
				llm.employeeRequest = text
				llm.control = payload
				llm.controlOnly = payload != nil && payload["operation"] == string(store.BotControlGoalCreate)
				nextStage(text)
				current, e := repo.Get(auth, b.ID)
				if e != nil {
					t.Fatal(e)
				}
				encoded, _ := json.Marshal(map[string]any{"version": current.Version, "idempotency_key": uuid.NewString(), "instructions": text})
				response := post(string(encoded))
				data, _ := io.ReadAll(response.Body)
				response.Body.Close()
				if response.StatusCode != http.StatusAccepted {
					t.Fatalf("control admission %d: %s", response.StatusCode, data)
				}
				var turn store.BotMessageReceipt
				if e = json.Unmarshal(data, &turn); e != nil {
					t.Fatal(e)
				}
				var persisted *store.BotRun
				var operations []store.BotOperation
				for attempt := 0; attempt < 3; attempt++ {
					if e = worker.Poll(ctx); e != nil && !errors.Is(e, store.ErrBotConflict) {
						persisted, _ = repo.GetRun(auth, b.ID, turn.Run.ID)
						t.Fatalf("owner control poll: %v run=%+v", e, persisted)
					}
					persisted, e = repo.GetRun(auth, b.ID, turn.Run.ID)
					if e != nil || persisted == nil || persisted.MandateRef == nil {
						t.Fatalf("missing control mandate: %+v %v", persisted, e)
					}
					operations, e = rt.ListAuthorizedOperations(auth, *persisted.MandateRef)
					if e != nil {
						t.Fatal(e)
					}
					// Poll is synchronous and normally applies the operation before
					// returning. Stop on the durable outcome, rather than charge two
					// extra full maintenance scans to this admission-to-receipt limit.
					if len(operations) == 1 && operations[0].Status == store.BotOperationApplied {
						break
					}
				}
				if len(operations) != 1 || operations[0].Status != store.BotOperationApplied {
					t.Fatalf("control %s: %+v %v run=%+v", text, operations, e, persisted)
				}
				return operations[0]
			}
			llm.cadence = &scheduleCadenceModel{t: t, bridge: scope == store.BotScopeTeam, pending: func(id string) {
				job := jobs.Get(auth, receipt.ScheduleID.String())
				if job == nil || job.Schedule.Cron != "0 9 * * *" {
					t.Fatalf("update applied before source completion: %+v", job)
				}
				runs, e := repo.ListRuns(auth, b.ID, store.BotPage{})
				if e != nil {
					t.Fatal(e)
				}
				var running store.BotRun
				for _, run := range runs {
					if run.Status == store.RunRunning {
						running = run
					}
				}
				if running.ID == uuid.Nil || running.MandateRef == nil {
					t.Fatalf("source not running: %+v", runs)
				}
				op, e := rt.ReadAuthorizedOperation(auth, *running.MandateRef, uuid.MustParse(id))
				if e != nil || op.Status != store.BotOperationPending || op.Receipt != nil {
					t.Fatalf("pending readback: %+v %v", op, e)
				}
			}}
			cadence := ownerTurn("Change the daily report to every two minutes instead.", nil)
			if llm.cadence.step != 3 {
				t.Fatalf("cadence retried: %d", llm.cadence.step)
			}
			llm.cadence = nil
			cadenceRun, err := repo.GetRun(auth, b.ID, cadence.Provenance.SourceRunID)
			if err != nil || cadenceRun.Status != store.RunSucceeded || cadenceRun.Usage.ToolCalls != 3 || len(cadenceRun.Report.Unverified) != 0 || len(cadenceRun.Report.Changed) != 0 || len(cadenceRun.Report.Unchanged) != 4 || !strings.Contains(cadenceRun.Report.Summary, "pending") || cadenceRun.Report.NextRunAt != nil {
				t.Fatalf("cadence source accounting: %+v %v", cadenceRun, err)
			}
			if cadenceRun.Report.Unchanged[0].Source != "bot-employee-chat://"+cadenceRun.ID.String()+"/employee-schedule-worker" || !strings.HasSuffix(cadenceRun.Report.Unchanged[1].Source, "/schedule-chat-delivery") || !strings.Contains(cadenceRun.Report.Unchanged[2].Source, "cadence-list") || cadenceRun.Report.Unchanged[3].Source != "bot-operation://"+b.ID.String()+"/"+cadence.ID.String() || cadence.Receipt.ObservedAt.Before(*cadenceRun.FinishedAt) || cadence.Receipt.Schedule.TriggerID != receipt.TriggerID || cadence.Receipt.Schedule.ScheduleID != receipt.ScheduleID {
				t.Fatalf("cadence lost call/target/source ordering: %+v", cadence)
			}
			jobAfter := jobs.Get(auth, receipt.ScheduleID.String())
			if jobAfter == nil || jobAfter.Schedule.Cron != "*/2 * * * *" || !jobAfter.Enabled || jobAfter.NextRun == nil {
				t.Fatalf("cadence not committed: %+v", jobAfter)
			}
			approvals, err = repo.ListApprovals(auth, b.ID, store.BotPage{})
			if err != nil || len(approvals) != 0 {
				t.Fatalf("cadence requested review: %+v %v", approvals, err)
			}
			llm.schedule = true
			other := ownerTurn("Create another daily report schedule.", nil)
			if other.Receipt.Schedule == nil {
				t.Fatal("missing second schedule")
			}
			ownerTurn("Pause immediately.", map[string]any{"operation": "priority_pause", "reason": "Owner requested pause"})
			for _, job := range jobs.List(auth) {
				if job.Enabled {
					t.Fatal("pause left a schedule enabled")
				}
			}
			ownerTurn("While paused, create a report goal.", map[string]any{"operation": "goal_create", "reason": "Owner requested paused change", "goal": map[string]any{"goal": map[string]any{"title": "Paused report", "description": "Prepare report", "completion_criteria": "Delivered", "status": "active"}}})
			paused, e := repo.Get(auth, b.ID)
			if e != nil || paused.Status != store.BotPaused {
				t.Fatalf("paused change activated bot: %+v %v", paused, e)
			}
			for _, job := range jobs.List(auth) {
				if job.Enabled {
					t.Fatal("paused change enabled schedule")
				}
			}
			llm.pausedRead = &pausedScheduleReadModel{t: t, inspected: func() {
				current, e := repo.Get(auth, b.ID)
				if e != nil || current.Status != store.BotPaused {
					t.Fatalf("inspection resumed bot: %+v %v", current, e)
				}
				for _, job := range jobs.List(auth) {
					if job.Enabled || job.NextRun != nil {
						t.Fatalf("inspection enabled work: %+v", job)
					}
				}
			}}
			resumed := ownerTurn("Inspect the paused schedules and resume only the first report schedule.", nil)
			if llm.pausedRead.step != 3 || llm.pausedRead.target != receipt.ScheduleID.String() {
				t.Fatalf("wrong inspected resume target: %+v", llm.pausedRead)
			}
			llm.pausedRead = nil
			readRun, e := repo.GetRun(auth, b.ID, resumed.Provenance.SourceRunID)
			if e != nil || readRun.Status != store.RunSucceeded || readRun.Usage.ToolCalls != 3 || len(readRun.Report.Unverified) != 0 || len(readRun.Report.Changed) != 0 || len(readRun.Report.Unchanged) != 3 || readRun.Report.NextRunAt != nil {
				t.Fatalf("paused read accounting: %+v %v", readRun, e)
			}
			// Both the list and independently verified pending capture are observations,
			// not mutations. The worker's later receipt establishes application.
			if !strings.HasSuffix(readRun.Report.Unchanged[0].Source, "/schedule-chat-delivery") {
				t.Fatalf("missing exact delivery declaration: %+v", readRun.Report.Unchanged)
			}
			listObservation, captureObservation := readRun.Report.Unchanged[1], readRun.Report.Unchanged[2]
			if listObservation.Kind != store.BotEvidenceObservation || !strings.HasSuffix(listObservation.Source, "/"+readRun.ID.String()+"/paused-list") {
				t.Fatalf("lost exact-call list evidence: %+v", listObservation)
			}
			if captureObservation.Kind != store.BotEvidenceObservation || captureObservation.Source != "bot-operation://"+b.ID.String()+"/"+resumed.ID.String() || !strings.Contains(captureObservation.Summary, "pending, not applied") || !strings.Contains(readRun.Report.Summary, "pending, not applied") {
				t.Fatalf("pending resume claimed application or lost operation evidence: %+v", readRun.Report)
			}
			mandates, e := entstore.NewBotMandateRuntime(repo, policy)
			if e != nil {
				t.Fatal(e)
			}
			record, e := mandates.ReadBotMandate(auth, *readRun.MandateRef)
			if e != nil {
				t.Fatal(e)
			}
			assertPausedReadMandate(t, record.Mandate)
			if job := jobs.Get(auth, receipt.ScheduleID.String()); job == nil || !job.Enabled {
				t.Fatal("target schedule not resumed")
			}
			if job := jobs.Get(auth, other.Receipt.Schedule.ScheduleID.String()); job == nil || job.Enabled {
				t.Fatal("unselected schedule resumed")
			}
			nextStage("offline reopen")
			worker.Stop()
			registry.Close()
			if err = db.Close(); err != nil {
				t.Fatal(err)
			}
			_, db, err = entstore.NewPlatformServices(ctx, cfg)
			if err != nil {
				t.Fatal(err)
			}
			if err := db.InstallBotContentHomeBackend(backend); err != nil {
				t.Fatal(err)
			}
			ods, err = db.ForOrg(org.Slug)
			if err != nil {
				t.Fatal(err)
			}
			repo = ods.ForTeam(team.Slug).Bots()
			ss = ods.ForTeam(team.Slug).Sessions()
			jobs = ods.ForTeam(team.Slug).ScheduledJobs()
			if scope == store.BotScopePersonal {
				repo = ods.ForUser(owner.String()).Bots()
				ss = ods.ForUser(owner.String()).Sessions()
				jobs = ods.ForUser(owner.String()).ScheduledJobs()
			}
			workerConfig.Backend = db
			workerConfig.TurnRegistry = nil
			worker, err = NewBotWorker(workerConfig)
			if err != nil {
				t.Fatal(err)
			}
			worker.schedulerReady.Store(true)
			job := jobs.Get(auth, receipt.ScheduleID.String())
			if job == nil || !job.Enabled || job.NextRun == nil {
				t.Fatal("lost enabled schedule after reopen")
			}
			nextStage("scheduled delivery and Home receipt")
			due := time.Now().UTC().Add(-time.Second)
			job.NextRun = &due
			if err = jobs.Update(auth, job); err != nil {
				t.Fatal(err)
			}
			executor := &scheduler.Executor{}
			mts := NewMultiTenantScheduler(db, executor, nil, nil)
			mts.SetBotSchedulerCallbacks(worker.SchedulerCallbacks())
			if scope == store.BotScopePersonal {
				mts.executePersonalJob(ctx, job, org.Slug, ods, ods.ForUser(owner.String()), jobs, owner.String())
			} else {
				mts.executeTeamJob(ctx, job, org.Slug, team.Slug, ods, ods.ForTeam(team.Slug), jobs)
			}
			runs, err := repo.ListRuns(auth, b.ID, store.BotPage{})
			if err != nil {
				t.Fatal(err)
			}
			var admitted struct{ Run store.BotRun }
			for _, run := range runs {
				if run.Trigger == store.TriggerCron {
					admitted.Run = run
				}
			}
			if admitted.Run.ID == uuid.Nil {
				t.Fatalf("scheduler did not admit occurrence: %+v", jobs.Get(auth, job.ID))
			}
			if admitted.Run.MandateRef == nil || *admitted.Run.MandateRef == *saved.MandateRef {
				t.Fatal("no exact future task pin")
			}
			taskRuntime, e := entstore.NewBotMandateRuntime(repo, policy)
			if e != nil {
				t.Fatal(e)
			}
			taskRecord, e := taskRuntime.ReadBotMandate(auth, *admitted.Run.MandateRef)
			if e != nil {
				t.Fatal(e)
			}
			for _, c := range taskRecord.Mandate.Constraints {
				for _, resource := range c.Resources {
					if resource == "propose_bot_control" || resource == "schedule_job" || strings.HasPrefix(resource, "bot_control:") {
						t.Fatalf("scheduled child inherited own-control %s", resource)
					}
				}
			}
			if err = worker.Poll(ctx); err != nil && !errors.Is(err, store.ErrBotConflict) {
				t.Fatal(err)
			}
			result, err := repo.GetRun(auth, b.ID, admitted.Run.ID)
			if err != nil || result.Status != store.RunSucceeded {
				t.Fatalf("future run %+v %v", result, err)
			}
			if result.Report.Summary != "durable controlled result" || result.Report.Quality != nil || result.Report.Work == nil || result.Report.Work.Protocol != store.BotWorkProtocolAgentLedV1 || result.Report.Work.Phase != store.BotWorkFinished || result.Report.Work.Disposition != store.AgentWorkAnswered {
				t.Fatalf("scheduled result lacks committed agent-led content: %+v", result.Report)
			}
			events, err := ss.ReadTranscriptEvents(auth, "astonish", owner.String(), b.HomeSessionID.String())
			if err != nil {
				t.Fatal(err)
			}
			if result.Report.ValidateEmployeeMessageIntegrity(result.Status) != nil {
				t.Fatal("scheduled result has no validated employee message")
			}
			scheduleReport, runReport := false, false
			canonicalSchedule := false
			scheduleAt, runAt, runReports := -1, -1, 0
			for eventIndex, e := range events {
				raw, _ := json.Marshal(e.Actions.StateDelta[store.BotOperationKey])
				var view store.BotOperationView
				if json.Unmarshal(raw, &view) == nil && view.Operation == store.BotControlScheduleCreate && view.Status == store.BotOperationApplied {
					canonicalSchedule = true
					if view.OperationID == ops[0].ID {
						scheduleAt = eventIndex
					}
				}
				if e.Content != nil {
					for _, p := range e.Content.Parts {
						scheduleReport = scheduleReport || strings.Contains(p.Text, "Schedule schedule_create applied")
						if strings.TrimSpace(p.Text) == strings.TrimSpace(bot.RenderReport(*result)) {
							runReport = true
							runAt = eventIndex
							runReports++
						}
					}
				}
			}
			if scheduleReport || !canonicalSchedule || !runReport || scheduleAt < 0 || runAt <= scheduleAt || runReports != 1 {
				t.Fatalf("Home must confirm the original schedule before exactly one scheduled result: schedule=%t run=%t scheduleAt=%d runAt=%d reports=%d", scheduleReport, runReport, scheduleAt, runAt, runReports)
			}
		})
	}
}
