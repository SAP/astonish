package daemon

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
	"unicode/utf8"

	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/model"
	"google.golang.org/adk/session"
	"google.golang.org/genai"
)

// Transport recovery only: historical corrected bytes and sources, new genuine
// reviewer verdict. No captured verdict is rebound to a different candidate.
type genuineCorrectedDeliveryModel struct {
	t            *testing.T
	real         model.LLM
	work         store.BotWorkState
	body         string
	title        session.Event
	sentTitle    bool
	readSources  bool
	sourceOffset int
	sources      string
	reviews      int
	dir          string
}

func (m *genuineCorrectedDeliveryModel) Name() string {
	if m.real != nil {
		return m.real.Name()
	}
	return "gpt-6-astra"
}
func (m *genuineCorrectedDeliveryModel) GenerateContent(ctx context.Context, q *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	prompt := ""
	if q.Config != nil && q.Config.SystemInstruction != nil {
		for _, p := range q.Config.SystemInstruction.Parts {
			prompt += p.Text
		}
	}
	if strings.HasPrefix(prompt, "BOT_WORK_REVIEW_V1") {
		return func(yield func(*model.LLMResponse, error) bool) {
			m.reviews++
			if m.reviews > 1 {
				yield(nil, fmt.Errorf("authorized genuine reviewer call exhausted"))
				return
			}
			raw, _ := json.MarshalIndent(q, "", "  ")
			if err := os.WriteFile(filepath.Join(m.dir, "review-request.json"), raw, 0600); err != nil {
				yield(nil, err)
				return
			}
			var frames []*model.LLMResponse
			defer func() {
				raw, _ := json.MarshalIndent(frames, "", "  ")
				_ = os.WriteFile(filepath.Join(m.dir, "review-responses.json"), raw, 0600)
			}()
			for r, e := range m.real.GenerateContent(ctx, q, stream) {
				frames = append(frames, r)
				if !yield(r, e) {
					return
				}
			}
		}
	}
	if strings.HasPrefix(prompt, "BOT_WORK_PREPARE_V1") {
		raw, _ := json.Marshal(map[string]any{"criteria": m.work.Criteria})
		return func(yield func(*model.LLMResponse, error) bool) {
			yield(&model.LLMResponse{Content: genai.NewContentFromText(string(raw), "model"), TurnComplete: true}, nil)
		}
	}
	if strings.HasPrefix(prompt, "BOT_EMPLOYEE_MESSAGE") || strings.HasPrefix(prompt, "BOT_ARTIFACT") || strings.HasPrefix(prompt, "BOT_WORK_PENDING") {
		return func(yield func(*model.LLMResponse, error) bool) { yield(nil, store.ErrBotUnsupported) }
	}
	if reply, ok := workerWorkAssessment(q, nil); ok {
		return reply
	}
	return func(yield func(*model.LLMResponse, error) bool) {
		if !m.sentTitle {
			m.sentTitle = true
			yield(&m.title.LLMResponse, nil)
			return
		}
		if m.sourceOffset < len(m.sources) {
			const marker = "<!-- ASTONISH_CONTINUE:historical-captured-research-8f1ad7 -->"
			start := m.sourceOffset
			end := min(start+40000, len(m.sources))
			for end < len(m.sources) && !utf8.RuneStart(m.sources[end]) {
				end--
			}
			chunk := m.sources[start:end]
			if end < len(m.sources) {
				chunk += marker
			}
			name := "write_file"
			args := map[string]any{"path": "outputs/historical-captured-research.json", "content": chunk, "create_only": true}
			if start > 0 {
				name = "edit_file"
				args = map[string]any{"path": "outputs/historical-captured-research.json", "old_string": marker, "new_string": chunk, "expected_sha256": fmt.Sprintf("%x", sha256.Sum256([]byte(m.sources[:start]+marker)))}
			}
			m.sourceOffset = end
			yield(&model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{ID: fmt.Sprintf("retain-historical-capture-%d", start), Name: name, Args: args}}}}}, nil)
			return
		}
		if !m.readSources {
			m.readSources = true
			yield(&model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{ID: "read-historical-capture", Name: "read_file", Args: map[string]any{"path": "outputs/historical-captured-research.json", "limit": 262144}}}}}}, nil)
			return
		}
		yield(&model.LLMResponse{Content: genai.NewContentFromText(m.body, "model"), TurnComplete: true, FinishReason: genai.FinishReasonStop}, nil)
	}
}
func TestGenuineCorrectedEvidenceUsesBoundedWrites(t *testing.T) {
	m := &genuineCorrectedDeliveryModel{sentTitle: true, sources: strings.Repeat("captured-✓", 12000)}
	var saved string
	for m.sourceOffset < len(m.sources) {
		var response *model.LLMResponse
		for r, err := range m.GenerateContent(t.Context(), &model.LLMRequest{}, false) {
			if err != nil {
				t.Fatal(err)
			}
			response = r
		}
		call := response.Content.Parts[0].FunctionCall
		if call.Args["path"] != "outputs/historical-captured-research.json" {
			t.Fatal("historical write outside allowed outputs")
		}
		if call.Name == "write_file" {
			if call.Args["create_only"] != true {
				t.Fatal("initial write must be create-only")
			}
			saved = call.Args["content"].(string)
			if len(saved) > 49152 || !utf8.ValidString(saved) {
				t.Fatal("invalid bounded initial write")
			}
		} else {
			if call.Args["expected_sha256"] != fmt.Sprintf("%x", sha256.Sum256([]byte(saved))) {
				t.Fatal("continuation compare-and-swap mismatch")
			}
			old, replacement := call.Args["old_string"].(string), call.Args["new_string"].(string)
			if len(replacement) > 49152 || !utf8.ValidString(replacement) || strings.Count(saved, old) != 1 {
				t.Fatal("invalid bounded continuation")
			}
			saved = strings.Replace(saved, old, replacement, 1)
		}
	}
	if saved != m.sources {
		t.Fatal("historical evidence bytes changed")
	}
}

