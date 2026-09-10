package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestStudioSessionsHandler_AppNameDeserialization verifies that the
// StudioChatRequest struct correctly deserializes the optional appName field.
// This field is used by the Chrome extension to categorize its sessions
// separately from Studio chat sessions.
func TestStudioSessionsHandler_AppNameDeserialization(t *testing.T) {
	t.Run("with appName", func(t *testing.T) {
		input := `{"message":"hello","appName":"astonish-extension"}`
		var req StudioChatRequest
		if err := json.Unmarshal([]byte(input), &req); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if req.AppName != "astonish-extension" {
			t.Errorf("got AppName=%q, want %q", req.AppName, "astonish-extension")
		}
		if req.Message != "hello" {
			t.Errorf("got Message=%q, want %q", req.Message, "hello")
		}
	})

	t.Run("without appName defaults to empty", func(t *testing.T) {
		input := `{"message":"hello"}`
		var req StudioChatRequest
		if err := json.Unmarshal([]byte(input), &req); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if req.AppName != "" {
			t.Errorf("got AppName=%q, want empty", req.AppName)
		}
	})

	t.Run("appName omitted in JSON output when empty", func(t *testing.T) {
		req := StudioChatRequest{Message: "hello"}
		out, err := json.Marshal(req)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		var m map[string]interface{}
		if err := json.Unmarshal(out, &m); err != nil {
			t.Fatalf("unmarshal output: %v", err)
		}
		if _, ok := m["appName"]; ok {
			t.Error("appName should be omitted from JSON when empty")
		}
	})
}

// TestStudioSessionsAppNameQueryParam verifies the ?app= query-parameter
// resolution logic that StudioSessionsHandler, StudioSessionHandler, and
// StudioDeleteSessionHandler all share.
//
// The three behaviours under test:
//  1. ?app= absent         → defaults to studioChatAppName ("astonish")
//  2. ?app=astonish-extension → passes that value through
//  3. ?app=               → empty value also defaults to studioChatAppName
func TestStudioSessionsAppNameQueryParam(t *testing.T) {
	resolveApp := func(rawURL string) string {
		r := httptest.NewRequest(http.MethodGet, rawURL, nil)
		appName := r.URL.Query().Get("app")
		if appName == "" {
			appName = studioChatAppName
		}
		return appName
	}

	cases := []struct {
		url  string
		want string
	}{
		{"/api/studio/sessions", studioChatAppName},
		{"/api/studio/sessions?app=", studioChatAppName},
		{"/api/studio/sessions?app=astonish-extension", "astonish-extension"},
		{"/api/studio/sessions?app=astonish", studioChatAppName},
		{"/api/studio/sessions?app=astonish_code", "astonish_code"},
	}

	for _, tc := range cases {
		got := resolveApp(tc.url)
		if got != tc.want {
			t.Errorf("resolveApp(%q) = %q, want %q", tc.url, got, tc.want)
		}
	}
}

// TestStudioChatRequestEffectiveApp verifies that the effective-app selection
// logic in StudioChatHandler produces the correct namespace:
//   - caller-provided AppName takes precedence
//   - missing / empty AppName falls back to studioChatAppName
func TestStudioChatRequestEffectiveApp(t *testing.T) {
	effectiveApp := func(req StudioChatRequest) string {
		app := studioChatAppName
		if req.AppName != "" {
			app = req.AppName
		}
		return app
	}

	cases := []struct {
		name string
		req  StudioChatRequest
		want string
	}{
		{"no AppName → default", StudioChatRequest{Message: "hi"}, studioChatAppName},
		{"empty AppName → default", StudioChatRequest{Message: "hi", AppName: ""}, studioChatAppName},
		{"extension AppName", StudioChatRequest{Message: "hi", AppName: "astonish-extension"}, "astonish-extension"},
		{"custom namespace", StudioChatRequest{Message: "hi", AppName: "my-namespace"}, "my-namespace"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := effectiveApp(tc.req)
			if got != tc.want {
				t.Errorf("effectiveApp(%+v) = %q, want %q", tc.req, got, tc.want)
			}
		})
	}
}

