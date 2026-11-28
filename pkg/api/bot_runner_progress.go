package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/entstore"
	"github.com/google/uuid"
	"google.golang.org/adk/session"
)

// observeContext reads only this factory-bound session and fenced work state.
// The model sees excerpts, while the fingerprint retains its original ID-only
// semantics. No model-written notes, token usage or evaluation count is progress.
func (p *botProgressRuntime) observeContext(ctx context.Context, resources *BotChatResources, s session.Session) (bot.ProgressObservation, error) {
	o := bot.ProgressObservation{Assignment: p.req.Instructions, Settled: true}
	work := p.req.Work
	if p.work != nil {
		lease := p.req.Run.Lease
		if p.req.CurrentLease != nil {
			lease = p.req.CurrentLease()
		}
		var err error
		work, err = p.work.LoadWork(ctx, lease)
		if err != nil {
			return o, err
		}
	}
	if work != nil {
		if work.Validate() != nil || work.Contract.BotID != p.req.Bot.ID || work.Contract.RunID != p.req.Run.ID || work.Contract.Version != p.req.Run.Version {
			return o, store.ErrBotInvalid
		}
		o.Assignment = work.Contract.Instructions
	}
	if confirmation := p.req.Run.TitleConfirmation; confirmation != nil {
		if confirmation.ValidateBinding(p.req.Run, p.req.Bot.OwnerID) != nil || confirmation.State != store.BotTitleConfirmationConfirmed {
			return o, store.ErrBotInvalid
		}
		o.Assignment = confirmation.ConfirmedAssignment
	}
	redact := func(text string) string {
		if resources.Redact != nil {
			return resources.Redact(text)
		}
		return text
	}
	o.Assignment, _ = botProgressExcerptText(redact(o.Assignment), 8<<10)
	remaining := 32 << 10 // encoded context bytes; leave room for assignment, refs and checkpoint metadata
	add := func(kind bot.ProgressContextKind, ref, tool, text string, limit int) {
		if text == "" || remaining < 256 {
			return
		}
		text, truncated := botProgressExcerptText(redact(text), min(limit, remaining-192))
		excerpt := bot.ProgressContextExcerpt{Kind: kind, Ref: ref, Tool: tool, Text: text, Truncated: truncated}
		raw, err := json.Marshal(excerpt)
		if err == nil && len(raw) <= remaining {
			o.Context = append(o.Context, excerpt)
			remaining -= len(raw) + 1
		}
	}
	persona, _ := json.Marshal(p.req.Version.Persona)
	add(bot.ProgressContextPersona, "", "", string(persona), 3<<10)
	add(bot.ProgressContextOwner, "", "", p.req.Version.Charter, 2<<10)
	if work != nil && work.AgentWork != nil {
		// Notes and incomplete obligations are judgments, not source evidence.
		notes, _ := json.Marshal(struct {
			Notes     string                    `json:"notes"`
			Items     []store.BotWorkItem       `json:"items,omitempty"`
			Remaining []store.BotWorkObligation `json:"remaining,omitempty"`
		}{work.AgentWork.Notes, work.AgentWork.Items, work.AgentWork.Remaining})
		add(bot.ProgressContextWorking, "", "", string(notes), 4<<10)
	}
	// Prioritize recent owner answers separately so large sources cannot crowd
	// them out. Only literal user text is included, never thought/model prose.
	owners := 0
	for i := s.Events().Len() - 1; i >= 0 && owners < 4; i-- {
		e := s.Events().At(i)
		if e == nil || e.Partial || e.Content == nil || e.Author != "user" {
			continue
		}
		for _, part := range e.Content.Parts {
			if part != nil && !part.Thought && part.Text != "" && owners < 4 {
				add(bot.ProgressContextOwner, "", "", part.Text, 1<<10)
				owners++
			}
		}
	}
	// Retain the last 64 response references exactly as before, but only marshal
	// bounded recent response bodies. Arbitrary refs never trigger filesystem or
	// network reads. Failures remain literal tool data, not inferred blockers.
	type responseExcerpt struct {
		ref, tool string
		response  map[string]any
	}
	var responses []responseExcerpt
	for i := 0; i < s.Events().Len(); i++ {
		e := s.Events().At(i)
		if e == nil || e.Partial || e.Content == nil {
			continue
		}
		for _, part := range e.Content.Parts {
			if part == nil || part.Thought || part.FunctionResponse == nil {
				continue
			}
			r := part.FunctionResponse
			if r.ID == "" || len(r.ID) > 512 || len(r.Name) > 256 {
				continue
			}
			responses = append(responses, responseExcerpt{"tool-response:" + r.ID, r.Name, r.Response})
			if len(responses) > 64 {
				responses = responses[1:]
			}
		}
	}
	for _, r := range responses {
		o.EvidenceRefs = append(o.EvidenceRefs, r.ref)
	}
	// Fingerprint the same last 64 valid IDs even if escaped reference strings
	// are too large to send. Selection for the model must not change measured
	// progress, and excerpts must only cite references actually supplied.
	raw, _ := json.Marshal(o.EvidenceRefs)
	o.Fingerprint = store.BotProgressContextDigest(raw)
	for len(raw) > 8<<10 && len(responses) > 0 {
		responses = responses[1:]
		o.EvidenceRefs = o.EvidenceRefs[1:]
		raw, _ = json.Marshal(o.EvidenceRefs)
	}
	for i := len(responses) - 1; i >= 0 && i >= len(responses)-16; i-- {
		r := responses[i]
		raw, err := json.Marshal(r.response)
		if err != nil {
			add(bot.ProgressContextTool, r.ref, r.tool, "Tool response could not be represented as JSON; contents unavailable.", 512)
			continue
		}
		add(bot.ProgressContextTool, r.ref, r.tool, string(raw), 2<<10)
	}
	// Drop lowest-priority excerpts if aggregate metadata and escaping consume
	// the remaining space; never change the immutable reference fingerprint.
	for len(o.Context) > 0 {
		encoded, err := json.Marshal(o)
		if err == nil && len(encoded) <= 48<<10 {
			break
		}
		o.Context = o.Context[:len(o.Context)-1]
	}
	return o, nil
}

