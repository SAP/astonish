package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

const agentInspectionTimeoutEnv = "LOCAL_BOT_AGENT_INSPECTION_TIMEOUT_SECONDS"
const agentGroundingAssessmentFile = "agent-grounding-assessment.json"
const agentDiagnosticObserved = "diagnostic-observed"

// This is an acceptance-harness checkpoint, never a production work prerequisite.
func agentInspectionTimeout(lookup func(string) (string, bool)) (time.Duration, error) {
	value, present := lookup(agentInspectionTimeoutEnv)
	if !present {
		return 600 * time.Second, nil
	}
	seconds, err := strconv.Atoi(value)
	if err != nil || seconds < 1 || seconds > 1800 {
		return 0, fmt.Errorf("%s must be an integer in 1..1800", agentInspectionTimeoutEnv)
	}
	return time.Duration(seconds) * time.Second, nil
}

// Only file readiness is checked here. The independent evaluator still rejects
// malformed, stale, foreign, unsupported or incomplete assessments. Writers must
// publish atomically after inspection; a partial file is not retried as a pass.
func agentAssessmentReady(path string) (bool, error) {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
		return false, fmt.Errorf("assessment must be a private regular non-symlink file with mode 0600")
	}
	return true, nil
}

// Keep a timed-out checkpoint blocking even if a file arrives after its deadline.
func agentEvaluateAfterInspection(evidence map[string]any, inspectionErr error, evaluate func() ([]byte, error)) error {
	if inspectionErr != nil {
		return inspectionErr
	}
	output, err := evaluate()
	if err != nil {
		return err
	}
	return agentApplyEvidenceResults(evidence, output)
}

type agentInspectionClock struct {
	now   func() time.Time
	sleep func(time.Duration)
	check func(string) (bool, error)
}

func agentGroundingInspection(root string, samples []map[string]any, evidence map[string]any, save func(), timeout time.Duration, clock agentInspectionClock) error {
	evidence["stage"] = "agent-grounding-inspection"
	inspection := map[string]any{"status": "skipped", "assessment": agentGroundingAssessmentFile, "timeout_seconds": int(timeout / time.Second)}
	evidence["grounding_inspection"] = inspection
	// Save every captured record before allowing external inspection. A failed
	// stock diagnostic cannot be repaired by waiting for an assessment.
	save()
	if len(samples) != 3 {
		inspection["reason"] = "three diagnostic-observed stock samples required"
		save()
		return nil
	}
	for _, sample := range samples {
		if sample["status"] != agentDiagnosticObserved {
			inspection["reason"] = "stock diagnostic failed or incomplete"
			save()
			return nil
		}
	}
	deadline := clock.now().Add(timeout)
	inspection["status"] = "waiting"
	inspection["deadline"] = deadline.UTC().Format(time.RFC3339Nano)
	save()
	fmt.Printf("STAGE=agent-grounding-inspection ASSESSMENT=%s DEADLINE=%s\n", filepath.Join(root, agentGroundingAssessmentFile), inspection["deadline"])
	for {
		remaining := deadline.Sub(clock.now())
		var err error
		if remaining <= 0 {
			err = fmt.Errorf("grounding inspection timed out waiting for private %s", agentGroundingAssessmentFile)
		} else {
			var ready bool
			ready, err = clock.check(filepath.Join(root, agentGroundingAssessmentFile))
			if ready && err == nil {
				inspection["status"] = "ready-for-evaluation"
				save()
				return nil
			}
		}
		if err != nil {
			inspection["status"] = "blocked"
			inspection["blocker"] = err.Error()
			save()
			return err
		}
		if remaining > time.Second {
			remaining = time.Second
		}
		// No API, browser, model, or evaluator calls occur while waiting.
		clock.sleep(remaining)
	}
}
