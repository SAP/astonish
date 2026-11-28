package api

// Opt-in, model-only experiment. No Bot, daemon, database, or tool execution.
import (
	"bytes"
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/store"
	"gopkg.in/yaml.v3"
)

func TestBotWorkModelOnlyReproduction(t *testing.T) {
	dir := os.Getenv("ASTONISH_REVIEW_REPRO_DIR")
	if dir == "" {
		t.Skip("opt-in private model-only reproduction")
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
		Runs []struct {
			Data string `json:"data"`
		} `json:"runs"`
	}
	temporal := os.Getenv("ASTONISH_REVIEW_REPRO_TEMPORAL") == "1"
	prepareOnly := os.Getenv("ASTONISH_REVIEW_REPRO_PREPARE") == "1"
	inputFile := "employee-legacy-schedule-disk.json"
	if prepareOnly {
		inputFile = "prepare-failure-disk.json"
	}
	if temporal {
		inputFile = "employee-quality-failure-disk.json"
	}
	decode(read(filepath.Join(dir, inputFile)), &disk)

	if len(disk.Runs) != 1 {
		t.Fatal("expected one saved failing run")
	}
	var data struct {
		Document string `json:"bot_document"`
	}
	decode([]byte(disk.Runs[0].Data), &data)
	var doc struct {
		Work *store.BotWorkState `json:"work"`
		Run  map[string]any      `json:"Run"`
	}
	decode([]byte(data.Document), &doc)
	if doc.Work == nil || (!prepareOnly && doc.Work.Candidate == nil) {
		t.Fatal("missing saved candidate")
	}
	w := doc.Work.Clone()
	w.Phase = store.BotWorkReviewing
	w.Unresolved = nil
	if temporal {
		w.Review = nil
	}
	var input map[string]any
	raw, _ := json.Marshal(w)
	decode(raw, &input)
	var controls []botWorkPendingControl
	if prepareOnly {
		w.Phase = store.BotWorkProducing
	}
	if w.Candidate != nil {
		for k, b := range w.Candidate.Bodies {
			if strings.HasPrefix(k, "pending-control:") {
				var c botWorkPendingControl
				decode([]byte(b), &c)
				controls = append(controls, c)
			}
		}
	}
	if !prepareOnly && len(controls) != 1 {
		t.Fatal("expected exactly one saved pending control")
	}
	input["scope"] = botWorkCurrentTurnPendingAcknowledgment
	input["trusted_pending_controls"] = controls
	input["receipt_only"] = true
	raw, _ = json.Marshal(input)
	// Extract the exact production guidance literal, without copying or editing it.
	tree, e := parser.ParseFile(token.NewFileSet(), "../launcher/bot_control_context.go", nil, 0)
	if e != nil {
		t.Fatal(e)
	}
	guidance := ""
	ast.Inspect(tree, func(n ast.Node) bool {
		if l, ok := n.(*ast.BasicLit); ok && l.Kind == token.STRING {
			s, _ := strconv.Unquote(l.Value)
			if strings.Contains(s, "The following JSON is current owner-scoped factual metadata") {
				guidance = s
			}
		}
		return true
	})
	if guidance == "" {
		t.Fatal("production guidance not found")
	}
	// Metadata is reconstructed from the saved failed Work; not claimed to be a
	// byte-for-byte capture of transient repository metadata at original dispatch.
	facts := map[string]any{"bot_id": w.Contract.BotID, "run_id": w.Contract.RunID, "user_event_id": doc.Run["user_event_id"], "intent": doc.Run["intent"], "execution_mode": doc.Run["execution_mode"], "lifecycle_status": "active", "scope": "personal", "version": w.Contract.Version, "revision": 3, "triggers": []any{}, "receipts": []any{}, "current_goals": []any{}, "goals_available": true, "prior_committed_findings": []any{}, "findings_available": true}
	factsRaw, _ := json.Marshal(facts)
	suffix := os.Getenv("ASTONISH_REVIEW_REPRO_SUFFIX")
	if temporal {
		suffix += "-temporal"
	}
	outdir := filepath.Join(dir, "review-model-reproduction"+suffix)
	if e := os.MkdirAll(outdir, 0700); e != nil {
		t.Fatal(e)
	}
	save := func(name string, b []byte) {
		if e := os.WriteFile(filepath.Join(outdir, name), b, 0600); e != nil {
			t.Fatal(e)
		}
	}
	var cfg struct {
		General struct {
			Provider string `yaml:"default_provider"`
			Model    string `yaml:"default_model"`
		} `yaml:"general"`
		Providers map[string]map[string]string `yaml:"providers"`
	}
	if e := yaml.Unmarshal(read(filepath.Join(dir, "config/astonish/config.yaml")), &cfg); e != nil {
		t.Fatal("invalid staged config")
	}
	p := cfg.Providers[cfg.General.Provider]
	base := p["base_url"]
	if base == "" {
		base = p["url"]
	}
	key := p["api_key"]
	if base == "" || key == "" || cfg.General.Model == "" {
		t.Fatal("staged provider config missing required fields (values suppressed)")
	}
	summaries := []map[string]any{}
	for _, enriched := range []bool{true, false} {
		name := "assessor-only"
		system := botWorkPendingReviewPrompt
		if enriched && !temporal {
			name = "producer-enriched"
			system += "\n" + guidance + string(factsRaw) + "\n"
		}
		schemaPrompt := botWorkPendingReviewPrompt
		if temporal && enriched {
			name = "temporal-extraction"
			system = botWorkPreparePrompt
			schemaPrompt = botWorkPreparePrompt
			raw, _ = json.Marshal(w.Contract)
		} else if temporal {
			name = "temporal-review"
			rawWork, _ := json.Marshal(w)
			decode(rawWork, &input)
			input["scope"] = botWorkCurrentTurnPendingAcknowledgment
			input["trusted_pending_controls"] = controls
			input["receipt_only"] = true
			raw, _ = json.Marshal(input)
		}
		if prepareOnly {
			if !enriched {
				break
			}
			name = "prepare-extraction"
			system = botWorkPreparePrompt
			schemaPrompt = botWorkPreparePrompt
			raw, _ = json.Marshal(w.Contract)
		}
		request := map[string]any{"model": cfg.General.Model, "stream": false, "max_tokens": 8192, "messages": []map[string]string{{"role": "system", "content": system}, {"role": "user", "content": string(raw)}}, "response_format": map[string]any{"type": "json_schema", "json_schema": map[string]any{"name": "response", "strict": true, "schema": botWorkResponseSchema(schemaPrompt)}}}
		if os.Getenv("ASTONISH_REVIEW_REPRO_NO_FORMAT") == "1" {
			delete(request, "response_format")
		}
		requestRaw, _ := json.Marshal(request)
		save(name+"-request.json", requestRaw)
		var responseRaw []byte
		status := 200
		if os.Getenv("ASTONISH_REVIEW_REPRO_OFFLINE") == "1" {
			responseRaw = read(filepath.Join(outdir, name+"-response.json"))
		} else {
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			req, e := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(base, "/")+"/chat/completions", bytes.NewReader(requestRaw))
			if e != nil {
				cancel()
				t.Fatal("request setup failed")
			}
			req.Header.Set("Authorization", "Bearer "+key)
			req.Header.Set("Content-Type", "application/json")
			// Exactly one HTTP attempt, no application retries or redirects.
			// Staged daemon config uses Docker's host alias. On the host this
			// resolves to loopback, preserving the exact provider service/port.
			transport := http.DefaultTransport.(*http.Transport).Clone()
			transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
				host, port, err := net.SplitHostPort(address)
				if err == nil && host == "host.docker.internal" {
					address = net.JoinHostPort("127.0.0.1", port)
				}
				return (&net.Dialer{}).DialContext(ctx, network, address)
			}
			client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
			resp, e := client.Do(req)
			if e != nil {
				cancel()
				if ue, ok := e.(*url.Error); ok {
					t.Fatalf("provider transport failed: %v", ue.Err)
				}
				t.Fatal("provider request failed; details suppressed to protect config")
			}
			status = resp.StatusCode
			responseRaw, e = io.ReadAll(io.LimitReader(resp.Body, 2<<20))
			resp.Body.Close()
			cancel()
			if e != nil {
				t.Fatal("response read failed")
			}
			save(name+"-response.json", responseRaw)
		}
		var response struct {
			Choices []struct {
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
			} `json:"choices"`
		}
		decode(responseRaw, &response)
		text := ""
		if len(response.Choices) > 0 {
			text = response.Choices[0].Message.Content
		}
		save(name+"-text.txt", []byte(text))
		if prepareOnly {
			var extraction struct {
				Criteria []store.BotWorkCriterion `json:"criteria"`
			}
			decode([]byte(text), &extraction)
			w.Criteria = extraction.Criteria
			if err := w.Validate(); err != nil {
				t.Fatalf("prepare extraction invalid: %v", err)
			}
			t.Logf("prepare extraction valid: %d criteria", len(w.Criteria))
			return
		}
		if temporal && enriched {
			var extraction struct {
				Criteria []store.BotWorkCriterion `json:"criteria"`
			}
			decode([]byte(text), &extraction)
			w.Criteria = extraction.Criteria
			if err := w.Validate(); err != nil {
				t.Fatalf("extracted scope invalid: %v", err)
			}
			future := 0
			for _, c := range w.Criteria {
				if c.FulfillmentScope == store.BotWorkCriterionFutureTaskDefinition {
					future++
				}
			}
			if future == 0 {
				t.Fatal("no future-task standards extracted")
			}
			t.Logf("live extraction: %d criteria, %d future-task definitions; original contract and full candidate retained", len(w.Criteria), future)
			continue
		}
		var assessment struct {
			Current  store.BotWorkReview `json:"current_turn"`
			Deferred store.BotWorkReview `json:"deferred_application"`
		}
		dec := json.NewDecoder(strings.NewReader(text))
		dec.DisallowUnknownFields()
		parseErr := dec.Decode(&assessment)
		valid := parseErr == nil
		var extra any
		if valid {
			valid = dec.Decode(&extra) == io.EOF
		}
		coverage := valid
		if valid {
			for _, review := range []*store.BotWorkReview{&assessment.Current, &assessment.Deferred} {
				next := w.Clone()
				next.Review = review
				if next.Validate() != nil {
					coverage = false
				}
			}
		}
		if temporal {
			if !coverage {
				t.Fatal("temporal review identity/coverage invalid")
			}
			for _, c := range assessment.Current.Criteria {
				if c.Verdict != store.BotCriterionSatisfied {
					t.Errorf("current criterion %s: %s: %s", c.CriterionID, c.Verdict, c.Explanation)
				}
			}
			unapplied := false
			for _, c := range assessment.Deferred.Criteria {
				if c.Verdict != store.BotCriterionSatisfied {
					unapplied = true
				}
			}
			if !unapplied {
				t.Error("pending application incorrectly accepted")
			}
		}
		summary := map[string]any{"case": name, "http_status": status, "json_valid": json.Valid([]byte(text)), "strict_shape": valid, "identity_coverage_valid": coverage, "response_bytes": len(text), "system_bytes": len(system), "guidance_reconstructed_metadata": !temporal}
		summaries = append(summaries, summary)
		b, _ := json.Marshal(summary)
		t.Log(string(b))
		if status != 200 || len(response.Choices) == 0 {
			t.Error("provider response unsuccessful (private body saved)")
		}
	}
	summaryRaw, _ := json.MarshalIndent(summaries, "", "  ")
	save("summary.json", summaryRaw)
}
