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
	"github.com/SAP/astonish/pkg/common"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/scheduler"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/entstore"
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
	identity execution.BotRunIdentity
	digest   string
	name     string // transport identity, including execute_tool
	concrete string // validated target captured from invocation, never result JSON
	read     bool
	source   bool // eligible source read, excluding work/skill bookkeeping
	bridge   bool
}
type botAgentReadJournal struct {
	mu                      sync.Mutex
	calls                   map[string]botAgentReadCall
	seen                    map[string]bool
	completed               map[string]bool
	confirmed               bool
	uncertain               bool
	constrainedTextComplete bool
}

type botProgressPendingRead struct {
	Identity execution.BotRunIdentity `json:"identity"`
	Digest   string                   `json:"digest"`
	Name     string                   `json:"name"`
	Concrete string                   `json:"concrete"`
	Read     bool                     `json:"read"`
	Source   bool                     `json:"source"`
	Bridge   bool                     `json:"bridge"`
}

type botProgressReadJournal struct {
	Seen      map[string]bool                   `json:"seen"`
	Completed map[string]bool                   `json:"completed"`
	Pending   map[string]botProgressPendingRead `json:"pending,omitempty"`
	Confirmed bool                              `json:"confirmed"`
	Uncertain bool                              `json:"uncertain"`
}

