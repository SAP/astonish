package api_test

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/SAP/astonish/pkg/store"
)

func TestBotPlanningOwnerHTTPControls(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			f := newInteractiveHTTPFixture(t, "read_file", map[string]any{"path": "/isolated/receipt.txt"}, []string{"chat"}, scope)
			t.Cleanup(f.worker.Stop)
			body := fmt.Sprintf(`{"version":%d,"idempotency_key":"planning-http","instructions":"Plan the change","planMode":true}`, f.b.Version)
			var receipt store.BotMessageReceipt
			if err := json.Unmarshal(f.request("POST", f.path+"/messages", body, 202), &receipt); err != nil {
				t.Fatal(err)
			}
			input, err := f.repo.(store.BotRuntimeStore).LoadRunInput(f.ctx, f.b.ID, receipt.Run.ID)
			if err != nil || !input.PlanMode {
				t.Fatalf("HTTP mode lost: %+v %v", input, err)
			}
			consentBody := fmt.Sprintf(`{"expected_revision":%d,"idempotency_key":"exact-consent","approvedPlanExecution":true,"planExecution":{"run_id":"%s","session_id":"%s","plan_id":"stale-plan","revision":1,"sequence":1}}`, f.b.Revision, receipt.Run.ID, receipt.Run.SessionID)
			f.request("POST", f.path+"/runs/"+receipt.Run.ID.String()+"/continue-interactive", consentBody, 409)
			f.request("POST", f.path+"/messages", fmt.Sprintf(`{"version":%d,"idempotency_key":"unknown","instructions":"Plan","planMode":true,"snapshot":{}}`, f.b.Version), 400)
			f.request("POST", f.path+"/messages", fmt.Sprintf(`{"version":%d,"idempotency_key":"imported","instructions":"Execute","approvedPlanExecution":true}`, f.b.Version), 400)
			f.request("POST", f.path+"/runs/"+receipt.Run.ID.String()+"/continue-interactive", fmt.Sprintf(`{"expected_revision":%d,"idempotency_key":"consent","approvedPlanExecution":true}`, f.b.Revision), 400)
			f.request("POST", f.path+"/runs/"+receipt.Run.ID.String()+"/continue-interactive", fmt.Sprintf(`{"expected_revision":%d,"idempotency_key":"snapshot-consent","approvedPlanExecution":true,"snapshot":{}}`, f.b.Revision), 400)
			var normal store.BotMessageReceipt
			if err := json.Unmarshal(f.request("POST", f.path+"/messages", fmt.Sprintf(`{"version":%d,"idempotency_key":"normal-http","instructions":"Unrelated question","planMode":false}`, f.b.Version), 202), &normal); err != nil {
				t.Fatal(err)
			}
			input, err = f.repo.(store.BotRuntimeStore).LoadRunInput(f.ctx, f.b.ID, normal.Run.ID)
			if err != nil || input.PlanMode {
				t.Fatalf("HTTP explicit false lost: %+v %v", input, err)
			}
		})
	}
}
