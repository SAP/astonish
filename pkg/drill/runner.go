package drill

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/SAP/astonish/pkg/browser"
	"github.com/SAP/astonish/pkg/config"
)

// ToolExecutor abstracts deterministic tool dispatch for the test runner.
type ToolExecutor interface {
	Execute(ctx context.Context, name string, args map[string]interface{}) (any, error)
}

// TerminalToolExecutor optionally latches a dispatch authority failure. Unlike
// ordinary tool errors, a terminal error stops all tests, retries and cleanup
// dispatch, even for tutorial/continue-on-failure suites. Ordinary executors keep
// their existing semantics by not implementing this interface.
type TerminalToolExecutor interface {
	ToolExecutor
	TerminalError() error
}

func (sr *SuiteRunner) terminalError() error {
	if e, ok := sr.toolExecutor.(TerminalToolExecutor); ok {
		return e.TerminalError()
	}
	return nil
}

// LLMProvider abstracts an LLM for semantic assertion evaluation.
// Any provider that can generate a text completion implements this.
type LLMProvider interface {
	// EvaluateText sends a prompt to the LLM and returns the response text.
	EvaluateText(ctx context.Context, prompt string) (string, error)
}

// SuiteRunner executes drill tests. Prep (template switch, git sync, start
// services, ready_check, teardown) is the caller's / agent's job — see
// GenerateRunInstructions. run_drill injects credentials then calls RunSuite.
type SuiteRunner struct {
	toolExecutor  ToolExecutor
	artifactMgr   *ArtifactManager
	verbose       bool
	bgSessions    []string          // session IDs from background tool steps during tests
	vars          map[string]string // runtime variables for placeholder substitution (e.g., CONTAINER_IP)
	baseURL       string            // resolved base_url from suite config (after placeholder substitution)
	triageAgent   *TriageAgent      // optional AI triage agent for failure analysis
	triageEnabled bool              // when true, all on_fail defaults become "triage"
	setupLog      string            // retained for triage context (unused by thin runner)
	llmProvider   LLMProvider       // optional LLM for semantic assertions
	manifestPath  string            // last tutorial scene_manifest.json written this suite run
	scenePaths    []string          // tutorial scene MP4 paths collected this suite run
}

// NewSuiteRunner creates a runner with the given tool executor and artifact manager.
func NewSuiteRunner(executor ToolExecutor, artifactMgr *ArtifactManager, verbose bool) *SuiteRunner {
	return &SuiteRunner{
		toolExecutor: executor,
		artifactMgr:  artifactMgr,
		verbose:      verbose,
	}
}

// SetVars sets runtime variables that will be substituted in tool args.
// The placeholder syntax is {{KEY}} — e.g., {{CONTAINER_IP}} in a browser_navigate
// URL will be replaced with the actual container bridge IP at runtime.
func (sr *SuiteRunner) SetVars(vars map[string]string) {
	sr.vars = vars
}

// SetTriageAgent enables AI triage analysis on test failures.
// When enabled is true, all on_fail defaults are overridden to "triage"
// so that every failure triggers investigation without editing YAML.
func (sr *SuiteRunner) SetTriageAgent(ta *TriageAgent, enableForAll bool) {
	sr.triageAgent = ta
	sr.triageEnabled = enableForAll
}

// SetLLMProvider sets the LLM used for semantic assertion evaluation.
// When set, assert.type: "semantic" will call the LLM to evaluate whether
// actual output satisfies the expected condition.
func (sr *SuiteRunner) SetLLMProvider(provider LLMProvider) {
	sr.llmProvider = provider
}

// RunSuite executes all tests in a suite. It does not run configure/setup/
// ready_check/teardown — callers (Studio agent prep or fleet) must prepare
// the environment first.
func (sr *SuiteRunner) RunSuite(ctx context.Context, suite *LoadedSuite, tests []LoadedTest) (*SuiteReport, error) {
	report := &SuiteReport{
		Suite:     suite.Name,
		StartedAt: time.Now(),
	}

	sc := suite.Config.SuiteConfig

	// Resolve base_url from suite config (apply placeholder substitution).
	// Shell and browser both run in the sandbox when sandboxed, so localhost
	// in base_url is kept for authors but normalized to 127.0.0.1 for Chromium
	// (avoids ::1 / IPv4-only listener mismatches).
	if sc != nil && sc.BaseURL != "" {
		sr.baseURL = browser.NormalizeLoopbackURL(substituteVarsInString(sc.BaseURL, sr.vars))
	}

	sr.executeTests(ctx, report, suite, tests)

	// Best-effort: kill background sessions started by test steps.
	if sr.terminalError() == nil {
		sr.killBackgroundSessions(ctx)
	}

	report.FinishedAt = time.Now()
	report.Duration = report.FinishedAt.Sub(report.StartedAt).Milliseconds()
	report.ManifestPath = sr.manifestPath
	report.ScenePaths = append([]string(nil), sr.scenePaths...)
	report.ComputeStatus()
	report.ComputeSummary()

	return report, sr.terminalError()
}

