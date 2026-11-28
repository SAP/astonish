package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestAgentInspectionTimeout(t *testing.T) {
	for _, tc := range []struct {
		value   string
		present bool
		want    time.Duration
	}{
		{"", false, 600 * time.Second}, {"1", true, time.Second}, {"1800", true, 1800 * time.Second},
		{"", true, 0}, {"0", true, 0}, {"-1", true, 0}, {"1801", true, 0},
		{"NaN", true, 0}, {"1.5", true, 0}, {" 10 ", true, 0}, {"99999999999999999999", true, 0},
	} {
		t.Run(fmt.Sprintf("%s-present-%v", tc.value, tc.present), func(t *testing.T) {
			got, err := agentInspectionTimeout(func(key string) (string, bool) {
				if key != agentInspectionTimeoutEnv {
					t.Fatal(key)
				}
				return tc.value, tc.present
			})
			if got != tc.want || (err != nil) != (tc.want == 0) {
				t.Fatalf("got %v, %v", got, err)
			}
		})
	}
}

func TestAgentAssessmentReady(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, agentGroundingAssessmentFile)
	if ready, err := agentAssessmentReady(path); ready || err != nil {
		t.Fatalf("missing: %v %v", ready, err)
	}
	if err := os.WriteFile(path, []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	if ready, err := agentAssessmentReady(path); !ready || err != nil {
		t.Fatalf("private: %v %v", ready, err)
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if ready, err := agentAssessmentReady(path); ready || err == nil {
		t.Fatalf("public: %v %v", ready, err)
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if ready, err := agentAssessmentReady(link); ready || err == nil {
		t.Fatalf("symlink: %v %v", ready, err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if ready, err := agentAssessmentReady(link); ready || err == nil {
		t.Fatalf("dangling symlink: %v %v", ready, err)
	}
	if ready, err := agentAssessmentReady(root); ready || err == nil {
		t.Fatalf("directory: %v %v", ready, err)
	}
}

func inspectionSamples() []map[string]any {
	return []map[string]any{{"status": agentDiagnosticObserved}, {"status": agentDiagnosticObserved}, {"status": agentDiagnosticObserved}}
}

func TestAgentGroundingInspectionBounded(t *testing.T) {
	for _, tc := range []struct {
		name       string
		readyAt    int
		bad        bool
		wantStatus string
	}{
		{"already-present", 1, false, "ready-for-evaluation"},
		{"arrival", 3, false, "ready-for-evaluation"},
		{"missing-timeout", 0, false, "blocked"},
		{"unsafe-file", 0, true, "blocked"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Unix(1000, 0)
			start := now
			checks, saves := 0, 0
			evidence := map[string]any{}
			clock := agentInspectionClock{
				now: func() time.Time { return now },
				sleep: func(d time.Duration) {
					if d <= 0 || d > time.Second {
						t.Fatal(d)
					}
					now = now.Add(d)
				},
				check: func(path string) (bool, error) {
					if path != filepath.Join("owned", agentGroundingAssessmentFile) || saves < 2 {
						t.Fatalf("evidence not saved before inspection: %s %d", path, saves)
					}
					checks++
					if tc.bad {
						return false, fmt.Errorf("unsafe")
					}
					return checks == tc.readyAt, nil
				},
			}
			err := agentGroundingInspection("owned", inspectionSamples(), evidence, func() { saves++ }, 2500*time.Millisecond, clock)
			inspection := evidence["grounding_inspection"].(map[string]any)
			if inspection["status"] != tc.wantStatus || (err != nil) != (tc.wantStatus == "blocked") {
				t.Fatalf("%v, %v", inspection, err)
			}
			if evidence["stage"] != "agent-grounding-inspection" || evidence["passed"] != nil || evidence["receipts"] != nil {
				t.Fatalf("unexpected pass: %v", evidence)
			}
			if tc.name == "missing-timeout" && (now.Sub(start) != 2500*time.Millisecond || checks != 3 || !strings.Contains(err.Error(), "timed out")) {
				t.Fatalf("unbounded: elapsed=%v checks=%d err=%v", now.Sub(start), checks, err)
			}
		})
	}
}

func TestAgentGroundingInspectionSkipsFailedOrMissingSamples(t *testing.T) {
	failed := inspectionSamples()
	failed[1]["status"] = "failed"
	for _, samples := range [][]map[string]any{nil, inspectionSamples()[:2], failed} {
		evidence := map[string]any{}
		// Nil clock/check functions prove a skipped checkpoint never waits/checks.
		if err := agentGroundingInspection("owned", samples, evidence, func() {}, time.Second, agentInspectionClock{}); err != nil {
			t.Fatal(err)
		}
		if evidence["grounding_inspection"].(map[string]any)["status"] != "skipped" {
			t.Fatal(evidence)
		}
	}
}

func TestAgentEvaluateAfterInspectionFailsClosed(t *testing.T) {
	evidence := map[string]any{}
	blocked := fmt.Errorf("inspection timed out")
	if err := agentEvaluateAfterInspection(evidence, blocked, func() ([]byte, error) {
		t.Fatal("must not evaluate after timeout, even if assessment arrives late")
		return nil, nil
	}); err != blocked {
		t.Fatal(err)
	}
	calls := 0
	if err := agentEvaluateAfterInspection(evidence, nil, func() ([]byte, error) {
		calls++
		return []byte(`{}`), nil
	}); err == nil || calls != 1 {
		t.Fatalf("readiness bypassed evaluator: calls=%d err=%v", calls, err)
	}
	if evidence["passed"] != nil || evidence["receipts"] != nil {
		t.Fatal(evidence)
	}
}

func TestAgentWorkflowRejectsBadInspectionConfigBeforeWork(t *testing.T) {
	t.Setenv(agentInspectionTimeoutEnv, "invalid")
	defer func() {
		if value := recover(); value == nil || !strings.Contains(fmt.Sprint(value), agentInspectionTimeoutEnv) {
			t.Fatalf("expected config rejection, got %v", value)
		}
	}()
	// No page/API/state dependencies: validation must precede any work.
	newAgentWorkflow(nil, nil, nil, "", "", "", nil, nil)
}
