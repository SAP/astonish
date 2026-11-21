package astonish

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/agent"
	persistentsession "github.com/SAP/astonish/pkg/session"
	adksession "google.golang.org/adk/session"
)

func TestCodePlanSidebarStyledPTY(t *testing.T) {
	f := newSidebarPTYFixture(t)
	store, err := persistentsession.NewFileStore(f.storePath)
	if err != nil {
		t.Fatal(err)
	}
	sess, err := store.Get(context.Background(), &adksession.GetRequest{AppName: "astonish_code", UserID: f.user, SessionID: f.sessionID})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := agent.PlanSnapshot{PlanID: "visual-sidebar", Revision: 1, Sequence: 1, Goal: "Persistent plan progress", Document: agent.PlanDocumentInfo{Lifecycle: agent.PlanLifecycleCompleted, Results: "Verified"}, Steps: []agent.PlanStepInfo{
		{Name: "progress-authority", Description: "Share a trustworthy phase-progress summary", Status: "complete", ParallelGroup: "wave-1"},
		{Name: "persistent-panel", Description: "Keep progress visible beside the conversation", Status: "complete", ParallelGroup: "wave-2"},
		{Name: "live-progress-acceptance", Description: "Verify live execution and restored-session parity", Status: "complete", ParallelGroup: "wave-3"},
	}}
	raw, _ := json.Marshal(snapshot)
	var value map[string]any
	if err = json.Unmarshal(raw, &value); err != nil {
		t.Fatal(err)
	}
	if err = store.AppendEvent(context.Background(), sess.Session, &adksession.Event{ID: "visual-plan", Author: "system", Timestamp: time.Now(), Actions: adksession.EventActions{StateDelta: map[string]any{agent.PlanSnapshotStateKey: value}}}); err != nil {
		t.Fatal(err)
	}
	// Exercise true-color output as well as the NO_COLOR lifecycle fixtures.
	cmd := exec.Command(os.Args[0], "-test.run=^TestBotCLIProcess$", "--", "code", "--model", "fixture:pty-model", "--dir", f.work, "--resume", f.sessionID)
	cmd.Dir = f.work
	for _, env := range os.Environ() {
		if !strings.HasPrefix(env, "NO_COLOR=") && !strings.HasPrefix(env, "TERM=") && !strings.HasPrefix(env, "COLORTERM=") {
			cmd.Env = append(cmd.Env, env)
		}
	}
	cmd.Env = append(cmd.Env, "ASTONISH_BOT_CLI_CHILD=1", "TERM=xterm-256color", "COLORTERM=truecolor", "NO_COLOR=", "PATH="+f.bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	term := newHandoffPTYSize(t, cmd, 120, 65)
	frame := waitPlanPTYRegion(t, term, 92, 120, "PLAN PROGRESS", "Ctrl+G", "✓ COMPLETED", "Results recorded", "3/3 phases complete", "100%", "━━━", "CURRENT", "PHASES", "Group: wave-3", "╰")
	panel := planPTYColumns(frame, 92, 120)
	if !strings.Contains(panel, "╭") || !strings.Contains(panel, "╰") || strings.Contains(panel, "[===") {
		t.Fatalf("missing styled frame: %s", panel)
	}
	term.mu.Lock()
	coloredOutput := term.raw.String()
	term.mu.Unlock()
	for _, bg := range []string{"48;2;16;25;34", "48;2;24;35;46"} {
		if !strings.Contains(coloredOutput, bg) {
			t.Fatalf("real CLI did not emit surface background %s", bg)
		}
	}
	t.Log("Terminal-native reference layout:\n" + panel)
	term.send("\x07")
	term.wait("Plan progress · esc back")
	term.send("\x1b")
	waitPlanPTYRegion(t, term, 92, 120, "PLAN PROGRESS", "100%")
	term.resize(80, 24)
	term.wait("Plan · 3/3 phases complete · 100%")
	term.close()
}
