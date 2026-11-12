package astonish

import (
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/config"
	"github.com/charmbracelet/x/ansi"
	"github.com/creack/pty"
)

// The provider only supplies model tool calls. The actual CLI owns approvals,
// tool execution, snapshot persistence, restoration and all terminal rendering.
func TestPlanRevisionCodePTYAcceptance(t *testing.T) {
	home := t.TempDir()
	// This sibling is declared in the plan but never written: plan approval and
	// lexical revision comparison are not external filesystem authorization.
	externalTarget := filepath.Join(t.TempDir(), "hello.txt")
	root, err := os.OpenRoot(home)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home)
	t.Setenv("SUDO_USER", "")
	var mu sync.Mutex
	stage, issued := 0, false
	planningCall := 0
	arm := func(n int) { mu.Lock(); stage, issued = n, false; mu.Unlock() }
	snapshot := func() (agent.PlanSnapshot, string) {
		var s agent.PlanSnapshot
		var sid string
		_ = fs.WalkDir(root.FS(), ".", func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".PLAN.md") {
				return nil
			}
			raw, e := root.ReadFile(path)
			if e != nil {
				return nil
			}
			doc, goal, steps, e := agent.ParsePlanDocument(string(raw))
			if e == nil {
				s = agent.PlanSnapshot{PlanID: doc.PlanID, Revision: doc.Revision, Sequence: doc.Sequence, Goal: goal, Document: doc, Steps: steps, PendingRevision: doc.PendingRevision}
				sid = strings.TrimSuffix(filepath.Base(path), ".PLAN.md")
			}
			return nil
		})
		return s, sid
	}
	steps := []agent.PlanStepDefinition{}
	for i := 1; i <= 3; i++ {
		steps = append(steps, agent.PlanStepDefinition{Name: fmt.Sprintf("phase-%d", i), Description: fmt.Sprintf("Phase %d", i), Details: "Implement the marker and verify it", Summary: "A verifiable marker", Outcome: "Marker exists", Files: []agent.PlanFileChange{{Path: "marker", Kind: "modify"}}, Verify: fmt.Sprintf("printf verified-%d >> marker", i), VerifyKind: "behavior"})
	}
	steps[1].Files = []agent.PlanFileChange{{Path: externalTarget, Kind: "new"}}
	steps[1].Details = "Write Hello without a newline"
	steps[1].Outcome = "Hello without a newline"
	steps[1].Verify = fmt.Sprintf("test \"$(wc -c < %q)\" -eq 5", externalTarget)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/models") {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"data":[{"id":"pty-model"}]}`)
			return
		}
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/chat/completions") {
			http.NotFound(w, r) // Optional provider metadata/capability probes.
			return
		}
		var req struct {
			Stream   bool            `json:"stream"`
			Messages json.RawMessage `json:"messages"`
			Tools    []struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		mu.Lock()
		n, call := stage, !issued && len(req.Tools) > 0
		if call && stage != 0 {
			issued = true
		}
		p := planningCall
		if call && stage == 0 {
			planningCall++
			issued = planningCall >= 3
		}
		mu.Unlock()
		t.Logf("provider stage=%d stream=%t tools=%d call=%t", n, req.Stream, len(req.Tools), call)
		if req.Stream && !call && n == 0 {
			var messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			}
			_ = json.Unmarshal(req.Messages, &messages)
			for _, m := range messages {
				if m.Role == "tool" {
					t.Logf("tool result: %s", m.Content)
				}
			}
		}
		name := ""
		var args any
		if call {
			switch n {
			case 0:
				if p == 0 {
					name, args = "gplan_gaps", map[string]any{"gaps": []any{map[string]any{"question": "Confirm marker acceptance", "why_codegraph_insufficient": "The temporary fixture project has no indexed code"}}}
					break
				}
				if p == 1 {
					name, args = "gplan_finalize", map[string]any{"acceptance": "Observe verified marker", "notes": "The fixture only writes and verifies marker"}
					break
				}
				name = "announce_plan"
				args = map[string]any{"goal": "PTY revision plan", "context": "## Problem\nNeed revisions\n## Root causes\nmarker:1\n## Approach\nVerify markers\n## Boundaries\nOnly marker", "what_not_to_do": "Do not change unrelated files", "verification": "Observe verified marker", "steps": steps}
			case 1:
				name = "update_plan"
				args = map[string]any{"step": "phase-1", "status": "complete"}
			case 2:
				s, _ := snapshot()
				d := s.Definition()
				if len(d.Steps) != 3 {
					t.Errorf("missing durable plan at provider stage %d: %+v", n, s)
					break
				}
				d.Steps[1].Details = "Write HelloWorld without a newline"
				d.Steps[1].Outcome = "HelloWorld without a newline"
				d.Steps[1].Verify = fmt.Sprintf("test \"$(wc -c < %q)\" -eq 10", externalTarget)
				goal := "HelloWorld greeting plan"
				d.Document.Context = "## Problem\nWrite HelloWorld\n## Root causes\nhello.txt:1\n## Approach\nKeep the same external target\n## Boundaries\nDo not write until authorized"
				name = "update_plan"
				args = map[string]any{"action": "revise", "revision": agent.PlanRevisionRequest{PlanID: s.PlanID, BaseRevision: s.Revision, Reason: "Correct greeting", Goal: &goal, Document: &d.Document, Steps: d.Steps, RequiresApproval: true}}
			}
		}
		text := fmt.Sprintf("PTY turn %d ready", n)
		message := map[string]any{"role": "assistant", "content": text}
		finish := "stop"
		if name != "" {
			raw, _ := json.Marshal(args)
			message = map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"index": 0, "id": fmt.Sprintf("pty-%d-%s", n, name), "type": "function", "function": map[string]any{"name": name, "arguments": string(raw)}}}}
			finish = "tool_calls"
		}
		if req.Stream {
			w.Header().Set("Content-Type", "text/event-stream")
			raw, _ := json.Marshal(map[string]any{"id": "pty", "object": "chat.completion.chunk", "model": "pty-model", "choices": []any{map[string]any{"index": 0, "delta": message}}})
			fmt.Fprintf(w, "data: %s\n\n", raw)
			raw, _ = json.Marshal(map[string]any{"id": "pty", "choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": finish}}})
			fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", raw)
		} else {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "pty", "model": "pty-model", "choices": []any{map[string]any{"index": 0, "message": message, "finish_reason": finish}}})
		}
	}))
	defer server.Close()
	if err := config.SaveAppConfig(&config.AppConfig{General: config.GeneralConfig{DefaultProvider: "fixture", DefaultModel: "pty-model"}, Providers: map[string]config.ProviderConfig{"fixture": {"type": "openai_compat", "base_url": server.URL, "api_key": "fixture"}}}); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(home, "bin")
	if err := os.MkdirAll(bin, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "npx"), []byte("#!/bin/sh\nexit 127\n"), 0700); err != nil {
		t.Fatal(err)
	}
	type terminal struct {
		pt     *os.File
		cmd    *exec.Cmd
		mu     sync.Mutex
		output string
		screen *revisionScreen
		done   chan struct{}
	}
	start := func(sid string) *terminal {
		args := []string{"-test.run=^TestBotCLIProcess$", "--", "code", "--model", "fixture:pty-model", "--dir", home}
		if sid != "" {
			args = append(args, "--resume", sid)
		}
		cmd := exec.Command(os.Args[0], args...)
		cmd.Dir = home
		cmd.Env = append(os.Environ(), "ASTONISH_BOT_CLI_CHILD=1", "TERM=xterm-256color", "NO_COLOR=1", "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
		pt, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 30, Cols: 100})
		if err != nil {
			t.Fatal(err)
		}
		term := &terminal{pt: pt, cmd: cmd, done: make(chan struct{}), screen: newRevisionScreen(100, 30)}
		go func() {
			defer close(term.done)
			var response botPTYTerminal
			buf := make([]byte, 8192)
			for {
				n, e := pt.Read(buf)
				if n > 0 {
					_ = response.respond(pt, buf[:n])
					term.mu.Lock()
					term.output += string(buf[:n])
					term.screen.p.Parse(buf[:n])
					if len(term.output) > 200000 {
						term.output = term.output[len(term.output)-200000:]
					}
					term.mu.Unlock()
				}
				if e != nil {
					return
				}
			}
		}()
		return term
	}
	stop := func(term *terminal) {
		_ = syscall.Kill(-term.cmd.Process.Pid, syscall.SIGKILL)
		_ = term.cmd.Wait()
		_ = term.pt.Close()
		<-term.done
	}
	term := start("")
	defer func() { stop(term) }()
	output := func() string { term.mu.Lock(); defer term.mu.Unlock(); return ansi.Strip(term.output) }
	wait := func(text string) {
		t.Helper()
		until := time.Now().Add(25 * time.Second)
		for time.Now().Before(until) {
			if strings.Contains(output(), text) {
				t.Logf("PTY rendered %q", text)
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		s := output()
		if len(s) > 6000 {
			s = s[len(s)-6000:]
		}
		t.Fatalf("missing %q: %s", text, s)
	}
	waitScreen := func(wants ...string) {
		t.Helper()
		until := time.Now().Add(25 * time.Second)
		var frame string
		for time.Now().Before(until) {
			term.mu.Lock()
			frame = term.screen.text()
			term.mu.Unlock()
			ok := true
			for _, want := range wants {
				if !strings.Contains(frame, want) {
					ok = false
					break
				}
			}
			if ok {
				t.Logf("current 100x30 screen:\n%s", frame)
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatalf("current screen missing simultaneous %q:\n%s", wants, frame)
	}
	send := func(text string) {
		t.Helper()
		time.Sleep(200 * time.Millisecond)
		term.mu.Lock()
		term.output = ""
		term.mu.Unlock()
		if _, e := io.WriteString(term.pt, text); e != nil {
			t.Fatal(e)
		}
	}
	state := func(check func(agent.PlanSnapshot) bool) agent.PlanSnapshot {
		t.Helper()
		until := time.Now().Add(20 * time.Second)
		for time.Now().Before(until) {
			s, _ := snapshot()
			if check(s) {
				return s
			}
			time.Sleep(20 * time.Millisecond)
		}
		s, _ := snapshot()
		t.Fatalf("durable state timeout: %+v; terminal: %.4000s", s, output())
		return s
	}
	wait("pty-model")
	send("/plan\r")
	send("create revision plan\r")
	wait("Plan Ready")
	wait("Approve & implement")
	wait("Request changes")
	wait("Decline")
	arm(1)
	send("\r")
	wait("PTY turn 1 ready")
	s := state(func(s agent.PlanSnapshot) bool { return len(s.Steps) == 3 && s.Steps[0].Status == "complete" })
	evidence := s.Steps[0].Evidence
	assertExternal := func(s agent.PlanSnapshot, outcome string) {
		t.Helper()
		step := s.Steps[1]
		if step.Files[0].Path != externalTarget || step.Outcome != outcome || step.Status != "pending" || step.Evidence != "" {
			t.Fatalf("external target changed or executed: %+v", step)
		}
		if _, err := os.Stat(externalTarget); !os.IsNotExist(err) {
			t.Fatalf("revision/approval created external target: %v", err)
		}
	}
	assertExternal(s, "Hello without a newline")
	if evidence == "" {
		t.Fatal("real verifier evidence missing")
	}
	if raw, e := os.ReadFile(filepath.Join(home, "marker")); e != nil || !strings.Contains(string(raw), "verified") {
		t.Fatalf("verifier marker: %q %v", raw, e)
	}
	assertNoReview := func() {
		t.Helper()
		time.Sleep(300 * time.Millisecond)
		term.mu.Lock()
		frame := term.screen.text()
		term.mu.Unlock()
		for _, forbidden := range []string{"Approve revision", "Reject revision", "Request changes", "Revision approval", "BEFORE:", "AFTER:", "Approve & implement"} {
			if strings.Contains(frame, forbidden) {
				t.Fatalf("unexpected current-screen review %q:\n%s", forbidden, frame)
			}
		}
	}
	arm(2)
	send("change Hello to HelloWorld including goal outcome and document\r")
	wait("PTY turn 2 ready")
	s = state(func(s agent.PlanSnapshot) bool { return s.Revision == 2 && s.PendingRevision == nil })
	assertExternal(s, "HelloWorld without a newline")
	if s.Goal != "HelloWorld greeting plan" || !strings.Contains(s.Document.Context, "Write HelloWorld") || s.Steps[1].Details != "Write HelloWorld without a newline" {
		t.Fatalf("definition revision missing: %+v", s)
	}
	// Document/goal scope changes may conservatively invalidate prior evidence;
	// the plan must not fabricate fresh verification while applying the revision.
	if s.Steps[0].Evidence != "" && s.Steps[0].Evidence != evidence {
		t.Fatal("revision fabricated evidence")
	}
	assertNoReview()
	arm(3)
	send("continue normally\r")
	wait("PTY turn 3 ready")
	assertNoReview()
	_, sid := snapshot()
	stop(term)
	term = start(sid)
	wait("pty-model")
	s = state(func(s agent.PlanSnapshot) bool { return s.Revision == 2 && s.PendingRevision == nil })
	assertExternal(s, "HelloWorld without a newline")
	if s.Goal != "HelloWorld greeting plan" {
		t.Fatal("restart lost revised goal")
	}
	assertNoReview()
	arm(4)
	send("continue after restart\r")
	waitScreen("PTY turn 4 ready")
	assertNoReview()
	t.Log("Actual Code CLI: initial Enter approval, immediate external Hello-to-HelloWorld goal/outcome/document revision, no review controls, normal continuation and restart verified.")
}
