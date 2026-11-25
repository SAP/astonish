package daemon

import (
	"encoding/json"
	"testing"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
)

// This is boundary evidence, not a scheduled-drill acceptance pass. The HTTP
// acceptance remains TestBotConfiguredServiceAdmissionHTTP/drill/*/scheduled-exact-resume.
// Approving a mutable suite name currently captures no exact child authority.
func TestScheduledDrillApprovalHasNoCapturedChildAuthority(t *testing.T) {
	runtime := productionBotPolicyRuntime(botLiveGuard{request: bot.RunRequest{Run: store.BotRun{Trigger: store.TriggerCron}}})
	parentArgs := json.RawMessage(`{"suite_name":"configured-service-suite"}`)
	parent, err := runtime.Classifier.DescribeAction(t.Context(), "run_drill", parentArgs)
	if err != nil {
		t.Fatal(err)
	}
	if parent.Effect != execution.EffectUnknown || parent.ConstraintsVerified || parent.TargetFingerprint != "" || len(parent.TargetSnapshot) != 0 {
		t.Fatalf("update boundary evidence: run_drill now has captured execution authority: %+v", parent)
	}
	childArgs := json.RawMessage(`{"json_data":"{\"evidence\":\"configured-drill-result\"}","fields_to_extract":["evidence"]}`)
	child, err := runtime.Classifier.DescribeAction(t.Context(), "filter_json", childArgs)
	if err != nil {
		t.Fatal(err)
	}
	if child.Effect != execution.EffectRead || !child.ConstraintsVerified {
		t.Fatalf("pure transformation lost existing read-safe classification: %+v", child)
	}
	parentDigest, err := parent.Digest()
	if err != nil {
		t.Fatal(err)
	}
	childDigest, err := child.Digest()
	if err != nil {
		t.Fatal(err)
	}
	if parentDigest == childDigest {
		t.Fatal("parent suite-name approval became child authority")
	}
	t.Log("filter_json is already read-safe; run_drill approval binds only mutable suite-name arguments, with no immutable node snapshot or exact child targets")
}
