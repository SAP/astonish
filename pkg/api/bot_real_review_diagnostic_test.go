package api

// Temporary opt-in diagnostic. No daemon/store writes, producer, or task tools.
import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/provider/litellm"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

type diagnosticStockRecorder struct {
	inner         model.LLM
	t             *testing.T
	dir           string
	calls, finals int
}

func (m *diagnosticStockRecorder) Name() string { return m.inner.Name() }
func (m *diagnosticStockRecorder) save(name string, value any) {
	m.t.Helper()
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		m.t.Fatal(err)
	}
	diagnosticStockWrite(m.t, filepath.Join(m.dir, name), raw)
}
func diagnosticStockWrite(t *testing.T, path string, raw []byte) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.Write(raw); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if err = f.Close(); err != nil {
		t.Fatal(err)
	}
}
func (m *diagnosticStockRecorder) GenerateContent(ctx context.Context, req *model.LLMRequest, streaming bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		m.calls++
		n := m.calls
		if n > 2 {
			m.t.Fatal("production review exceeded two-call ceiling")
		}
		if len(req.Tools) > 0 || (req.Config != nil && len(req.Config.Tools) > 0) {
			m.t.Fatal("review offered tools")
		}
		m.save(fmt.Sprintf("request-%d.json", n), req)
		for response, err := range m.inner.GenerateContent(ctx, req, streaming) {
			if err != nil {
				m.save(fmt.Sprintf("provider-error-%d.json", n), map[string]string{"stage": "GenerateContent", "error": err.Error()})
			}
			if response != nil && !response.Partial && response.Content != nil {
				m.finals++
				m.save(fmt.Sprintf("response-%d-%d.json", n, m.finals), response)
				var text strings.Builder
				for _, p := range response.Content.Parts {
					if p != nil && !p.Thought {
						text.WriteString(p.Text)
					}
				}
				diagnosticStockWrite(m.t, filepath.Join(m.dir, fmt.Sprintf("reviewer-%d-%d.txt", n, m.finals)), []byte(text.String()))
			}
			if !yield(response, err) {
				return
			}
		}
	}
}

