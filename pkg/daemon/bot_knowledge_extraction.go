package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/credentials"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/launcher"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/entstore"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

// Extraction is synchronous within the existing worker Poll: Stop cancels and
// joins the owning Poll goroutine. There is no learner scheduler or producer tool.
type botKnowledgeModel struct {
	llm    model.LLM
	before func(context.Context) error
}
type botKnowledgeUsageStore struct {
	store.BotKnowledgeExtractionStore
	persist func(context.Context) error
	lease   *store.BotKnowledgeExtractionLease
}

func (s botKnowledgeUsageStore) BeginBotKnowledgeExtraction(c context.Context, l store.BotKnowledgeExtractionLease, revision int64) (*store.BotKnowledgeExtraction, bool, error) {
	r, started, err := s.BotKnowledgeExtractionStore.BeginBotKnowledgeExtraction(c, l, revision)
	if err == nil && started && s.lease != nil {
		*s.lease = l
	}
	return r, started, err
}

func (s botKnowledgeUsageStore) TransitionBotKnowledgeExtraction(c context.Context, q store.BotKnowledgeExtractionTransition) (*store.BotKnowledgeExtraction, error) {
	if err := s.persist(c); err != nil {
		return nil, err
	}
	return s.BotKnowledgeExtractionStore.TransitionBotKnowledgeExtraction(c, q)
}

func (m botKnowledgeModel) GenerateKnowledgeExtraction(c context.Context, q bot.KnowledgeExtractionModelRequest) (bot.KnowledgeExtractionModelResponse, error) {
	var out bot.KnowledgeExtractionModelResponse
	if m.before != nil {
		if err := m.before(c); err != nil {
			return out, err
		}
	}
	request := &model.LLMRequest{Contents: []*genai.Content{genai.NewContentFromText(q.InputJSON, "user")}, Config: &genai.GenerateContentConfig{SystemInstruction: genai.NewContentFromText(q.SystemInstruction, "system"), ResponseMIMEType: "application/json", MaxOutputTokens: int32(q.MaxOutputTokens)}}
	complete := false
	for r, e := range m.llm.GenerateContent(c, request, false) {
		// Preserve billed usage even when the provider returns an error with it.
		if r != nil && r.UsageMetadata != nil {
			input, output := bot.ModelUsageTokens(r.UsageMetadata)
			out.Usage.InputTokens = max(out.Usage.InputTokens, input)
			out.Usage.OutputTokens = max(out.Usage.OutputTokens, output)
		}
		if e != nil {
			return out, e
		}
		if r == nil {
			continue
		}
		if r.Interrupted || r.ErrorCode != "" || r.ErrorMessage != "" {
			return out, store.ErrBotConflict
		}
		if r.Content == nil {
			continue
		}
		if !r.Partial {
			out.JSON = ""
			complete = true
		}
		for _, p := range r.Content.Parts {
			if p == nil {
				continue
			}
			if p.FunctionCall != nil || p.FunctionResponse != nil {
				return out, store.ErrBotForbidden
			}
			if !p.Thought {
				out.JSON += p.Text
			}
		}
		if len(out.JSON) > store.BotKnowledgeExtractionMaxOutputBytes {
			return out, store.ErrBotInvalid
		}
	}
	if e := c.Err(); e != nil {
		return out, e
	}
	if !complete {
		return out, store.ErrBotConflict
	}
	return out, nil
}

