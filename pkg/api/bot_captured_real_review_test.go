package api

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"iter"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/provider"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/entstore"
	"github.com/google/uuid"
	"google.golang.org/adk/model"
)

type capturedRealReviewLLM struct {
	model.LLM
	t     *testing.T
	dir   string
	calls int
}

func (m *capturedRealReviewLLM) save(name string, v any) {
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		m.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(m.dir, name), raw, 0600); err != nil {
		m.t.Fatal(err)
	}
}
func (m *capturedRealReviewLLM) GenerateContent(ctx context.Context, q *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		m.calls++
		call := m.calls
		if call > 2 {
			m.t.Fatal("production exceeded two-attempt bound")
		}
		if os.Getenv("ASTONISH_BOT_REAL_REVIEW_ARTIFACT") != "" {
			var input botWorkContentInput
			if len(q.Contents) == 0 || len(q.Contents[0].Parts) == 0 || json.Unmarshal([]byte(q.Contents[0].Parts[0].Text), &input) != nil {
				m.t.Fatal("actual reviewer request missing content input")
			}
			var body strings.Builder
			for _, item := range input.Items {
				if strings.HasPrefix(item.ID, "body:1:") {
					body.WriteString(item.Text)
				}
			}
			if len(input.Items) > botWorkContentMaxItems || len(body.String()) != 44779 || fmt.Sprintf("%x", sha256.Sum256([]byte(body.String()))) != "e4d5778d062adb54220986bae153a645ae134500fcc364c20dd1b1163ec24807" {
				m.t.Fatal("actual request omitted exact artifact coverage")
			}
		}
		m.save(fmt.Sprintf("request-%d.json", call), q)
		var frames []*model.LLMResponse
		defer func() { m.save(fmt.Sprintf("responses-%d.json", call), frames) }()
		if call == 1 && os.Getenv("ASTONISH_BOT_REAL_REPAIR_RESUME") != "" && !strings.HasSuffix(m.dir, "review") {
			raw, err := os.ReadFile(filepath.Join(os.Getenv("ASTONISH_BOT_REAL_REPAIR_RESUME"), "responses-1.json"))
			if err != nil {
				m.t.Fatal(err)
			}
			var prior []*model.LLMResponse
			if json.Unmarshal(raw, &prior) != nil || len(prior) == 0 {
				m.t.Fatal("missing captured first attempt")
			}
			frames = prior // Historical frames are retained, not charged as a new provider call.
			for _, r := range prior {
				if !yield(r, nil) {
					return
				}
			}
			return
		}
		for r, err := range m.LLM.GenerateContent(ctx, q, stream) {
			frames = append(frames, r)
			if !yield(r, err) {
				return
			}
		}
	}
}