func TestDiagnosticRealStockReview(t *testing.T) {
	if os.Getenv("ASTONISH_REAL_STOCK_REVIEW") != "1" {
		t.Skip("private real-provider diagnostic; opt in explicitly")
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	dir, err := os.MkdirTemp(filepath.Join(root, ".tmp"), "real-stock-review-")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	t.Logf("private artifacts: %s", dir)
	recorder := &diagnosticStockRecorder{t: t, dir: dir}
	configRaw, err := os.ReadFile(filepath.Join(root, ".tmp", "employee-native-config.json"))
	if err != nil {
		t.Fatal("config read failed")
	}
	var cfg struct {
		URL   string `json:"provider_url"`
		Key   string `json:"provider_api_key"`
		Model string `json:"model"`
	}
	if json.Unmarshal(configRaw, &cfg) != nil || cfg.URL == "" || cfg.Key == "" || cfg.Model == "" {
		t.Fatal("config missing explicit provider_url/provider_api_key/model")
	}
	// Use the production LiteLLM constructor, without config mutation or key logging.
	// This config was authored for a container. Docker reports the same proxy
	// published on host port 4000; map only this known container-host address
	// for this host-run diagnostic. Never alter the config or proxy.
	providerURL := cfg.URL
	if providerURL == "http://host.docker.internal:4000" {
		providerURL = "http://127.0.0.1:4000"
		t.Log("diagnostic host routing: configured container-host alias -> published loopback port 4000")
	}
	recorder.inner = litellm.NewProvider(cfg.Key, providerURL, cfg.Model)
	raw, err := os.ReadFile(filepath.Join(root, ".tmp", "real-review-stock-session.sse"))
	if err != nil {
		t.Fatal(err)
	}
	diagnosticStockWrite(t, filepath.Join(dir, "transcript.sse"), raw)
	candidate := &store.BotWorkCandidate{Revision: 1, Bodies: map[string]string{}}
	assignment := ""
	scanner := bufio.NewScanner(strings.NewReader(string(raw)))
	scanner.Buffer(make([]byte, 65536), 4<<20)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var event struct {
			Content   *genai.Content
			Partial   bool
			ID        string
			Timestamp time.Time
			Author    string
		}
		if err = json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &event); err != nil {
			t.Fatal("transcript JSON parse failed")
		}
		if event.Content == nil || event.Partial {
			continue
		}
		var text strings.Builder
		hasCall := false
		for _, p := range event.Content.Parts {
			if p == nil {
				continue
			}
			if p.FunctionCall != nil {
				hasCall = true
			}
			if !p.Thought {
				text.WriteString(p.Text)
			}
			if r := p.FunctionResponse; r != nil {
				body, err := json.Marshal(r.Response)
				if err != nil {
					t.Fatal(err)
				}
				ref := "tool-response:" + r.ID
				if r.ID == "" {
					ref = "tool-response:" + event.ID + ":" + r.Name
				}
				candidate.Bodies[ref] = string(body)
				source, _ := r.Response["url"].(string)
				if source == "" {
					source = ref
				}
				candidate.Observations = append(candidate.Observations, store.BotEvidence{Kind: store.BotEvidenceObservation, Summary: "Captured " + r.Name + " response from original session (reconstructed observation)", Source: source, ObservedAt: event.Timestamp, ActorID: uuid.MustParse("6f0c3554-f43f-4b0c-a689-6e60e76384fd")})
			}
		}
		if event.Content.Role == "user" && event.Author == "user" && text.Len() > 0 {
			assignment = text.String()
		}
		if event.Content.Role == "model" && !hasCall && text.Len() > 0 {
			candidate.Text = text.String()
			candidate.EventID = event.ID
		}
	}
	if err = scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if assignment != "Stock Market News" || candidate.Text == "" || len(candidate.Bodies) == 0 {
		t.Fatal("missing exact assignment, draft, or captured responses")
	}
	candidate.Digest = candidate.ContentDigest()
	req := bot.RunRequest{Bot: store.Bot{ID: uuid.MustParse("6f0c3554-f43f-4b0c-a689-6e60e76384fd"), HomeSessionID: uuid.MustParse("0f36366d-0dfe-45d8-8870-88a71f1bcfc1")}, Budget: &botChatTestBudget{}}
	w := store.NewBotWorkState(store.BotWorkContract{BotID: req.Bot.ID, RunID: uuid.MustParse("bb6a419d-6e91-4c3d-8ebf-a705fd96b523"), Version: 1, Instructions: assignment, Charter: "Provide useful stock market news grounded in captured sources; honestly disclose uncertainty and unavailable information."})
	w.Criteria = []store.BotWorkCriterion{{ID: "c1", SourceIDs: []string{"assignment"}, Text: "Provide relevant stock market news.", VerificationKind: store.BotWorkVerificationModel}, {ID: "c2", SourceIDs: []string{"charter"}, Text: "Ground factual claims in captured sources and disclose uncertainty honestly.", VerificationKind: store.BotWorkVerificationModel}}
	w.Candidate = candidate
	w.Phase = store.BotWorkReviewing
	if err = w.Validate(); err != nil {
		t.Fatalf("reconstructed work validation: %v", err)
	}
	recorder.save("reconstruction.json", map[string]any{"note": "DIAGNOSTIC RECONSTRUCTION, NOT EXACT DURABLE WORK REPLAY. Assignment and final draft are exact transcript content; bodies use production json.Marshal(FunctionResponse.Response). Charter, criteria, observations and work revision are reconstructed. No producer/task execution, Bot mutation, or publication.", "model": cfg.Model, "work": w})
	recorder.save("content-input.json", botContentInput(w))
	t.Logf("captured draft bytes=%d responses=%d observations=%d model=%s", len(candidate.Text), len(candidate.Bodies), len(candidate.Observations), cfg.Model)
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	var usage store.BotUsage
	started := time.Now()
	review, reviewErr := reviewBotCandidate(ctx, req, recorder, w, &usage)
	result := map[string]any{"stage": "reviewBotCandidate", "calls": recorder.calls, "final_responses": recorder.finals, "elapsed_seconds": time.Since(started).Seconds(), "usage": usage, "review": review}
	if reviewErr != nil {
		result["error"] = reviewErr.Error()
	}
	recorder.save("result.json", result)
	t.Logf("review invocation finished: calls=%d finals=%d elapsed=%s error=%v", recorder.calls, recorder.finals, time.Since(started).Round(time.Millisecond), reviewErr)
	if recorder.calls == 0 || recorder.finals == 0 {
		t.Fatal("no final real-provider reviewer response; inspect private failure stage")
	}
	// A completed provider call is not a successful review. Preserve diagnostics
	// first, then fail the command on protocol rejection.
	if reviewErr != nil {
		t.Fatalf("real review rejected: %v", reviewErr)
	}
}
