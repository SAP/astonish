package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAgentTerminalWaitDoesNotReuseOldInspectorText(t *testing.T) {
	p := &agentTerminalPTY{done: make(chan struct{})}
	p.output.WriteString("Bots from an earlier screen")
	close(p.done)
	if err := p.waitSince("Bots", p.output.Len()); err == nil {
		t.Fatal("earlier screen satisfied a new inspector wait")
	}
	if err := p.waitSince("Bots", 0); err != nil {
		t.Fatal("existing startup output cannot be observed")
	}
}

func TestAgentTerminalFailureEvidenceIsRetained(t *testing.T) {
	root := t.TempDir()
	p := &agentTerminalPTY{}
	p.output.WriteString("\x1b[2J\x1b[Hmissing answer")
	pass := agentTerminalPass{Frames: map[string]agentTerminalFrame{"failure": p.rawFrame(220)}, Exchanges: []agentTerminalExchange{{Path: "raw", Status: 503, Body: "unavailable"}}}
	failure := fmt.Errorf("CLI Home final publication not rendered")
	ref, err := agentWriteTerminalManifest(root, "http://127.0.0.1:12345", "bot", "hash", map[string]any{}, []agentTerminalPass{pass}, 0, failure)
	if err != failure || ref == nil {
		t.Fatalf("lost failure or receipt: %v %v", ref, err)
	}
	raw, err := os.ReadFile(filepath.Join(root, ref["Source"]))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		ComparisonError string              `json:"comparison_error"`
		Passes          []agentTerminalPass `json:"passes"`
	}
	if json.Unmarshal(raw, &doc) != nil || doc.ComparisonError != failure.Error() || len(doc.Passes) != 1 || doc.Passes[0].Frames["failure"] != pass.Frames["failure"] || doc.Passes[0].Exchanges[0].Body != "unavailable" {
		t.Fatal("failure evidence changed")
	}
}

func TestAgentTerminalFrameRetainsInitialCursorAndControlHistory(t *testing.T) {
	p := &agentTerminalPTY{cols: 220}
	wire := "\x1b[?1049h\x1b[2J\x1b[Hinitial\r\nvisible answer"
	p.output.WriteString(wire)
	frame, err := p.frame("", "visible answer")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := base64.StdEncoding.DecodeString(frame.Data)
	if err != nil || string(raw) != wire || frame.Cols != 220 {
		t.Fatal("frame lost initial terminal state")
	}
	p.output.WriteString("\r\x1b[2Kreplacement")
	frame, err = p.frame("", "replacement")
	if err != nil {
		t.Fatal(err)
	}
	raw, err = base64.StdEncoding.DecodeString(frame.Data)
	if err != nil || string(raw) != wire+"\r\x1b[2Kreplacement" {
		t.Fatal("later frame lost cursor/erase history")
	}
}

func TestAgentTerminalRawFramePreservesControls(t *testing.T) {
	p := &agentTerminalPTY{}
	wire := []byte("\x1b[2J\x1b[Hhidden\rvisible\x1b[0m")
	p.output.Write(wire)
	frame := p.rawFrame(219)
	decoded, err := base64.StdEncoding.DecodeString(frame.Data)
	if err != nil || !bytes.Equal(decoded, wire) || frame.Encoding != "base64" || frame.Rows != 120 || frame.Cols != 219 {
		t.Fatalf("raw capture changed: %+v %v", frame, err)
	}
	if strings.Contains(p.text(), "\x1b") {
		t.Fatal("wait helper stopped stripping ANSI")
	}
}
func TestAgentTerminalBuildHashComesFromBinary(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "cli")
	body := []byte("fixture native candidate")
	if err := os.WriteFile(binary, body, 0600); err != nil {
		t.Fatal(err)
	}
	state := map[string]any{"binary_sha256": "not-the-cli", "agent_cli_sha256": "stale"}
	digest, err := agentBindCLIHash(binary, state)
	if err != nil || digest != fmt.Sprintf("%x", sha256.Sum256(body)) || state["agent_cli_sha256"] != digest {
		t.Fatalf("not independently hashed: %v %v", state, err)
	}
}
func TestAgentTerminalWaitTargetRequiresFinalInvocation(t *testing.T) {
	row := agentTerminalExchange{Path: "/api/studio/bots/b/sessions/h/events", Status: 200, Body: `data: {"invocationId":"worker","content":{"parts":[{"text":"Actual answer"}]}}`}
	if agentTerminalAnswer([]agentTerminalExchange{row}) != "" {
		t.Fatal("draft accepted")
	}
	row.Body = strings.ReplaceAll(row.Body, "worker", "bot-report:run")
	if agentTerminalAnswer([]agentTerminalExchange{row}) != "Actual" {
		t.Fatal("final target absent")
	}
	latest := row
	latest.Body = strings.ReplaceAll(latest.Body, "Actual answer", "Newest publication")
	if agentTerminalAnswer([]agentTerminalExchange{row, latest}) != "Newest" {
		t.Fatal("wait target reused an earlier run publication")
	}
	barrier := agentTerminalExchange{Path: row.Path, Status: 200, Body: "event: replay_complete\ndata: {}"}
	if agentTerminalLiveAnswer([]agentTerminalExchange{row}) != "" || agentTerminalLiveAnswer([]agentTerminalExchange{row, barrier}) != "" {
		t.Fatal("replayed publication satisfied live wait target")
	}
	if agentTerminalLiveAnswer([]agentTerminalExchange{row, barrier, latest}) != "Newest" {
		t.Fatal("live publication not selected after replay barrier")
	}
}