// Bound the JSON-encoded string rather than rune count: escaping source markup,
// control bytes and quotes must not overflow the existing 64 KiB context cap.
func botProgressExcerptText(text string, limit int) (string, bool) {
	text = strings.ToValidUTF8(text, "�")
	raw, _ := json.Marshal(text)
	if len(raw) <= limit {
		return text, false
	}
	const suffix = " [truncated]"
	lo, hi := 0, min(len(text), limit)
	for lo < hi {
		mid := (lo + hi + 1) / 2
		raw, _ = json.Marshal(text[:mid] + suffix)
		if len(raw) <= limit {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	for lo > 0 && !utf8.ValidString(text[:lo]) {
		lo--
	}
	return text[:lo] + suffix, true
}

var errBotProgressIntegrationUnavailable = errors.New("durable progress runtime integration unavailable: trusted dispatch and context restoration required")

func botProgressActivationGuard(req bot.RunRequest) error {
	// Title clarification is bounded ask_user-only work, before an assignment
	// exists. It uses its ordinary approval checkpoint, not a progress journal.
	// Keep the saved policy intact so confirmed execution uses durable progress.
	if req.Run.TitleConfirmation != nil && req.Run.TitleConfirmation.State == store.BotTitleConfirmationNeedsQuestion {
		if req.Progress != nil || req.Version.Budget.RuntimePolicy.Validate() != nil {
			return store.ErrBotInvalid
		}
		return nil
	}
	if bot.ProgressExecutionEnabled(req) {
		if req.Version.Budget.RuntimePolicy.Validate() != nil || req.ProgressStore == nil || req.Work == nil {
			return errBotProgressIntegrationUnavailable
		}
		if _, ok := req.ProgressStore.(store.BotProgressContextStore); !ok {
			return errBotProgressIntegrationUnavailable
		}
		return nil
	}
	if req.Progress != nil {
		return store.ErrBotInvalid
	}
	return nil
}

func botHasProgressCheckpoint(req bot.RunRequest) bool {
	if req.Progress != nil {
		return true
	}
	var header struct {
		Kind       store.BotProgressCheckpointKind `json:"kind"`
		Checkpoint *struct {
			Kind store.BotProgressCheckpointKind `json:"kind"`
		} `json:"checkpoint"`
	}
	if json.Unmarshal(req.Run.Checkpoint, &header) != nil {
		return false
	}
	return header.Kind == store.BotProgressCheckpointV1 || header.Checkpoint != nil && header.Checkpoint.Kind == store.BotProgressCheckpointV1
}

// botProgressRuntime serializes checkpoint and dispatch mutations. The scoped
// repository remains authoritative for pending operations, fences and work CAS.
type botProgressRuntime struct {
	mu              sync.Mutex
	req             bot.RunRequest
	state           *store.BotProgressState
	cursor          int64
	pending         map[uuid.UUID]bool
	unsafe          bool
	work            store.BotWorkStore
	restoredJournal *botProgressReadJournal
}

func (p *botProgressRuntime) mutation(ctx context.Context) (store.BotProgressMutation, error) {
	lease := p.req.Run.Lease
	if p.req.CurrentLease != nil {
		lease = p.req.CurrentLease()
	}
	work := p.req.Work
	if p.work != nil {
		var err error
		work, err = p.work.LoadWork(ctx, lease)
		if err != nil {
			return store.BotProgressMutation{}, err
		}
	}
	if work == nil {
		return store.BotProgressMutation{}, store.ErrBotUnsupported
	}
	m := store.BotProgressMutation{Lease: lease, Binding: store.BotProgressBinding{BotID: p.req.Bot.ID, RunID: p.req.Run.ID, SessionID: p.req.Run.SessionID, Version: p.req.Run.Version, Task: p.req.Run.Task.Clone(), WorkRevision: work.Revision}}
	if p.state != nil {
		m.ExpectedRevision = p.state.Checkpoint.Revision
	}
	return m, nil
}
func (p *botProgressRuntime) Begin(ctx context.Context, kind store.BotProgressDispatchKind, digest string, usage store.BotUsage) (*store.BotProgressDispatchReceipt, error) {
	ctx, end := entstore.WithBotContentReadOperationIfAbsent(ctx)
	defer end()
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.unsafe {
		return nil, bot.ErrProgressBoundaryUnsafe
	}
	m, err := p.mutation(ctx)
	if err != nil {
		return nil, err
	}
	receipt, err := p.req.ProgressStore.BeginProgressDispatch(ctx, store.BotProgressBeginDispatchRequest{Mutation: m, DispatchID: uuid.New(), Kind: kind, OperationDigest: digest, ReservedUsage: usage})
	if err == nil {
		p.pending[receipt.DispatchID] = true
		p.cursor = receipt.LedgerCursor
	}
	return receipt, err
}
func (p *botProgressRuntime) Settle(ctx context.Context, id uuid.UUID, outcome store.BotProgressDispatchOutcome, usage store.BotUsage, evidence string) error {
	ctx, end := entstore.WithBotContentReadOperationIfAbsent(ctx)
	defer end()
	p.mu.Lock()
	defer p.mu.Unlock()
	m, err := p.mutation(ctx)
	if err != nil {
		return err
	}
	receipt, err := p.req.ProgressStore.SettleProgressDispatch(ctx, store.BotProgressSettleDispatchRequest{Mutation: m, DispatchID: id, Outcome: outcome, Usage: usage, EvidenceRef: evidence})
	if err != nil {
		p.unsafe = true
		return err
	}
	delete(p.pending, id)
	p.cursor = receipt.LedgerCursor
	if outcome == store.BotProgressDispatchUncertain {
		p.unsafe = true
	}
	return nil
}
func (p *botProgressRuntime) Boundary(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.unsafe || len(p.pending) != 0 {
		return bot.ErrProgressBoundaryUnsafe
	}
	return botWorkBoundary(ctx, p.req)
}

type botProgressWorking struct {
	Assignment  string                  `json:"assignment"`
	SessionID   string                  `json:"session_id"`
	EventCount  int                     `json:"event_count"`
	ReadJournal *botProgressReadJournal `json:"read_journal,omitempty"`
}

func (p *botProgressRuntime) compose(ctx context.Context, resources *BotChatResources, initial ...session.Session) (context.Context, error) {
	if resources.ProgressDispatch != p {
		return ctx, errBotProgressIntegrationUnavailable
	}
	contexts, ok := p.req.ProgressStore.(store.BotProgressContextStore)
	if !ok {
		return ctx, errBotProgressIntegrationUnavailable
	}
	p.work = resources.Work
	policy := p.req.Version.Budget.RuntimePolicy
	controller := bot.ProgressControllerFromContext(ctx)
	if controller == nil || controller.Boundary == nil {
		return ctx, errBotProgressIntegrationUnavailable
	}
	// Progress evaluation is a long-horizon safety/cadence check, not per-turn
	// orchestration. Seed its cadence from durable run start so the first settled
	// model/tool boundary does not immediately trigger an auxiliary provider call.
	if p.req.Progress == nil && p.req.Run.StartedAt != nil && !p.req.Run.StartedAt.IsZero() {
		controller.SeedCadence(*p.req.Run.StartedAt)
	} else if p.req.Progress == nil && !p.req.Run.CreatedAt.IsZero() {
		controller.SeedCadence(p.req.Run.CreatedAt)
	}
	controller.BeginBookkeeping = entstore.WithBotContentReadOperationIfAbsent
	controller.LLM = resources.Agent.LLM
	controller.Interval = time.Duration(policy.EvaluationIntervalSeconds) * time.Second
	controller.Timeout = time.Duration(policy.ModelCallTimeoutSeconds) * time.Second
	controller.Now = time.Now
	controller.YieldRequested = p.req.ProgressYieldRequested
	// The agent hook turns this decision into the genuine ask_user tool call; it
	// must not fabricate a response or an owner answer at this boundary.
	controller.AskUser = func(context.Context, string) error { return nil }
	observedSessions := &botProgressSessions{Service: resources.SessionService, app: resources.AppName, user: p.req.Bot.OwnerID.String(), id: p.req.Run.SessionID.String()}
	if len(initial) > 0 {
		observedSessions.current = initial[0]
	}
	resources.SessionService = observedSessions
	resources.Agent.SessionService = observedSessions
	getSession := observedSessions.snapshot
	controller.Observe = func(c context.Context) (bot.ProgressObservation, error) {
		c, end := entstore.WithBotContentReadOperationIfAbsent(c)
		defer end() // Never retain immutable bytes across the evaluator/provider call.
		s, _, err := getSession(c)
		if err != nil {
			return bot.ProgressObservation{}, err
		}
		return p.observeContext(c, resources, s)
	}
	controller.Commit = func(c context.Context, o bot.ProgressObservation, d bot.ProgressDecision, at time.Time) error {
		c, end := entstore.WithBotContentReadOperationIfAbsent(c)
		defer end()
		p.mu.Lock()
		defer p.mu.Unlock()
		if p.unsafe || len(p.pending) != 0 {
			return bot.ErrProgressBoundaryUnsafe
		}
		// A yield can occur immediately after a tool response, before the next
		// producer preflight flushes its pending durable evidence references.
		// Persist those references before binding the checkpoint's work revision;
		// otherwise a fresh process restores the transcript but loses the source
		// index required by independent employee assessment.
		if service, ok := bot.BotWorkServicesFromContext(c).(*botAgentWorkService); ok {
			service.mu.Lock()
			var err error
			if len(service.accessPending) != 0 || service.accessErr != nil {
				_, err = service.load(c)
			}
			service.mu.Unlock()
			if err != nil {
				return err
			}
		}
		m, err := p.mutation(c)
		if err != nil {
			return err
		}
		_, eventCount, err := getSession(c)
		if err != nil {
			return err
		}
		journal, _ := c.Value(botAgentReadJournalKey{}).(*botAgentReadJournal)
		working, err := json.Marshal(botProgressWorking{Assignment: p.req.Instructions, SessionID: p.req.Run.SessionID.String(), EventCount: eventCount, ReadJournal: journal.progressSnapshot()})
		if err != nil {
			return err
		}
		cp := &store.BotProgressCheckpoint{Kind: store.BotProgressCheckpointV1, SchemaVersion: store.BotProgressSchemaVersion, Binding: m.Binding, Revision: m.ExpectedRevision + 1, TurnID: p.req.Run.ID, TranscriptHighWater: int64(eventCount), NextEligibleAt: at, DispatchLedgerCursor: p.cursor, Usage: p.req.Run.Usage}
		if b, ok := p.req.Budget.(interface{ Usage() store.BotUsage }); ok {
			cp.Usage = b.Usage()
		}
		if p.state != nil {
			cp.TurnID = p.state.Checkpoint.TurnID
			cp.EventCursor = p.state.Checkpoint.EventCursor
		}
		phase := store.BotProgressWorking
		if d.Decision == store.BotProgressSynthesize {
			phase = store.BotProgressSynthesizing
		}
		if p.req.ProgressYieldRequested != nil && p.req.ProgressYieldRequested() {
			phase = store.BotProgressSuspended
		}
		next, err := (bot.ProgressCheckpointer{Store: p.req.ProgressStore, Contexts: contexts}).Commit(c, store.BotProgressCheckpointRequest{Mutation: m, State: store.BotProgressState{SchemaVersion: store.BotProgressSchemaVersion, Phase: phase, Reason: store.BotProgressReasonCadence, Checkpoint: cp}}, working, o, d, at)
		if err == nil {
			p.state = next
		}
		return err
	}
	if p.state != nil {
		m, err := p.mutation(ctx)
		if err != nil {
			return ctx, err
		}
		working, err := (bot.ProgressCheckpointer{Store: p.req.ProgressStore, Contexts: contexts}).Restore(ctx, m, p.state, controller)
		if err != nil {
			return ctx, err
		}
		var saved botProgressWorking
		if json.Unmarshal(working, &saved) != nil || saved.SessionID != p.req.Run.SessionID.String() {
			return ctx, store.ErrBotInvalid
		}
		_, eventCount, err := getSession(ctx)
		if err != nil {
			return ctx, err
		}
		if eventCount < saved.EventCount {
			return ctx, fmt.Errorf("%w: checkpoint transcript unavailable", store.ErrBotConflict)
		}
		p.restoredJournal = saved.ReadJournal
	}
	return bot.WithProgressController(ctx, controller), nil
}