// executeTests runs all tests and appends results to the report.
func (sr *SuiteRunner) executeTests(ctx context.Context, report *SuiteReport, suite *LoadedSuite, tests []LoadedTest) {
	for _, test := range tests {
		if sr.terminalError() != nil {
			break
		}
		if err := ValidateTest(&test); err != nil {
			report.Tests = append(report.Tests, TestReport{
				Name:       test.Config.Description,
				File:       test.File,
				Status:     "error",
				StartedAt:  time.Now(),
				FinishedAt: time.Now(),
				Steps:      []StepResult{{Name: "validation", Status: "error", Error: err.Error()}},
			})
			continue
		}

		// Parameterized tests: run once per parameter set
		if len(test.Config.Parameters) > 0 {
			for i, paramSet := range test.Config.Parameters {
				if sr.terminalError() != nil {
					break
				}
				testReport := sr.runParameterizedTest(ctx, &test, suite, paramSet, i)
				report.Tests = append(report.Tests, testReport)
			}
		} else {
			testReport := sr.runTest(ctx, &test, suite)
			report.Tests = append(report.Tests, testReport)
		}
	}

	// Build overall analysis summary from individual triage verdicts
	if sr.triageAgent != nil {
		sr.buildAnalysisSummary(report)
	}
}

// runTest executes a single test and returns its report.
// When a triage agent is configured and a step fails with on_fail: "triage",
// the agent investigates the failure and may trigger an automatic retry.
func (sr *SuiteRunner) runTest(ctx context.Context, test *LoadedTest, suite *LoadedSuite) TestReport {
	tc := test.Config.DrillConfig
	timeout := 120
	stepTimeout := 30
	onFail := "stop"
	maxRetries := 0
	var tags []string
	tutorial := IsTutorialMode(tc)
	explicitOnFail := false

	if tc != nil {
		if tc.Timeout > 0 {
			timeout = tc.Timeout
		}
		if tc.StepTimeout > 0 {
			stepTimeout = tc.StepTimeout
		}
		if tc.OnFail != "" {
			onFail = tc.OnFail
			explicitOnFail = true
		}
		if tc.MaxRetries > 0 {
			maxRetries = tc.MaxRetries
		}
		tags = tc.Tags
	}

	// Tutorial drills default to continue-on-assert and no retries/triage.
	if tutorial && !explicitOnFail {
		onFail = "continue"
	}

	// When --analyze is active, override defaults (tutorials keep continue-on-fail
	// unless YAML explicitly set on_fail — content asserts still mark steps failed).
	if sr.triageEnabled {
		if !tutorial || explicitOnFail {
			if onFail == "stop" {
				onFail = "triage"
			}
			if maxRetries == 0 {
				maxRetries = 1
			}
		}
	}

	report := sr.runTestAttempt(ctx, test, suite, timeout, stepTimeout, onFail, tags, tutorial)
	report.Tags = tags

	retriesLeft := maxRetries
	for retriesLeft > 0 && sr.terminalError() == nil && report.Status == "failed" && sr.shouldRetry(report) {
		retriesLeft--
		report.Retries++
		if sr.verbose {
			fmt.Printf("  Retrying test %q (attempt %d)...\n", test.Config.Description, report.Retries+1)
		}
		report = sr.runTestAttempt(ctx, test, suite, timeout, stepTimeout, onFail, tags, tutorial)
		report.Retries = maxRetries - retriesLeft
		report.Tags = tags
	}

	return report
}

// runParameterizedTest runs a single test with a specific parameter set.
// The parameter values are merged into the runner's vars for placeholder
// substitution, then restored after the run completes.
func (sr *SuiteRunner) runParameterizedTest(ctx context.Context, test *LoadedTest, suite *LoadedSuite, paramSet map[string]string, paramIdx int) TestReport {
	// Save current vars and merge parameter values
	savedVars := sr.vars
	mergedVars := make(map[string]string)
	for k, v := range sr.vars {
		mergedVars[k] = v
	}
	for k, v := range paramSet {
		mergedVars[k] = v
	}
	sr.vars = mergedVars

	report := sr.runTest(ctx, test, suite)

	// Tag the report with the parameter set
	report.ParameterSet = paramSet
	// Append parameter index to the name for clarity
	report.Name = fmt.Sprintf("%s [param %d]", report.Name, paramIdx+1)

	// Restore original vars
	sr.vars = savedVars

	return report
}

