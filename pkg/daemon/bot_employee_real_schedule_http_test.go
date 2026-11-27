package daemon

import (
	"bufio"
	"context"
	"encoding/json"
	"iter"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"bytes"
	"github.com/SAP/astonish/pkg/provider/litellm"
	"google.golang.org/adk/model"
	"net/http"
)

type realScheduleHTTPOptions struct{ model *realScheduleHTTPModel }
type realScheduleHTTPModel struct {
	model.LLM
	t                                                                  *testing.T
	calls, pendingReviews, finals, employeeCalls, progressiveSchedules int
	searched, described                                                bool
	beforeFinal                                                        func(*model.LLMRequest)
}

func (m *realScheduleHTTPModel) GenerateContent(ctx context.Context, r *model.LLMRequest, stream bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		m.calls++
		prompt := ""
		if r.Config != nil && r.Config.SystemInstruction != nil {
			for _, p := range r.Config.SystemInstruction.Parts {
				prompt += p.Text
			}
		}
		stage := "producer"
		if strings.HasPrefix(prompt, "BOT_WORK_PENDING_REVIEW_V1") {
			m.pendingReviews++
			stage = "pending_review"
		}
		if strings.Contains(prompt, "BOT_EMPLOYEE_PRESENTATION_V1") {
			m.finals++
			stage = "employee_final"
			if len(r.Tools) != 0 {
				m.t.Fatal("final exposes tools")
			}
			if m.beforeFinal == nil {
				m.t.Fatal("final without application fence")
			}
			m.beforeFinal(r)
		}
		m.t.Logf("REAL MODEL call=%d stage=%s tools=%d prompt_marker=%s", m.calls, stage, len(r.Tools), realSchedulePromptMarker(prompt))
		for response, err := range m.LLM.GenerateContent(ctx, r, stream) {
			if err != nil {
				if response != nil {
					m.t.Logf("REAL MODEL rejected stage=%s error_type=%T finish_reason=%s interrupted=%t", stage, err, response.FinishReason, response.Interrupted)
				} else {
					m.t.Logf("REAL MODEL rejected stage=%s error_type=%T", stage, err)
				}
				yield(response, contextSafeRealScheduleError{})
				return
			}
			if response != nil && response.Content != nil {
				for _, p := range response.Content.Parts {
					if p.Text != "" && !response.Partial {
						m.t.Logf("REAL MODEL text=%s", p.Text)
					}
					if c := p.FunctionCall; c != nil {
						m.t.Logf("REAL MODEL tool=%s", c.Name)
						switch c.Name {
						case "employee_chat":
							m.employeeCalls++
						case "search_tools":
							if strings.Contains(strings.ToLower(string(mustRealScheduleJSON(c.Args))), "schedule") {
								m.searched = true
							}
						case "describe_tools":
							if strings.Contains(string(mustRealScheduleJSON(c.Args)), "schedule_job") {
								m.described = true
							}
						case "execute_tool":
							if c.Args["name"] == "schedule_job" {
								if !m.searched || !m.described {
									m.t.Fatal("schedule executed without native discovery/schema inspection")
								}
								m.progressiveSchedules++
							}
						case "schedule_job", "propose_bot_control":
							m.t.Fatal("schedule bypassed required progressive path")
						}
					}
				}
			}
			if !yield(response, err) {
				return
			}
		}
	}
}

func realSchedulePromptMarker(prompt string) string {
	for _, marker := range []string{"BOT_WORK_PENDING_REVIEW_V1", "BOT_EMPLOYEE_PRESENTATION_V1", "BOT_EMPLOYEE_REVIEW_V1"} {
		if strings.Contains(prompt, marker) {
			return marker
		}
	}
	return "none"
}

type contextSafeRealScheduleError struct{}

func (contextSafeRealScheduleError) Error() string {
	return "real provider call failed (details withheld)"
}
func mustRealScheduleJSON(v any) []byte { raw, _ := json.Marshal(v); return raw }
func (m *realScheduleHTTPModel) assert(t *testing.T) {
	t.Helper()
	if m.pendingReviews != 0 || m.finals != 1 || m.employeeCalls == 0 || m.progressiveSchedules != 1 {
		t.Fatalf("missing strict all-real sequence: reviews=%d finals=%d employee=%d schedule=%d", m.pendingReviews, m.finals, m.employeeCalls, m.progressiveSchedules)
	}
}
func realScheduleHTTPStage(t *testing.T, parent context.Context, name string) (context.Context, func()) {
	t.Helper()
	ctx, cancel := context.WithTimeout(parent, 2*time.Minute)
	start := time.Now()
	t.Logf("REAL stage=%s", name)
	return ctx, func() {
		t.Helper()
		defer cancel()
		t.Logf("REAL stage=%s elapsed=%s", name, time.Since(start))
		if ctx.Err() != nil {
			t.Error(ctx.Err())
		}
	}
}
func realSchedulePublicationID(t *testing.T, raw []byte, text string) string {
	t.Helper()
	s := bufio.NewScanner(strings.NewReader(string(raw)))
	s.Buffer(make([]byte, 65536), 4<<20)
	kind, id, count := "", "", 0
	for s.Scan() {
		line := s.Text()
		if strings.HasPrefix(line, "event: ") {
			kind = strings.TrimPrefix(line, "event: ")
		}
		if kind != "text" || !strings.HasPrefix(line, "data: ") {
			continue
		}
		var f struct {
			Text       string `json:"text"`
			Projection struct {
				EventID string `json:"event_id"`
			} `json:"_projection"`
		}
		if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &f) != nil {
			t.Fatal("invalid history frame")
		}
		if f.Text == text {
			count++
			id = f.Projection.EventID
		}
	}
	if s.Err() != nil || count != 1 || id == "" || text == "" {
		t.Fatalf("expected exactly one projected Employee reply: count=%d id=%s", count, id)
	}
	return id
}