func TestAgentTerminalRejectsMutation(t *testing.T) {
	for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		for _, path := range []string{"/api/studio/bots/b/messages", "/api/studio/bots/b/skills", "/api/studio/bots/b/runs/r/cancel", "/api/studio/bots/b/proposals/p/decision"} {
			if agentTerminalAllowed(method, path) {
				t.Fatalf("allowed mutation %s %s", method, path)
			}
		}
	}
	for _, path := range []string{"/api/auth/login", "/oauth/token"} {
		if !agentTerminalAllowed(http.MethodPost, path) {
			t.Fatal("native auth blocked")
		}
	}
}
func TestAgentTerminalIsolatedEnvironment(t *testing.T) {
	t.Setenv("ASTONISH_API_KEY", "secret")
	t.Setenv("HOME", "/normal-user")
	t.Setenv("SUDO_USER", "normal")
	env := strings.Join(agentTerminalEnvironment("/owned"), "\n")
	if strings.Contains(env, "secret") || strings.Contains(env, "normal") || !strings.Contains(env, "XDG_CONFIG_HOME=/owned") {
		t.Fatal("inherited live configuration")
	}
}
func TestAgentTerminalRejectsNormalDaemon(t *testing.T) {
	for _, base := range []string{"http://127.0.0.1:9393", "http://example.com:1234", "http://127.0.0.1:1234/path", "http://user:pass@127.0.0.1:1234"} {
		if _, err := agentObserveTerminal("/tmp/astonish-chat-first-owned", base, "", nil); err == nil {
			t.Fatal("unsafe target accepted")
		}
	}
}
func TestAgentTerminalRejectsReconnectDrift(t *testing.T) {
	rows := []agentTerminalExchange{{"/api/studio/bots/b/snapshot", 200, `{"run_states":[{"run_id":"r","sequence":2,"work":{"summary":"a"}}]}`}, {"/api/studio/bots/b/skills", 200, `{"skills":[]}`}, {"/api/studio/bots/b/sessions/h/events", 200, "id: h:1\nevent: transcript\ndata: {\"id\":\"final-1\",\"text\":\"answer\"}"}}
	base := agentTerminalPass{Exchanges: rows}
	if err := agentTerminalCompare([]agentTerminalPass{base, base}); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func([]agentTerminalExchange) []agentTerminalExchange{
		func(r []agentTerminalExchange) []agentTerminalExchange {
			r[0].Body = `{"run_states":[{"sequence":3}]}`
			return r
		},
		func(r []agentTerminalExchange) []agentTerminalExchange {
			r[1].Body = `{"skills":[{"revision":9}]}`
			return r
		},
		func(r []agentTerminalExchange) []agentTerminalExchange {
			r[2].Body = strings.ReplaceAll(r[2].Body, "final-1", "final-2")
			return r
		},
		func(r []agentTerminalExchange) []agentTerminalExchange { r[0].Status = 403; return r },
		func(r []agentTerminalExchange) []agentTerminalExchange { return r[:2] },
	} {
		changed := agentTerminalPass{Exchanges: mutate(append([]agentTerminalExchange{}, rows...))}
		if agentTerminalCompare([]agentTerminalPass{base, changed}) == nil {
			t.Fatal("accepted missing/changed projection, skills, or final identity")
		}
	}
}
func TestAgentTerminalSplitQueries(t *testing.T) {
	wire := "text\x1b]11;?\x1b\\\x1b[6n\x1b[c"
	for size := 1; size <= len(wire); size++ {
		var got bytes.Buffer
		pending := ""
		for start := 0; start < len(wire); start += size {
			pending = agentTerminalRespond(&got, pending+wire[start:min(start+size, len(wire))])
		}
		if pending != "" || got.String() != "\x1b]11;rgb:0000/0000/0000\x1b\\\x1b[1;1R\x1b[?1;2c" {
			t.Fatal("query split lost")
		}
	}
}