// runTestAttempt executes a single attempt of a test.
func (sr *SuiteRunner) runTestAttempt(ctx context.Context, test *LoadedTest, suite *LoadedSuite, timeout, stepTimeout int, onFail string, _ []string, tutorial bool) TestReport {
	testCtx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()

	report := TestReport{
		Name:      test.Config.Description,
		File:      test.File,
		StartedAt: time.Now(),
	}

	autoWait := false
	autoWaitTimeout := 5000
	if tc := test.Config.DrillConfig; tc != nil {
		autoWait = tc.AutoWait
		if tc.AutoWaitTimeout > 0 {
			autoWaitTimeout = tc.AutoWaitTimeout
		}
	}

	nodes := resolveExecutionOrder(test.Config)
	allPassed := true
	ts := &tutorialRunState{}

	for _, node := range nodes {
		stepResult := sr.executeStep(testCtx, node, stepTimeout, autoWait, autoWaitTimeout, tutorial, ts)
		report.Steps = append(report.Steps, stepResult)
		if sr.terminalError() != nil {
			allPassed = false
			break
		}

		if stepResult.Status == "failed" || stepResult.Status == "error" {
			allPassed = false
			stepOnFail := onFail
			if node.Assert != nil && node.Assert.OnFail != "" {
				stepOnFail = node.Assert.OnFail
			}
			if sr.triageEnabled && stepOnFail == "stop" {
				stepOnFail = "triage"
			}

			if stepOnFail == "triage" && suite != nil {
				var verdict *TriageVerdict
				if known := ClassifyKnownFailure(stepResult); known != nil {
					verdict = known
				} else if sr.triageAgent != nil {
					v, err := sr.triageAgent.Investigate(testCtx, stepResult, *test, suite, sr.setupLog)
					if err != nil {
						if sr.verbose {
							fmt.Printf("  Triage error: %v\n", err)
						}
					} else {
						verdict = v
					}
				}
				if verdict != nil {
					report.Steps[len(report.Steps)-1].Triage = verdict
				}
				break
			}
			if known := ClassifyKnownFailure(stepResult); known != nil && known.Retry {
				report.Steps[len(report.Steps)-1].Triage = known
			}
			if stepOnFail == "stop" || stepOnFail == "triage" {
				break
			}
		}
	}

	if tutorial && sr.terminalError() == nil {
		sr.finalizeTutorialRecording(testCtx, ts)
		var cutList []config.TutorialSceneSpec
		if test.Config.DrillConfig != nil {
			cutList = test.Config.DrillConfig.Scenes
		}
		scenes := MergeSceneManifest(cutList, ts.scenes)
		for _, clip := range scenes {
			if clip.Path != "" {
				sr.scenePaths = append(sr.scenePaths, clip.Path)
			}
		}
		if sr.artifactMgr != nil {
			suiteName := ""
			if suite != nil {
				suiteName = suite.Name
			}
			path, err := WriteSceneManifest(sr.artifactMgr.Dir(), SceneManifest{
				Mode:   "tutorial",
				Suite:  suiteName,
				Drill:  test.Name,
				Scenes: scenes,
			})
			if err == nil && path != "" {
				sr.manifestPath = path
				report.Steps = append(report.Steps, StepResult{
					Name:      "_scene_manifest",
					Tool:      "scene_manifest",
					Status:    "passed",
					Artifacts: []string{path},
				})
			}
		}
	}

	report.FinishedAt = time.Now()
	report.Duration = report.FinishedAt.Sub(report.StartedAt).Milliseconds()

	if allPassed {
		report.Status = "passed"
	} else {
		report.Status = "failed"
	}

	return report
}

// tutorialRunState tracks open recordings and completed scenes during a tutorial drill.
type tutorialRunState struct {
	recording     bool
	pendingID     string
	pendingNarr   string
	pendingHoldMs int
	scenes        []SceneClip
}

// shouldRetry checks if any step in the report has a triage verdict recommending retry.
func (sr *SuiteRunner) shouldRetry(report TestReport) bool {
	for _, step := range report.Steps {
		if step.Triage != nil && step.Triage.Retry {
			return true
		}
	}
	return false
}

// buildAnalysisSummary aggregates triage verdicts into a suite-level analysis string.
func (sr *SuiteRunner) buildAnalysisSummary(report *SuiteReport) {
	var summaries []string
	for _, test := range report.Tests {
		for _, step := range test.Steps {
			if step.Triage != nil {
				summary := fmt.Sprintf("[%s] %s > %s: %s (confidence: %.0f%%) — %s",
					step.Triage.Classification,
					test.Name, step.Name,
					step.Triage.RootCause,
					step.Triage.Confidence*100,
					step.Triage.Recommendation)
				summaries = append(summaries, summary)
			}
		}
	}
	if len(summaries) > 0 {
		report.Analysis = strings.Join(summaries, "\n")
	}
}

