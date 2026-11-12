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
	persistentsession "github.com/SAP/astonish/pkg/session"
	"github.com/creack/pty"
)

// This is a real Code composition-root subprocess in a PTY, not a model.View
// test. Only the provider is scripted: delegate_tasks, child edit_file, durable
// session state, detail loading, terminal input and rendering are production.
func TestDelegationHandoffCodePTYAcceptance(t *testing.T) {
	home := t.TempDir()
	work := filepath.Join(home, "project")
	if err := os.Mkdir(work, 0700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(work, "greeting.txt")
	if err := os.WriteFile(target, []byte("before-child-edit\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home)
	t.Setenv("SUDO_USER", "")
	const parentSecret = "PARENT-PRIVATE-CONTEXT-7b19"
	const task = "Update the greeting file"
	const instructions = "CHILD-ASSIGNMENT-42: change only greeting.txt."
	var mu sync.Mutex
	delegated, read, edited, childResultSeen := false, false, false, false
	var childInput string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/models") {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"data":[{"id":"pty-model"}]}`)
			return
		}
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/chat/completions") {
			http.NotFound(w, r)
			return
		}
		var req struct {
			Stream   bool              `json:"stream"`
			Messages json.RawMessage   `json:"messages"`
			Tools    []json.RawMessage `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		isChild := strings.Contains(string(req.Messages), "Execute the immutable delegation handoff")
		name, text := "", "Delegation acceptance finished"
		if len(req.Tools) == 0 {
			text = "Greeting acceptance"
		}
		var args any
		mu.Lock()
		if len(req.Tools) > 0 && isChild {
			if childInput == "" {
				childInput = string(req.Messages)
			}
			if !read {
				read = true
				name, args = "read_file", map[string]any{"path": target}
			} else if !edited {
				edited = true
				name, args = "edit_file", map[string]any{"path": target, "old_string": "before-child-edit", "new_string": "after-child-edit", "regex": false, "replace_all": false}
			} else {
				childResultSeen = strings.Contains(string(req.Messages), "verification_context")
				text = "Child greeting edit verified"
			}
		} else if len(req.Tools) > 0 && !delegated {
			delegated = true
			name, args = "delegate_tasks", map[string]any{"tasks": []any{map[string]any{"name": "greeting-worker", "task": task, "instructions": instructions, "tools": []string{"read_file", "edit_file"}}}}
		}
		mu.Unlock()
		t.Logf("loopback provider: child=%t tools=%d call=%s", isChild, len(req.Tools), name)
		message := map[string]any{"role": "assistant", "content": text}
		finish := "stop"
		if name != "" {
			raw, _ := json.Marshal(args)
			message = map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"index": 0, "id": "pty-" + name, "type": "function", "function": map[string]any{"name": name, "arguments": string(raw)}}}}
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
	// Keep the optional CodeGraph startup from downloading npm packages. This
	// executable lives only in the private HOME; no installed binary is changed.
	bin := filepath.Join(home, "bin")
	if err := os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "npx"), []byte("#!/bin/sh\nexit 127\n"), 0700); err != nil {
		t.Fatal(err)
	}
	start := func(sid string) *handoffPTY {
		args := []string{"-test.run=^TestBotCLIProcess$", "--", "code", "--model", "fixture:pty-model", "--dir", work, "--yolo"}
		if sid != "" {
			args = append(args, "--resume", sid)
		}
		cmd := exec.Command(os.Args[0], args...)
		cmd.Dir = work
		cmd.Env = append(os.Environ(), "ASTONISH_BOT_CLI_CHILD=1", "TERM=xterm-256color", "NO_COLOR=1", "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
		return newHandoffPTY(t, cmd)
	}
	term := start("")
	term.wait("pty-model")
	term.send("Delegate the greeting update. Keep this parent-only context private: " + parentSecret + "\r")
	term.wait("Delegation acceptance finished", "greeting-worker", "Enter send")
	if got, err := os.ReadFile(target); err != nil || string(got) != "after-child-edit\n" {
		t.Fatalf("real child edit: %q, %v", got, err)
	}
	mu.Lock()
	input, gotResult := childInput, childResultSeen
	mu.Unlock()
	if !gotResult {
		t.Fatal("child did not receive real edit verification_context")
	}
	if strings.Contains(input, parentSecret) || !strings.Contains(input, instructions) {
		t.Fatalf("child input leaked parent or missed assignment: %s", input)
	}

	// Read the actual state-only event to identify the parent for --resume.
	var handoff agent.DelegationHandoff
	var record string
	var childPath string
	err := filepath.WalkDir(home, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || !strings.HasSuffix(path, ".jsonl") {
			return nil
		}
		events, err := persistentsession.NewTranscript(path).ReadEvents()
		if err != nil {
			return err
		}
		for _, event := range events {
			if raw, ok := event.Actions.StateDelta[agent.DelegationHandoffStateKey].(string); ok {
				if record != "" {
					return fmt.Errorf("unexpected second child handoff")
				}
				record, childPath = raw, path
				if err := json.Unmarshal([]byte(raw), &handoff); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if handoff.ParentSessionID == "" || handoff.ChildSessionID == "" || handoff.EffectiveInstructions != instructions || handoff.Plan.Status != agent.DelegationPlanMissing {
		t.Fatalf("durable handoff: %+v", handoff)
	}
	rawChild, err := os.ReadFile(childPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(rawChild), "verification_context") {
		t.Fatal("child transcript lacks durable diff")
	}
	assertDetail := func(term *handoffPTY) {
		term.click("greeting-worker")
		term.wait("[Activity]", "− before-child-edit", "+ after-child-edit", "+1 −1")
		term.assertAbsent(parentSecret)
		term.send("\t")
		term.wait("[Handoff]", "Immutable handoff", instructions)
		term.assertAbsent(parentSecret)
		// Retry is a reload of the selected record, never another child run.
		term.send(strings.Repeat("\x1b[B", 12))
		term.wait("no_scoped_plan", "Source mirror (provenance only; not read)")
		term.send("r")
		term.assertAbsent(parentSecret)
		term.send("h")
		term.wait("[Activity]", "− before-child-edit", "+ after-child-edit", "+1 −1")
		term.send("\x1b")
		term.wait("Delegation acceptance finished", "greeting-worker", "Enter send")
	}
	assertDetail(term)
	term.close()
	// Deliberately change the target after completion: restored diffs must use
	// persisted verification_context, not infer a diff from today's filesystem.
	if err := os.WriteFile(target, []byte("unrelated-current-content\n"), 0600); err != nil {
		t.Fatal(err)
	}
	term = start(handoff.ParentSessionID)
	term.wait("Delegation acceptance finished", "greeting-worker", "Enter send")
	assertDetail(term)
	restored, err := os.ReadFile(childPath)
	if err != nil || string(restored) != string(rawChild) {
		t.Fatalf("opening restored attempt mutated child transcript: %v", err)
	}
	t.Logf("real child %s: immutable handoff and persisted edit diff rendered live and after Code restart at 72x26", handoff.ChildSessionID)
}

// Reuse the existing current-cell ANSI emulator and terminal-query responder.
// Every action waits for terminal state; there are no fixed startup sleeps.
type handoffPTY struct {
	t       *testing.T
	pt      *os.File
	cmd     *exec.Cmd
	mu      sync.Mutex
	screen  *revisionScreen
	changed chan struct{}
	done    chan struct{}
	once    sync.Once
}

func newHandoffPTY(t *testing.T, cmd *exec.Cmd) *handoffPTY {
	t.Helper()
	pt, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 26, Cols: 72})
	if err != nil {
		t.Fatal(err)
	}
	p := &handoffPTY{t: t, pt: pt, cmd: cmd, screen: newRevisionScreen(72, 26), changed: make(chan struct{}, 1), done: make(chan struct{})}
	t.Cleanup(p.close)
	go func() {
		defer close(p.done)
		var responder botPTYTerminal
		buf := make([]byte, 8192)
		for {
			n, err := pt.Read(buf)
			if n > 0 {
				if err := responder.respond(pt, buf[:n]); err != nil {
					return
				}
				p.mu.Lock()
				p.screen.p.Parse(buf[:n])
				p.mu.Unlock()
				select {
				case p.changed <- struct{}{}:
				default:
				}
			}
			if err != nil {
				return
			}
		}
	}()
	return p
}
func (p *handoffPTY) close() {
	p.once.Do(func() {
		_ = syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL)
		_ = p.cmd.Wait()
		_ = p.pt.Close()
		<-p.done
	})
}
func (p *handoffPTY) frame() string { p.mu.Lock(); defer p.mu.Unlock(); return p.screen.text() }
func (p *handoffPTY) send(s string) {
	p.t.Helper()
	if _, err := io.WriteString(p.pt, s); err != nil {
		p.t.Fatal(err)
	}
}
func (p *handoffPTY) wait(wants ...string) string {
	p.t.Helper()
	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()
	for {
		frame, ok := p.frame(), true
		for _, want := range wants {
			if !strings.Contains(frame, want) {
				ok = false
			}
		}
		if ok {
			p.t.Logf("72x26 frame matches %q:\n%s", wants, frame)
			return frame
		}
		select {
		case <-p.changed:
		case <-p.done:
			p.t.Fatalf("Code exited before %q:\n%s", wants, p.frame())
		case <-timer.C:
			p.t.Fatalf("Code screen missing %q:\n%s", wants, p.frame())
		}
	}
}
func (p *handoffPTY) assertAbsent(s string) {
	p.t.Helper()
	if strings.Contains(p.frame(), s) {
		p.t.Fatalf("detail leaked %q:\n%s", s, p.frame())
	}
}
func (p *handoffPTY) click(label string) {
	p.t.Helper()
	frame := p.wait(label)
	for row, line := range strings.Split(frame, "\n") {
		if i := strings.Index(line, label); i >= 0 {
			col := len([]rune(line[:i])) + 2
			p.send(fmt.Sprintf("\x1b[<0;%d;%dM\x1b[<0;%d;%dm", col, row+1, col, row+1))
			return
		}
	}
	p.t.Fatalf("no clickable %q", label)
}
