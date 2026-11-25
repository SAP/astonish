package astonish

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/client"
	"github.com/SAP/astonish/pkg/store"
	"github.com/charmbracelet/x/ansi"
	"github.com/creack/pty"
	"github.com/google/uuid"
)

// This is a real Execute subprocess, not a model/reducer test. The fixture
// admits work separately from publishing durable chronology so optimistic
// echoes and duplicate watch replay can be detected in a forced terminal frame.
func TestBotEmployeeObjectivePTY(t *testing.T) {
	// The API package owns the disposable production worker and configured search
	// fixture. It drives this package's real Execute child on a native PTY; no
	// synthetic SSE transcript is involved.
	cmd := exec.Command("go", "test", "./pkg/api", "-run", "^TestBotEmployeeObjectivePTYFixture$", "-count=1", "-timeout=6m", "-v")
	cmd.Dir = "../.."
	cmd.Env = append(os.Environ(), "ASTONISH_EMPLOYEE_PTY_BINARY="+os.Args[0])
	var evidence strings.Builder
	cmd.Stdout = io.MultiWriter(os.Stdout, &evidence)
	cmd.Stderr = cmd.Stdout
	err := cmd.Run()
	out := evidence.String()
	if err != nil {
		t.Fatalf("shared-worker native PTY: %v", err)
	}
	if !strings.Contains(string(out), "native PTY personal live/reconnect/restore") || !strings.Contains(string(out), "native PTY team live/reconnect/restore") {
		t.Fatal("physical personal/team journey did not execute")
	}
}

// The API-owned fixture must prove format-3 database writes as well as the
// physical personal/team dispatcher journey. Checking its evidence prevents a
// misspelled -run expression ("no tests to run") from passing this acceptance.
func TestBotDatabaseEmployeePTY(t *testing.T) {
	cmd := exec.Command("go", "test", "./pkg/api", "-run", "^TestBotDatabaseEmployeePTYFixture$", "-count=1", "-timeout=4m", "-v")
	cmd.Dir = "../.."
	cmd.Env = append(os.Environ(), "ASTONISH_EMPLOYEE_PTY_BINARY="+os.Args[0])
	var evidence strings.Builder
	cmd.Stdout = io.MultiWriter(os.Stdout, &evidence)
	cmd.Stderr = cmd.Stdout
	if err := cmd.Run(); err != nil {
		t.Fatalf("database shared-worker native PTY: %v", err)
	}
	out := evidence.String()
	for _, required := range []string{
		"--- PASS: TestBotDatabaseEmployeePTYFixture",
		"native PTY personal live/reconnect/restore",
		"native PTY team live/reconnect/restore",
	} {
		if !strings.Contains(out, required) {
			t.Fatalf("database PTY evidence missing %q: %s", required, out)
		}
	}
	if strings.Contains(out, "--- SKIP:") {
		t.Fatal("database PTY acceptance must execute without skips")
	}
}

func TestBotParityPTYAcceptance(t *testing.T) { runBotParityPTYAcceptance(t, false) }

func TestBotWorkQualityPTYAcceptance(t *testing.T) { runBotParityPTYAcceptance(t, true) }

func TestBotAgentWorkPTYAcceptance(t *testing.T) { runBotParityPTYAcceptance(t, false, true) }

// Public mixed presentation frames must use the real remote terminal renderer,
// including replay after leaving a read-only run trace and returning Home.
func TestBotConversationPresentationPTYAcceptance(t *testing.T) {
	runBotParityPTYAcceptance(t, false, false, true)
}