// executeStep runs a single tool node and evaluates its assertion.
// When autoWait is true and the tool is an interactive browser tool,
// a browser_wait_for call is injected before the actual tool execution
// to wait for the target element to appear.
func (sr *SuiteRunner) executeStep(ctx context.Context, node config.Node, stepTimeout int, autoWait bool, autoWaitTimeout int, tutorial bool, ts *tutorialRunState) StepResult {
	stepCtx, cancel := context.WithTimeout(ctx, time.Duration(stepTimeout)*time.Second)
	defer cancel()

	toolName := extractToolName(node)

	result := StepResult{
		Name:      node.Name,
		Tool:      toolName,
		Narration: node.Narration,
		HoldMs:    node.HoldMs,
	}

	if toolName == "" {
		result.Status = "error"
		result.Error = "no tool specified in node args"
		return result
	}

	toolArgs := make(map[string]interface{})
	for k, v := range node.Args {
		if k != "tool" {
			toolArgs[k] = v
		}
	}

	if len(sr.vars) > 0 {
		toolArgs = substituteVarsInArgs(toolArgs, sr.vars)
	}

	if sr.baseURL != "" && toolName == "browser_navigate" {
		if urlStr, ok := toolArgs["url"].(string); ok && strings.HasPrefix(urlStr, "/") {
			toolArgs["url"] = strings.TrimRight(sr.baseURL, "/") + urlStr
		}
	}
	if strings.HasPrefix(toolName, "browser_") {
		if urlStr, ok := toolArgs["url"].(string); ok && urlStr != "" {
			toolArgs["url"] = browser.NormalizeLoopbackURL(urlStr)
		}
	}

	if tutorial && ts != nil {
		if err := sr.applyTutorialRecording(stepCtx, node, ts); err != nil {
			result.Status = "error"
			result.Error = err.Error()
			return result
		}
	}

	if autoWait && isInteractiveBrowserTool(toolName) {
		if waitTarget := extractWaitTarget(toolName, toolArgs); waitTarget != "" {
			waitArgs := map[string]interface{}{
				"selector": waitTarget,
				"timeout":  autoWaitTimeout,
			}
			sr.toolExecutor.Execute(stepCtx, "browser_wait_for", waitArgs) // best-effort for ordinary executors
			if err := sr.terminalError(); err != nil {
				result.Status, result.Error = "error", err.Error()
				return result
			}
		}
	}

	start := time.Now()

	toolResult, err := sr.toolExecutor.Execute(stepCtx, toolName, toolArgs)
	result.Duration = time.Since(start).Milliseconds()

	if err != nil {
		result.Status = "error"
		result.Error = err.Error()
		errOutput := err.Error()
		if len(errOutput) > 10240 {
			errOutput = errOutput[:10240] + "\n... (truncated)"
		}
		result.Output = errOutput
		return result
	}

	if node.HoldMs > 0 {
		select {
		case <-stepCtx.Done():
			result.Status = "error"
			result.Error = "context cancelled during hold_ms"
			return result
		case <-time.After(time.Duration(node.HoldMs) * time.Millisecond):
		}
	}

	output := ExtractOutput(toolResult)

	if sr.artifactMgr != nil && output != "" {
		if isShellTool(toolName) {
			path, _ := sr.artifactMgr.SaveLog(node.Name, output)
			if path != "" {
				result.Artifacts = append(result.Artifacts, path)
			}
		}
	}

	if node.Assert != nil {
		content := getAssertionContent(node.Assert, toolResult, output)

		if node.Assert.Type == "semantic" && sr.llmProvider != nil {
			assertResult := EvaluateSemantic(stepCtx, node.Assert, content, sr.llmProvider)
			result.Assertion = assertResult
		} else if node.Assert.Type == "visual_match" {
			assertResult := sr.evaluateVisual(node, toolResult)
			result.Assertion = assertResult
		} else {
			assertResult := Evaluate(node.Assert, content)
			result.Assertion = assertResult
		}

		if result.Assertion.Passed {
			result.Status = "passed"
		} else {
			// Tutorials fail content asserts (broken/empty pages must not film green).
			result.Status = "failed"
			if len(output) > 10240 {
				result.Output = output[:10240] + "\n... (truncated)"
			} else {
				result.Output = output
			}
		}
	} else {
		result.Status = "passed"
	}

	return result
}

func (sr *SuiteRunner) applyTutorialRecording(ctx context.Context, node config.Node, ts *tutorialRunState) error {
	rec := node.Record
	if rec == "" && node.Narration != "" {
		rec = "segment"
	}
	switch rec {
	case "":
		return nil
	case "start":
		return sr.tutorialStartRecording(ctx, node, ts)
	case "stop":
		return sr.tutorialStopRecording(ctx, ts)
	case "segment":
		if ts.recording {
			if err := sr.tutorialStopRecording(ctx, ts); err != nil {
				return err
			}
		}
		return sr.tutorialStartRecording(ctx, node, ts)
	default:
		return fmt.Errorf("unknown record %q", rec)
	}
}

func (sr *SuiteRunner) tutorialStartRecording(ctx context.Context, node config.Node, ts *tutorialRunState) error {
	filename := SanitizeSceneFilename(node.Name)
	_, err := sr.toolExecutor.Execute(ctx, "browser_start_recording", map[string]interface{}{
		"filename": filename,
	})
	if err != nil {
		return fmt.Errorf("browser_start_recording: %w", err)
	}
	ts.recording = true
	ts.pendingID = node.Name
	ts.pendingNarr = node.Narration
	ts.pendingHoldMs = node.HoldMs
	return nil
}

