package api

// Opt-in: read-only saved work, production assessor, no daemon/producer/tools.
import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"iter"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/provider"
	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
	"gopkg.in/yaml.v3"
	_ "modernc.org/sqlite"
)

type recoveryLiveModel struct {
	t                    *testing.T
	dir, base, key, name string
	calls                int
	client               *http.Client
}

func (m *recoveryLiveModel) Name() string { return m.name }
func (m *recoveryLiveModel) save(name string, v any) {
	b, e := json.MarshalIndent(v, "", "  ")
	if e != nil {
		m.t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(m.dir, name), b, 0600); e != nil {
		m.t.Fatal(e)
	}
}
func (m *recoveryLiveModel) GenerateContent(ctx context.Context, r *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		if m.calls >= 2 || len(r.Tools) != 0 || len(r.Config.Tools) != 0 {
			yield(nil, errors.New("live call/tool cap"))
			return
		}
		m.calls++
		messages := []map[string]string{{"role": "system", "content": r.Config.SystemInstruction.Parts[0].Text}}
		for _, c := range r.Contents {
			messages = append(messages, map[string]string{"role": "user", "content": c.Parts[0].Text})
		}
		wire := map[string]any{"model": m.name, "stream": false, "max_tokens": r.Config.MaxOutputTokens, "messages": messages, "response_format": map[string]any{"type": "json_schema", "json_schema": map[string]any{"name": "response", "strict": true, "schema": r.Config.ResponseJsonSchema}}}
		m.save(fmt.Sprintf("request-%d.json", m.calls), wire)
		body, _ := json.Marshal(wire)
		req, e := http.NewRequestWithContext(ctx, "POST", strings.TrimRight(m.base, "/")+"/chat/completions", bytes.NewReader(body))
		if e != nil {
			yield(nil, errors.New("request setup failed"))
			return
		}
		req.Header.Set("Authorization", "Bearer "+m.key)
		req.Header.Set("Content-Type", "application/json")
		response, e := m.client.Do(req)
		if e != nil {
			yield(nil, errors.New("provider transport failed"))
			return
		}
		defer response.Body.Close()
		raw, e := io.ReadAll(io.LimitReader(response.Body, 2<<20))
		if e != nil {
			yield(nil, errors.New("provider read failed"))
			return
		}
		if e = os.WriteFile(filepath.Join(m.dir, fmt.Sprintf("response-%d.json", m.calls)), raw, 0600); e != nil {
			m.t.Fatal(e)
		}
		if response.StatusCode != 200 {
			yield(nil, fmt.Errorf("provider status %d", response.StatusCode))
			return
		}
		var parsed struct {
			Choices []struct {
				Message struct {
					Content   string `json:"content"`
					ToolCalls []any  `json:"tool_calls"`
				} `json:"message"`
			} `json:"choices"`
			Usage struct {
				Input  int32 `json:"prompt_tokens"`
				Output int32 `json:"completion_tokens"`
			} `json:"usage"`
		}
		if json.Unmarshal(raw, &parsed) != nil || len(parsed.Choices) != 1 {
			yield(nil, errors.New("invalid provider envelope"))
			return
		}
		if len(parsed.Choices[0].Message.ToolCalls) != 0 {
			yield(nil, errors.New("provider attempted tools"))
			return
		}
		yield(&model.LLMResponse{Content: genai.NewContentFromText(parsed.Choices[0].Message.Content, "model"), UsageMetadata: &genai.GenerateContentResponseUsageMetadata{PromptTokenCount: parsed.Usage.Input, CandidatesTokenCount: parsed.Usage.Output}}, nil)
	}
}
func TestBotWorkFormatRecoveryLive(t *testing.T) {
	dir := os.Getenv("ASTONISH_FORMAT_REPRO_DIR")
	if dir == "" {
		t.Skip("opt-in private saved-work model reproduction")
	}
	read := func(p string) []byte {
		b, e := os.ReadFile(p)
		if e != nil {
			t.Fatal(e)
		}
		return b
	}
	var runs struct {
		Runs []store.BotRun `json:"runs"`
	}
	if e := json.Unmarshal(read(filepath.Join(dir, "employee-runs.json")), &runs); e != nil || len(runs.Runs) != 1 {
		t.Fatal("missing exact failed run")
	}
	paths, e := filepath.Glob(filepath.Join(dir, "data/orgs/*/personal/*.db"))
	if e != nil || len(paths) != 1 {
		t.Fatal("expected one personal database")
	}
	db, e := sql.Open("sqlite", "file:"+paths[0]+"?mode=ro")
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	var data string
	if e = db.QueryRow("SELECT data FROM bot_runs WHERE id = ?", runs.Runs[0].ID.String()).Scan(&data); e != nil {
		t.Fatal(e)
	}
	var envelope struct {
		Document string `json:"bot_document"`
	}
	if e = json.Unmarshal([]byte(data), &envelope); e != nil {
		t.Fatal(e)
	}
	var doc struct {
		Work *store.BotWorkState `json:"work"`
	}
	if e = json.Unmarshal([]byte(envelope.Document), &doc); e != nil {
		t.Fatal(e)
	}
	if doc.Work == nil || doc.Work.Candidate == nil || doc.Work.Candidate.Digest != runs.Runs[0].Report.Quality.CandidateDigest {
		t.Fatal("missing exact saved candidate")
	}
	var cfg struct {
		General struct {
			Provider string `yaml:"default_provider"`
			Model    string `yaml:"default_model"`
		} `yaml:"general"`
		Providers map[string]map[string]string `yaml:"providers"`
	}
	if yaml.Unmarshal(read(filepath.Join(dir, "config/astonish/config.yaml")), &cfg) != nil {
		t.Fatal("invalid config")
	}
	p := cfg.Providers[cfg.General.Provider]
	base := p["base_url"]
	if base == "" {
		base = p["url"]
	}
	if base == "" || p["api_key"] == "" {
		t.Fatal("missing provider config")
	}
	outdir := filepath.Join(dir, "format-recovery-model-only")
	if e = os.MkdirAll(outdir, 0700); e != nil {
		t.Fatal(e)
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, e := net.SplitHostPort(address)
		if e == nil && host == "host.docker.internal" {
			address = net.JoinHostPort("127.0.0.1", port)
		}
		return (&net.Dialer{}).DialContext(ctx, network, address)
	}
	m := &recoveryLiveModel{t: t, dir: outdir, base: base, key: p["api_key"], name: cfg.General.Model, client: &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	m.save("saved-work.json", doc.Work)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	limit := int64(1000000)
	budget, e := bot.NewRunBudget(store.BudgetSpec{MaxRuntimeSeconds: 120, MaxTokens: &limit}, time.Now())
	if e != nil {
		t.Fatal(e)
	}
	llm, e := provider.NewBudgetLLM(m, budget, provider.BudgetLLMOptions{MaxOutputTokens: 8192, SupportsOutputLimit: true})
	if e != nil {
		t.Fatal(e)
	}
	req := bot.RunRequest{Budget: budget}
	var usage store.BotUsage
	review, err := reviewBotCandidate(ctx, req, llm, doc.Work, &usage)
	summary := map[string]any{"actual_model_calls": m.calls, "tool_calls": 0, "producer_calls": 0, "candidate_digest": doc.Work.Candidate.Digest, "saved_work_revision": doc.Work.Revision, "model": m.name, "usage": usage, "strict_semantic_valid": err == nil}
	if err != nil {
		summary["error"] = err.Error()
	}
	m.save("summary.json", summary)
	if review != nil {
		m.save("review.json", review)
	}
	b, _ := json.Marshal(summary)
	t.Log(string(b))
	if err != nil {
		t.Fatalf("saved-work assessment did not validate: %v", err)
	}
}
