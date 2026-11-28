package api

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"gopkg.in/yaml.v3"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Opt-in private capture: one HTTP request, zero tools, no database or run writes.
func TestBotWorkCitationModelOnlyReproduction(t *testing.T) {
	dir := os.Getenv("ASTONISH_CITATION_REPRO_DIR")
	if dir == "" {
		t.Skip("private saved-work model-only capture")
	}
	read := func(p string) []byte {
		b, e := os.ReadFile(filepath.Join(dir, p))
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
	inputFile := os.Getenv("ASTONISH_CITATION_REPRO_FILE")
	if inputFile == "" {
		inputFile = "employee-quality-idle.json"
	}
	decode(read(inputFile), &disk)
	var w *store.BotWorkState
	for _, r := range disk.Runs {
		var d struct {
			Document string `json:"bot_document"`
		}
		decode([]byte(r.Data), &d)
		var doc struct {
			Work *store.BotWorkState `json:"work"`
		}
		decode([]byte(d.Document), &doc)
		if doc.Work != nil {
			t.Logf("saved run %s phase=%s candidate=%t", doc.Work.Contract.RunID, doc.Work.Phase, doc.Work.Candidate != nil)
		}
		if doc.Work != nil && doc.Work.Candidate != nil && doc.Work.Phase != store.BotWorkAccepted {
			w = doc.Work.Clone()
			break
		}
	}
	if w == nil {
		t.Fatal("saved citation failure not found")
	}
	req := bot.RunRequest{}
	req.Bot.ID = w.Contract.BotID
	for _, b := range disk.Bots {
		if b.ID == req.Bot.ID {
			req.Bot.HomeSessionID = b.Home
		}
	}
	w.Phase = store.BotWorkReviewing
	w.Review = nil
	w.Unresolved = nil
	prompt := botWorkReviewPrompt
	var assessmentInput any = w
	var controls []botWorkPendingControl
	for key, body := range w.Candidate.Bodies {
		if strings.HasPrefix(key, "pending-control:") {
			var c botWorkPendingControl
			decode([]byte(body), &c)
			controls = append(controls, c)
		}
	}
	if len(controls) > 0 {
		prompt = botWorkPendingReviewPrompt
		assessmentInput = struct {
			*store.BotWorkState
			Scope       botWorkReviewScope      `json:"scope"`
			Controls    []botWorkPendingControl `json:"trusted_pending_controls"`
			ReceiptOnly bool                    `json:"receipt_only"`
		}{w, botWorkCurrentTurnPendingAcknowledgment, controls, w.Candidate.Text == botPendingAcknowledgment(controls) && len(w.Candidate.Artifacts) == 0}
	}
	input, e := botWorkAssessmentInput(req, assessmentInput)
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
	if yaml.Unmarshal(read("config/astonish/config.yaml"), &cfg) != nil {
		t.Fatal("private config invalid")
	}
	p := cfg.Providers[cfg.General.Provider]
	base := p["base_url"]
	if base == "" {
		base = p["url"]
	}
	if base == "" || p["api_key"] == "" {
		t.Fatal("private provider config incomplete")
	}
	request := map[string]any{"model": cfg.General.Model, "stream": false, "max_tokens": 8192, "messages": []map[string]string{{"role": "system", "content": prompt}, {"role": "user", "content": string(input)}}, "response_format": map[string]any{"type": "json_schema", "json_schema": map[string]any{"name": "response", "strict": true, "schema": botWorkResponseSchema(prompt, input)}}}
	raw, _ := json.Marshal(request)
	out := filepath.Join(dir, "citation-model-reproduction")
	if os.MkdirAll(out, 0700) != nil {
		t.Fatal("private directory failed")
	}
	save := func(n string, b []byte) {
		if os.WriteFile(filepath.Join(out, n), b, 0600) != nil {
			t.Fatal("private capture failed")
		}
	}
	save("request.json", raw)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	h, e := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(base, "/")+"/chat/completions", bytes.NewReader(raw))
	if e != nil {
		t.Fatal("request setup failed")
	}
	h.Header.Set("Authorization", "Bearer "+p["api_key"])
	h.Header.Set("Content-Type", "application/json")
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, e := net.SplitHostPort(address)
		if e == nil && host == "host.docker.internal" {
			address = net.JoinHostPort("127.0.0.1", port)
		}
		return (&net.Dialer{}).DialContext(ctx, network, address)
	}
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, e := client.Do(h)
	if e != nil {
		t.Fatal("provider transport failed (details suppressed)")
	}
	defer resp.Body.Close()
	body, e := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if e != nil {
		t.Fatal(e)
	}
	save("response.json", body)
	if resp.StatusCode != 200 {
		t.Fatalf("HTTP %d", resp.StatusCode)
	}
	var envelope struct {
		Choices []struct {
			Message struct {
				Content string            `json:"content"`
				Tools   []json.RawMessage `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}
	decode(body, &envelope)
	if len(envelope.Choices) != 1 || len(envelope.Choices[0].Message.Tools) != 0 {
		t.Fatal("unexpected response/tool call")
	}
	var review store.BotWorkReview
	decode([]byte(envelope.Choices[0].Message.Content), &review)
	if len(controls) > 0 {
		var pending struct {
			Current store.BotWorkReview `json:"current_turn"`
		}
		decode([]byte(envelope.Choices[0].Message.Content), &pending)
		review = pending.Current
	}
	save("review.json", []byte(envelope.Choices[0].Message.Content))
	w.Review = &review
	valid := map[string]bool{}
	for _, r := range w.Candidate.AvailableEvidenceRefs() {
		valid[r] = true
	}
	unknown := 0
	for i, c := range review.Criteria {
		for j, r := range c.EvidenceRefs {
			if !valid[r] {
				unknown++
				t.Logf("unknown criteria[%d].evidence_refs[%d] URL=%t source_id=%t", i, j, strings.HasPrefix(r, "http"), r == "assignment" || r == "charter" || strings.HasPrefix(r, "goal:"))
			}
		}
	}
	t.Logf("one call, zero tools; run=%s criteria=%d unknown=%d validation=%v", w.Contract.RunID, len(review.Criteria), unknown, w.Validate())
}