func (w *BotWorker) knowledgeCommitContext(c context.Context, repo store.BotStore, ss store.SessionStore) context.Context {
	backend, _ := w.config.Sandbox.(launcher.BotKnowledgeHomePublisher)
	workspace := &launcher.BotKnowledgeWorkspace{Backend: backend, Bots: repo}
	var expected store.BotKnowledgeExtractionInput
	var sourceRedactor *credentials.Redactor
	var safetyUnavailable bool
	c = entstore.WithBotKnowledgeSourceVerifier(c, func(_ context.Context, i store.BotKnowledgeExtractionInput) error {
		check := i.Clone()
		check.Source = store.BotKnowledgeExtractionSource{}
		check.Outcome.Status = ""
		check.Outcome.Answer = ""
		if !reflect.DeepEqual(check, expected) {
			return fmt.Errorf("knowledge source mismatch check=%s expected=%s: %w", store.BotKnowledgeDigest(check), store.BotKnowledgeDigest(expected), store.ErrBotConflict)
		}
		return nil
	})
	c = entstore.WithBotKnowledgeInputSanitizer(c, func(live context.Context, input store.BotKnowledgeExtractionInput) (store.BotKnowledgeExtractionInput, string, error) {
		r := sourceRedactor
		if r == nil && !safetyUnavailable {
			r = credentials.RedactorFromContext(live)
		}
		if r == nil {
			// Keep the canonical source receipt without retaining unredacted text.
			input.Complete = false
			input.OwnerMessage = ""
			input.Conversation = ""
			input.PriorFiles = nil
			input.Outcome.Answer = ""
			input.Outcome.Tools = nil
			input.Outcome.Review = ""
			return input, "safety_unavailable", nil
		}
		raw, e := json.Marshal(input)
		if e != nil {
			return input, "", e
		}
		clean := r.Redact(string(raw))
		reason := ""
		if clean != string(raw) {
			reason = "credential_source"
		}
		if e := json.Unmarshal([]byte(clean), &input); e != nil {
			return input, "", e
		}
		if !input.Complete {
			reason = "incomplete_source"
		}
		if len(clean) > store.BotKnowledgeExtractionMaxInputBytes-4096 {
			input.Complete = false
			input.Conversation = ""
			input.PriorFiles = nil
			input.Outcome.Tools = nil
			input.Outcome.Review = ""
			reason = "source_too_large"
		}
		// Huge owner text/answer is not silently described as complete, and is not
		// duplicated into the journal. The immutable source tuple remains the receipt.
		if len(input.OwnerMessage)+len(input.Outcome.Answer) > store.BotKnowledgeExtractionMaxInputBytes/2 {
			input.OwnerMessage = "[oversize source retained in canonical run]"
			input.Outcome.Answer = ""
			input.Complete = false
			reason = "source_too_large"
		}
		if reason == "" && input.Validate() != nil {
			input.Complete = false
			reason = "invalid_source"
		}
		bounded, e := json.Marshal(input)
		if e != nil {
			return input, "", e
		}
		if len(bounded) > store.BotKnowledgeExtractionMaxInputBytes {
			input.Complete = false
			input.OwnerMessage = "[oversize source retained in canonical run]"
			input.Conversation = ""
			input.PriorFiles = nil
			input.Outcome = store.BotKnowledgeOutcome{Status: input.Outcome.Status}
			reason = "source_too_large"
		}
		return input, reason, nil
	})
	return entstore.WithBotKnowledgeCommitSource(c, func(live context.Context, l store.BotLease) (store.BotKnowledgeExtractionInput, error) {
		run, e := repo.GetRun(live, l.BotID, l.RunID)
		if e != nil {
			return expected, e
		}
		in, e := entstore.ReadBotKnowledgeTurnInput(live, repo, l)
		if e != nil {
			return expected, fmt.Errorf("knowledge load run input: %w", e)
		}
		b, e := repo.Get(live, l.BotID)
		if e != nil {
			return expected, e
		}
		home, homeErr := ss.ReadTranscriptEvents(live, "astonish", b.OwnerID.String(), b.HomeSessionID.String())
		turn, turnErr := ss.ReadTranscriptEvents(live, "astonish", b.OwnerID.String(), run.SessionID.String())
		raw, e := json.Marshal(struct {
			Home any `json:"home"`
			Turn any `json:"turn"`
		}{home, turn})
		if e != nil {
			return expected, e
		}
		files, fileErr := workspace.Snapshot(live, l.BotID)
		snapshot := store.BotKnowledgeExtractionInput{SchemaVersion: 1, Complete: fileErr == nil && homeErr == nil && turnErr == nil, OwnerMessage: in.Instructions, Conversation: string(raw), PriorFiles: files}
		if in.Work != nil && in.Work.Review != nil {
			review, e := json.Marshal(in.Work.Review)
			if e != nil {
				return expected, e
			}
			snapshot.Outcome.Review = string(review)
		}
		tools := map[string]int{}
		for _, event := range turn {
			if event == nil || event.Content == nil {
				continue
			}
			for _, p := range event.Content.Parts {
				if p == nil {
					continue
				}
				if f := p.FunctionCall; f != nil {
					args, _ := json.Marshal(f.Args)
					tools[f.ID] = len(snapshot.Outcome.Tools)
					snapshot.Outcome.Tools = append(snapshot.Outcome.Tools, store.BotKnowledgeToolEvidence{ID: f.ID, Name: f.Name, Arguments: string(args)})
				}
				if f := p.FunctionResponse; f != nil {
					if idx, ok := tools[f.ID]; ok {
						result, _ := json.Marshal(f.Response)
						snapshot.Outcome.Tools[idx].Result = string(result)
					}
				}
			}
		}
		redactor := credentials.RedactorFromContext(live)
		if w.config.KnowledgeRedactor != nil {
			p, _ := execution.PrincipalFromContext(live)
			redactor, e = w.config.KnowledgeRedactor(live, p, b.Scope)
			if e != nil {
				redactor = nil
			}
		}
		sourceRedactor = redactor
		safetyUnavailable = redactor == nil
		if redactor == nil {
			// Missing trusted safety must not prevent the answer commit, nor
			// retain any of its sensitive evidence in the extraction journal.
			snapshot.Complete = false
			snapshot.Conversation = ""
			snapshot.PriorFiles = nil
			snapshot.Outcome.Tools = nil
			snapshot.Outcome.Review = ""
			expected = snapshot.Clone()
			return snapshot, nil
		}
		encoded, e := json.Marshal(snapshot)
		if e != nil {
			return expected, e
		}
		if e = json.Unmarshal([]byte(redactor.Redact(string(encoded))), &snapshot); e != nil {
			return expected, e
		}
		snapshot.OwnerMessage = in.Instructions // exact issuer binding; sanitizer runs before journal persistence
		expected = snapshot.Clone()
		return snapshot, nil
	})
}

