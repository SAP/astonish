package api_test

// Opt-in, one real sidecar design journey. The source database is read-only;
// every created Bot, approval and run belongs to the disposable fixture.
import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/model"
)

func sidecarOriginalAssignment(t *testing.T) string {
	t.Helper()
	db := os.Getenv("ASTONISH_SIDECAR_SOURCE_DB")
	if db == "" {
		t.Fatal("explicit read-only source DB required")
	}
	query := `SELECT json_extract(event_data,'$.Content.parts[0].text') FROM session_events WHERE session_id='a470d18e-41a5-4578-9bd8-dbe4b67060aa' AND json_extract(event_data,'$.Author')='user' AND json_extract(event_data,'$.Content.parts[0].text') LIKE 'In Astonish%';`
	raw, err := exec.Command("sqlite3", "-readonly", db, query).Output()
	if err != nil {
		t.Fatal("cannot read original assignment")
	}
	assignment := strings.TrimSuffix(string(raw), "\n")
	if !strings.HasPrefix(assignment, "In Astonish it has support with openshell,") || !strings.Contains(assignment, "without using a tool exposed to the AI agent") || len(assignment) != 1585 {
		t.Fatal("original assignment missing or not exact hard task")
	}
	return assignment
}

func sidecarHasEvidence(req *model.LLMRequest) bool {
	if len(req.Contents) != 1 || len(req.Contents[0].Parts) != 1 {
		return false
	}
	var in struct {
		Observation bot.ProgressObservation `json:"observation"`
	}
	if json.Unmarshal([]byte(req.Contents[0].Parts[0].Text), &in) != nil || !strings.Contains(strings.ToLower(in.Observation.Assignment), "sidecar") {
		return false
	}
	for _, excerpt := range in.Observation.Context {
		if excerpt.Kind == bot.ProgressContextTool && len(excerpt.Text) > 200 && strings.Contains(excerpt.Text, "http") && (excerpt.Tool == "perplexity_web_search" || excerpt.Tool == "web_fetch") {
			return true
		}
	}
	return false
}

// Section-level obligations are necessary coverage, not a security review. Each
// obligation needs a substantial section explaining related concepts together;
// a glossary or a bag of keywords is insufficient. UI branding and a particular
// word for uncertainty are not requirements of the original assignment.
func sidecarCoverage(summary string) []string {
	sections := regexp.MustCompile(`(?m)^#{1,6} `).Split(strings.ToLower(summary), -1)
	obligations := map[string][]string{
		"sidecar topology and workloads":                 {`openshell`, `sidecar`, `container|workload`, `network|egress`},
		"credential reference and injection boundary":    {`credential`, `opaque|placeholder|reference`, `inject`, `proxy|broker|supervisor`},
		"trusted human approval outside agent authority": {`approval|approve`, `human|operator`, `ui|control.plane`, `terminal|tui`, `agent`, `outside|not exposed|cannot|never|separat|must not`},
		"egress bypass and TLS threats":                  {`bypass|fail.closed`, `tls|https|connect`, `network|egress|proxy`},
		"integration assumptions and validation":         {`astonish|openshell`, `version|internals|assum|depend|verif|confirm`, `test|validat|spike|inspect`},
		"implementation and acceptance":                  {`implement|rollout|migration|phase`, `test|validat|acceptance`},
	}
	var missing []string
	if len(summary) < 7000 {
		missing = append(missing, "deep design length")
	}
	for label, patterns := range obligations {
		found := false
		for _, section := range sections {
			if len(section) < 200 {
				continue
			}
			match := true
			for _, pattern := range patterns {
				match = match && regexp.MustCompile(pattern).MatchString(section)
			}
			found = found || match
		}
		if !found {
			missing = append(missing, label)
		}
	}
	sort.Strings(missing)
	return missing
}

func TestBotRealSidecarBrowserPreflight(t *testing.T) {
	if os.Getenv("ASTONISH_SIDECAR_SOURCE_DB") == "" {
		t.Skip("explicit read-only assignment DB required")
	}
	assignment := sidecarOriginalAssignment(t)
	_ = realProgressBrowserConfig(t)
	t.Logf("exact original hard assignment loaded (%d bytes); no model calls", len(assignment))
}