func (sr *SuiteRunner) tutorialStopRecording(ctx context.Context, ts *tutorialRunState) error {
	if !ts.recording {
		return nil
	}
	raw, err := sr.toolExecutor.Execute(ctx, "browser_stop_recording", map[string]interface{}{})
	ts.recording = false
	if err != nil {
		return fmt.Errorf("browser_stop_recording: %w", err)
	}
	clip := SceneClip{
		ID:                ts.pendingID,
		Narration:         ts.pendingNarr,
		Voiceover:         ts.pendingNarr,
		HoldMs:            ts.pendingHoldMs,
		VisualKind:        "screen",
		VisualDescription: ts.pendingNarr,
	}
	if m, ok := raw.(map[string]any); ok {
		if p, ok := m["path"].(string); ok {
			clip.Path = p
		}
		switch d := m["duration_seconds"].(type) {
		case float64:
			clip.DurationSeconds = d
		case int:
			clip.DurationSeconds = float64(d)
		}
	} else {
		// Results may be structs from tool handlers — marshal round-trip.
		data, _ := json.Marshal(raw)
		var m map[string]any
		if json.Unmarshal(data, &m) == nil {
			if p, ok := m["path"].(string); ok {
				clip.Path = p
			}
			if d, ok := m["duration_seconds"].(float64); ok {
				clip.DurationSeconds = d
			}
		}
	}
	ts.scenes = append(ts.scenes, clip)
	ts.pendingID, ts.pendingNarr, ts.pendingHoldMs = "", "", 0
	return nil
}

func (sr *SuiteRunner) finalizeTutorialRecording(ctx context.Context, ts *tutorialRunState) {
	if ts == nil || !ts.recording {
		return
	}
	_ = sr.tutorialStopRecording(ctx, ts)
}

// runShellCommand executes a shell command via the tool executor.
// Commands ending with & are automatically run in background mode to prevent
// the PTY from closing and killing the process. Canonical start-services.sh
// scripts detach restart supervisors (setsid+nohup+restart loop) and exit;
// they run in the foreground so suite setup waits for their readiness poll.
func (sr *SuiteRunner) runShellCommand(ctx context.Context, command string, timeout int) (string, error) {
	bg := isBackgroundCommand(command)

	args := map[string]interface{}{
		"command": command,
		"timeout": timeout,
	}

	if bg {
		// Strip trailing & and use the background flag instead.
		// When background=true, the process manager keeps the PTY session
		// alive so the child process survives. With "cmd &" in non-background
		// mode, sh exits immediately and waitLoop closes the PTY, killing
		// the backgrounded child via SIGHUP (or leaving Vite hung).
		args["command"] = stripBackgroundSuffix(command)
		args["background"] = true
	}

	result, err := sr.toolExecutor.Execute(ctx, "shell_command", args)
	if err != nil {
		return "", err
	}

	output := ExtractOutput(result)

	// Track background session IDs for cleanup
	if bg {
		if sid := extractSessionID(result); sid != "" {
			sr.bgSessions = append(sr.bgSessions, sid)
		}
	}

	return output, nil
}

// isBackgroundCommand returns true if the command ends with & (after trimming).
func isBackgroundCommand(cmd string) bool {
	trimmed := strings.TrimSpace(cmd)
	return strings.HasSuffix(trimmed, "&") && !strings.HasSuffix(trimmed, "&&")
}

// stripBackgroundSuffix removes the trailing & from a command.
func stripBackgroundSuffix(cmd string) string {
	trimmed := strings.TrimSpace(cmd)
	trimmed = strings.TrimSuffix(trimmed, "&")
	return strings.TrimSpace(trimmed)
}

// extractSessionID extracts the session_id from a shell command result.
func extractSessionID(result any) string {
	if result == nil {
		return ""
	}
	data, err := json.Marshal(result)
	if err != nil {
		return ""
	}
	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		return ""
	}
	if sid, ok := m["session_id"]; ok {
		return fmt.Sprintf("%v", sid)
	}
	return ""
}

// killBackgroundSessions kills all tracked background sessions via process_kill.
// This is called after teardown commands have run, as a safety net to ensure
// PTY resources are released even if teardown pkill commands miss a process.
func (sr *SuiteRunner) killBackgroundSessions(ctx context.Context) {
	for _, sid := range sr.bgSessions {
		sr.toolExecutor.Execute(ctx, "process_kill", map[string]interface{}{
			"session_id": sid,
		})
	}
	sr.bgSessions = nil
}