// Paid real provider, actual HTTP admission and production worker/factory/policy,
// temporary SQLite authority. Home/files compute transport is fake, not a physical sandbox.
func realScheduleLiveCapture(ctx context.Context, client *http.Client, url string) (func(string) []byte, error) {
	liveCtx, cancel := context.WithCancel(ctx)
	request, err := http.NewRequestWithContext(liveCtx, http.MethodGet, url, nil)
	if err != nil {
		cancel()
		return nil, err
	}
	response, err := client.Do(request)
	if err != nil {
		cancel()
		return nil, err
	}
	if response.StatusCode != http.StatusOK {
		response.Body.Close()
		cancel()
		return nil, contextSafeRealScheduleError{}
	}
	var raw bytes.Buffer
	done := make(chan struct{})
	frames := make(chan string, 256)
	go func() {
		defer close(done)
		defer close(frames)
		defer response.Body.Close()
		scanner := bufio.NewScanner(response.Body)
		scanner.Buffer(make([]byte, 65536), 4<<20)
		for scanner.Scan() {
			line := scanner.Text()
			raw.WriteString(line + "\n")
			if strings.HasPrefix(line, "data: ") {
				var frame struct {
					Text string `json:"text"`
				}
				if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &frame) == nil && frame.Text != "" {
					select {
					case frames <- frame.Text:
					case <-liveCtx.Done():
						return
					}
				}
			}
		}
	}()
	return func(text string) []byte {
		if text != "" {
			for {
				select {
				case observed, ok := <-frames:
					if !ok || observed == text {
						goto finish
					}
				case <-liveCtx.Done():
					goto finish
				}
			}
		}
	finish:
		cancel()
		response.Body.Close() // Unblock Scan even when a transport does not propagate cancellation.
		<-done
		return raw.Bytes()
	}, nil
}

// Receipt/job assertions remain authoritative; this additionally rejects a final
// that describes current application as pending without rejecting truthful history
// or the fact that the first scheduled delivery has not happened yet.
func realScheduleFinalReportsApplied(text string) bool {
	lower := strings.ToLower(text)
	lower = strings.ReplaceAll(lower, "scheduling health is normal; pending actions await verified receipts", "[briefing text]")
	cadence := strings.Contains(lower, "5 minute") || strings.Contains(lower, "5-minute") || strings.Contains(lower, "five minute")
	positive := false
	for _, sentence := range strings.Split(strings.ReplaceAll(lower, "\n", "."), ".") {
		if strings.Contains(sentence, "earlier") {
			continue // Historical capture wording is not the current outcome.
		}
		for _, negation := range []string{"not enabled", "not active", "not scheduled", "not applied", "not yet enabled", "not yet applied"} {
			if strings.Contains(sentence, negation) {
				return false
			}
		}
		if strings.Contains(sentence, "pending") || strings.Contains(sentence, "queued") {
			return false
		}
		if strings.Contains(sentence, "not yet") && !strings.Contains(sentence, "delivery") && !strings.Contains(sentence, "briefing") {
			return false
		}
		positive = positive || strings.Contains(sentence, "enabled") || strings.Contains(sentence, "active") || strings.Contains(sentence, "scheduled")
	}
	return cadence && positive
}

func TestBotEmployeeRealScheduleHTTPAcceptance(t *testing.T) {
	if os.Getenv("ASTONISH_REAL_SCHEDULE_HTTP") != "1" {
		t.Skip("paid real-model acceptance requires explicit opt-in")
	}
	raw, err := os.ReadFile(filepath.Join("../..", ".tmp", "employee-native-config.json"))
	if err != nil {
		t.Fatal("private provider configuration unavailable")
	}
	var cfg struct {
		URL   string `json:"provider_url"`
		Key   string `json:"provider_api_key"`
		Model string `json:"model"`
	}
	if json.Unmarshal(raw, &cfg) != nil || cfg.URL == "" || cfg.Key == "" || cfg.Model == "" {
		t.Fatal("explicit provider URL/key/model required")
	}
	if cfg.URL == "http://host.docker.internal:4000" {
		cfg.URL = "http://127.0.0.1:4000"
	}
	m := &realScheduleHTTPModel{LLM: litellm.NewProvider(cfg.Key, cfg.URL, cfg.Model), t: t}
	testBotAuthorizedScheduleHTTPScenario(t, false, false, true, "", &realScheduleHTTPOptions{model: m})
}