func TestSidecarCoverageRejectsShallowAnswer(t *testing.T) {
	if len(sidecarCoverage("Use OpenShell as a sidecar proxy with credentials and approvals.")) == 0 {
		t.Fatal("shallow answer accepted")
	}
	// The old whole-document keyword check accepted a long unrelated body with
	// one glossary entry per required word. Related concepts must co-occur in
	// substantial explanatory sections, not merely exist somewhere in the text.
	glossary := "openshell sidecar container workload network egress credential opaque reference inject proxy approval human ui terminal agent outside bypass tls astonish version validation implement acceptance"
	var fragmented strings.Builder
	for _, term := range strings.Fields(glossary) {
		fragmented.WriteString("\n## " + term + "\n\n" + strings.Repeat("Unrelated filler sentence. ", 30))
	}
	if len(sidecarCoverage(fragmented.String())) < 6 {
		t.Fatal("fragmented keyword glossary accepted")
	}
}

// TestBotRealSidecarCapturedAssessment evaluates saved real responses without
// contacting a provider; it is not a replacement for live/reload acceptance.
func TestBotRealSidecarCapturedAssessment(t *testing.T) {
	dir := os.Getenv("ASTONISH_SIDECAR_CAPTURE_DIR")
	if dir == "" {
		t.Skip("explicit capture directory required; no provider calls")
	}
	read := func(name string, value any) {
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, value); err != nil {
			t.Fatal(err)
		}
	}
	var run store.BotRun
	read("run.json", &run)
	requests, err := filepath.Glob(filepath.Join(dir, "*-producer-request.json"))
	if err != nil {
		t.Fatal(err)
	}
	full := 0
	for _, path := range requests {
		var req model.LLMRequest
		read(filepath.Base(path), &req)
		if req.Config != nil && req.Config.MaxOutputTokens == 32768 {
			full++
		}
	}
	responses, err := filepath.Glob(filepath.Join(dir, "*-producer-response-*.json"))
	if err != nil {
		t.Fatal(err)
	}
	finishes := map[string]int{}
	for _, path := range responses {
		var captured struct {
			Response *model.LLMResponse `json:"response"`
		}
		read(filepath.Base(path), &captured)
		if captured.Response != nil {
			finishes[string(captured.Response.FinishReason)]++
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "design-summary.md"), []byte(run.Report.Summary), 0600); err != nil {
		t.Fatal(err)
	}
	t.Logf("BACKEND ONLY status=%s bytes=%d 32768_requests=%d finish_reasons=%v missing=%v", run.Status, len(run.Report.Summary), full, finishes, sidecarCoverage(run.Report.Summary))
	if run.Status != store.RunSucceeded || full == 0 {
		t.Fatal("missing completion/output request")
	}
}

func TestBotRealSidecarBrowserAcceptance(t *testing.T) {
	if os.Getenv("ASTONISH_REAL_SIDECAR_BROWSER") != "1" {
		t.Skip("explicit paid hard-task browser opt-in required")
	}
	assignment := sidecarOriginalAssignment(t)
	var fullOutputRequests, badOutputRequests atomic.Int64
	runLiveProgressBrowser(t, liveProgressScenario{
		title: "Astonish Sidecar Proxy", answer: assignment, prefix: "real-sidecar-browser", evaluationInterval: 5,
		evidence: sidecarHasEvidence,
		checkRequest: func(req *model.LLMRequest) {
			if req.Config != nil && req.Config.MaxOutputTokens == 32768 {
				fullOutputRequests.Add(1)
			}
			if req.Config != nil && req.Config.MaxOutputTokens > 32768 {
				badOutputRequests.Add(1)
			}
		},
		assess: func(t *testing.T, run *store.BotRun, dir string) {
			if err := os.WriteFile(filepath.Join(dir, "design-summary.md"), []byte(run.Report.Summary), 0600); err != nil {
				t.Fatal(err)
			}
			if strings.Count(run.Report.Summary, "```mermaid") == 0 {
				t.Error("design must include a rendered architecture diagram")
			}
			if fullOutputRequests.Load() == 0 || badOutputRequests.Load() != 0 {
				t.Fatal("missing actual 32768-token generation request evidence")
			}
			missing := sidecarCoverage(run.Report.Summary)
			raw, _ := json.MarshalIndent(map[string]any{"missing_obligations": missing, "full_output_requests": fullOutputRequests.Load(), "source_bot_id": "ad210320-90f5-4857-8f19-c8eea51654aa", "source_home_session": "a470d18e-41a5-4578-9bd8-dbe4b67060aa", "original_assignment": assignment, "project_access_verified": false}, "", "  ")
			if err := os.WriteFile(filepath.Join(dir, "sidecar-assessment.json"), raw, 0600); err != nil {
				t.Fatal(err)
			}
			if len(missing) > 0 {
				t.Errorf("final sidecar design missing obligations: %v; artifacts=%s", missing, dir)
			}
		},
	})
}
