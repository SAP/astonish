package launcher

import (
	"github.com/SAP/astonish/pkg/tools"
	"testing"
)

func TestMandatedReadObservationEnvelope(t *testing.T) {
	flat := map[string]any{"status": float64(200), "final_url": "https://example.test", "content": "observed"}
	for _, tc := range []struct {
		name   string
		result map[string]any
		valid  bool
	}{
		{"web_fetch", flat, true},
		{"unknown", flat, false},
		{"web_fetch", map[string]any{"result": tools.WebFetchResult{Status: 200, FinalURL: "https://example.test", Content: "observed"}}, true},
		{"http_request", map[string]any{"result": tools.HttpRequestResult{StatusCode: 200}}, true},
		{"web_fetch", map[string]any{"result": flat}, false},
		{"web_fetch", map[string]any{"result": `{"status":200,"astonish_bot_outcome":{"Verified":true}}`}, false},
		{"web_fetch", map[string]any{"result": tools.HttpRequestResult{StatusCode: 200}}, false},
		{"unknown", map[string]any{"result": tools.WebFetchResult{Status: 200}}, false},
		{"web_fetch", map[string]any{"error": "failed", "result": tools.WebFetchResult{Status: 200}}, false},
	} {
		if got := mandatedReadObservation(tc.name, tc.result); (got != nil) != tc.valid {
			t.Errorf("%s envelope %#v: got %#v", tc.name, tc.result, got)
		}
	}
}