func (w *BotWorker) reconcileKnowledge(c context.Context, repo store.BotStore, ss store.SessionStore, b store.Bot) error {
	journal, supported := repo.(store.BotKnowledgeExtractionStore)
	backend, _ := w.config.Sandbox.(launcher.BotKnowledgeHomePublisher)
	// Intake and generation do not depend on the optional physical publisher.
	// An unavailable Home snapshot produces a durable Held source at commit;
	// already-Pending sources still get their one bounded extraction attempt.
	workspace := &launcher.BotKnowledgeWorkspace{Backend: backend, Bots: repo}
	var failures []error
	// Enumerate canonical result activity identities, not model hints or run lengths.
	after := int64(0)
	for {
		activities, e := repo.ListActivity(c, b.ID, after, 200)
		if e != nil {
			return e
		}
		for _, a := range activities {
			after = a.Sequence
			if a.Kind != store.BotActivityReport || a.RunID == nil {
				continue
			}
			var envelope struct {
				Source *store.BotKnowledgeExtractionSource `json:"knowledge_source"`
			}
			if e := json.Unmarshal(a.Payload, &envelope); e != nil {
				return e
			}
			if envelope.Source == nil {
				continue
			} // pre-extraction legacy result
			// Empty/legacy activity has no extraction to reconcile. Require the
			// journal only for an actual source; never hide a lost eligible turn
			// behind optional-store compatibility during retirement/publication.
			if !supported {
				return store.ErrBotUnsupported
			}
			run, e := repo.GetRun(c, b.ID, *a.RunID)
			if e != nil {
				return e
			}
			if !run.Status.Terminal() {
				continue
			}
			if envelope.Source.BotID != b.ID || envelope.Source.RunID != run.ID || envelope.Source.Sequence != a.Sequence {
				return store.ErrBotConflict
			}
			record, e := journal.ReadBotKnowledgeExtraction(c, *envelope.Source)
			if e != nil {
				failures = append(failures, e)
				continue
			}
			if record.State == store.BotKnowledgeExtractionExtracting {
				// Expiry recovery records Held, never Pending; no model replay.
				_, e := journal.ClaimBotKnowledgeExtraction(c, record.Input.Source, w.config.WorkerID, time.Minute)
				if e != nil && !errors.Is(e, store.ErrBotConflict) {
					failures = append(failures, e)
				}
				continue
			}
			if record.State == store.BotKnowledgeExtractionPending {
				if w.config.KnowledgeSafety == nil {
					return store.ErrBotUnsupported
				}
				principal, _ := execution.PrincipalFromContext(c)
				version, e := repo.GetVersion(c, b.ID, run.Version)
				if e != nil {
					return e
				}
				budget, e := bot.RestoreRunAccounting(version.Budget, time.Now(), run.Usage)
				if e != nil {
					return e
				}
				scope, e := w.loadScope(c, bot.RunRequest{Bot: b, Run: *run, Version: *version, Principal: principal})
				if e != nil {
					return e
				}
				selected, e := scope.Models.Resolve(c, run.SessionID)
				if e != nil {
					return e
				}
				var usageLease store.BotKnowledgeExtractionLease
				persist := func(ctx context.Context) error {
					u := budget.Usage()
					u.InputTokens -= run.Usage.InputTokens
					u.OutputTokens -= run.Usage.OutputTokens
					u.ToolCalls -= run.Usage.ToolCalls
					u.RuntimeNanos -= run.Usage.RuntimeNanos
					if u.CostMicros != nil && run.Usage.CostMicros != nil {
						n := *u.CostMicros - *run.Usage.CostMicros
						u.CostMicros = &n
					} else {
						u.CostMicros = nil
					}
					return entstore.RecordBotKnowledgeUsage(ctx, repo, record.Input.Source, usageLease, u)
				}
				if w.config.KnowledgeRedactor != nil {
					r, e := w.config.KnowledgeRedactor(c, principal, b.Scope)
					if e != nil {
						return e
					}
					c = credentials.WithRedactor(c, r)
				}
				// Exactly one meter owns the auxiliary reservation; do not wrap this
				// model in BudgetLLM and charge the same call twice.
				extractor, e := bot.NewKnowledgeExtractor(bot.KnowledgeExtractorConfig{Store: botKnowledgeUsageStore{BotKnowledgeExtractionStore: journal, persist: persist, lease: &usageLease}, Model: botKnowledgeModel{llm: selected.LLM, before: persist}, Budget: budget, WorkerID: w.config.WorkerID, ValidateSafety: w.config.KnowledgeSafety, EstimateCost: func(in, out int64) *int64 {
					if w.config.ModelOptions.Price == nil {
						return nil
					}
					cost, e := w.config.ModelOptions.Price(in, out)
					if e != nil {
						return nil
					}
					return cost
				}})
				if e != nil {
					return e
				}
				record, e = extractor.Extract(c, record.Input)
				if e != nil {
					failures = append(failures, e)
					continue
				}
			}
			if record.State != store.BotKnowledgeExtractionPrepared && record.State != store.BotKnowledgeExtractionFilesReady {
				continue
			}
			lease, e := journal.ClaimBotKnowledgeExtraction(c, record.Input.Source, w.config.WorkerID, time.Minute)
			if e != nil {
				if !errors.Is(e, store.ErrBotConflict) {
					failures = append(failures, e)
				}
				continue
			}
			if e := workspace.PublishCommitted(c, lease, *record); e != nil {
				failures = append(failures, e)
			}
		}
		if len(activities) < 200 {
			break
		}
	}
	_ = ss
	return errors.Join(failures...)
}

// The production composition installs its scoped redactor and inert-file policy
// without another model call or a producer tool.