// Explicit paid opt-in. This invokes only the production reviewer, never a producer,
// dispatcher, publisher, or synthetic approval. Inputs are retained captured bytes.
func TestBotCapturedConfiguredReviewerLive(t *testing.T) {
	if os.Getenv("ASTONISH_BOT_REAL_REVIEW") != "1" {
		t.Skip("explicit paid reviewer opt-in required")
	}
	input := os.Getenv("ASTONISH_BOT_REAL_REVIEW_WORK")
	dir := os.Getenv("ASTONISH_BOT_REAL_REVIEW_OUTPUT")
	if input == "" || dir == "" {
		t.Fatal("work and private output paths required")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(input)
	if err != nil {
		t.Fatal(err)
	}
	var w store.BotWorkState
	if err = json.Unmarshal(raw, &w); err != nil {
		t.Fatal(err)
	}
	if w.Candidate == nil || w.Candidate.Digest != w.Candidate.ContentDigest() {
		t.Fatal("captured candidate digest mismatch")
	}
	if os.Getenv("ASTONISH_BOT_REAL_REVIEW_ARTIFACT") != "" {
		hydrateCapturedReviewArtifact(t, &w)
	}
	before := w.Candidate.Digest
	w.Review = nil // Prior failure feedback is not input to this isolated validator call.
	ctx, cancel := context.WithTimeout(context.Background(), 240*time.Second)
	defer cancel()
	db, err := entstore.New(ctx, entstore.Config{DSN: "file:/Users/I851355/.local/share/astonish/platform.db?mode=ro", DataDir: "/Users/I851355/.local/share/astonish"})
	if err != nil {
		t.Fatalf("settings open: %T", err)
	}
	defer db.Close()
	ps, err := db.PlatformSettings().Get(ctx)
	if err != nil {
		t.Fatalf("platform settings: %T", err)
	}
	org, err := db.OrgSettings("my-org").Get(ctx)
	if err != nil {
		t.Fatalf("org settings: %T", err)
	}
	name := "New AI Core Provider"
	pc := config.ProviderConfig{}
	for k, v := range ps.Providers[name] {
		pc[k] = v
	}
	if p := org.Providers[name]; p != nil {
		pc = config.ProviderConfig{}
		for k, v := range p {
			pc[k] = v
		}
	}
	llm, err := provider.GetProvider(provider.WithConfinedProviderSecrets(ctx), name, "gpt-6-astra", &config.AppConfig{Providers: map[string]config.ProviderConfig{name: pc}})
	if err != nil {
		t.Fatalf("provider setup: %T", err)
	}
	recorder := &capturedRealReviewLLM{LLM: llm, t: t, dir: dir}
	recorder.save("candidate-work.json", &w)
	budget, err := bot.NewRunBudget(store.BudgetSpec{MaxRuntimeSeconds: 240}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	req := bot.RunRequest{Bot: store.Bot{ID: w.Contract.BotID, HomeSessionID: uuid.MustParse("2ae7be13-0f41-463f-a1fb-2acfc8ae276c")}, Run: store.BotRun{ID: w.Contract.RunID}, Budget: budget, Work: &w, Instructions: w.Contract.Instructions}
	in := botContentInput(&w)
	recorder.save("content-input.json", in)
	t.Logf("candidate=%s items=%d sources=%d artifacts=%d bodies=%d", before, len(in.Items), len(in.Sources), len(w.Candidate.Artifacts), len(w.Candidate.Bodies))
	if os.Getenv("ASTONISH_BOT_REAL_REPAIR") == "1" {
		var captured struct {
			Feedback BotEmployeeRepairFeedback `json:"feedback"`
			Review   *botWorkContentReview     `json:"review"`
		}
		feedbackRaw, e := os.ReadFile(os.Getenv("ASTONISH_BOT_REAL_REPAIR_FEEDBACK"))
		if e != nil {
			t.Fatal(e)
		}
		if e = json.Unmarshal(feedbackRaw, &captured); e != nil {
			t.Fatal(e)
		}
		encoded, _ := json.Marshal(in)
		repairInput := map[string]json.RawMessage{}
		if e = json.Unmarshal(encoded, &repairInput); e != nil {
			t.Fatal(e)
		}
		repairInput["review_findings"], _ = json.Marshal(captured.Review)
		repairInput["review_feedback"], _ = json.Marshal(captured.Feedback)
		var patch botArtifactTextPatch
		op := uuid.NewString()
		if prior := os.Getenv("ASTONISH_BOT_REAL_REPAIR_RESUME"); prior != "" {
			raw, err := os.ReadFile(filepath.Join(prior, "operation.json"))
			var operation struct {
				ID string `json:"id"`
			}
			if err != nil || json.Unmarshal(raw, &operation) != nil || operation.ID == "" {
				t.Fatal("missing original operation")
			}
			op = operation.ID
		}
		recorder.save("operation.json", map[string]any{"id": op, "kind": "bounded-exact-edit-diagnostic", "resumed_from": os.Getenv("ASTONISH_BOT_REAL_REPAIR_RESUME"), "source_candidate": before, "publication_verified": false})
		var repairUsage store.BotUsage
		if e = botWorkJSON(ctx, req, recorder, botArtifactRepairPrompt, repairInput, &patch, &repairUsage, func(v any) error { _, err := applyBotArtifactTextPatch(&w, *v.(*botArtifactTextPatch)); return err }); e != nil {
			t.Fatal(e)
		}
		recorder.save("patch.json", patch)
		revised, e := applyBotArtifactTextPatch(&w, patch)
		if e != nil {
			recorder.save("result.json", map[string]any{"operation_id": op, "accepted": false, "valid": false, "error": e.Error(), "repair_calls": recorder.calls, "repair_usage": repairUsage, "review_calls": 0, "publication_verified": false})
			t.Fatal(e)
		}
		diagnosticStockWrite(t, filepath.Join(dir, "revised-report.md"), []byte(revised))
		next := w.Clone()
		a := next.Candidate.Artifacts[0]
		delete(next.Candidate.Bodies, a.ID.String())
		a.ID = uuid.New()
		ref := *a.ContentRef
		ref.Key = "content-v1/artifact-1-diagnostic-" + op
		ref.SHA256 = botAgentDigest([]byte(revised))
		ref.SizeBytes = int64(len(revised))
		a.ContentRef, a.SHA256, a.SizeBytes = &ref, ref.SHA256, ref.SizeBytes
		next.Candidate.Artifacts = []store.BotArtifact{a}
		next.Candidate.Bodies[a.ID.String()] = revised
		next.Candidate.Revision++
		next.Candidate.Text = "Attached report."
		next.Candidate.Digest = next.Candidate.ContentDigest()
		next.Review = nil
		recorder.save("revised-work.json", next)
		// A separate normal two-attempt review, not another repair operation.
		rr := &capturedRealReviewLLM{LLM: llm, t: t, dir: filepath.Join(dir, "review")}
		if e = os.Mkdir(rr.dir, 0700); e != nil {
			t.Fatal(e)
		}
		var reviewUsage store.BotUsage
		r, f, re := reviewBotEmployeeCandidate(ctx, req, rr, next, &reviewUsage)
		next.Review = r
		out := map[string]any{"operation_id": op, "accepted": next.HasAcceptedEmployeeReview(), "valid": re == nil, "review": r, "feedback": f, "repair_usage": repairUsage, "review_usage": reviewUsage, "repair_calls": recorder.calls, "review_calls": rr.calls, "publication_verified": false, "revised_sha256": a.SHA256, "revised_bytes": len(revised)}
		if re != nil {
			out["error"] = re.Error()
		}
		recorder.save("result.json", out)
		if w.Candidate.Digest != before || w.Candidate.ContentDigest() != before {
			t.Fatal("original changed")
		}
		t.Logf("actual model patch edits=%d revised_bytes=%d valid=%t accepted=%t; publication NOT verified", len(patch.Edits), len(revised), re == nil, next.HasAcceptedEmployeeReview())
		if re != nil {
			t.Fatal(re)
		}
		return
	}
	var usage store.BotUsage
	review, feedback, reviewErr := reviewBotEmployeeCandidate(ctx, req, recorder, &w, &usage)
	result := map[string]any{"review": review, "feedback": feedback, "usage": usage, "calls": recorder.calls, "valid": reviewErr == nil, "accepted": false}
	if reviewErr != nil {
		result["error"] = reviewErr.Error()
		result["error_type"] = fmt.Sprintf("%T", reviewErr)
	}
	if review != nil {
		checked := w.Clone()
		checked.Review = review
		result["accepted"] = checked.HasAcceptedEmployeeReview()
	}
	recorder.save("result.json", result)
	if w.Candidate.Digest != before || w.Candidate.ContentDigest() != before {
		t.Fatal("candidate mutated")
	}
	if recorder.calls == 0 {
		t.Fatal("configured reviewer was not called")
	}
	t.Logf("real reviewer calls=%d valid=%t accepted=%v usage_input=%d usage_output=%d", recorder.calls, reviewErr == nil, result["accepted"], usage.InputTokens, usage.OutputTokens)
	if reviewErr != nil {
		t.Fatalf("review rejected; exact private diagnostic recorded (%T)", reviewErr)
	}
}