func runBotParityPTYAcceptance(t *testing.T, quality bool, agentMode ...bool) {
	agentWork := len(agentMode) > 0 && agentMode[0]
	mixed := len(agentMode) > 1 && agentMode[1]
	for _, status := range []store.BotStatus{store.BotDraft, store.BotActive, store.BotPaused} {
		t.Run(string(status), func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			t.Setenv("XDG_CONFIG_HOME", home)
			t.Setenv("SUDO_USER", "")
			if err := os.MkdirAll(filepath.Join(home, "astonish"), 0700); err != nil {
				t.Fatal(err)
			}
			id, sid, rid, trace := uuid.New(), uuid.New(), uuid.New(), uuid.New()
			bot := store.Bot{ID: id, Name: "Parity", Scope: store.BotScopePersonal, Status: status, Version: 7, Revision: 9, HomeSessionID: sid}
			run := store.BotRun{ID: rid, BotID: id, SessionID: trace, Status: store.RunQueued}
			if status == store.BotPaused {
				// The picker can expose an earlier execution, distinct from this home conversation.
				run.ID = uuid.New()
			}
			base := "/api/studio/bots/" + id.String()
			var posts, closed, watches atomic.Int32
			var panelState, reject, headerState atomic.Int32
			var reviewMode, proposalPosts, approvalPosts atomic.Int32
			var accessDenied atomic.Bool
			responsibility := "Reviewed responsibility"
			proposal := store.BotControlProposal{ID: uuid.New(), BotID: id, Version: 7, ExpectedRevision: 9, Digest: "saved-proposal-digest", Status: store.BotControlProposed, ExpiresAt: time.Now().Add(time.Hour), Payload: store.BotControlPayload{Operation: store.BotControlResponsibility, Reason: "Owner requested change", Responsibility: &responsibility}}
			approval := store.BotApproval{ID: uuid.New(), BotID: id, RunID: rid, Version: 7, ActionDigest: "saved-action-digest", Status: store.ApprovalPending, ExpiresAt: time.Now().Add(time.Hour)}
			admitted := make(chan struct{}, 1)
			release := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer parity-token" {
					t.Errorf("missing auth: %s", r.URL)
					http.Error(w, "unauthorized", 401)
					return
				}
				if strings.HasPrefix(r.URL.Path, "/api/studio/bots") && r.URL.Query().Get("scope") != "personal" {
					t.Errorf("wrong scope: %s", r.URL)
					http.Error(w, "forbidden", 403)
					return
				}
				write := func(v any) { w.Header().Set("Content-Type", "application/json"); _ = json.NewEncoder(w).Encode(v) }
				if r.Method == http.MethodGet {
					switch r.URL.Path {
					case "/api/studio/sessions":
						write(map[string]any{"sessions": []any{}})
						return
					case "/api/settings/providers/effective":
						write(map[string]any{})
						return
					case "/api/studio/bots":
						write(map[string]any{"bots": []store.Bot{bot}})
						return
					case base:
						write(bot)
						return
					case base + "/snapshot":
						if accessDenied.Load() {
							http.Error(w, "forbidden", http.StatusForbidden)
							return
						}
						if panelState.Load() == 2 {
							// Retryable outage, not revoked access: 5d892dc5 intentionally
							// clears protected state on 403 (exercised separately below).
							http.Error(w, "snapshot temporarily unavailable", http.StatusServiceUnavailable)
							return
						}
						snap := store.BotSnapshot{Bot: bot}
						if quality {
							// Transport compatibility fixture only: unlike the browser
							// acceptance this does not certify WorkStore publication.
							latest := run
							latest.Report.Quality = &store.BotQualityReport{SchemaVersion: store.BotWorkSchemaVersion, Outcome: store.BotWorkAccepted, Criteria: []store.BotCriterionAssessment{{CriterionID: "receipt", Verdict: store.BotCriterionSatisfied, Method: store.BotAssessmentModel, EvidenceRefs: []string{"candidate:receipt"}}}}
							if panelState.Load() == 1 {
								latest.Report.Quality.Limitations = []string{"Receipt freshness unavailable"}
							}
							if panelState.Load() == 3 {
								latest.Report.Quality = nil
							}
							if panelState.Load() == 4 {
								latest.Report.Quality.SchemaVersion = 999
							}
							snap.LatestOutcome = &latest
						}
						if panelState.Load() == 0 {
							snap.UnreadCount = 4
							snap.PendingApprovals = []store.BotApproval{{ID: uuid.New()}}
							if !quality {
								snap.LatestOutcome = &run
							}
						}
						if agentWork {
							latest := run
							latest.Work = &store.BotWorkProjection{SchemaVersion: 1, Protocol: store.BotWorkProtocolAgentLedV1, BotID: id, RunID: run.ID, WorkRevision: 2, Phase: store.BotWorkFinished, Disposition: store.AgentWorkAnswered}
							if panelState.Load() == 1 {
								latest.Work.Disposition = store.AgentWorkPartial
								latest.Work.Remaining = []store.BotWorkObligation{{Summary: "Missing source", Reason: "Source unavailable", Continuation: store.BotWorkContinueOwnerRequest}}
							}
							if panelState.Load() == 3 {
								latest.Work = nil
							}
							if panelState.Load() == 4 {
								latest.Work.SchemaVersion = 99
							}
							snap.LatestOutcome = &latest
						}
						if headerState.Load() == 1 {
							snap.Bot.Status = store.BotPaused
							snap.PendingApprovals = nil
						} else if headerState.Load() == 2 {
							snap.Bot.Status = store.BotDeleted
							snap.PendingApprovals = []store.BotApproval{{ID: uuid.New()}, {ID: uuid.New()}}
						}
						if reviewMode.Load() == 1 {
							snap.PendingApprovals = nil
							if approvalPosts.Load() == 0 {
								snap.PendingApprovals = []store.BotApproval{approval}
							}
							if proposalPosts.Load() == 0 {
								snap.PendingProposals = []store.BotControlProposal{proposal}
							}
						}
						write(snap)
						return
					case base + "/goals":
						goals := []store.BotGoal{}
						if panelState.Load() == 0 {
							goals = append(goals, store.BotGoal{Title: "Persisted parity goal"})
						}
						write(map[string]any{"goals": goals})
						return
					case base + "/runs":
						write(map[string]any{"runs": []store.BotRun{run}})
						return
					case base + "/sessions/" + sid.String() + "/events", base + "/sessions/" + trace.String() + "/events":
						watches.Add(1)
						defer closed.Add(1)
						if r.URL.Query().Get("after") != "" {
							t.Errorf("cursor leaked: %s", r.URL)
						}
						w.Header().Set("Content-Type", "text/event-stream")
						feed := sid
						text := "Durable parity home"
						isTrace := strings.Contains(r.URL.Path, trace.String())
						if isTrace {
							feed = trace
							text = "Read-only parity trace"
						}
						wire := func(seq int, kind string, payload any) {
							writeBotCanonicalPTYFrame(t, w, feed, rid, seq, fmt.Sprintf("%s:%d", feed, seq), kind, payload)
						}
						emit := func(seq int, author, text string) {
							kind := "text"
							if author == "user" {
								kind = "user_message"
							}
							wire(seq, kind, map[string]any{"text": text})
						}
						emit(1, "model", text)
						fmt.Fprintf(w, "id: %s:1\nevent: replay_complete\ndata: {}\n\n", feed)
						w.(http.Flusher).Flush()
						select {
						case <-release:
							emit(2, "user", "parity-message")
							emit(2, "user", "parity-message")
							wire(3, "session", map[string]any{"sessionId": feed.String()})
							// One durable row projects to multiple Chat frames. Replay repeats
							// identities; only the last part acknowledges the row for reconnect.
							for replay := 0; replay < 2; replay++ {
								for part, text := range []string{"Durable parity ", "answer"} {
									cursor := fmt.Sprintf("%s:%d", feed, 3+part)
									data, err := json.Marshal(map[string]any{
										"text": text,
										"_projection": map[string]any{
											"event_id":   fmt.Sprintf("%s:4:%d", feed, part),
											"session_id": feed.String(), "run_id": rid.String(), "turn_id": rid.String(),
											"sequence": 4, "cursor": cursor,
										},
									})
									if err != nil {
										t.Error(err)
										return
									}
									fmt.Fprintf(w, "id: %s\nevent: text\ndata: %s\n\n", cursor, data)
									w.(http.Flusher).Flush()
								}
							}
							wire(5, "turn_complete", map[string]any{})
							if mixed {
								wire(7, "tool_call", map[string]any{"id": "mixed-read", "name": "read_file", "args": map[string]any{"path": "/workspace/public-receipt.txt"}})
								wire(8, "tool_result", map[string]any{"id": "mixed-read", "name": "read_file", "result": map[string]any{"content": "Public mixed receipt"}})
								wire(9, "subtask_progress", map[string]any{"event_type": "delegation_start", "batch_id": "mixed-batch", "tasks": []any{map[string]any{"task_id": "research", "name": "Mixed research", "description": "Compare public evidence"}}})
								wire(10, "subtask_progress", map[string]any{"event_type": "task_start", "batch_id": "mixed-batch", "task_id": "research", "task_name": "Mixed research"})
								wire(11, "subtask_progress", map[string]any{"event_type": "task_complete", "batch_id": "mixed-batch", "task_id": "research", "task_name": "Mixed research", "status": "success", "summary": "Public evidence compared"})
								wire(12, "subtask_progress", map[string]any{"event_type": "plan_snapshot", "snapshot": map[string]any{"plan_id": "mixed-plan", "goal": "Mixed verification plan", "revision": 1, "sequence": 1, "document": map[string]any{"lifecycle": "approved"}, "steps": []any{map[string]any{"name": "check", "description": "Verify public receipt", "status": "complete"}}}})
								wire(13, "error", map[string]any{"message": "Public mixed recoverable error"})
								emit(14, "model", "Mixed presentation complete")
								wire(15, "turn_complete", map[string]any{})
							}
							if isTrace {
								artifactSequence := 6
								if mixed {
									artifactSequence = 16
								}
								wire(artifactSequence, "artifact", map[string]any{"path": "/workspace/parity-report.md", "fileName": "parity-report.md", "fileType": "text/markdown", "isReport": true, "reportTitle": "Runtime parity findings"})
							}
						case <-r.Context().Done():
							return
						}
						<-r.Context().Done()
						return
					}
				}
				if r.Method == http.MethodPost && r.URL.Path == base+"/proposals/"+proposal.ID.String()+"/decision" {
					var body map[string]any
					_ = json.NewDecoder(r.Body).Decode(&body)
					if len(body) != 2 || body["digest"] != proposal.Digest || body["decision"] != "confirm" {
						t.Errorf("unbound proposal: %#v", body)
					}
					proposalPosts.Add(1)
					p := proposal
					p.Status = store.BotControlApplied
					write(p)
					return
				}
				if r.Method == http.MethodPost && r.URL.Path == base+"/approvals/"+approval.ID.String()+"/approve-and-continue" {
					var body map[string]any
					_ = json.NewDecoder(r.Body).Decode(&body)
					if len(body) != 1 || body["action_digest"] != approval.ActionDigest {
						t.Errorf("unbound approval: %#v", body)
					}
					approvalPosts.Add(1)
					state := store.BotContinuationQueued
					if status == store.BotPaused {
						state = store.BotContinuationBlockedByPause
					}
					write(store.BotApproveContinueResult{Approval: approval, RunID: rid, Continuation: state})
					return
				}
				endpoint := "/messages"
				if r.Method == http.MethodPost && r.URL.Path == base+endpoint {
					posts.Add(1)
					var body map[string]any
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Errorf("decode: %v", err)
					}
					key, _ := body["idempotency_key"].(string)
					if _, err := uuid.Parse(key); err != nil || len(body) != 3 || body["version"] != float64(7) {
						t.Errorf("unreviewed admission: %#v", body)
					}
					want := "parity-message"
					if reject.Load() != 0 {
						want = "retain-this-message"
					}
					if body["instructions"] != want {
						t.Errorf("instructions: %#v", body)
					}
					if reject.Load() != 0 {
						http.Error(w, "review-conflict", int(reject.Load()))
						return
					}
					admittedRun := run
					admittedRun.ID = rid
					if status == store.BotPaused {
						admittedRun.SessionID = sid
					}
					write(store.BotMessageReceipt{Run: admittedRun, HomeSessionID: sid, UserEventID: "user-event", HistoryCursor: 2})
					admitted <- struct{}{}
					return
				}
				t.Errorf("unexpected endpoint (including lifecycle/cancellation): %s %s", r.Method, r.URL)
				http.Error(w, "unexpected endpoint", 500)
			}))
			defer server.Close()
			if err := client.SaveRemoteConfig(&client.RemoteConfig{URL: server.URL}); err != nil {
				t.Fatal(err)
			}
			tokens, err := client.NewTokenStore()
			if err != nil {
				t.Fatal(err)
			}
			if err = tokens.Save(&client.Tokens{AccessToken: "parity-token"}); err != nil {
				t.Fatal(err)
			}
			args := []string{"chat"}
			// Active proves ordinary remote /bots entry; paused proves --bot startup.
			if status != store.BotActive {
				args = append(args, "--bot", "Parity")
			}
			cmd := exec.Command(os.Args[0], append([]string{"-test.run=^TestBotCLIProcess$", "--"}, args...)...)
			cmd.Env = append(os.Environ(), "ASTONISH_BOT_CLI_CHILD=1", "TERM=xterm-256color", "NO_COLOR=1")
			pt, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 50, Cols: 140})
			if err != nil {
				t.Fatal(err)
			}
			defer pt.Close()
			defer func() { _ = cmd.Process.Kill() }()
			var mu sync.Mutex
			var output strings.Builder
			go func() {
				var terminal botPTYTerminal
				buf := make([]byte, 8192)
				for {
					n, e := pt.Read(buf)
					if n > 0 {
						_ = terminal.respond(pt, buf[:n])
						mu.Lock()
						output.Write(buf[:n])
						mu.Unlock()
					}
					if e != nil {
						return
					}
				}
			}()
			start := 0
			read := func() string { mu.Lock(); defer mu.Unlock(); return ansi.Strip(output.String()[start:]) }
			mark := func() { mu.Lock(); start = output.Len(); mu.Unlock() }
			send := func(s string) {
				t.Helper()
				mark()
				if _, e := io.WriteString(pt, s); e != nil {
					t.Fatal(e)
				}
			}
			wait := func(text string) {
				t.Helper()
				deadline := time.Now().Add(10 * time.Second)
				for time.Now().Before(deadline) {
					if strings.Contains(read(), text) {
						return
					}
					time.Sleep(20 * time.Millisecond)
				}
				mu.Lock()
				raw := output.String()
				mu.Unlock()
				t.Fatalf("PTY missing %q:\n%s\nraw: %q", text, read(), raw)
			}
			settle := func() { time.Sleep(180 * time.Millisecond) }
			escape := func() { send("\x1b"); settle() }
			frame := func(cols uint16) string {
				t.Helper()
				settle()
				mark()
				if e := pty.Setsize(pt, &pty.Winsize{Rows: 50, Cols: cols}); e != nil {
					t.Fatal(e)
				}
				settle()
				return read()
			}
			if status == store.BotActive {
				wait("platform")
				send("/bots\r")
				wait("Parity [active]")
				send("\r")
			}
			wait("Durable parity home")
			if agentWork {
				for _, tc := range []struct {
					state int32
					want  string
				}{{0, "Answer provided"}, {1, "Partial answer"}, {3, "No work result yet"}, {4, "Work details unavailable"}} {
					panelState.Store(tc.state)
					send("/bots status\r")
					wait(tc.want)
					if tc.state == 1 {
						wait("Remaining work")
						wait("no automatic continuation")
					}
					escape()
				}
				panelState.Store(0)
				send("/bots status\r")
				wait("Answer provided")
				panelState.Store(2)
				send("r")
				wait("Press r to retry")
				wait("Last known work details")
				panelState.Store(0)
				send("r")
				// Refresh keeps the same answer text; the terminal may emit only
				// deleted stale/error rows. Assert a complete resized frame.
				refreshed := frame(139)
				if !strings.Contains(refreshed, "Answer provided") || strings.Contains(refreshed, "Last known") || strings.Contains(refreshed, "Press r to retry") {
					t.Fatalf("refresh did not restore current work: %s", refreshed)
				}
				escape()
				t.Log("AGENT_SURFACE pty/work-states PASS")
			}
			if quality {
				for _, tc := range []struct {
					state int32
					want  string
				}{{0, "Requirements checked"}, {1, "Requirements remain unmet or unverified"}, {3, "No work result yet"}, {4, "Assessment unavailable"}} {
					panelState.Store(tc.state)
					send("/bots status\r")
					wait(tc.want)
					if tc.state == 1 {
						wait("Receipt freshness unavailable")
					}
					escape()
				}
				panelState.Store(0)
			}
			send("parity-message\r")
			select {
			case <-admitted:
			case <-time.After(10 * time.Second):
				t.Fatalf("composer did not admit work: %s", read())
			}
			wait("Message accepted.")
			before := frame(141)
			if strings.Count(before, "Message accepted.") != 1 {
				t.Fatalf("want exactly one admission notice: %s", before)
			}
			if strings.Contains(before, "parity-message") {
				t.Fatalf("admission invented local chronology: %s", before)
			}
			close(release)
			wait("Durable parity answer")
			if mixed {
				wait("Mixed presentation complete")
			}
			after := frame(142)
			assertMixed := func(rendered string) {
				t.Helper()
				if !mixed {
					return
				}
				for _, marker := range []string{"Read file /workspace/public-receipt.txt", "Mixed research", "Mixed verification plan", "I can’t confirm the latest outcome right now.", "Mixed presentation complete"} {
					if !strings.Contains(rendered, marker) {
						t.Errorf("mixed PTY presentation missing %q: %s", marker, rendered)
					}
				}
			}
			assertMixed(after)
			if strings.Count(after, "parity-message") != 1 || strings.Count(after, "Durable parity answer") != 1 {
				t.Fatalf("home must show single durable prompt/reply: %s", after)
			}
			for _, command := range []string{"goals", "schedule", "status"} {
				send("/bots " + command + "\r")
				wait("version 7")
				wait("revision 9")
				wait("Parity [" + string(status) + "]")
				wait("Pending decisions: 1")
				wait("Unread: 4")
				wait("Next run: unavailable from server snapshot")
				wait(run.ID.String())
				if command == "goals" {
					wait("Persisted parity goal")
				} else {
					wait("Bot Schedule")
				}
				escape()
			}
			panelState.Store(1)
			send("/bots goals\r")
			wait("No goals.")
			wait("Pending decisions: 0")
			escape()
			send("/bots schedule\r")
			wait("Unread: 0")
			wait("Next run: unavailable")
			escape()
			panelState.Store(2)
			for _, command := range []string{"goals", "schedule"} {
				send("/bots " + command + "\r")
				wait("Work details unavailable. Press r to retry.")
				if strings.Contains(read(), "server error (") || strings.Contains(read(), "snapshot temporarily unavailable") {
					t.Fatal("overview exposed raw transport error")
				}
				// An unavailable review must never execute a stale decision.
				send("y\r")
				settle()
				if posts.Load() != 1 || proposalPosts.Load() != 0 || approvalPosts.Load() != 0 {
					t.Fatal("unavailable overview mutated work")
				}
				escape()
			}
			panelState.Store(0)
			send("/bots runs\r")
			wait("read-only traces")
			wait(trace.String())
			send("\r")
			wait("Read-only parity trace")
			wait("Runtime parity findings")
			runFrame := frame(143)
			if strings.Count(runFrame, "parity-message") != 1 || strings.Count(runFrame, "Durable parity answer") != 1 || !strings.Contains(runFrame, "parity-report.md") {
				t.Fatalf("run must show single durable prompt/result and runtime report artifact: %s", runFrame)
			}
			send("do-not-run\r")
			wait("selected run trace is read-only")
			if posts.Load() != 1 {
				t.Fatalf("read-only trace admitted work: %d", posts.Load())
			}
			// A rejected submission stays in the composer; Ctrl+U removes it explicitly.
			send("\x15/bots home\r")
			wait("Durable parity home")
			homeFrame := frame(144)
			assertMixed(homeFrame)
			if strings.Count(homeFrame, "parity-message") != 1 || strings.Count(homeFrame, "Durable parity answer") != 1 || strings.Contains(homeFrame, "Runtime parity findings") {
				t.Fatalf("restored home chronology mismatch: %s", homeFrame)
			}
			if agentWork {
				t.Log("AGENT_SURFACE pty/restore-readonly PASS")
			}
			for _, code := range []int32{409, 403} {
				reject.Store(code)
				send("retain-this-message\r")
				// Composer transport errors are not typed yet, so even 403 uses safe generic feedback.
				wait("I can’t confirm the latest outcome right now. Please refresh before trying again; your work may still be running.")
				retained := frame(uint16(143 + code%10))
				if strings.Contains(retained, "server error (") || strings.Contains(retained, "review-conflict") {
					t.Fatalf("composer exposed raw diagnostic: %s", retained)
				}
				if !strings.Contains(retained, "retain-this-message") {
					t.Fatalf("rejected composer input lost: %s", retained)
				}
				send("\x15")
				settle()
			}
			if posts.Load() != 3 {
				t.Fatalf("unexpected retries/mutations: %d", posts.Load())
			}
			reviewMode.Store(1)
			send("/bots proposals\r")
			wait("Reviewed responsibility")
			wait("Pending decisions: 2")
			wait("[Cancel]")
			send("\r")
			settle()
			if proposalPosts.Load() != 0 {
				t.Fatal("default Cancel mutated proposal")
			}
			send("/bots proposals\r")
			wait("Reviewed responsibility")
			send("y")
			wait("Confirm approved")
			escape()
			if proposalPosts.Load() != 0 {
				t.Fatal("Escape mutated proposal")
			}
			send("/bots proposals\r")
			wait("Reviewed responsibility")
			send("y")
			wait("Confirm approved")
			send("\r")
			wait("No pending proposals.")
			escape()
			send("/bots approvals\r")
			wait("saved-action-digest")
			send("\r")
			settle()
			if approvalPosts.Load() != 0 {
				t.Fatal("default approval mutated")
			}
			send("y")
			wait("Confirm approved")
			send("\r")
			wait("No approvals.")
			escape()
			if status == store.BotPaused {
				wait("I saved your approval, but I’m still paused.")
			}
			if proposalPosts.Load() != 1 || approvalPosts.Load() != 1 {
				t.Fatalf("decision counts: %d/%d", proposalPosts.Load(), approvalPosts.Load())
			}
			reviewMode.Store(0)
			// Header refresh is automatic and read-only: no command triggers these transitions.
			// Force complete frames: terminal delta rendering may emit only the changed digit.
			waitHeader := func(wants ...string) {
				t.Helper()
				deadline := time.Now().Add(10 * time.Second)
				for cols := uint16(150); time.Now().Before(deadline); cols = 301 - cols {
					full := frame(cols)
					found := true
					for _, want := range wants {
						found = found && strings.Contains(full, want)
					}
					if found {
						return
					}
				}
				t.Fatalf("header missing %v: %s", wants, read())
			}
			headerState.Store(1)
			waitHeader("Decisions 0", "paused")
			headerState.Store(2)
			waitHeader("Decisions 2", "deleted")
			panelState.Store(2)
			waitHeader("deleted · Updates unavailable", "Decisions unavailable", "Durable parity answer")
			if posts.Load() != 3 {
				t.Fatalf("header refresh mutated work: %d", posts.Load())
			}
			// Unlike the 503 outage, revoked access must clear the protected
			// conversation and selection, not retain an actionable stale Bot.
			accessDenied.Store(true)
			waitHeader("Bot access unavailable. Select again after access is restored.")
			cleared := frame(152)
			for _, protected := range []string{"Durable parity answer", "Durable parity home", "Bot: Parity", "Decisions 2"} {
				if strings.Contains(cleared, protected) {
					t.Fatalf("revoked access retained %q: %s", protected, cleared)
				}
			}
			send("/bots approvals\r")
			wait("Select a Bot first with /bots.")
			if posts.Load() != 3 || proposalPosts.Load() != 1 || approvalPosts.Load() != 1 {
				t.Fatal("revoked access mutated work")
			}
			send("/bots detach\r")
			wait("Bot work continues")
			send("/exit\r")
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(10 * time.Second):
				t.Fatal("PTY did not exit")
			}
			deadline := time.Now().Add(2 * time.Second)
			for closed.Load() != watches.Load() && time.Now().Before(deadline) {
				time.Sleep(20 * time.Millisecond)
			}
			if closed.Load() != watches.Load() || watches.Load() != 3 {
				t.Fatalf("viewer streams not closed: watched=%d closed=%d", watches.Load(), closed.Load())
			}
			if posts.Load() != 3 {
				t.Fatalf("detach cancelled/mutated work: %d", posts.Load())
			}
		})
	}
}
