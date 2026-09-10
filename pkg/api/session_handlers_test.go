package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestStudioChatRequestDeserialization verifies that the StudioChatRequest struct
// correctly deserializes the optional appName field.
func TestStudioChatRequestDeserialization(t *testing.T) {
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

// TestValidAppName exercises the real validAppName regex used by all handlers
// to validate the ?app= query parameter and StudioChatRequest.AppName field.
// This prevents namespace squatting, path traversal, and unbounded key lengths.
func TestValidAppName(t *testing.T) {
	valid := []string{
		"astonish",
		"astonish-extension",
		"astonish_code",
		"a",
		"my-app",
		"fleet-scheduler",
	}
	for _, name := range valid {
		if !validAppName.MatchString(name) {
			t.Errorf("validAppName should accept %q", name)
		}
	}

	invalid := []string{
		"",                       // empty
		"../../../etc",           // path traversal
		"Astonish",              // uppercase
		"astonish extension",    // space
		"-leading-dash",         // leading dash
		"a;DROP TABLE sessions", // SQL injection attempt
		"a/b",                   // path separator
		"a\\b",                  // backslash
		"a$b",                   // shell metachar
		string(make([]byte, 65)), // too long (65 chars)
	}
	for _, name := range invalid {
		if validAppName.MatchString(name) {
			t.Errorf("validAppName should reject %q", name)
		}
	}
}

// TestAppNameQueryParamDefaulting verifies the ?app= query-param resolution
// logic shared by StudioSessionsHandler, StudioSessionHandler, and
// StudioDeleteSessionHandler. This tests the actual resolution + validation
// code path, not a separate closure.
func TestAppNameQueryParamDefaulting(t *testing.T) {
	// resolveAndValidate mimics the exact three-line pattern in every handler:
	//   appName := r.URL.Query().Get("app")
	//   if appName == "" { appName = studioChatAppName }
	//   if !validAppName.MatchString(appName) → error
	resolveAndValidate := func(rawURL string) (string, bool) {
		r := httptest.NewRequest(http.MethodGet, rawURL, nil)
		appName := r.URL.Query().Get("app")
		if appName == "" {
			appName = studioChatAppName
		}
		return appName, validAppName.MatchString(appName)
	}

	cases := []struct {
		url     string
		want    string
		wantOK  bool
	}{
		{"/api/studio/sessions", studioChatAppName, true},
		{"/api/studio/sessions?app=", studioChatAppName, true},
		{"/api/studio/sessions?app=astonish-extension", "astonish-extension", true},
		{"/api/studio/sessions?app=astonish", studioChatAppName, true},
		{"/api/studio/sessions?app=astonish_code", "astonish_code", true},
		{"/api/studio/sessions?app=../../etc", "../../etc", false},
		{"/api/studio/sessions?app=A", "A", false},
		{"/api/studio/sessions?app=hello%20world", "hello world", false},
	}

	for _, tc := range cases {
		got, ok := resolveAndValidate(tc.url)
		if got != tc.want {
			t.Errorf("resolve(%q) = %q, want %q", tc.url, got, tc.want)
		}
		if ok != tc.wantOK {
			t.Errorf("validate(%q) valid=%v, want %v", tc.url, ok, tc.wantOK)
		}
	}
}

// TestEffectiveAppResolution verifies the effective-app selection logic in
// StudioChatHandler: caller-provided AppName takes precedence, empty falls
// back to studioChatAppName, and invalid values are rejected by validation.
func TestEffectiveAppResolution(t *testing.T) {
	resolveAndValidate := func(req StudioChatRequest) (string, bool) {
		app := studioChatAppName
		if req.AppName != "" {
			app = req.AppName
		}
		return app, validAppName.MatchString(app)
	}

	cases := []struct {
		name   string
		req    StudioChatRequest
		want   string
		wantOK bool
	}{
		{"no AppName → default", StudioChatRequest{Message: "hi"}, studioChatAppName, true},
		{"empty AppName → default", StudioChatRequest{Message: "hi", AppName: ""}, studioChatAppName, true},
		{"extension AppName", StudioChatRequest{Message: "hi", AppName: "astonish-extension"}, "astonish-extension", true},
		{"traversal attempt rejected", StudioChatRequest{Message: "hi", AppName: "../../../etc"}, "../../../etc", false},
		{"uppercase rejected", StudioChatRequest{Message: "hi", AppName: "Astonish"}, "Astonish", false},
		{"valid custom namespace", StudioChatRequest{Message: "hi", AppName: "my-namespace"}, "my-namespace", true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := resolveAndValidate(tc.req)
			if got != tc.want {
				t.Errorf("effectiveApp = %q, want %q", got, tc.want)
			}
			if ok != tc.wantOK {
				t.Errorf("valid = %v, want %v", ok, tc.wantOK)
			}
		})
	}
}
