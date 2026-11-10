package api

// Opt-in saved-candidate assessment only: no daemon, database writes or tools.
import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"gopkg.in/yaml.v3"
)

func TestBotWorkHomeModelOnlyReproduction(t *testing.T) {
	dir := os.Getenv("ASTONISH_HOME_REVIEW_REPRO_DIR")
	if dir == "" {
		t.Skip("opt-in single-call saved Home assessment")
	}
	read := func(path string) []byte {
		b, e := os.ReadFile(path)
		if e != nil {
			t.Fatal(e)
		}
		return b
	}
	decode := func(b []byte, v any) {
		if e := json.Unmarshal(b, v); e != nil {
			t.Fatal(e)
		}
	}
	var disk struct {
		Bots []struct {
			ID   uuid.UUID `json:"id"`
			Home uuid.UUID `json:"home_session_id"`
		} `json:"bots"`
		Runs []struct {
			Data string `json:"data"`
		} `json:"runs"`
	}
	decode(read(filepath.Join(dir, "employee-offline-restart-tick.json")), &disk)
	var work *store.BotWorkState
	for _, row := range disk.Runs {
		var data struct {
			Document string `json:"bot_document"`
		}
		decode([]byte(row.Data), &data)
		var doc struct {
			Work *store.BotWorkState `json:"work"`
			Run  store.BotRun        `json:"Run"`
		}
		decode([]byte(data.Document), &doc)
		if doc.Work != nil && doc.Work.Candidate != nil && strings.HasPrefix(doc.Work.Contract.RunID.String(), "9bbb") {
			work = doc.Work.Clone()
			break
		}
	}
	if work == nil {
		t.Fatal("recorded cron 9bbb candidate missing")
	}
	req := bot.RunRequest{}
	req.Bot.ID = work.Contract.BotID
	for _, b := range disk.Bots {
		if b.ID == req.Bot.ID {
			req.Bot.HomeSessionID = b.Home
		}
	}
	if req.Bot.HomeSessionID == uuid.Nil {
		t.Fatal("recorded trusted Home missing")
	}
	digest := work.Candidate.Digest
	work.Phase = store.BotWorkReviewing
	work.Review = nil
	work.Unresolved = nil
	input, e := botWorkAssessmentInput(req, work)
	if e != nil {
		t.Fatal(e)
	}
	var cfg struct {
		General struct {
			Provider string `yaml:"default_provider"`
			Model    string `yaml:"default_model"`
		} `yaml:"general"`
		Providers map[string]map[string]string `yaml:"providers"`
	}
	if e := yaml.Unmarshal(read(filepath.Join(dir, "config/astonish/config.yaml")), &cfg); e != nil {
		t.Fatal("invalid private provider config")
	}
	p := cfg.Providers[cfg.General.Provider]
	base := p["base_url"]
	if base == "" {
		base = p["url"]
	}
	if base == "" || p["api_key"] == "" || cfg.General.Model == "" {
		t.Fatal("private provider configuration incomplete")
	}
	request := map[string]any{"model": cfg.General.Model, "stream": false, "max_tokens": 8192, "messages": []map[string]string{{"role": "system", "content": botWorkReviewPrompt}, {"role": "user", "content": string(input)}}, "response_format": map[string]any{"type": "json_schema", "json_schema": map[string]any{"name": "response", "strict": true, "schema": botWorkResponseSchema(botWorkReviewPrompt)}}}
	raw, _ := json.Marshal(request)
	out := filepath.Join(dir, "home-publication-model-reproduction")
	if e := os.MkdirAll(out, 0700); e != nil {
		t.Fatal(e)
	}
	save := func(name string, b []byte) {
		if e := os.WriteFile(filepath.Join(out, name), b, 0600); e != nil {
			t.Fatal(e)
		}
	}
	save("request.json", raw)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	httpReq, e := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(base, "/")+"/chat/completions", bytes.NewReader(raw))
	if e != nil {
		t.Fatal("provider request setup failed")
	}
	httpReq.Header.Set("Authorization", "Bearer "+p["api_key"])
	httpReq.Header.Set("Content-Type", "application/json")
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, e := net.SplitHostPort(address)
		if e == nil && host == "host.docker.internal" {
			address = net.JoinHostPort("127.0.0.1", port)
		}
		return (&net.Dialer{}).DialContext(ctx, network, address)
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, e := client.Do(httpReq)
	if e != nil {
		t.Fatal("provider transport failed (private details suppressed)")
	}
	defer response.Body.Close()
	body, e := io.ReadAll(io.LimitReader(response.Body, 2<<20))
	if e != nil {
		t.Fatal(e)
	}
	save("response.json", body)
	if response.StatusCode != 200 {
		t.Fatalf("provider status %d", response.StatusCode)
	}
	var envelope struct {
		Choices []struct {
			Message struct {
				Content   string            `json:"content"`
				ToolCalls []json.RawMessage `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}
	decode(body, &envelope)
	if len(envelope.Choices) != 1 || len(envelope.Choices[0].Message.ToolCalls) != 0 {
		t.Fatal("unexpected response or tool call")
	}
	var review store.BotWorkReview
	dec := json.NewDecoder(strings.NewReader(envelope.Choices[0].Message.Content))
	dec.DisallowUnknownFields()
	if e := dec.Decode(&review); e != nil {
		t.Fatal(e)
	}
	work.Review = &review
	if e := work.Validate(); e != nil {
		t.Fatal(e)
	}
	applyBotMechanicalChecks(work, &review)
	for _, c := range review.Criteria {
		if c.Verdict != store.BotCriterionSatisfied {
			t.Errorf("%s: %s: %s", c.CriterionID, c.Verdict, c.Explanation)
		}
	}
	if work.Candidate.ContentDigest() != digest {
		t.Fatal("recorded candidate altered")
	}
	save("review.json", []byte(envelope.Choices[0].Message.Content))
	t.Logf("one model call, zero tools; %d original criteria reviewed; original candidate %s; readiness only, no delivery receipt minted", len(review.Criteria), digest)
}