func (j *botAgentReadJournal) progressSnapshot() *botProgressReadJournal {
	if j == nil {
		return nil
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	// Preserve bounded in-flight invocation bindings across a progress checkpoint.
	// The response may arrive after restore and must still settle the exact
	// classified read. Historical checkpoints without Pending remain fail-closed
	// through their persisted Uncertain bit.
	s := &botProgressReadJournal{Seen: map[string]bool{}, Completed: map[string]bool{}, Pending: map[string]botProgressPendingRead{}, Confirmed: j.confirmed, Uncertain: j.uncertain}
	for k, v := range j.seen {
		s.Seen[k] = v
	}
	for k, v := range j.completed {
		s.Completed[k] = v
	}
	for k, v := range j.calls {
		s.Pending[k] = botProgressPendingRead{Identity: v.identity, Digest: v.digest, Name: v.name, Concrete: v.concrete, Read: v.read, Source: v.source, Bridge: v.bridge}
	}
	return s
}

func (j *botAgentReadJournal) restoreProgress(s *botProgressReadJournal) {
	if s == nil {
		return
	}
	j.calls = map[string]botAgentReadCall{}
	for k, v := range s.Pending {
		j.calls[k] = botAgentReadCall{identity: v.Identity, digest: v.Digest, name: v.Name, concrete: v.Concrete, read: v.Read, source: v.Source, bridge: v.Bridge}
	}
	j.seen, j.completed = s.Seen, s.Completed
	j.confirmed, j.uncertain = s.Confirmed, s.Uncertain
}

func (j *botAgentReadJournal) capture(ctx context.Context, call *genai.FunctionCall) {
	if call.ID == "" {
		call.ID = uuid.NewString()
	}
	entry := botAgentReadCall{name: call.Name, bridge: call.Name == common.ExecuteToolName}
	name, args := call.Name, call.Args
	valid := true
	if entry.bridge {
		// Decode the invocation, not outcome claims in model arguments. Preserve
		// transport identity for response matching and duplicate detection.
		var err error
		name, args, err = common.NormalizeExecuteToolArgs(call.Args)
		valid = err == nil && name != "" && name != common.ExecuteToolName && name != common.SearchToolsName && name != common.DescribeToolsName
	}
	if valid {
		entry.concrete = name
		entry.source = name != "" && name != common.SearchToolsName && name != common.DescribeToolsName && !strings.HasPrefix(name, "bot_skill_") && !strings.HasPrefix(name, "bot_work_")
	}
	if args == nil {
		args = map[string]any{}
	}
	raw, err := json.Marshal(args)
	entry.identity, _ = execution.BotRunIdentityFromContext(ctx)
	if valid && err == nil {
		entry.digest, _ = (execution.ActionDescriptor{Tool: name, Arguments: raw, Effect: execution.EffectUnknown}).Digest()
	}
	if classifier := agent.BotPolicyRuntimeFromContext(ctx).Classifier; valid && err == nil && classifier != nil {
		original := execution.ActionDescriptor{Tool: name, Arguments: raw, Effect: execution.EffectUnknown}
		classified, classifyErr := classifier.DescribeAction(ctx, name, append(json.RawMessage(nil), raw...))
		// As at the authorization boundary, classifiers may add constraints but
		// cannot substitute an executable identity or different arguments.
		candidate := original
		candidate.Tool, candidate.Arguments = classified.Tool, classified.Arguments
		want, e1 := original.Digest()
		got, e2 := candidate.Digest()
		entry.read = classifyErr == nil && e1 == nil && e2 == nil && want == got && classified.Effect == execution.EffectRead && classified.ConstraintsVerified
	}
	// Sealed local catalog discovery performs metadata lookup only. It may settle
	// effect accounting, but never qualifies as factual source evidence. The
	// progressive bridge forbids wrapping these identities, so only native calls
	// can reach this path.
	if valid && !entry.bridge && (name == common.SearchToolsName || name == common.DescribeToolsName) {
		entry.read = true
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.calls == nil {
		j.calls = make(map[string]botAgentReadCall)
		j.seen = make(map[string]bool)
		j.completed = make(map[string]bool)
	}
	if j.seen[call.ID] {
		entry.read = false
		entry.source = false
		j.uncertain = true
	}
	j.seen[call.ID] = true
	j.calls[call.ID] = entry
}

func (s *botAgentWorkService) captureReadReference(callID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.resources == nil || s.resources.EvidenceReader == nil {
		return
	}
	if s.accessPending == nil {
		s.accessPending = map[string][]store.BotSourceAccessFact{}
	}
	if _, exists := s.accessPending[callID]; exists {
		return
	}
	if callID == "" || len(s.accessPending) >= 200 {
		s.accessErr = store.ErrBotInvalid
		return
	}
	// A nil fact slice is intentional: flushAccess still verifies and binds the
	// exact durable transcript reference without inventing HTTP or truth facts.
	s.accessPending[callID] = nil
}

func (j *botAgentReadJournal) take(response *genai.FunctionResponse, receipts ...bot.ToolOutcome) (read, source bool) {
	if j == nil {
		return false, false
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	entry, ok := j.calls[response.ID]
	delete(j.calls, response.ID)
	read = ok && entry.read && entry.name == response.Name
	if entry.bridge {
		// A bridge error may precede resolution or authorization. It cannot
		// attest execution of the requested read, regardless of model claims.
		read = read && common.ToolResultSucceeded(response.Response, nil)
	}
	noEffect := false
	verifiedRead := false
	confirmedEffect := false
	if len(receipts) == 1 {
		o := receipts[0]
		r := o.Nonexecution
		noEffect = ok && entry.name == response.Name && response.ID != "" && r.ValidFor(entry.identity, response.ID, entry.concrete, entry.digest) && o.Verified && !o.ReceiptConflict && len(o.Changed) == 0 && len(o.Unchanged) == 0 && len(o.Unverified) == 0 && len(o.SourceAccess) == 0 && o.SourceRead == nil && len(o.Artifacts) == 0 && o.RoutineCapture == nil && o.ScheduleCapture == nil && o.NextRunAt == nil
		boundSourceRead := ok && entry.name == response.Name && o.SourceRead.ValidFor(entry.identity, response.ID, entry.concrete, entry.digest, response.Response) && !o.ReceiptConflict && r == nil && len(o.SourceAccess) == 1 && len(o.Changed) == 0 && len(o.Unverified) == 0 && len(o.Artifacts) == 0 && o.RoutineCapture == nil && o.ScheduleCapture == nil && o.NextRunAt == nil
		if boundSourceRead {
			fact := o.SourceAccess[0]
			boundSourceRead = fact.CallID == response.ID && fact.EvidenceRef == "capture:"+response.ID && fact.BodyDigest == o.SourceRead.ResponseDigest && !fact.ObservedAt.IsZero() && strings.TrimSpace(fact.OriginalURL) != "" && (fact.Status == store.BotSourceAccessFailed || fact.Status == store.BotSourceAccessReturned)
			for _, observation := range o.Unchanged {
				boundSourceRead = boundSourceRead && bot.ValidObservation(observation)
			}
		}
		if boundSourceRead {
			read = true
		} else if o.SourceRead != nil {
			j.uncertain = true
		}
		// A bare Verified flag is not positive effect evidence. Internal Task
		// bookkeeping supplies actual independently observed Home facts; require
		// every observation to validate before it can complete a non-read call.
		observedUnchanged := len(o.Unchanged) > 0
		for _, observation := range o.Unchanged {
			observedUnchanged = observedUnchanged && bot.ValidObservation(observation)
		}
		verifiedRead = observedUnchanged && ok && entry.name == response.Name && response.ID != "" && r == nil && o.Verified && !o.ReceiptConflict && len(o.Unverified) == 0 && len(o.Changed) == 0 && len(o.Artifacts) == 0 && o.RoutineCapture == nil && o.ScheduleCapture == nil && o.NextRunAt == nil
		confirmedEffect = ok && !entry.read && entry.name == response.Name && response.ID != "" && r == nil && o.Verified && !o.ReceiptConflict && len(o.Unverified) == 0 && (len(o.Changed) > 0 || len(o.Artifacts) > 0 || o.RoutineCapture != nil || o.ScheduleCapture != nil || o.NextRunAt != nil)
	}
	if verifiedRead {
		read = true
	}
	if confirmedEffect {
		j.confirmed = true
	}
	if (!read && !noEffect && !confirmedEffect) || (len(receipts) == 1 && receipts[0].Nonexecution != nil && !noEffect) || j.completed[response.ID] {
		j.uncertain = true
	}
	if j.completed == nil {
		j.completed = make(map[string]bool)
	}
	// Carry source eligibility out under the same lock that consumes identity.
	// A prior uncertain call does not suppress a later, independently bound read.
	source = read && entry.source && !j.completed[response.ID] && (len(receipts) == 0 || (len(receipts) == 1 && receipts[0].Nonexecution == nil && !receipts[0].ReceiptConflict))
	j.completed[response.ID] = true
	return read, source
}

// effectState requires complete invocation-bound receipts, not absence of changed
// or unverified prose. Ordinary empty attempts have no classified execution
// evidence; only trusted constrained final text can establish nonexecution.
func (j *botAgentReadJournal) effectState() store.BotReportEffectState {
	if j == nil {
		return store.BotReportEffectsUnknown
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.uncertain || len(j.calls) != 0 || len(j.completed) != len(j.seen) {
		return store.BotReportEffectsUncertain
	}
	if len(j.seen) == 0 && !j.constrainedTextComplete {
		return store.BotReportEffectsUnknown
	}
	if j.confirmed {
		return store.BotReportEffectsConfirmed
	}
	return store.BotReportEffectsReadOnly
}

// completeConstrainedText is only for the validated read-only research path,
// never an inference from missing receipts on an ordinary attempt.
func (j *botAgentReadJournal) completeConstrainedText(c ChatCompletion) {
	if j == nil || c.Err != nil || c.Blocked || !c.Final || c.FinalEventID == "" || strings.TrimSpace(c.FinalText) == "" || c.Usage.ToolCalls != 0 || c.toolResponses != 0 || len(c.pendingControls) != 0 || len(c.reportBodies) != 0 || len(c.Report.Changed) != 0 || len(c.Report.Unverified) != 0 || len(c.Report.Artifacts) != 0 || len(c.Report.NeedsApproval) != 0 || c.Report.NextRunAt != nil || c.Report.EffectState != store.BotReportEffectsUnknown {
		return
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if !j.uncertain && len(j.calls) == 0 && len(j.seen) == 0 && len(j.completed) == 0 {
		j.constrainedTextComplete = true
	}
}

func (j *botAgentReadJournal) observeEffect(outcome bot.ToolOutcome, captured bool) {
	if j == nil || !captured {
		return
	}
	// A conflicting runtime receipt always beats a read classification. Read
	// access failures carry Unverified diagnostics but do not imply a send.
	if outcome.ReceiptConflict || (!outcome.Verified && (len(outcome.Changed) != 0 || len(outcome.Artifacts) != 0 || outcome.RoutineCapture != nil || outcome.ScheduleCapture != nil || outcome.NextRunAt != nil)) {
		j.mu.Lock()
		j.uncertain = true
		j.mu.Unlock()
	}
}

type botAgentReadModel struct {
	model.LLM
	journal *botAgentReadJournal
}

func (m *botAgentReadModel) SupportsModelPreflight() bool {
	next, ok := m.LLM.(interface{ SupportsModelPreflight() bool })
	return ok && next.SupportsModelPreflight()
}

func (m *botAgentReadModel) GenerateContent(parent context.Context, req *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		// Each producer invocation owns fresh bytes, never a tool/previous-call scope.
		ctx, endReads := entstore.WithBotContentReadPreflight(parent)
		end := sync.OnceFunc(func() { endReads(); entstore.EndImmutableReadWindow(parent); execution.EndModelPreflight(parent) })
		defer end()
		ctx = execution.WithModelPreflightEnd(ctx, end)
		originalYield := yield
		yield = func(r *model.LLMResponse, err error) bool { end(); return originalYield(r, err) }
		if service, ok := bot.BotWorkServicesFromContext(ctx).(*botAgentWorkService); ok {
			err := func() error {
				service.mu.Lock()
				defer service.mu.Unlock()
				_, err := service.load(ctx)
				return err
			}()
			if err != nil {
				yield(nil, err)
				return
			}
		}
		if !m.SupportsModelPreflight() {
			end()
		}
		for response, err := range m.LLM.GenerateContent(ctx, req, stream) {
			end() // No response processing/classifier may borrow preflight bytes.
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
type botToolObservation struct {
	SourceEligible    bool
	KnownNonexecution bool
	Recoverable       bool
}

func (cr *ChatRunner) observeBotToolOutcome(role string, response *genai.FunctionResponse) (observation botToolObservation) {
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
	var invocation botAgentReadCall
	var boundInvocation bool
	if journal != nil {
		journal.mu.Lock()
		invocation, boundInvocation = journal.calls[response.ID]
		journal.mu.Unlock()
	}
	read, source := journal.take(response, outcome)
	observation.SourceEligible = source
	if boundInvocation && invocation.name == response.Name && outcome.Nonexecution.ValidFor(invocation.identity, response.ID, invocation.concrete, invocation.digest) && outcome.Verified && !outcome.ReceiptConflict && len(outcome.Changed) == 0 && len(outcome.Unchanged) == 0 && len(outcome.Unverified) == 0 && len(outcome.Artifacts) == 0 && len(outcome.SourceAccess) == 0 && outcome.SourceRead == nil && outcome.RoutineCapture == nil && outcome.ScheduleCapture == nil && outcome.NextRunAt == nil {
		observation.KnownNonexecution = true
		observation.Recoverable = cr.ctx.Err() == nil && (outcome.Nonexecution.Disposition == bot.ToolFeedbackCorrectCall || outcome.Nonexecution.Disposition == bot.ToolFeedbackUnresolvedTarget)
	}
	journal.observeEffect(outcome, ok)
	cr.completion.Report.EffectState = journal.effectState()
	if service, enabled := bot.BotWorkServicesFromContext(cr.ctx).(*botAgentWorkService); enabled && source && (!ok || (len(outcome.SourceAccess) == 0 && outcome.SourceRead == nil && outcome.Nonexecution == nil)) {
		service.captureReadReference(response.ID) // bind even when a local read has no HTTP facts
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
		observation.Recoverable = cr.ctx.Err() == nil && botBoundSourceAccessFailure(response, outcome.SourceAccess)
		return
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

// botToolFeedbackError preserves the original diagnostic and native error identity.
// Only non-native execution feedback can be recovered; policy, cancellation and
// budget callback errors, model errors and runner/sink errors remain terminal.
// It never grants effect authority or makes a tool result successful.
type botToolFeedbackError struct {
	cause       error
	recoverable bool
}

func (e *botToolFeedbackError) Error() string { return e.cause.Error() }
func (e *botToolFeedbackError) Unwrap() error { return e.cause }

func botOnlyRecoverableToolFeedback(err error) bool {
	if feedback, ok := err.(*botToolFeedbackError); ok {
		return feedback.recoverable
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		causes := joined.Unwrap()
		if len(causes) == 0 {
			return false
		}
		for _, cause := range causes {
			if !botOnlyRecoverableToolFeedback(cause) {
				return false
			}
		}
		return true
	}
	// Diagnostic context (for example "chat runner: %w") must not erase the
	// typed feedback boundary. Still require every joined cause to qualify;
	// native, policy, budget, persistence and cancellation errors fail closed.
	if wrapped, ok := err.(interface{ Unwrap() error }); ok {
		return botOnlyRecoverableToolFeedback(wrapped.Unwrap())
	}
	return false
}

func botCanPromoteStaleBlockedReadOnlyFinal(c ChatCompletion, req bot.RunRequest, report store.BotReport) bool {
	return req.Work != nil && req.Work.Protocol == store.BotWorkProtocolAgentLedV1 &&
		c.FinalEventID != "" && strings.TrimSpace(c.FinalText) != "" && len(c.pendingControls) == 0 &&
		(c.Err == nil || botOnlyRecoverableToolFeedback(c.Err)) && store.BotReportIsReadOnlyResult(report)
}

// botCanRetainReadOnlyFinal allows the employee's completed communication, not
// a retry or a claim of full success. The existing candidate/commit boundaries
// still own final publication. Unknown effects and absent final output fail shut.
func botCanRetainReadOnlyFinal(c ChatCompletion, req bot.RunRequest, report store.BotReport) bool {
	return req.Work != nil && req.Work.Protocol == store.BotWorkProtocolAgentLedV1 &&
		!c.Blocked && c.Final && c.FinalEventID != "" && strings.TrimSpace(c.FinalText) != "" &&
		c.Usage.ToolCalls > 0 && c.Usage.ToolCalls == c.toolResponses && len(c.pendingControls) == 0 &&
		botOnlyRecoverableToolFeedback(c.Err) && store.BotReportIsReadOnlyResult(report)
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
		final := completion.FinalText
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
		report.EffectState = store.BotReportEffectsUncertain
		report.Unverified = append(report.Unverified, "Some tool calls have no observed response.")
	}
	return report
}
