package api

import (
	"encoding/json"
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