func TestBotGenuineCorrectedDockerPublication(t *testing.T) {
	if os.Getenv("ASTONISH_GENUINE_CORRECTED_PUBLICATION") != "1" {
		t.Skip("explicit one paid reviewer and Docker opt-in required")
	}
	dir := os.Getenv("ASTONISH_GENUINE_CORRECTED_INPUT")
	body, err := os.ReadFile(filepath.Join(dir, "revised-report.md"))
	if err != nil {
		t.Fatal(err)
	}
	if len(body) != 45639 || fmt.Sprintf("%x", sha256.Sum256(body)) != "b5a06dff23615f0947c2c8d63115a8930853eaa68ddf13876b59839171677302" {
		t.Fatal("accepted corrected bytes changed")
	}
	m := &genuineCorrectedDeliveryModel{t: t, body: string(body), dir: os.Getenv("ASTONISH_EXACT_DELIVERABLE_DIR")}
	if err = os.MkdirAll(m.dir, 0700); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "revised-work.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &m.work); err != nil {
		t.Fatal(err)
	}
	if m.work.Candidate == nil || m.work.Candidate.Digest != m.work.Candidate.ContentDigest() {
		t.Fatal("original candidate mismatch")
	}
	raw, err = os.ReadFile(os.Getenv("ASTONISH_CAPTURED_TITLE_EVENT"))
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &m.title); err != nil {
		t.Fatal(err)
	}
	sourceRaw, err := os.ReadFile(filepath.Join(dir, "content-input.json"))
	if err != nil {
		t.Fatal(err)
	}
	var sources struct {
		Sources map[string]string `json:"content_sources"`
	}
	if err = json.Unmarshal(sourceRaw, &sources); err != nil {
		t.Fatal(err)
	}
	evidence, _ := json.Marshal(map[string]any{"provenance": "Historical captured research for transport recovery; not fresh retrieval or execution proof.", "original_contract": m.work.Contract, "captured_sources": sources.Sources})
	m.sources = string(evidence)
	runBotExactDockerReproduction(t, m)
	if m.reviews != 1 {
		t.Fatalf("genuine reviews=%d", m.reviews)
	}
}