// runReadyCheckViaExecutor polls for service readiness by running shell commands
// (curl for HTTP, nc for port) through the tool executor. This ensures the check
// runs in the same environment as the service (inside the sandbox container when
// sandbox is active, or on the host in local mode). This avoids the problem where
// RunReadyCheck() makes HTTP/TCP calls directly from the host process, which
// cannot reach services listening on localhost inside a container.
func (sr *SuiteRunner) runReadyCheckViaExecutor(ctx context.Context, rc *config.ReadyCheck) error {
	if rc == nil {
		return nil
	}

	timeout := rc.Timeout
	if timeout <= 0 {
		timeout = DefaultReadyCheckTimeout
	}
	interval := rc.Interval
	if interval <= 0 {
		interval = DefaultReadyCheckInterval
	}
	need := readyCheckStableCount(rc)
	successes := 0

	var checkCmd string
	switch rc.Type {
	case "http":
		if rc.URL == "" {
			return fmt.Errorf("ready check http: url is required")
		}
		// Use curl to get the HTTP status code. If curl is not installed,
		// fall back to wget (common in Alpine/minimal containers).
		checkCmd = fmt.Sprintf(
			"if command -v curl >/dev/null 2>&1; then curl -s -o /dev/null -w '%%{http_code}' --max-time 5 %q; "+
				"elif command -v wget >/dev/null 2>&1; then wget -q -O /dev/null --server-response --timeout=5 %q 2>&1 | awk '/HTTP/{print $2}' | tail -1; "+
				"else echo 'ERR_NO_HTTP_CLIENT'; fi",
			rc.URL, rc.URL)
	case "port":
		host := rc.Host
		if host == "" {
			host = "localhost"
		}
		if rc.Port <= 0 {
			return fmt.Errorf("ready check port: port is required")
		}
		// Try nc first; fall back to bash /dev/tcp if nc is not installed.
		// The shell_command tool uses "sh -c", so we explicitly invoke bash
		// for the /dev/tcp fallback which is a bash-specific feature.
		checkCmd = fmt.Sprintf("(command -v nc >/dev/null 2>&1 && nc -z %s %d) || bash -c 'echo >/dev/tcp/%s/%d' 2>/dev/null", host, rc.Port, host, rc.Port)
	case "output_contains":
		// output_contains is handled inline by the runner (checks setup output)
		return fmt.Errorf("output_contains ready check must be handled by the runner")
	default:
		return fmt.Errorf("unknown ready check type: %q", rc.Type)
	}

	deadline := time.After(time.Duration(timeout) * time.Second)
	ticker := time.NewTicker(time.Duration(interval) * time.Second)
	defer ticker.Stop()

	// Track last error for diagnostic reporting on timeout
	var lastErr error

	record := func(err error) bool {
		lastErr = err
		if err == nil {
			successes++
			return successes >= need
		}
		successes = 0
		return false
	}

	// Try immediately before first tick
	if record(sr.checkReadyOnce(ctx, rc.Type, checkCmd)) {
		return nil
	}

	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("ready check cancelled: %w", ctx.Err())
		case <-deadline:
			if lastErr != nil {
				return fmt.Errorf("ready check timed out after %ds (type: %s, need %d consecutive successes, had %d): last error: %v", timeout, rc.Type, need, successes, lastErr)
			}
			return fmt.Errorf("ready check timed out after %ds (type: %s, need %d consecutive successes, had %d)", timeout, rc.Type, need, successes)
		case <-ticker.C:
			if record(sr.checkReadyOnce(ctx, rc.Type, checkCmd)) {
				return nil
			}
		}
	}
}

// checkReadyOnce runs a single ready check poll via the tool executor.
func (sr *SuiteRunner) checkReadyOnce(ctx context.Context, checkType, cmd string) error {
	result, err := sr.toolExecutor.Execute(ctx, "shell_command", map[string]interface{}{
		"command": cmd,
		"timeout": 10,
	})
	if err != nil {
		return err
	}

	// For HTTP checks, verify the status code is 2xx
	if checkType == "http" {
		output := ExtractOutput(result)
		output = strings.TrimSpace(output)
		if len(output) >= 3 {
			// Extract the last 3 chars (the HTTP status code from curl -w)
			code := output[len(output)-3:]
			if code[0] == '2' {
				return nil
			}
			return fmt.Errorf("http ready check: status %s", code)
		}
		return fmt.Errorf("http ready check: unexpected output %q", output)
	}

	// For port checks, nc -z exits 0 on success; check exit_code
	if checkType == "port" {
		exitCode := extractExitCode(result)
		if exitCode == "0" {
			return nil
		}
		return fmt.Errorf("port ready check: connection failed")
	}

	return nil
}
func extractToolName(node config.Node) string {
	if tool, ok := node.Args["tool"]; ok {
		if s, ok := tool.(string); ok {
			return s
		}
	}
	// Fallback: check tools_selection
	if len(node.ToolsSelection) > 0 {
		return node.ToolsSelection[0]
	}
	return ""
}

// ExtractOutput converts a tool result to a string representation.
func ExtractOutput(result any) string {
	if result == nil {
		return ""
	}

	// Try common result struct fields via JSON round-trip
	data, err := json.Marshal(result)
	if err != nil {
		return fmt.Sprintf("%v", result)
	}

	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		return string(data)
	}

	// Prefer stdout field (from ShellCommandResult)
	if stdout, ok := m["stdout"]; ok {
		return fmt.Sprintf("%v", stdout)
	}
	// Prefer content field (from ReadFileResult)
	if content, ok := m["content"]; ok {
		return fmt.Sprintf("%v", content)
	}
	// Prefer body field (from HttpRequestResult)
	if body, ok := m["body"]; ok {
		return fmt.Sprintf("%v", body)
	}

	return string(data)
}

