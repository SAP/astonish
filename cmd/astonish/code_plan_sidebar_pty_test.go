package astonish

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/config"
	persistentsession "github.com/SAP/astonish/pkg/session"
	"github.com/charmbracelet/x/ansi"
	adkmodel "google.golang.org/adk/model"
	adksession "google.golang.org/adk/session"
	"google.golang.org/genai"
)

// This is an actual Code CLI subprocess, restoring durable snapshots through
// FileStore into current PTY cells, before any sidebar/layout changes. It uses
// only an isolated HOME and loopback model catalogue; no model turn is needed.
func TestCodePlanProgressSummaryPTYAcceptance(t *testing.T) {
	for _, tc := range []struct{ name, status, results, want string }{
		{"failed-only", "failed", "", "0/1 · failed"},
		{"results-backed", "complete", "Verified fixture outcome", "1/1 done"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			work := filepath.Join(home, "project")
			if err := os.Mkdir(work, 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("HOME", home)
			t.Setenv("XDG_CONFIG_HOME", home)
			t.Setenv("SUDO_USER", "")
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v1/model/info" || r.URL.Path == "/model/info" {
					http.NotFound(w, r) // Optional provider capability discovery.
					return
				}
				if r.URL.Path != "/models" && r.URL.Path != "/v1/models" {
					t.Errorf("unexpected model request: %s", r.URL.Path)
					http.Error(w, "no model calls expected", http.StatusBadRequest)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, `{"data":[{"id":"pty-model"}]}`)
			}))
			defer server.Close()
			cfg := &config.AppConfig{General: config.GeneralConfig{DefaultProvider: "fixture", DefaultModel: "pty-model"}, Providers: map[string]config.ProviderConfig{"fixture": {"type": "openai_compat", "base_url": server.URL, "api_key": "fixture"}}}
			if err := config.SaveAppConfig(cfg); err != nil {
				t.Fatal(err)
			}
			base, err := config.GetSessionsDir(&cfg.Sessions)
			if err != nil {
				t.Fatal(err)
			}
			store, err := persistentsession.NewFileStore(filepath.Join(base, "code"))
			if err != nil {
				t.Fatal(err)
			}
			sum := sha256.Sum256([]byte(work))
			ctx := context.Background()
			created, err := store.Create(ctx, &adksession.CreateRequest{AppName: "astonish_code", UserID: fmt.Sprintf("local_user_%x", sum[:8])})
			if err != nil {
				t.Fatal(err)
			}
			doc := agent.PlanDocumentInfo{Lifecycle: agent.PlanLifecycleExecuting, Results: tc.results}
			if tc.results != "" {
				doc.Lifecycle = agent.PlanLifecycleCompleted
			}
			snapshot := agent.PlanSnapshot{PlanID: "summary-plan", Revision: 1, Sequence: 2, Goal: "Plan summary fixture", Document: doc,
				Steps: []agent.PlanStepInfo{{Name: "verify", Description: "Verify fixture", Status: tc.status}}}
			raw, err := json.Marshal(snapshot)
			if err != nil {
				t.Fatal(err)
			}
			var value map[string]any
			if err := json.Unmarshal(raw, &value); err != nil {
				t.Fatal(err)
			}
			if err := store.AppendEvent(ctx, created.Session, &adksession.Event{ID: "summary", Author: "system", Timestamp: time.Now(), Actions: adksession.EventActions{StateDelta: map[string]any{agent.PlanSnapshotStateKey: value}}}); err != nil {
				t.Fatal(err)
			}
			bin := filepath.Join(home, "bin")
			if err := os.Mkdir(bin, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(bin, "npx"), []byte("#!/bin/sh\nexit 127\n"), 0700); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(os.Args[0], "-test.run=^TestBotCLIProcess$", "--", "code", "--model", "fixture:pty-model", "--dir", work, "--resume", created.Session.ID())
			cmd.Dir = work
			cmd.Env = append(os.Environ(), "ASTONISH_BOT_CLI_CHILD=1", "TERM=xterm-256color", "NO_COLOR=1", "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			term := newHandoffPTY(t, cmd)
			term.wait(tc.want)
			// At compact widths Results may push the phase above the viewport.
			term.send(strings.Repeat("\x1b[<64;10;8M", 12))
			term.wait(tc.want, "Verify fixture")
			term.assertAbsent("choose how to proceed")
			if tc.status == "failed" {
				term.assertAbsent("0/1 done")
				term.assertAbsent("1/1 done")
			} else {
				term.wait("RESULTS", tc.results)
			}
			term.close()
		})
	}
}

// Keep the phase-level acceptance entry point stable while naming the terminal
// geometry explicitly for focused reruns.
func TestCodePlanSidebarPTYBasic(t *testing.T) {
	t.Run("120x30", TestCodePlanSidebarPTYBasic120x30)
}

