package openshell

import (
	"context"
	"encoding/json"
	"testing"
)

func TestBotHomeComposeRejectsMalformedEnvelope(t *testing.T) {
	b := newTestBackendWithGateway(t, &mockGateway{})
	binding := homeTestBinding(t, b)
	for _, raw := range []string{
		`{}`, `{"kubernetes":{}}`,
		`{"kubernetes":{"containers":{"agent":{}},"volumes":[]}}`,
		`{"kubernetes":{"containers":{"agent":{"volume_mounts":[]}},"volumes":[false]}}`,
		`{"kubernetes":{"containers":{"agent":{"volume_mounts":[false]}},"volumes":[]}}`,
		`{"kubernetes":{"containers":{"agent":{"volume_mounts":[]}},"volumes":[{"name":"astonish-bot-home"}]}}`,
		`{"kubernetes":{"containers":{"agent":{"volume_mounts":[]}},"volumes":[{"persistent_volume_claim":{"claim_name":"retained-homes"}}]}}`,
		`{"kubernetes":{"containers":{"agent":{"volume_mounts":[{"mount_path":"/etc/../home/astonish"}]}},"volumes":[]}}`,
		`{"kubernetes":{"containers":{"agent":{"volume_mounts":[{"mount_path":"/home/astonish"}]}},"volumes":[]}}`,
		`{"kubernetes":{"containers":{"agent":{"volume_mounts":[{"mount_path":"/"}]}},"volumes":[]}}`,
	} {
		var driver map[string]any
		if err := json.Unmarshal([]byte(raw), &driver); err != nil {
			t.Fatal(err)
		}
		before, _ := json.Marshal(driver)
		if _, err := b.composeBotHome(context.Background(), binding, map[string]string{}, driver); err == nil {
			t.Fatalf("accepted %s", raw)
		}
		after, _ := json.Marshal(driver)
		if string(before) != string(after) {
			t.Fatal("failed composition mutated input")
		}
	}
	if _, err := b.composeBotHome(context.Background(), binding, nil, nil); err == nil {
		t.Fatal("accepted nil env")
	}
}