// getAssertionContent extracts the content to assert against based on the assertion source.
func getAssertionContent(assert *config.AssertConfig, result any, defaultOutput string) string {
	source := assert.Source
	if source == "" {
		source = "output"
	}

	switch source {
	case "output":
		return defaultOutput
	case "exit_code":
		return extractExitCode(result)
	default:
		// For snapshot, screenshot, pty_buffer — fall back to output in Phase 1
		return defaultOutput
	}
}

// extractExitCode extracts exit code from a shell command result.
func extractExitCode(result any) string {
	data, err := json.Marshal(result)
	if err != nil {
		return ""
	}
	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		return ""
	}
	if code, ok := m["exit_code"]; ok {
		if code == nil {
			return ""
		}
		return fmt.Sprintf("%v", code)
	}
	return ""
}

// isReadyCheckConfigured returns true if the ready check has enough configuration
// to actually run. A ReadyCheck with port: 0, no URL, and no pattern is treated
// as "not configured" — the AI likely generated a placeholder.
func isReadyCheckConfigured(rc *config.ReadyCheck) bool {
	if rc == nil {
		return false
	}
	switch rc.Type {
	case "http":
		return rc.URL != ""
	case "port":
		return rc.Port > 0
	case "output_contains":
		return rc.Pattern != ""
	default:
		return false
	}
}

// isShellTool returns true if the tool name is a shell/process tool.
func isShellTool(name string) bool {
	return name == "shell_command" || strings.HasPrefix(name, "process_")
}

// isInteractiveBrowserTool returns true if the tool is a browser interaction
// that targets a specific element and benefits from auto-wait.
func isInteractiveBrowserTool(name string) bool {
	switch name {
	case "browser_click", "browser_type", "browser_hover",
		"browser_select_option", "browser_fill_form", "browser_drag":
		return true
	}
	return false
}

// extractWaitTarget derives a CSS selector or ref to wait for from the tool args.
// Returns empty string if no meaningful wait target can be determined.
func extractWaitTarget(toolName string, args map[string]interface{}) string {
	// Check for explicit "selector" argument
	if sel, ok := args["selector"].(string); ok && sel != "" {
		return sel
	}
	// Check for "ref" argument (snapshot-based ref like "ref5")
	if ref, ok := args["ref"].(string); ok && ref != "" {
		// Refs are positional identifiers from browser_snapshot; we can't
		// translate them to CSS selectors for wait_for. Skip auto-wait.
		return ""
	}
	// For browser_fill_form, check for fields with selectors
	if toolName == "browser_fill_form" {
		if fields, ok := args["fields"].([]interface{}); ok && len(fields) > 0 {
			if field, ok := fields[0].(map[string]interface{}); ok {
				if sel, ok := field["selector"].(string); ok && sel != "" {
					return sel
				}
			}
		}
	}
	return ""
}

// evaluateVisual compares a screenshot from the tool result against a stored baseline.
// On first run (no baseline exists), the screenshot is saved as the baseline and passes.
func (sr *SuiteRunner) evaluateVisual(node config.Node, toolResult any) *AssertionResult {
	result := &AssertionResult{
		Type:     "visual_match",
		Expected: node.Assert.Expected, // baseline name
	}

	// Extract screenshot data from tool result
	screenshotData := extractScreenshotData(toolResult)
	if screenshotData == nil {
		result.Passed = false
		result.Message = "no screenshot data in tool result (use browser_take_screenshot)"
		return result
	}

	threshold := node.Assert.Threshold
	if threshold <= 0 {
		threshold = 0.01 // default 1%
	}

	baselineName := node.Assert.Expected
	if baselineName == "" {
		baselineName = node.Name
	}

	// Load or create baseline
	baselineDir := BaselineDir(sr.artifactMgr)
	baseline, err := LoadBaseline(baselineDir, baselineName)
	if err != nil {
		// No baseline exists — save current as baseline and pass
		if saveErr := SaveBaseline(baselineDir, baselineName, screenshotData); saveErr != nil {
			result.Passed = false
			result.Message = fmt.Sprintf("failed to save baseline: %v", saveErr)
			return result
		}
		result.Passed = true
		result.Message = "baseline created (first run)"
		return result
	}

	// Compare images
	diffPct, diffImg, err := CompareImages(baseline, screenshotData, threshold)
	if err != nil {
		result.Passed = false
		result.Message = fmt.Sprintf("image comparison failed: %v", err)
		return result
	}

	result.Actual = fmt.Sprintf("%.2f%% different", diffPct*100)

	if diffPct <= threshold {
		result.Passed = true
		result.Message = fmt.Sprintf("visual match within threshold (%.2f%% diff, %.2f%% allowed)", diffPct*100, threshold*100)
	} else {
		result.Passed = false
		result.Message = fmt.Sprintf("visual regression: %.2f%% pixels differ (threshold: %.2f%%)", diffPct*100, threshold*100)

		// Save diff image as artifact
		if sr.artifactMgr != nil && diffImg != nil {
			path, _ := sr.artifactMgr.SaveDiffImage(node.Name, diffImg)
			if path != "" {
				result.Message += fmt.Sprintf(" (diff saved: %s)", path)
			}
		}
	}

	return result
}

