package sandbox

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// TestParseNodeResponse_CleanOutput verifies normal NDJSON protocol parsing.
func TestParseNodeResponse_CleanOutput(t *testing.T) {
	stdout := []byte("{\"ready\":true}\n{\"id\":\"1\",\"result\":{\"output\":\"hello\"}}\n")
	result, err := parseNodeResponse(stdout, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var parsed struct {
		Output string `json:"output"`
	}
	if err := json.Unmarshal(result, &parsed); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if parsed.Output != "hello" {
		t.Errorf("result.output = %q, want %q", parsed.Output, "hello")
	}
}

// TestParseNodeResponse_ANSIBannerPrefix is the regression test for the
// "new version available" ANSI banner that corrupted the NDJSON stream.
// The decoder must skip the non-JSON lines and find the response.
func TestParseNodeResponse_ANSIBannerPrefix(t *testing.T) {
	// Exact reproduction of what the sandbox-base binary emitted.
	stdout := []byte(
		"\x1b[93mA new version of Astonish is available: v2.9.1\x1b[0m\n" +
			"\x1b[93mRun \x1b[1mbrew upgrade SAP/astonish/astonish\x1b[0m\x1b[93m to update.\x1b[0m\n" +
			"\n" +
			"{\"ready\":true}\n" +
			"{\"id\":\"1\",\"result\":{\"content\":\"file contents here\"}}\n",
	)
	result, err := parseNodeResponse(stdout, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var parsed struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal(result, &parsed); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if parsed.Content != "file contents here" {
		t.Errorf("result.content = %q, want %q", parsed.Content, "file contents here")
	}
}

// TestParseNodeResponse_ErrorFromNode verifies that node-side errors are
// propagated correctly.
func TestParseNodeResponse_ErrorFromNode(t *testing.T) {
	stdout := []byte("{\"ready\":true}\n{\"id\":\"1\",\"error\":\"file not found\"}\n")
	_, err := parseNodeResponse(stdout, 0)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if got := err.Error(); got != "node: file not found" {
		t.Errorf("error = %q, want %q", got, "node: file not found")
	}
}

// TestParseNodeResponse_NoResponse verifies proper error when stdout has no
// valid NDJSON response (e.g. the node process crashed before emitting one).
func TestParseNodeResponse_NoResponse(t *testing.T) {
	stdout := []byte("some random output\nnothing useful\n")
	_, err := parseNodeResponse(stdout, 1)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if got := err.Error(); !contains(got, "no node response") {
		t.Errorf("error = %q, want contains %q", got, "no node response")
	}
	if got := err.Error(); !contains(got, "exit=1") {
		t.Errorf("error = %q, want contains %q", got, "exit=1")
	}
}

// TestParseNodeResponse_GarbageBetweenLines verifies robustness when garbage
// lines are interspersed between valid NDJSON lines.
func TestParseNodeResponse_GarbageBetweenLines(t *testing.T) {
	stdout := []byte(
		"locale: cannot set LC_ALL\n" +
			"{\"ready\":true}\n" +
			"WARNING: something something\n" +
			"{\"id\":\"42\",\"result\":{\"ok\":true}}\n" +
			"trailing garbage\n",
	)
	result, err := parseNodeResponse(stdout, 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var parsed struct {
		OK bool `json:"ok"`
	}
	if err := json.Unmarshal(result, &parsed); err != nil {
		t.Fatalf("unmarshal result: %v", err)
	}
	if !parsed.OK {
		t.Error("result.ok = false, want true")
	}
}

// TestParseNodeResponse_EmptyStdout verifies error on empty output.
func TestParseNodeResponse_EmptyStdout(t *testing.T) {
	_, err := parseNodeResponse(nil, 137)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if got := err.Error(); !contains(got, "exit=137") {
		t.Errorf("error = %q, want contains %q", got, "exit=137")
	}
}

// TestParseNodeResponse_LargeLines pins the wire-byte budget, excluding LF/CRLF.
// Keep the literal budget here so the tests can first run against the old parser.
func TestParseNodeResponse_LargeLines(t *testing.T) {
	const budget = 10 * 1024 * 1024
	for _, size := range []int{64*1024 + 1, 256 * 1024, budget - 1, budget, budget + 1, budget + 2} {
		for _, field := range []string{"result", "error"} {
			for _, ending := range []struct{ name, value string }{{"LF", "\n"}, {"CRLF", "\r\n"}, {"EOF", ""}} {
				t.Run(fmt.Sprintf("%s/%d/%s", field, size, ending.name), func(t *testing.T) {
					prefix := fmt.Sprintf(`{"id":"1","%s":"`, field)
					payload := strings.Repeat("x", size-len(prefix)-2)
					line := prefix + payload + `"}`
					stdout := []byte("\x1b[93mstartup banner\x1b[0m\n{\"ready\":true}\n" + line + ending.value)
					result, err := parseNodeResponse(stdout, 17)
					if size > budget {
						if err == nil || !strings.Contains(err.Error(), "node response line exceeds") || !strings.Contains(err.Error(), "10485760") || !strings.Contains(err.Error(), "exit=17") {
							t.Fatalf("want explicit line-budget error, got %v", err)
						}
						if len(err.Error()) > 512 || result != nil {
							t.Fatal("oversize diagnostics must be bounded and return no result")
						}
						return
					}
					if field == "error" {
						if err == nil || err.Error() != "node: "+payload || result != nil {
							t.Fatalf("node error payload not preserved (%d-byte line)", size)
						}
						return
					}
					if err != nil {
						t.Fatalf("parse %d-byte line: %v", size, err)
					}
					var got string
					if err := json.Unmarshal(result, &got); err != nil || got != payload {
						t.Fatalf("result payload not preserved (%d-byte line): %v", size, err)
					}
				})
			}
		}
	}
}

func TestParseNodeResponse_MalformedAndNoResponse(t *testing.T) {
	for _, prefix := range []string{"", "{\"ready\":true}\n", "{malformed}\n", "{" + strings.Repeat("x", 256*1024) + "\n"} {
		t.Run(fmt.Sprintf("prefix-%d", len(prefix)), func(t *testing.T) {
			result, err := parseNodeResponse([]byte(prefix+`{"id":"1","result":{"ok":true}}`), 0)
			if err != nil || string(result) != `{"ok":true}` {
				t.Fatalf("malformed/ready prefix should be skipped: result=%s err=%v", result, err)
			}
			_, err = parseNodeResponse([]byte(prefix), 23)
			if err == nil || !strings.Contains(err.Error(), "no node response") || !strings.Contains(err.Error(), "exit=23") || !strings.Contains(err.Error(), fmt.Sprintf("%d bytes", len(prefix))) {
				t.Fatalf("missing no-response diagnostics: %v", err)
			}
			if len(err.Error()) > 512 {
				t.Fatalf("diagnostic not bounded: %d bytes", len(err.Error()))
			}
		})
	}
}

func TestParseNodeResponse_FirstResponseWins(t *testing.T) {
	stdout := []byte(`{"id":"first","result":true}` + "\n" + strings.Repeat("x", 10*1024*1024+2))
	result, err := parseNodeResponse(stdout, 0)
	if err != nil || string(result) != "true" {
		t.Fatalf("must stop at first response: result=%s err=%v", result, err)
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsStr(s, substr))
}

func containsStr(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
