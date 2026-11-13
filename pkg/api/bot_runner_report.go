package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"iter"
	"path"
	"strings"
	"sync"

	"github.com/SAP/astonish/pkg/agent"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/scheduler"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

// botAgentReadJournal is attempt-local. It recognizes only calls classified by
// the trusted runtime with their original arguments before tool execution. This
// is source journaling, not authority to execute or proof of an external effect.
// Authorization remains at the existing tool boundary.
type botAgentReadJournalKey struct{}
type botAgentReadCall struct {
	name string
	read bool
}
type botAgentReadJournal struct {
	mu    sync.Mutex
	calls map[string]botAgentReadCall
	seen  map[string]bool
}

func (j *botAgentReadJournal) capture(ctx context.Context, call *genai.FunctionCall) {
	if call.ID == "" {
		call.ID = uuid.NewString()
	}
	entry := botAgentReadCall{name: call.Name}
	args := call.Args
	if args == nil {
		args = map[string]any{}
	}
	raw, err := json.Marshal(args)
	if classifier := agent.BotPolicyRuntimeFromContext(ctx).Classifier; err == nil && classifier != nil {
		original := execution.ActionDescriptor{Tool: call.Name, Arguments: raw, Effect: execution.EffectUnknown}
		classified, classifyErr := classifier.DescribeAction(ctx, call.Name, append(json.RawMessage(nil), raw...))
		// As at the authorization boundary, classifiers may add constraints but
		// cannot substitute an executable identity or different arguments.
		candidate := original
		candidate.Tool, candidate.Arguments = classified.Tool, classified.Arguments
		want, e1 := original.Digest()
		got, e2 := candidate.Digest()
		entry.read = classifyErr == nil && e1 == nil && e2 == nil && want == got && classified.Effect == execution.EffectRead && classified.ConstraintsVerified
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.calls == nil {
		j.calls = make(map[string]botAgentReadCall)
		j.seen = make(map[string]bool)
	}
	if j.seen[call.ID] {
		entry.read = false
	}
	j.seen[call.ID] = true
	j.calls[call.ID] = entry
}

func (j *botAgentReadJournal) take(response *genai.FunctionResponse) bool {
	if j == nil {
		return false
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	entry, ok := j.calls[response.ID]
	delete(j.calls, response.ID)
	return ok && entry.read && entry.name == response.Name
}

type botAgentReadModel struct {
	model.LLM
	journal *botAgentReadJournal
}

func (m *botAgentReadModel) GenerateContent(ctx context.Context, req *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		if service, ok := bot.BotWorkServicesFromContext(ctx).(*botAgentWorkService); ok {
			service.mu.Lock()
			_, err := service.load(ctx)
			service.mu.Unlock()
			if err != nil {
				yield(nil, err)
				return
			}
		}
		for response, err := range m.LLM.GenerateContent(ctx, req, stream) {
			if err == nil && response != nil && !response.Partial && response.Content != nil {
				for _, part := range response.Content.Parts {
					if part != nil && part.FunctionCall != nil {
						m.journal.capture(ctx, part.FunctionCall)
					}
				}
			}
			if !yield(response, err) {
				return
			}
		}
	}
}

// Called under completionMu. Only current tool responses consume the server
// ledger; neither model text nor serialized result maps can mint attestations.
// The return value permits only a bound source-access failure to remain tool
// feedback in agent-led work; generic control and native errors still win.
func (cr *ChatRunner) observeBotToolOutcome(role string, response *genai.FunctionResponse) (recoverableReadFailure bool) {
	if _, ok := execution.BotRunIdentityFromContext(cr.ctx); !ok || role == "model" {
		return
	}
	cr.completion.toolResponses++
	// Legacy review requires captured bodies in its candidate. Agent-led work
	// keeps original responses in the durable scoped transcript and addresses
	// them by reference; copying them here would impose a second aggregate cap.
	_, agentLed := cr.ctx.Value(botAgentReadJournalKey{}).(*botAgentReadJournal)
	if raw, err := json.Marshal(response.Response); !agentLed && err == nil {
		if len(raw) > store.BotWorkMaxCandidateBytes {
			cr.completion.Report.Unverified = append(cr.completion.Report.Unverified, "Required tool response content exceeds assessment limit.")
		} else {
			if cr.completion.reportBodies == nil {
				cr.completion.reportBodies = make(map[string]string)
			}
			cr.completion.reportBodies["tool-response:"+response.ID] = string(raw)
		}
	}
	outcome, ok := bot.TakeToolOutcome(cr.ctx, response.ID)
	if service, enabled := bot.BotWorkServicesFromContext(cr.ctx).(*botAgentWorkService); enabled && ok && len(outcome.SourceAccess) > 0 {
		service.captureAccess(response.ID, outcome.SourceAccess)
	}
	journal, _ := cr.ctx.Value(botAgentReadJournalKey{}).(*botAgentReadJournal)
	read := journal.take(response)
	if service, enabled := bot.BotWorkServicesFromContext(cr.ctx).(*botAgentWorkService); enabled && read && (!ok || len(outcome.SourceAccess) == 0) {
		service.captureAccess(response.ID, nil) // bind the journal reference even without HTTP facts
	}
	if capture, _ := cr.ctx.Value(botApprovalCaptureKey{}).(*botApprovalCapture); capture != nil {
		// Consume only identity-bound, guard-captured nonexecution. An existing
		// ledger outcome (especially a failed effect) always remains authoritative.
		if capture.takeNonexecution(response.ID, response.Name) && !ok {
			return
		}
	}
	if agentLed && ok && read && len(outcome.SourceAccess) > 0 && len(outcome.Changed) == 0 && len(outcome.Artifacts) == 0 && outcome.RoutineCapture == nil && outcome.ScheduleCapture == nil && outcome.NextRunAt == nil {
		// Source access failure is an observation, not an irreversible veto on
		// an answer recovered using another source. The journal retains the
		// complete negative response and the typed access fact remains bound.
		return cr.ctx.Err() == nil && botBoundSourceAccessFailure(response, outcome.SourceAccess)
	}
	if !ok && read {
		// The actual source response is already journaled above. A read has no
		// external effect to attest; do not promote its JSON to observation,
		// artifact, schedule, or send receipts. Explicit failed receipts still win.
		return
	}
	if !ok || !outcome.Verified {
		cr.completion.Report.Unverified = append(cr.completion.Report.Unverified, fmt.Sprintf("Tool %s returned without independently verified outcome metadata.", response.Name))
		if ok {
			cr.completion.Report.Unverified = append(cr.completion.Report.Unverified, outcome.Unverified...)
		}
		return
	}
	report := &cr.completion.Report
	report.Unverified = append(report.Unverified, outcome.Unverified...)
	if capture := outcome.RoutineCapture; capture != nil {
		identity, _ := execution.BotRunIdentityFromContext(cr.ctx)
		operationID, _ := response.Response["operation_id"].(string)
		status := fmt.Sprint(response.Response["status"])
		decision := fmt.Sprint(response.Response["decision"])
		if !capture.ValidFor(identity) || operationID != capture.OperationID.String() || status != string(store.BotOperationPending) || decision != string(store.BotOperationExecute) || outcome.ScheduleCapture != nil || len(outcome.Changed) != 0 || len(outcome.Artifacts) != 0 || outcome.NextRunAt != nil {
			report.Unverified = append(report.Unverified, "Routine enqueue receipt did not match the current run and pending tool response.")
			return
		}
		cr.completion.pendingControls = append(cr.completion.pendingControls, botWorkPendingControl{OperationID: capture.OperationID.String(), Operation: capture.Operation, Acknowledgment: capture.Acknowledgment(), Payload: json.RawMessage(capture.PayloadJSON)})
		report.Unchanged = append(report.Unchanged, capture.Observation())
		if report.Summary != "" {
			report.Summary += "\n\n"
		}
		report.Summary += capture.Acknowledgment()
		return
	}
	if capture := outcome.ScheduleCapture; capture != nil {
		identity, _ := execution.BotRunIdentityFromContext(cr.ctx)
		operationID, _ := response.Response["operation_id"].(string)
		status, _ := response.Response["status"].(string)
		if !capture.ValidFor(identity) || operationID != capture.OperationID.String() || status != string(store.BotOperationPending) || len(outcome.Changed) != 0 || len(outcome.Artifacts) != 0 || outcome.NextRunAt != nil {
			report.Unverified = append(report.Unverified, "Schedule enqueue receipt did not match the current run and pending tool response.")
			return
		}
		// This observation is of a pending record, not a changed schedule. The
		// server acknowledgment, not premature model 'Done', owns the summary.
		cr.completion.pendingControls = append(cr.completion.pendingControls, botWorkPendingControl{OperationID: capture.OperationID.String(), Operation: capture.Operation, Acknowledgment: capture.Acknowledgment(), Payload: json.RawMessage(capture.PayloadJSON)})
		report.Unchanged = append(report.Unchanged, capture.Observation())
		if report.Summary != "" {
			report.Summary += "\n\n"
		}
		report.Summary += capture.Acknowledgment()
		return
	}
	appendObservations := func(dst *[]store.BotEvidence, observations []store.BotEvidence) {
		for _, e := range observations {
			if !bot.ValidObservation(e) {
				report.Unverified = append(report.Unverified, "Tool outcome contained incomplete observation evidence.")
				continue
			}
			duplicate := false
			for _, prior := range *dst {
				if prior == e {
					duplicate = true
					break
				}
			}
			if !duplicate {
				*dst = append(*dst, e)
			}
		}
	}
	appendObservations(&report.Changed, outcome.Changed)
	appendObservations(&report.Unchanged, outcome.Unchanged)
	for _, artifact := range outcome.Artifacts {
		a := artifact.Artifact
		digest := sha256.Sum256([]byte(artifact.Body))
		if a.ID == uuid.Nil || a.HomeID == uuid.Nil || a.RelativePath == "" || path.IsAbs(a.RelativePath) || path.Clean(a.RelativePath) != a.RelativePath || a.RelativePath == ".." || strings.HasPrefix(a.RelativePath, "../") || strings.Contains(a.RelativePath, "\\") || a.SHA256 != hex.EncodeToString(digest[:]) {
			report.Unverified = append(report.Unverified, "Tool report artifact metadata or digest could not be verified.")
			continue
		}
		report.Artifacts = append(report.Artifacts, a)
		if cr.completion.reportBodies == nil {
			cr.completion.reportBodies = make(map[string]string)
		}
		cr.completion.reportBodies[a.RelativePath] = artifact.Body
	}
	if outcome.NextRunAt != nil && !outcome.NextRunAt.IsZero() {
		next := outcome.NextRunAt.UTC()
		report.NextRunAt = &next
	}
	return
}

// Only server ledger facts bound to this exact response can make a failed read
// recoverable. A remote result map, stale receipt, or mixed access batch cannot.
func botBoundSourceAccessFailure(response *genai.FunctionResponse, facts []store.BotSourceAccessFact) bool {
	raw, err := json.Marshal(response.Response)
	if err != nil || response.ID == "" || len(facts) == 0 {
		return false
	}
	digest := sha256.Sum256(raw)
	bodyDigest := hex.EncodeToString(digest[:])
	for _, fact := range facts {
		if fact.Status != store.BotSourceAccessFailed || fact.CallID != response.ID || fact.EvidenceRef != "capture:"+response.ID || fact.BodyDigest != bodyDigest || fact.ObservedAt.IsZero() || strings.TrimSpace(fact.OriginalURL) == "" {
			return false
		}
	}
	return true
}

func botCompletionReport(completion ChatCompletion, req bot.RunRequest) store.BotReport {
	report := completion.Report
	// Do not retain artifacts (or use their bodies) from another durable home.
	artifacts := report.Artifacts
	report.Artifacts = nil
	bodies := make(map[string]string)
	for _, a := range artifacts {
		if a.HomeID != req.Bot.Home.ID || a.HomeID == uuid.Nil {
			report.Unverified = append(report.Unverified, "Report artifact does not belong to this Bot home.")
			continue
		}
		report.Artifacts = append(report.Artifacts, a)
		bodies[a.RelativePath] = completion.reportBodies[a.RelativePath]
	}
	if req.Work != nil && req.Work.Protocol == store.BotWorkProtocolAgentLedV1 {
		// The final answer and pending-operation receipt are distinct. Do not
		// replace independently useful work with the enqueue acknowledgment.
		final := scheduler.ResolveAdaptiveReportBody(completion.FinalText, bodies)
		if len(completion.pendingControls) > 0 {
			ack := botPendingAcknowledgment(completion.pendingControls)
			if strings.TrimSpace(final) == "" || strings.TrimSpace(final) == strings.TrimSpace(ack) {
				report.Summary = ack
			} else {
				report.Summary = final + "\n\n" + ack
			}
		} else {
			report.Summary = final
		}
	} else if report.Summary == "" {
		report.Summary = scheduler.ResolveAdaptiveReportBody(completion.FinalText, bodies)
	}
	if completion.Usage.ToolCalls > completion.toolResponses {
		report.Unverified = append(report.Unverified, "Some tool calls have no observed response.")
	}
	return report
}