// extractScreenshotData extracts raw PNG bytes from a browser_take_screenshot result.
func extractScreenshotData(result any) []byte {
	if result == nil {
		return nil
	}
	data, err := json.Marshal(result)
	if err != nil {
		return nil
	}
	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		return nil
	}
	// browser_take_screenshot returns base64-encoded image data
	if b64, ok := m["image"].(string); ok && b64 != "" {
		decoded, err := base64Decode(b64)
		if err != nil {
			return nil
		}
		return decoded
	}
	if b64, ok := m["screenshot"].(string); ok && b64 != "" {
		decoded, err := base64Decode(b64)
		if err != nil {
			return nil
		}
		return decoded
	}
	return nil
}

// base64Decode decodes a base64 string, trying standard then URL encoding.
func base64Decode(s string) ([]byte, error) {
	data, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		data, err = base64.RawStdEncoding.DecodeString(s)
	}
	return data, err
}

// substituteVarsInString replaces {{KEY}} and {KEY} placeholders in a string
// with values from the vars map. Double braces are replaced first to avoid
// partial matches. Single braces are replaced as a fallback because LLMs
// sometimes emit {CONTAINER_IP} instead of {{CONTAINER_IP}}.
// Unknown placeholders are left as-is.
func substituteVarsInString(s string, vars map[string]string) string {
	if len(vars) == 0 {
		return s
	}
	for key, val := range vars {
		// Double braces first (canonical form)
		s = strings.ReplaceAll(s, "{{"+key+"}}", val)
		// Single braces fallback (LLM sometimes drops one layer)
		s = strings.ReplaceAll(s, "{"+key+"}", val)
	}
	return s
}

// substituteVarsInArgs recursively walks a tool args map and replaces
// {{KEY}} placeholders in all string values. Returns a new map (does not
// modify the input).
func substituteVarsInArgs(args map[string]interface{}, vars map[string]string) map[string]interface{} {
	if len(vars) == 0 || len(args) == 0 {
		return args
	}
	result := make(map[string]interface{}, len(args))
	for k, v := range args {
		result[k] = substituteVarsInValue(v, vars)
	}
	return result
}

// substituteVarsInValue recursively substitutes placeholders in a single value.
func substituteVarsInValue(v interface{}, vars map[string]string) interface{} {
	switch val := v.(type) {
	case string:
		return substituteVarsInString(val, vars)
	case map[string]interface{}:
		result := make(map[string]interface{}, len(val))
		for k, v2 := range val {
			result[k] = substituteVarsInValue(v2, vars)
		}
		return result
	case []interface{}:
		result := make([]interface{}, len(val))
		for i, v2 := range val {
			result[i] = substituteVarsInValue(v2, vars)
		}
		return result
	default:
		return v
	}
}

// resolveExecutionOrder returns nodes in flow order.
// For now, this does a simple topological sort based on the flow edges.
// If no flow is defined, nodes are returned in declaration order.
func resolveExecutionOrder(cfg *config.AgentConfig) []config.Node {
	if len(cfg.Flow) == 0 {
		return cfg.Nodes
	}

	// Build adjacency: from → to
	nodeMap := make(map[string]config.Node)
	for _, n := range cfg.Nodes {
		nodeMap[n.Name] = n
	}

	// Build edge list and find roots (nodes not targeted by any edge)
	targets := make(map[string]bool)
	edges := make(map[string][]string)
	for _, f := range cfg.Flow {
		if f.To != "" {
			edges[f.From] = append(edges[f.From], f.To)
			targets[f.To] = true
		}
		for _, e := range f.Edges {
			edges[f.From] = append(edges[f.From], e.To)
			targets[e.To] = true
		}
	}

	// Find root nodes (appear in flow but not as targets)
	var roots []string
	seen := make(map[string]bool)
	for _, f := range cfg.Flow {
		if !targets[f.From] && !seen[f.From] {
			roots = append(roots, f.From)
			seen[f.From] = true
		}
	}

	if len(roots) == 0 {
		return cfg.Nodes
	}

	// BFS from roots
	var ordered []config.Node
	visited := make(map[string]bool)
	queue := roots

	for len(queue) > 0 {
		name := queue[0]
		queue = queue[1:]
		if visited[name] {
			continue
		}
		visited[name] = true
		if node, ok := nodeMap[name]; ok {
			ordered = append(ordered, node)
		}
		for _, next := range edges[name] {
			if !visited[next] {
				queue = append(queue, next)
			}
		}
	}

	// Add any nodes not in the flow (shouldn't happen in well-formed tests, but be safe)
	for _, n := range cfg.Nodes {
		if !visited[n.Name] {
			ordered = append(ordered, n)
		}
	}

	return ordered
}