// The panel must survive moving the conversation away from its plan card. All
// assertions inspect current cells (and the rightmost 28 columns), never the
// historical ANSI log. No model generation is necessary for these restore and
// input-routing checks.
func TestCodePlanSidebarPTYBasic120x30(t *testing.T) {
	home := t.TempDir()
	work := filepath.Join(home, "project")
	if err := os.Mkdir(work, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home)
	t.Setenv("SUDO_USER", "")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/model/info" || r.URL.Path == "/model/info" {
			http.NotFound(w, r)
			return
		}
		if r.URL.Path != "/models" && r.URL.Path != "/v1/models" {
			t.Errorf("unexpected model request: %s", r.URL.Path)
			http.Error(w, "no model turn expected", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":[{"id":"pty-model"}]}`)
	}))
	defer server.Close()
	cfg := &config.AppConfig{General: config.GeneralConfig{DefaultProvider: "fixture", DefaultModel: "pty-model"}, Providers: map[string]config.ProviderConfig{"fixture": {"type": "openai_compat", "base_url": server.URL, "api_key": "fixture"}}}
	if err := config.SaveAppConfig(cfg); err != nil {
		t.Fatal(err)
	}
	base, err := config.GetSessionsDir(&cfg.Sessions)
	if err != nil {
		t.Fatal(err)
	}
	store, err := persistentsession.NewFileStore(filepath.Join(base, "code"))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(work))
	ctx := context.Background()
	bin := filepath.Join(home, "bin")
	if err := os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "npx"), []byte("#!/bin/sh\nexit 127\n"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, pending := range []bool{false, true} {
		name := "executing-scroll-resize"
		if pending {
			name = "pending-approval-arrows"
		}
		t.Run(name, func(t *testing.T) {
			created, err := store.Create(ctx, &adksession.CreateRequest{AppName: "astonish_code", UserID: fmt.Sprintf("local_user_%x", sum[:8])})
			if err != nil {
				t.Fatal(err)
			}
			appendEvent := func(event *adksession.Event) {
				t.Helper()
				event.Timestamp = time.Now()
				if err := store.AppendEvent(ctx, created.Session, event); err != nil {
					t.Fatal(err)
				}
			}
			// Earlier transcript makes wheel scrolling visibly move away from the card.
			for i := 0; i < 45; i++ {
				appendEvent(&adksession.Event{ID: fmt.Sprintf("history-%d", i), Author: "agent", LLMResponse: adkmodel.LLMResponse{Content: genai.NewContentFromText(fmt.Sprintf("HISTORY MARKER %02d: earlier conversation before the plan.", i), genai.RoleModel)}})
			}
			snapshot := agent.PlanSnapshot{PlanID: "sidebar-plan", Revision: 1, Sequence: 3, Goal: "Ship sidebar fixture",
				Document: agent.PlanDocumentInfo{PlanID: "sidebar-plan", Revision: 1, Sequence: 3, Lifecycle: agent.PlanLifecycleExecuting, CodeExecutionMode: agent.CodePlanRunToCompletion},
				Steps: []agent.PlanStepInfo{
					{Name: "Prepare fixture", Description: "Prepare fixture", Status: "complete"},
					{Name: "Implement panel", Description: "Implement panel", Status: "running"},
					{Name: "Verify behavior", Description: "Verify behavior", Status: "pending"},
				}}
			if pending {
				snapshot.Document = agent.PlanDocumentInfo{PlanID: snapshot.PlanID, Revision: 1, Sequence: 3}
				for i := range snapshot.Steps {
					snapshot.Steps[i].Status = "pending"
				}
			}
			raw, err := json.Marshal(snapshot)
			if err != nil {
				t.Fatal(err)
			}
			var value map[string]any
			if err := json.Unmarshal(raw, &value); err != nil {
				t.Fatal(err)
			}
			appendEvent(&adksession.Event{ID: "sidebar-snapshot", Author: "system", Actions: adksession.EventActions{StateDelta: map[string]any{agent.PlanSnapshotStateKey: value}}})
			cmd := exec.Command(os.Args[0], "-test.run=^TestBotCLIProcess$", "--", "code", "--model", "fixture:pty-model", "--dir", work, "--resume", created.Session.ID())
			cmd.Dir = work
			cmd.Env = append(os.Environ(), "ASTONISH_BOT_CLI_CHILD=1", "TERM=xterm-256color", "NO_COLOR=1", "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			term := newHandoffPTYSize(t, cmd, 120, 30)
			defer term.close()
			panelWants := []string{"PLAN PROGRESS", snapshot.Goal, "Prepare fixture", "Implement panel", "Verify behavior"}
			if pending {
				panelWants = append(panelWants, "0/3", "0%")
			} else {
				panelWants = append(panelWants, "1/3", "33%", "complete", "running", "pending")
			}
			frame := waitPlanPTYRegion(t, term, 92, 120, panelWants...)
			panel := planPTYColumns(frame, 92, 120)
			if strings.Contains(panel, "CURRENT") || strings.Contains(panel, "Current:") {
				t.Fatalf("duplicate current section: %s", panel)
			}
			if !pending {
				waitPlanPTYRegion(t, term, 92, 120, "● Implement panel", "running · recorded, not live")
			}
			if pending {
				const run = "▸ 1/y  Approve and run autonomously until done"
				const review = "▸ 2    Approve and pause for review after each phase"
				labels := []string{"Approve and run autonomously until done", "Approve and pause for review after each phase", "Request changes", "Decline", "Enter select"}
				term.wait(append(labels, run)...)
				term.send("\x1b[B")
				term.wait(append(labels, review)...)
				term.assertAbsent(run)
				waitPlanPTYRegion(t, term, 92, 120, panelWants...)
				term.send("\x1b[A")
				term.wait(append(labels, run)...)
				term.assertAbsent(review)
				waitPlanPTYRegion(t, term, 92, 120, panelWants...)
				return
			}
			term.assertAbsent("choose how to proceed")
			term.send(strings.Repeat("\x1b[<64;10;8M", 70))
			waitPlanPTYRegion(t, term, 0, 91, "HISTORY MARKER 00", "HISTORY MARKER 30")
			frame = waitPlanPTYRegion(t, term, 92, 120, panelWants...)
			left := planPTYColumns(frame, 0, 91)
			for _, phase := range []string{"Prepare fixture", "Implement panel", "Verify behavior"} {
				if strings.Contains(left, phase) {
					t.Fatalf("plan card did not scroll out of transcript: %s", frame)
				}
			}
			// Real SIGWINCH round trip proves the synchronized fixture is usable
			// by the later narrow/full-list acceptance, not just its constructor.
			term.resize(80, 24)
			term.wait(snapshot.Goal, "1/3", "33%", "Current: Implement panel")
			term.resize(120, 30)
			waitPlanPTYRegion(t, term, 92, 120, panelWants...)
		})
	}
}

// sidebarPTYModelTurn is a barrier at the actual loopback model boundary. A
// tool result must have reached the CLI before the next request is offered.
// Holding a request lets the test inspect a stable frame, not race a fast model.
type sidebarPTYModelTurn struct {
	name string
	args any
	text string
}

type sidebarPTYFixture struct {
	t                                     *testing.T
	work, bin, storePath, user, sessionID string
	turns                                 chan chan sidebarPTYModelTurn
	calls                                 atomic.Int32
}

func newSidebarPTYFixture(t *testing.T) *sidebarPTYFixture {
	t.Helper()
	home := t.TempDir()
	f := &sidebarPTYFixture{t: t, work: filepath.Join(home, "project"), bin: filepath.Join(home, "bin"), turns: make(chan chan sidebarPTYModelTurn)}
	for _, dir := range []string{f.work, f.bin} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	for key, value := range map[string]string{"HOME": home, "XDG_CONFIG_HOME": home, "SUDO_USER": ""} {
		t.Setenv(key, value)
	}
	if err := os.WriteFile(filepath.Join(f.bin, "npx"), []byte("#!/bin/sh\nexit 127\n"), 0700); err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/models") {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"data":[{"id":"pty-model"}]}`)
			return
		}
		if !strings.HasSuffix(r.URL.Path, "/chat/completions") {
			http.NotFound(w, r)
			return
		}
		var req struct {
			Stream   bool              `json:"stream"`
			Tools    []json.RawMessage `json:"tools"`
			Messages json.RawMessage   `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		turn := sidebarPTYModelTurn{text: "Sidebar fixture"}
		n := f.calls.Add(1)
		t.Logf("sidebar loopback request %d tools=%d", n, len(req.Tools))
		if len(req.Messages) > 0 {
			var messages []map[string]any
			_ = json.Unmarshal(req.Messages, &messages)
			if len(messages) > 0 && messages[len(messages)-1]["role"] == "tool" {
				t.Logf("last tool result: %v", messages[len(messages)-1])
			}
		}
		if len(req.Tools) > 0 {
			reply := make(chan sidebarPTYModelTurn)
			select {
			case f.turns <- reply:
			case <-r.Context().Done():
				return
			case <-stop:
				return
			}
			select {
			case turn = <-reply:
			case <-r.Context().Done():
				return
			case <-stop:
				return
			}
		}
		message := map[string]any{"role": "assistant", "content": turn.text}
		finish := "stop"
		if turn.name != "" {
			raw, _ := json.Marshal(turn.args)
			message = map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"index": 0, "id": fmt.Sprintf("sidebar-%d", n), "type": "function", "function": map[string]any{"name": turn.name, "arguments": string(raw)}}}}
			finish = "tool_calls"
		}
		if req.Stream {
			w.Header().Set("Content-Type", "text/event-stream")
			raw, _ := json.Marshal(map[string]any{"id": "sidebar", "object": "chat.completion.chunk", "model": "pty-model", "choices": []any{map[string]any{"index": 0, "delta": message}}})
			fmt.Fprintf(w, "data: %s\n\n", raw)
			raw, _ = json.Marshal(map[string]any{"id": "sidebar", "choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": finish}}})
			fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", raw)
		} else {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "sidebar", "model": "pty-model", "choices": []any{map[string]any{"index": 0, "message": message, "finish_reason": finish}}})
		}
	}))
	t.Cleanup(func() { close(stop); server.Close() })
	cfg := &config.AppConfig{General: config.GeneralConfig{DefaultProvider: "fixture", DefaultModel: "pty-model"}, Providers: map[string]config.ProviderConfig{"fixture": {"type": "openai_compat", "base_url": server.URL, "api_key": "fixture"}}}
	if err := config.SaveAppConfig(cfg); err != nil {
		t.Fatal(err)
	}
	base, err := config.GetSessionsDir(&cfg.Sessions)
	if err != nil {
		t.Fatal(err)
	}
	f.storePath = filepath.Join(base, "code")
	store, err := persistentsession.NewFileStore(f.storePath)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(f.work))
	f.user = fmt.Sprintf("local_user_%x", sum[:8])
	created, err := store.Create(context.Background(), &adksession.CreateRequest{AppName: "astonish_code", UserID: f.user})
	if err != nil {
		t.Fatal(err)
	}
	f.sessionID = created.Session.ID()
	for i := 0; i < 45; i++ {
		e := &adksession.Event{ID: fmt.Sprintf("history-%d", i), Author: "agent", Timestamp: time.Now(), LLMResponse: adkmodel.LLMResponse{Content: genai.NewContentFromText(fmt.Sprintf("HISTORY MARKER %02d: earlier conversation before the plan.", i), genai.RoleModel)}}
		if err := store.AppendEvent(context.Background(), created.Session, e); err != nil {
			t.Fatal(err)
		}
	}
	return f
}

func (f *sidebarPTYFixture) start() *handoffPTY {
	f.t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestBotCLIProcess$", "--", "code", "--model", "fixture:pty-model", "--dir", f.work, "--resume", f.sessionID)
	cmd.Dir = f.work
	cmd.Env = append(os.Environ(), "ASTONISH_BOT_CLI_CHILD=1", "TERM=xterm-256color", "NO_COLOR=1", "PATH="+f.bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	term := newHandoffPTYSize(f.t, cmd, 120, 30)
	term.mu.Lock()
	term.screen = newSidebarPTYScreen(120, 30)
	term.screen.p.Parse([]byte(term.raw.String()))
	term.mu.Unlock()
	return term
}

// The live renderer optimizes edits with character/line insertion/deletion and
// tab movement, which the shared historical screen does not implement. Forward
// its existing protocol unchanged and add these standard operations locally;
// assertions still inspect current cells.
func newSidebarPTYScreen(width, height int) *revisionScreen {
	s := newRevisionScreen(width, height)
	base := s.p
	p := ansi.NewParser()
	p.SetHandler(ansi.Handler{
		Print:   func(r rune) { base.Parse([]byte(string(r))) },
		Execute: func(b byte) { base.Parse([]byte{b}) },
		HandleEsc: func(cmd ansi.Cmd) {
			sequence := []byte{0x1b}
			if cmd.Intermediate() != 0 {
				sequence = append(sequence, cmd.Intermediate())
			}
			base.Parse(append(sequence, cmd.Final()))
		},
		HandleCsi: func(cmd ansi.Cmd, params ansi.Params) {
			if cmd.Prefix() == 0 && (cmd.Final() == 'M' || cmd.Final() == 'L') {
				n, _, _ := params.Param(0, 1)
				if s.y >= s.top && s.y <= s.bottom {
					top := s.top
					s.top = s.y
					if cmd.Final() == 'M' {
						s.scrollUp(max(1, n))
					} else {
						s.scrollDown(max(1, n))
					}
					s.top = top
				}
				return
			}
			if cmd.Prefix() == 0 && (cmd.Final() == 'Z' || cmd.Final() == 'I') {
				n, _, _ := params.Param(0, 1)
				for range max(1, n) {
					if cmd.Final() == 'Z' {
						s.x = max(0, ((s.x-1)/8)*8)
					} else {
						s.x = min(s.w-1, (s.x/8+1)*8)
					}
				}
				return
			}
			if cmd.Prefix() == 0 && (cmd.Final() == 'P' || cmd.Final() == '@') {
				n, _, _ := params.Param(0, 1)
				x := min(s.x, s.w-1)
				n = min(max(1, n), s.w-x)
				row := s.cells[s.y]
				if cmd.Final() == 'P' {
					copy(row[x:], row[x+n:])
					s.erase(s.y, s.w-n, s.w)
				} else {
					copy(row[x+n:], row[x:s.w-n])
					s.erase(s.y, x, x+n)
				}
				return
			}
			var sequence strings.Builder
			sequence.WriteString("\x1b[")
			if cmd.Prefix() != 0 {
				sequence.WriteByte(cmd.Prefix())
			}
			for i := range params {
				v, more, _ := params.Param(i, 0)
				fmt.Fprint(&sequence, v)
				if i+1 < len(params) {
					if more {
						sequence.WriteByte(':')
					} else {
						sequence.WriteByte(';')
					}
				}
			}
			if cmd.Intermediate() != 0 {
				sequence.WriteByte(cmd.Intermediate())
			}
			sequence.WriteByte(cmd.Final())
			base.Parse([]byte(sequence.String()))
		},
	})
	s.p = p
	return s
}

func TestCodePlanSidebarPTYScreenCharacterEdits(t *testing.T) {
	s := newSidebarPTYScreen(30, 3)
	s.p.Parse([]byte("Current: none\x1b[10Go\x1b["))
	s.p.Parse([]byte("P"))
	if !strings.Contains(s.text(), "Current: one ") {
		t.Fatal(s.text())
	}
	s.p.Parse([]byte("\x1b[10G\x1b[2@tw"))
	if !strings.Contains(s.text(), "Current: twone") {
		t.Fatal(s.text())
	}
	s.p.Parse([]byte("\x1b[2;1Hsecond\r\nthird\x1b[2;1H\x1b[M"))
	if string(s.cells[1][:5]) != "third" || strings.TrimSpace(string(s.cells[2])) != "" {
		t.Fatal(s.text())
	}
	s.p.Parse([]byte("\x1b[L"))
	if strings.TrimSpace(string(s.cells[1])) != "" || string(s.cells[2][:5]) != "third" {
		t.Fatal(s.text())
	}
	s.p.Parse([]byte("\x1b[1;18H\x1b[Z"))
	if s.x != 16 {
		t.Fatalf("backtab column=%d", s.x)
	}
	s.p.Parse([]byte("\x1b[I"))
	if s.x != 24 {
		t.Fatalf("forward tab column=%d", s.x)
	}
}

func (f *sidebarPTYFixture) turn(name string, args any) {
	f.t.Helper()
	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()
	select {
	case reply := <-f.turns:
		select {
		case reply <- sidebarPTYModelTurn{name: name, args: args, text: "SIDEBAR EXECUTION FINISHED"}:
		case <-timer.C:
			f.t.Fatal("model request canceled before reply")
		}
	case <-timer.C:
		f.t.Fatal("CLI did not request next scripted model turn")
	}
}

func (f *sidebarPTYFixture) update(step, status string) {
	f.t.Helper()
	f.turn("update_plan", map[string]any{"step": step, "status": status})
}

func (f *sidebarPTYFixture) durable() agent.PlanSnapshot {
	f.t.Helper()
	store, err := persistentsession.NewFileStore(f.storePath)
	if err != nil {
		f.t.Fatal(err)
	}
	got, err := store.Get(context.Background(), &adksession.GetRequest{AppName: "astonish_code", UserID: f.user, SessionID: f.sessionID})
	if err != nil {
		f.t.Fatal(err)
	}
	value, err := got.Session.State().Get(agent.PlanSnapshotStateKey)
	if err != nil {
		f.t.Fatal(err)
	}
	raw, err := json.Marshal(value)
	if err != nil {
		f.t.Fatal(err)
	}
	var snapshot agent.PlanSnapshot
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		f.t.Fatal(err)
	}
	return snapshot
}

func (f *sidebarPTYFixture) announce(term *handoffPTY, failFirst bool) {
	f.t.Helper()
	term.wait("Enter send")
	term.send("\x1b[Z") // Real Shift+Tab enables Plan mode before announcement.
	term.wait("Plan")
	term.send("Announce the three phase sidebar fixture plan.\r")
	f.turn("gplan_gaps", map[string]any{"gaps": []any{map[string]any{"question": "No index in isolated fixture", "why_codegraph_insufficient": "npx deliberately unavailable in temporary HOME"}}})
	f.turn("gplan_finalize", map[string]any{"acceptance": "Observe phase markers and terminal progress", "notes": "Self-contained isolated fixture"})
	var steps []map[string]any
	for _, name := range []string{"one", "two", "three"} {
		marker := filepath.Join(f.work, name+".marker")
		verify := fmt.Sprintf("printf verified-%s > '%s' && test -s '%s'", name, marker, marker)
		if name == "one" && failFirst {
			// The first real verifier fails and leaves its own retry prerequisite;
			// the test never creates or edits verifier artifacts.
			verify = fmt.Sprintf("if test -f '%s'; then printf verified-one > '%s'; else printf retry > '%s'; exit 1; fi", marker, marker, marker)
		}
		group := "samewave"
		if name == "three" {
			group = "finalwave"
		}
		steps = append(steps, map[string]any{"name": name, "description": "Phase " + name, "details": "Run the isolated marker verifier for " + name, "summary": "Verify the " + name + " capability", "outcome": "The " + name + " marker is verified", "verify": verify, "verify_kind": "behavior", "parallel_group": group, "files": []any{map[string]any{"path": marker, "kind": "new"}}})
	}
	f.turn("announce_plan", map[string]any{"goal": "Live sidebar fixture", "context": "## Problem\nProve real CLI plan progress.\n## Root causes\nFixture needs terminal evidence.\n## Approach\nRun isolated marker commands.\n## Boundaries\nTemporary HOME only.", "what_not_to_do": "Never touch user sessions or production files", "verification": "Observe phase markers and terminal progress", "steps": steps})
	term.wait("Approve and run autonomously until done")
	waitPlanPTYRegion(f.t, term, 92, 120, "Awaiting approval", "0/3", "0%", "pending")
}

func sidebarPTYScrollOffCard(t *testing.T, term *handoffPTY) {
	t.Helper()
	term.send(strings.Repeat("\x1b[<64;10;8M", 70))
	waitPlanPTYRegion(t, term, 0, 91, "HISTORY MARKER 00", "HISTORY MARKER 24")
	// A frame can be observed between terminal write chunks. Wait for the old
	// card rows to be erased rather than failing on that intermediate paint.
	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()
	for strings.Contains(planPTYColumns(term.frame(), 0, 91), "Phase one") {
		select {
		case <-term.changed:
		case <-timer.C:
			t.Fatalf("plan card remains visible:\n%s", term.frame())
		}
	}
}

func TestCodePlanSidebarPTYLifecycle(t *testing.T) {
	f := newSidebarPTYFixture(t)
	term := f.start()
	f.announce(term, true)
	term.send("\r")
	f.update("one", "running")
	waitPlanPTYRegion(t, term, 92, 120, "0/3", "0%")
	waitSidebarPTYRunningCard(t, term, "one", "Phase one")
	sidebarPTYScrollOffCard(t, term)
	f.update("one", "complete")
	waitPlanPTYRegion(t, term, 92, 120, "Phase failed", "0/3", "0%", "1 failed")
	term.close()
	if s := f.durable(); s.Steps[0].Status != "failed" {
		t.Fatalf("failed verifier not persisted: %+v", s)
	}
	term = f.start()
	term.wait("Enter send")
	waitPlanPTYRegion(t, term, 92, 120, "Phase failed", "0/3", "0%")
	term.assertAbsent("choose how to proceed")
	term.send("Retry the failed verifier and continue.\r")
	f.update("one", "running")
	waitSidebarPTYRunningCard(t, term, "one", "Phase one")
	f.update("two", "running")
	waitPlanPTYRegion(t, term, 92, 120, "2 running", "0%")
	waitSidebarPTYRunningCard(t, term, "one", "Phase one")
	waitSidebarPTYRunningCard(t, term, "two", "Phase two")
	sidebarPTYScrollOffCard(t, term)
	f.update("one", "complete")
	waitPlanPTYRegion(t, term, 92, 120, "1/3", "33%")
	waitSidebarPTYRunningCard(t, term, "two", "Phase two")
	f.update("two", "complete")
	waitPlanPTYRegion(t, term, 92, 120, "2/3", "66%")
	// A definition-only revision invalidates real evidence; no synthetic
	// percentage or snapshot injection is involved.
	s := f.durable()
	definition := s.Definition()
	definition.Steps[0].Description = "Revised phase one"
	definition.Steps[0].Verify += " && printf revised"
	goal := "Revised sidebar fixture"
	f.turn("update_plan", map[string]any{"action": "revise", "revision": agent.PlanRevisionRequest{PlanID: s.PlanID, BaseRevision: s.Revision, Reason: "Strengthen first capability verification", Goal: &goal, Steps: definition.Steps}})
	term.send(strings.Repeat("\x1b[<64;110;15M", 20))
	waitPlanPTYRegion(t, term, 92, 120, goal, "0/3", "0%", "Revised phase one")
	term.assertAbsent("choose how to proceed")
	f.update("one", "running")
	waitSidebarPTYRunningCard(t, term, "one", "")
	f.update("one", "complete")
	waitPlanPTYRegion(t, term, 92, 120, "1/3", "33%")
	// Revision invalidates the changed phase and downstream evidence.
	f.update("two", "running")
	waitSidebarPTYRunningCard(t, term, "two", "")
	f.update("two", "complete")
	waitPlanPTYRegion(t, term, 92, 120, "2/3", "66%")
	f.update("three", "running")
	waitSidebarPTYRunningCard(t, term, "three", "")
	f.update("three", "complete")
	waitPlanPTYRegion(t, term, 92, 120, "3/3", "100%", "Awaiting completion record")
	if s := f.durable(); s.Document.Results != "" {
		t.Fatal("Results recorded before announce_completion")
	}
	f.turn("announce_completion", map[string]any{"outcome_observed": "All three isolated marker capabilities verified", "unverified": ""})
	waitPlanPTYRegion(t, term, 92, 120, "3/3", "100%", "COMPLETED", "Results recorded")
	f.turn("", nil)
	term.wait("Enter send")
	term.close()
	s = f.durable()
	if s.Document.Results == "" {
		t.Fatal("announce_completion did not persist Results")
	}
	for _, p := range s.Steps {
		if p.Status != "complete" || p.Evidence == "" {
			t.Fatalf("missing verified evidence: %+v", p)
		}
	}
}

// Restore a late active card, then drive actual update_plan tools across a
// provider barrier. Unlike the inspection helper, these assertions never wheel
// toward a target: positioning must be performed by the product itself.
func TestCodePlanSidebarPTYActiveFollow(t *testing.T) {
	f := newSidebarPTYFixture(t)
	store, err := persistentsession.NewFileStore(f.storePath)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	stored, err := store.Get(ctx, &adksession.GetRequest{AppName: "astonish_code", UserID: f.user, SessionID: f.sessionID})
	if err != nil {
		t.Fatal(err)
	}
	s := agent.PlanSnapshot{PlanID: "active-follow", Revision: 1, Sequence: 1, Goal: "Active follow fixture",
		Document: agent.PlanDocumentInfo{PlanID: "active-follow", Revision: 1, Sequence: 1, Lifecycle: agent.PlanLifecycleExecuting, CodeExecutionMode: agent.CodePlanRunToCompletion}}
	for i := 1; i <= 15; i++ {
		status := "pending"
		if i < 10 {
			status = "complete"
		} else if i == 10 {
			status = "running"
		}
		s.Steps = append(s.Steps, agent.PlanStepInfo{Name: fmt.Sprintf("phase-%02d", i), Description: fmt.Sprintf("Follow phase %02d", i), Status: status, ParallelGroup: "wave", Verify: "printf verified", VerifyKind: "behavior", Evidence: "restored fixture evidence"})
	}
	raw, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendEvent(ctx, stored.Session, &adksession.Event{ID: "active-follow", Author: "system", Timestamp: time.Now(), Actions: adksession.EventActions{StateDelta: map[string]any{agent.PlanSnapshotStateKey: value}}}); err != nil {
		t.Fatal(err)
	}
	term := f.start()
	defer term.close()
	waitPlanPTYRegion(t, term, 92, 120, "● Follow phase 10", "running · recorded, not live", "Progress: unavailable")
	term.send("Continue the restored plan.\r")
	receive := func() chan sidebarPTYModelTurn {
		t.Helper()
		select {
		case reply := <-f.turns:
			return reply
		case <-time.After(30 * time.Second):
			t.Fatal("missing provider barrier")
			return nil
		}
	}
	reply := receive()
	tool := func(step, status string) {
		t.Helper()
		select {
		case reply <- sidebarPTYModelTurn{name: "update_plan", args: map[string]any{"step": step, "status": status}}:
		case <-time.After(30 * time.Second):
			t.Fatal("provider did not accept tool")
		}
		reply = receive() // real result has returned to the agent, not just rendered text
		got := f.durable()
		for _, phase := range got.Steps {
			if phase.Name == step && phase.Status != status {
				t.Fatalf("tool did not transition %s to %s: %+v", step, status, phase)
			}
		}
	}
	sidebarPTYScrollOffCard(t, term)
	history := func(frame string) string {
		var rows []string
		for _, row := range strings.Split(planPTYColumns(frame, 0, 91), "\n") {
			if strings.Contains(row, "HISTORY MARKER") {
				rows = append(rows, row)
			}
		}
		return strings.Join(rows, "\n")
	}
	before := history(term.frame())
	assertHistory := func() {
		t.Helper()
		if got := history(term.frame()); got != before {
			t.Fatalf("phase follow moved transcript:\nbefore %s\nafter %s", before, got)
		}
	}
	for _, phase := range []int{10, 11} {
		term.send(strings.Repeat("\x1b[<64;110;15M", 100))
		waitPlanPTYRegion(t, term, 92, 120, "Follow phase 01")
		tool(fmt.Sprintf("phase-%02d", phase), "running") // repeated same target
		// Allow multiple actual paints while insisting manual browsing is stable.
		deadline := time.After(250 * time.Millisecond)
		tick := time.NewTicker(20 * time.Millisecond)
	checkBrowse:
		for {
			select {
			case <-deadline:
				break checkBrowse
			case <-tick.C:
				if !strings.Contains(planPTYColumns(term.frame(), 92, 120), "Follow phase 01") {
					t.Fatal("same-phase event overrode browsing")
				}
				assertHistory()
			}
		}
		tick.Stop()
		tool(fmt.Sprintf("phase-%02d", phase), "complete")
		tool(fmt.Sprintf("phase-%02d", phase+1), "running")
		waitPlanPTYRegion(t, term, 92, 120, fmt.Sprintf("● Follow phase %02d", phase+1), "running", "Progress: unavailable")
		assertHistory()
	}
	// Resizing with the same target must not undo manual browsing. A target
	// transition while compact is instead revealed on the next wide paint.
	term.send(strings.Repeat("\x1b[<64;110;15M", 100))
	waitPlanPTYRegion(t, term, 92, 120, "Follow phase 01")
	term.resize(80, 24)
	term.wait("Current: phase-12", "73%")
	term.resize(120, 30)
	waitPlanPTYRegion(t, term, 92, 120, "Follow phase 01")
	// Resizing may change the number of visible rows, but not the top anchor.
	waitPlanPTYRegion(t, term, 0, 91, "HISTORY MARKER 00")
	term.resize(80, 24)
	term.wait("Current: phase-12")
	tool("phase-12", "complete")
	tool("phase-13", "running")
	term.wait("Current: phase-13", "80%")
	term.resize(120, 30)
	waitPlanPTYRegion(t, term, 92, 120, "● Follow phase 13", "running", "Progress: unavailable")
	waitPlanPTYRegion(t, term, 0, 91, "HISTORY MARKER 00")
	term.resize(120, 65)
	frame := waitPlanPTYRegion(t, term, 92, 120, "PLAN PROGRESS", "PHASES", "80%", "● Follow phase 13")
	if panel := planPTYColumns(frame, 92, 120); strings.Contains(panel, "CURRENT") || strings.Contains(panel, "Current:") {
		t.Fatal("wide running sidebar repeated current-phase names")
	}
	term.close()
	// A different persisted plan/session starts from its own late target, not
	// the prior plan's browsing state. Only isolated fixture sessions are used.
	replacement, err := store.Create(ctx, &adksession.CreateRequest{AppName: "astonish_code", UserID: f.user})
	if err != nil {
		t.Fatal(err)
	}
	s.PlanID = "replacement-follow"
	s.Document.PlanID = s.PlanID
	s.Steps[9].Status = "complete"
	s.Steps[13].Status = "running"
	raw, err = json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &value); err != nil {
		t.Fatal(err)
	}
	if err = store.AppendEvent(ctx, replacement.Session, &adksession.Event{ID: "replacement-follow", Author: "system", Timestamp: time.Now(), Actions: adksession.EventActions{StateDelta: map[string]any{agent.PlanSnapshotStateKey: value}}}); err != nil {
		t.Fatal(err)
	}
	f.sessionID = replacement.Session.ID()
	restored := f.start()
	defer restored.close()
	waitPlanPTYRegion(t, restored, 92, 120, "● Follow phase 14", "running · recorded, not live", "Progress: unavailable")
}

func TestCodePlanSidebarPTYResizeRestore(t *testing.T) {
	f := newSidebarPTYFixture(t)
	term := f.start()
	f.announce(term, false)
	term.send("\x1b[B")
	term.wait("▸ 2    Approve and pause for review after each phase")
	term.send("\r")
	f.update("one", "running")
	waitSidebarPTYRunningCard(t, term, "one", "")
	f.update("one", "complete")
	term.wait("Paused for review", "Enter send")
	waitPlanPTYRegion(t, term, 92, 120, "Paused for phase review", "1/3", "33%")
	before := f.calls.Load()
	select {
	case <-f.turns:
		t.Fatal("model continued without explicit review")
	case <-time.After(300 * time.Millisecond):
	}
	if f.calls.Load() != before {
		t.Fatal("review was not quiescent")
	}
	term.resize(80, 24)
	term.wait("Plan · 1/3 phases complete · 33%", "Live sidebar fixture")
	term.send("DRAFT-KEEP")
	term.wait("DRAFT-KEEP")
	term.send(strings.Repeat("\x1b[<64;10;8M", 70))
	term.wait("HISTORY MARKER 00", "DRAFT-KEEP")
	beforeFrame := term.frame()
	term.send("\x07")
	term.wait("Plan progress · esc back", "Phase one")
	term.send("\x1b[6~")
	term.wait("Phase three", "pending · not started")
	term.assertAbsent("HISTORY MARKER 00")
	term.send("\x1b")
	term.wait("HISTORY MARKER 00", "DRAFT-KEEP", "Plan · 1/3 phases complete · 33%")
	// Compare the actual history rows, not animated chrome or token counters.
	historyRows := func(frame string) string {
		var rows []string
		for _, row := range strings.Split(frame, "\n") {
			if strings.Contains(row, "HISTORY MARKER") {
				rows = append(rows, row)
			}
		}
		return strings.Join(rows, "\n")
	}
	if historyRows(term.frame()) != historyRows(beforeFrame) {
		term.mu.Lock()
		raw := term.raw.String()
		term.mu.Unlock()
		t.Logf("terminal protocol suffix: %q", raw[max(0, len(raw)-12000):])
		t.Fatalf("plan list moved the transcript:\nbefore %s\nafter %s", beforeFrame, term.frame())
	}
	term.resize(120, 30)
	waitPlanPTYRegion(t, term, 92, 120, "Paused for phase review", "1/3", "33%")
	term.wait("DRAFT-KEEP")
	term.close()
	term = f.start()
	term.wait("Enter send")
	waitPlanPTYRegion(t, term, 92, 120, "Paused for phase review", "1/3", "33%")
	term.assertAbsent("choose how to proceed")
	select {
	case <-f.turns:
		t.Fatal("restore resumed without review input")
	case <-time.After(300 * time.Millisecond):
	}
	term.send("continue\r")
	f.update("two", "running")
	waitSidebarPTYRunningCard(t, term, "two", "")
	f.update("two", "complete")
	term.wait("Paused for review", "Enter send")
	waitPlanPTYRegion(t, term, 92, 120, "2/3", "66%")
	term.send("continue\r")
	f.update("three", "running")
	waitSidebarPTYRunningCard(t, term, "three", "")
	f.update("three", "complete")
	waitPlanPTYRegion(t, term, 92, 120, "3/3", "100%", "Awaiting completion record")
	f.turn("announce_completion", map[string]any{"outcome_observed": "Reviewed all three marker capabilities", "unverified": ""})
	waitPlanPTYRegion(t, term, 92, 120, "COMPLETED", "Results recorded")
	f.turn("", nil)
	term.wait("SIDEBAR EXECUTION FINISHED", "Enter send")
	term.send("/new\r")
	term.wait("Enter send")
	// Wait for the session switch paint rather than inspecting the old frame.
	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()
	for strings.Contains(term.frame(), "PLAN PROGRESS") {
		select {
		case <-term.changed:
		case <-timer.C:
			t.Fatalf("new session retained old plan:\n%s", term.frame())
		}
	}
	term.assertAbsent("Live sidebar fixture")
	term.assertAbsent("1/3 phases")
}

// Inspect one complete running card at a time; concurrent cards need not fit
// together beneath the pinned summary in a short window.
func waitSidebarPTYRunningCard(t *testing.T, term *handoffPTY, name, title string) {
	t.Helper()
	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()
	tick := time.NewTicker(80 * time.Millisecond)
	defer tick.Stop()
	for attempts := 0; ; attempts++ {
		panel := planPTYColumns(term.frame(), 92, 120)
		lines := strings.Split(panel, "\n")
		for i, line := range lines {
			if !strings.Contains(line, "●") || title != "" && !strings.Contains(line, title) {
				continue
			}
			end := i + 1
			for end < len(lines) && !strings.ContainsAny(lines[end], "●✓○✗") {
				end++
			}
			card := strings.Join(lines[i:end], "\n")
			if strings.Contains(card, name) && strings.Contains(card, "running") && strings.Contains(card, "Progress: unavailable") {
				return
			}
		}
		select {
		case <-timer.C:
			t.Fatalf("running card %s not inspectable:\n%s", name, term.frame())
		case <-tick.C:
			if attempts%20 == 0 {
				term.send(strings.Repeat("\x1b[<64;110;15M", 20))
			} else {
				term.send("\x1b[<65;110;15M")
			}
		}
	}
}

func planPTYColumns(frame string, start, end int) string {
	var lines []string
	for _, line := range strings.Split(frame, "\n") {
		cells := []rune(line)
		lines = append(lines, string(cells[min(start, len(cells)):min(end, len(cells))]))
	}
	return strings.Join(lines, "\n")
}

func waitPlanPTYRegion(t *testing.T, term *handoffPTY, start, end int, wants ...string) string {
	t.Helper()
	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()
	for {
		frame := term.frame()
		region := strings.Join(strings.Fields(strings.ReplaceAll(planPTYColumns(frame, start, end), "│", " ")), " ")
		ok := true
		for _, want := range wants {
			ok = ok && strings.Contains(region, want)
		}
		if ok {
			t.Logf("current columns %d:%d match %q:\n%s", start, end, wants, frame)
			return frame
		}
		select {
		case <-term.changed:
		case <-term.done:
			t.Fatalf("Code exited before columns %d:%d contained %q:\n%s", start, end, wants, term.frame())
		case <-timer.C:
			term.mu.Lock()
			raw := term.raw.String()
			term.mu.Unlock()
			t.Logf("terminal protocol suffix: %q", raw[max(0, len(raw)-6000):])
			t.Fatalf("columns %d:%d missing %q:\n%s", start, end, wants, term.frame())
		}
	}
}
