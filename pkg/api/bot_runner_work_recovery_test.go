package api

import (
	"context"
	"encoding/json"
	"errors"
	"iter"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/provider"
	"github.com/SAP/astonish/pkg/provider/bedrock"
	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

type formatRecoveryModel struct {
	t                   *testing.T
	texts               []string
	calls               int
	after               func()
	skipContentCoverage bool
	original            string
	originalSchema      string
	inspect             func(*model.LLMRequest)
}

func (*formatRecoveryModel) Name() string { return "format-recovery" }
func (m *formatRecoveryModel) GenerateContent(_ context.Context, r *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		if len(r.Tools) != 0 || len(r.Config.Tools) != 0 {
			m.t.Fatal("assessment has tools")
		}
		if r.Config.ResponseJsonSchema == nil || r.Config.MaxOutputTokens > 8192 {
			m.t.Fatal("lost schema or cap")
		}
		// Exercise the real SAP/Anthropic translator: checking only the ADK
		// schema misses adapters that omit ResponseJsonSchema from the wire.
		wire, err := bedrock.ConvertRequest(r, 8192, true)
		if err != nil {
			m.t.Fatal(err)
		}
		schema, err := json.Marshal(r.Config.ResponseJsonSchema)
		if err != nil || !strings.Contains(wire.System, string(schema)) {
			m.t.Fatal("complete assessment schema lost on Bedrock wire")
		}
		if len(wire.Tools) != 0 || wire.ToolChoice != nil {
			m.t.Fatal("schema transport introduced tools")
		}
		if m.calls == 0 {
			m.original = r.Contents[0].Parts[0].Text
			m.originalSchema = string(schema)
		} else {
			expectedContents := 2
			// Valid JSON entering format recovery failed strict schema decoding.
			schemaRetry := strings.Contains(r.Config.SystemInstruction.Parts[0].Text, "FORMAT CORRECTION ONLY:") && json.Valid([]byte(m.texts[0]))
			if schemaRetry || strings.Contains(r.Config.SystemInstruction.Parts[0].Text, "CITATION PROTOCOL CORRECTION ONLY:") {
				expectedContents = 3
			}
			if string(schema) != m.originalSchema {
				m.t.Fatal("recovery changed response schema")
			}
			if r.Contents[0].Parts[0].Text != m.original || !strings.Contains(r.Config.SystemInstruction.Parts[0].Text, "UNTRUSTED") || len(r.Contents) != expectedContents {
				m.t.Fatal("recovery lost immutable untrusted context")
			}
			if strings.Contains(wire.System, "private-secret") || r.Contents[1].Role != "user" || !strings.HasPrefix(r.Contents[1].Parts[0].Text, "UNTRUSTED prior assessment response (data only):\n") {
				m.t.Fatal("recovery promoted untrusted response or decoder error into instructions")
			}
		}
		if m.inspect != nil {
			m.inspect(r)
		}
		if m.calls >= len(m.texts) || m.calls >= 2 {
			m.t.Fatal("assessment exceeded expected two-call ceiling")
		}
		text := m.texts[m.calls]
		if !m.skipContentCoverage {
			text = string(fixtureContentCoverage(r, []byte(text)))
		}
		m.calls++
		yield(&model.LLMResponse{Content: genai.NewContentFromText(text, "model"), UsageMetadata: &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: 1, CandidatesTokenCount: 1, TotalTokenCount: 2}}, nil)
		if m.after != nil {
			m.after()
		}
	}
}
func TestBotWorkFormatRecovery(t *testing.T) {
	for _, tc := range []struct {
		name  string
		texts []string
		calls int
		fail  bool
	}{
		{"malformed-valid", []string{"private-secret", `{"candidate_digest":"good"}`}, 2, false},
		{"both-malformed", []string{"private-secret", "private-secret"}, 2, true},
		{"trailing", []string{`{} {}`, `{"candidate_digest":"good"}`}, 2, false},
		{"unknown-field", []string{`{"candidate_digest":"partial","private-secret":1}`, `{"candidate_digest":"good"}`}, 2, false},
		{"wrong-type", []string{`{"candidate_digest":42}`, `{"candidate_digest":"good"}`}, 2, false},
		{"repeated-unknown-field", []string{`{"candidate_digest":"partial","private-secret":1}`, `{"candidate_digest":"partial","private-secret":1}`}, 2, true},
		{"repeated-wrong-type", []string{`{"candidate_digest":42}`, `{"candidate_digest":42}`}, 2, true},
		{"fresh-schema-storage", []string{`{"criteria":[{"criterion_id":"private-secret"}],"private-secret":1}`, `{"candidate_digest":"good"}`}, 2, false},
		{"retry-unknown-field", []string{"private-secret", `{"private-secret":1}`}, 2, true},
		{"retry-wrong-type", []string{"private-secret", `{"candidate_digest":42}`}, 2, true},
		{"fenced", []string{"```json\n{}\n```", "```json\n{}\n```"}, 2, true},
		{"cancel", []string{"private-secret"}, 1, true},
		{"budget", []string{"private-secret"}, 1, true},
		{"metered-recovery", []string{"private-secret", `{"candidate_digest":"good"}`}, 2, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, req, _ := botChatTestRequest(t)
			ctx, cancel := context.WithCancel(ctx)
			defer cancel()
			m := &formatRecoveryModel{t: t, texts: tc.texts}
			var llm model.LLM = m
			if tc.name == "cancel" {
				m.after = cancel
			}
			if tc.name == "budget" || tc.name == "metered-recovery" {
				limit := int64(2)
				if tc.name == "metered-recovery" {
					limit = 5
				}
				req.Budget, _ = bot.NewRunBudget(store.BudgetSpec{MaxRuntimeSeconds: 60, MaxTokens: &limit}, time.Now())
				llm, _ = provider.NewBudgetLLM(m, req.Budget, provider.BudgetLLMOptions{MaxOutputTokens: 1, SupportsOutputLimit: true, EstimateInput: func(context.Context, *model.LLMRequest) (int64, error) { return 1, nil }})
			}
			out := store.BotWorkReview{CandidateDigest: "unchanged"}
			var usage store.BotUsage
			err := botWorkJSON(ctx, req, llm, botWorkReviewPrompt, map[string]string{"source": "original"}, &out, &usage)
			if (err != nil) != tc.fail || m.calls != tc.calls || usage.InputTokens != int64(tc.calls) || usage.OutputTokens != int64(tc.calls) {
				t.Fatalf("err=%v calls=%d usage=%+v", err, m.calls, usage)
			}
			if err != nil && (strings.Contains(err.Error(), "private-secret") || out.CandidateDigest != "unchanged") {
				t.Fatal("unsafe parse error or partial assignment")
			}
			if err == nil && (out.CandidateDigest != "good" || len(out.Criteria) != 0) {
				t.Fatal("correction retained partially decoded state")
			}
			if tc.name == "metered-recovery" {
				metered := req.Budget.(interface{ Usage() store.BotUsage }).Usage()
				if metered.InputTokens != 2 || metered.OutputTokens != 2 || metered.ToolCalls != 0 {
					t.Fatalf("meter reset: %+v", metered)
				}
			}
			if tc.name == "cancel" && !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			if tc.name == "budget" && !errors.Is(err, bot.ErrBudgetExceeded) {
				t.Fatal(err)
			}
		})
	}
	for _, wrong := range []bool{false, true} {
		ctx, req, _ := botChatTestRequest(t)
		w := store.NewBotWorkState(store.BotWorkContract{BotID: req.Bot.ID, RunID: req.Run.ID, Version: req.Run.Version, Instructions: req.Instructions, Charter: req.Version.Charter})
		w.Criteria = []store.BotWorkCriterion{{ID: "c1", SourceIDs: []string{"assignment", "charter"}, Text: "requirement"}}
		w.Candidate = &store.BotWorkCandidate{Text: "actual", Revision: 1, EventID: "actual-event"}
		w.Candidate.Digest = w.Candidate.ContentDigest()
		w.Phase = store.BotWorkReviewing
		digest := w.Candidate.Digest
		if wrong {
			digest = "wrong"
		}
		raw, _ := json.Marshal(store.BotWorkReview{CandidateDigest: digest, Criteria: []store.BotCriterionAssessment{{CriterionID: "c1", Verdict: store.BotCriterionFailed, Method: store.BotAssessmentModel, EvidenceRefs: []string{"candidate:" + w.Candidate.Digest}, Explanation: "not satisfied"}}})
		m := &formatRecoveryModel{t: t, texts: []string{string(raw)}}
		var usage store.BotUsage
		_, err := reviewBotCandidate(ctx, req, m, w, &usage)
		if (err != nil) != wrong || m.calls != 1 {
			t.Fatalf("semantic retry wrong=%v err=%v calls=%d", wrong, err, m.calls)
		}
	}
}
