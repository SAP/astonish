package astonish

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/config"
	persistentsession "github.com/SAP/astonish/pkg/session"
	"github.com/charmbracelet/x/ansi"
	adksession "google.golang.org/adk/session"
)

func startCodeThemePTY(t *testing.T, f *sidebarPTYFixture, noColor bool, args ...string) (*handoffPTY, *sidebarPTYColorScreen) {
	t.Helper()
	argv := append([]string{"-test.run=^TestBotCLIProcess$", "--"}, args...)
	cmd := exec.Command(os.Args[0], argv...)
	cmd.Dir = f.work
	for _, env := range os.Environ() {
		if !strings.HasPrefix(env, "NO_COLOR=") && !strings.HasPrefix(env, "TERM=") && !strings.HasPrefix(env, "COLORTERM=") {
			cmd.Env = append(cmd.Env, env)
		}
	}
	plain := ""
	if noColor {
		plain = "1"
	}
	cmd.Env = append(cmd.Env, "ASTONISH_BOT_CLI_CHILD=1", "TERM=xterm-256color", "COLORTERM=truecolor", "NO_COLOR="+plain, "PATH="+f.bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	term := newHandoffPTYSize(t, cmd, 120, 65)
	term.mu.Lock()
	colored := newSidebarPTYColorScreen(120, 65)
	colored.screen.p.Parse([]byte(term.raw.String()))
	term.screen = colored.screen
	term.mu.Unlock()
	return term, colored
}

func assertCodeThemePTYText(t *testing.T, term *handoffPTY, colored *sidebarPTYColorScreen, phrase, foreground, background string, noColor bool) {
	t.Helper()
	term.wait(phrase)
	if noColor {
		foreground, background = "default", "default"
	}
	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()
	for {
		term.mu.Lock()
		var mismatch string
		matched := false
		for y, row := range colored.screen.cells {
			if x := strings.Index(string(row), phrase); x >= 0 {
				x = ansi.StringWidth(string(row)[:x])
				fg, bg := colored.colorNames[colored.foreground.cells[y][x]], colored.colorNames[colored.background.cells[y][x]]
				matched = fg == foreground && bg == background
				mismatch = fmt.Sprintf("%q at %d,%d fg=%s bg=%s want %s/%s", phrase, x, y, fg, bg, foreground, background)
				if matched {
					break
				}
			}
		}
		term.mu.Unlock()
		if matched {
			return
		}
		select {
		case <-term.changed:
		case <-term.done:
			t.Fatalf("Code exited before themed text: %s", mismatch)
		case <-timer.C:
			t.Fatalf("themed text did not complete: %s", mismatch)
		}
	}
}

func assertCodeThemePTYComposer(t *testing.T, term *handoffPTY, colored *sidebarPTYColorScreen, label, foreground string, plain bool) {
	t.Helper()
	phrase := " " + label + " "
	assertCodeThemePTYText(t, term, colored, phrase, foreground, "#101922", plain)
	if plain {
		foreground = "default"
	}
	term.mu.Lock()
	defer term.mu.Unlock()
	for y, row := range colored.screen.cells {
		x := strings.Index(string(row), phrase)
		if x < 0 || row[0] != '╰' {
			continue
		}
		x = ansi.StringWidth(string(row)[:x])
		// Check every label and bottom-border cell, plus the matching top/side
		// outlines. Old frames in raw stdout are deliberately not evidence.
		for cy := y; cy >= 0; cy-- {
			for cx, cell := range colored.screen.cells[cy] {
				labelCell := cy == y && cx >= x && cx < x+len(phrase)
				borderCell := strings.ContainsRune("╭╮╰╯─│", cell)
				if labelCell || borderCell {
					if got := colored.colorNames[colored.foreground.cells[cy][cx]]; got != foreground {
						t.Fatalf("%s composer cell %d,%d = %s want %s", label, cx, cy, got, foreground)
					}
				}
			}
			if colored.screen.cells[cy][0] == '╭' {
				return
			}
		}
		t.Fatal("composer top border missing")
	}
	t.Fatalf("composer bottom label %q missing", label)
}

func assertCodeThemePTYCanvas(t *testing.T, term *handoffPTY, colored *sidebarPTYColorScreen, noColor bool) {
	t.Helper()
	term.mu.Lock()
	defer term.mu.Unlock()
	for y, row := range colored.background.cells {
		for x, cell := range row {
			key := colored.colorNames[cell]
			if noColor {
				if key != "default" {
					t.Fatalf("NO_COLOR bg at %d,%d: %s", x, y, key)
				}
				continue
			}
			if key != "#101922" && key != "#18232e" && key != "#1a3320" && key != "#3d1f1f" {
				t.Fatalf("non-theme canvas %s at %d,%d (%q)", key, x, y, colored.screen.cells[y][x])
			}
		}
	}
}

// A real Code CLI drill: every frame below is emitted by the actual launch,
// provider stream, tools, permissions and durable restore, never a model.View
// invoked by the test. The palette assertions inspect current terminal cells.
func TestCodeThemePTYAcceptance(t *testing.T) {
	for _, plain := range []bool{false, true} {
		for _, launch := range []string{"bare", "default", "explicit"} {
			t.Run(fmt.Sprintf("welcome/%s/plain=%v", launch, plain), func(t *testing.T) {
				f := newSidebarPTYFixture(t)
				// Keep the loopback model configured but do not submit any turn:
				// an unconfigured backend deliberately shows /model guidance
				// instead of the empty welcome.
				// CodeGraph is injected even into an empty config. Seed its
				// discovery metadata in this private HOME so the intentionally
				// unavailable npx stub cannot replace the empty welcome with an
				// MCP discovery warning. No MCP tool or model is invoked here.
				configDir, err := config.GetConfigDir()
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(configDir, "tools_cache.json"), []byte(`{"version":3,"tools":[{"name":"codegraph_explore","description":"Isolated welcome fixture metadata","source":"codegraph","inputSchema":{"type":"object","properties":{}}}]}`), 0600); err != nil {
					t.Fatal(err)
				}
				args := []string{}
				if launch != "bare" {
					args = append(args, "code")
				}
				if launch == "explicit" {
					args = append(args, "--theme", "blue-gray")
				}
				term, colored := startCodeThemePTY(t, f, plain, args...)
				defer term.close()
				term.wait("Ready when you are.", "Enter send")
				for i, mode := range []struct{ label, rgb string }{{"Normal", "#344552"}, {"Plan", "#e5b766"}, {"Ask", "#38df92"}, {"Normal", "#344552"}} {
					if i > 0 {
						term.send("\x1b[Z")
					} // real Shift+Tab input
					assertCodeThemePTYComposer(t, term, colored, mode.label, mode.rgb, plain)
				}
				assertCodeThemePTYText(t, term, colored, "Astonish · code", "#ff8700", "#101922", plain)
				assertCodeThemePTYText(t, term, colored, "Astonish Code", "#ff8700", "#101922", plain)
				assertCodeThemePTYText(t, term, colored, "Ready when you are.", "#9cafbf", "#101922", plain)
				assertCodeThemePTYCanvas(t, term, colored, plain)
				resizeSidebarPTYColor(term, colored, 72, 40)
				term.wait("Ready when you are.", "Enter send")
				assertCodeThemePTYCanvas(t, term, colored, plain)
				if f.calls.Load() != 0 {
					t.Fatal("no-model welcome contacted provider")
				}
			})
		}
		t.Run(fmt.Sprintf("stream-tools-plan-restore/plain=%v", plain), func(t *testing.T) {
			f := newSidebarPTYFixture(t)
			args := []string{"code", "--theme", "blue-gray", "--dir", f.work, "--resume", f.sessionID}
			term, colored := startCodeThemePTY(t, f, plain, args...)
			defer func() { term.close() }()
			term.wait("Enter send")
			term.send("Show the reference document.\r")
			select {
			case reply := <-f.turns:
				reply <- sidebarPTYModelTurn{text: "# Reference document\n\nReadable cool-white prose.\n\n| Component | State | Evidence |\n| --- | --- | --- |\n| Unicode café | Ready | Long readable evidence preserves every word and wraps without dropping the final sentinel TABLE-END |\n\n```go\nfunc theme() string {\n    return \"blue-gray\"\n}\n```\n\nDOCUMENT-END"}
			case <-time.After(30 * time.Second):
				t.Fatal("no stream request")
			}
			term.wait("Reference document", "TABLE-END", "DOCUMENT-END", "func theme()")
			assertCodeThemePTYText(t, term, colored, "Reference document", "#ff8700", "#101922", plain)
			assertCodeThemePTYText(t, term, colored, "Component", "#ff8700", "#101922", plain)
			assertCodeThemePTYText(t, term, colored, "func theme()", "#ff8700", "#101922", plain)
			assertCodeThemePTYText(t, term, colored, "Readable cool-white prose.", "#e2edf5", "#101922", plain)
			assertCodeThemePTYCanvas(t, term, colored, plain)
			frame := term.frame()
			if !strings.Contains(frame, "┌") || !strings.Contains(frame, "┬") || !strings.Contains(frame, "┼") {
				t.Fatalf("table is not framed: %s", frame)
			}
			resizeSidebarPTYColor(term, colored, 72, 65)
			term.wait("TABLE-END", "DOCUMENT-END")
			assertCodeThemePTYCanvas(t, term, colored, plain)
			resizeSidebarPTYColor(term, colored, 120, 65)
			term.wait("TABLE-END", "DOCUMENT-END")
			term.send("Show a wide inventory table.\r")
			select {
			case reply := <-f.turns:
				reply <- sidebarPTYModelTurn{text: "| Identifier | A | B | C | D | E | F | G | H | I | J | K | L | M |\n| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |\n| STACK-END | a | b | c | d | e | f | g | h | i | j | k | l | m |\n\nINVENTORY-END"}
			case <-time.After(30 * time.Second):
				t.Fatal("no inventory request")
			}
			term.wait("STACK-END", "INVENTORY-END")
			resizeSidebarPTYColor(term, colored, 72, 65)
			term.wait("Identifier:", "STACK-END", "M:", "INVENTORY-END")
			assertCodeThemePTYCanvas(t, term, colored, plain)
			resizeSidebarPTYColor(term, colored, 120, 65)
			term.wait("STACK-END", "INVENTORY-END")
			term.send("Write a reference code file.\r")
			f.turn("write_file", map[string]any{"path": filepath.Join(f.work, "theme.go"), "content": "package reference\nfunc theme() string { return \"blue-gray\" }\n"})
			term.wait("Tool authorization required", "write_file", "Deny")
			assertCodeThemePTYCanvas(t, term, colored, plain)
			term.send("1")
			f.turn("", nil)
			term.wait("SIDEBAR EXECUTION FINISHED", "Enter send", "Edited 1 file")
			// Completed edits are folded by default. Exercise the real fold
			// control before asserting the diff's current terminal cells.
			term.click("Edited 1 file")
			term.wait("package reference", "theme.go")
			// Assert the actual write-file diff, not only a tool success label.
			assertCodeThemePTYCanvas(t, term, colored, plain)
			term.click("Edited 1 file")
			term.wait("▸ Edited 1 file", "Enter send")
			f.announce(term, false)
			waitPlanPTYRegion(t, term, 92, 120, "Awaiting approval", "PHASES", "╰")
			assertCodeThemePTYText(t, term, colored, "PLAN PROGRESS", "#ff8700", "#101922", plain)
			assertSidebarPTYDivider(t, term, colored, plain)
			term.send("\r")
			f.update("one", "running")
			waitSidebarPTYRunningCard(t, term, "one", "Phase one")
			assertCodeThemePTYCanvas(t, term, colored, plain)
			term.send("\x07")
			term.wait("Plan progress · esc back")
			term.send("\x1b")
			waitSidebarPTYRunningCard(t, term, "one", "Phase one")
			f.update("one", "complete")
			waitPlanPTYRegion(t, term, 92, 120, "1/3", "33%")
			f.update("two", "running")
			waitSidebarPTYRunningCard(t, term, "two", "Phase two")
			f.update("two", "complete")
			waitPlanPTYRegion(t, term, 92, 120, "2/3", "66%")
			f.update("three", "running")
			waitSidebarPTYRunningCard(t, term, "three", "Phase three")
			f.update("three", "complete")
			waitPlanPTYRegion(t, term, 92, 120, "3/3", "100%", "Awaiting completion record")
			f.turn("announce_completion", map[string]any{"outcome_observed": "Reference marker capabilities verified", "unverified": ""})
			waitPlanPTYRegion(t, term, 92, 120, "COMPLETED", "Results recorded", "PHASES", "╰")
			f.turn("", nil)
			term.wait("Enter send")
			// The completion badge belongs to the sidebar header canvas;
			// inset phase cards, not the header, use the card surface.
			assertCodeThemePTYText(t, term, colored, "COMPLETED", "#38df92", "#101922", plain)
			assertCodeThemePTYText(t, term, colored, "Phase one", "#e2edf5", "#18232e", plain)
			assertSidebarPTYDivider(t, term, colored, plain)
			term.close()
			term, colored = startCodeThemePTY(t, f, plain, args...)
			waitPlanPTYRegion(t, term, 92, 120, "COMPLETED", "Results recorded", "100%", "PHASES", "╰")
			assertCodeThemePTYText(t, term, colored, "PLAN PROGRESS", "#ff8700", "#101922", plain)
			assertSidebarPTYDivider(t, term, colored, plain)
			term.send("/permissions\r")
			term.wait("Current approvals")
			assertCodeThemePTYCanvas(t, term, colored, plain)
			term.send("\x1b")
			term.wait("Enter send")
			term.send("\x15Inspect an outside helper.\r")
			f.turn("read_file", map[string]any{"path": filepath.Join(f.bin, "npx")})
			term.wait("Folder access required", "❯ 1  Allow once", "4  Deny", "3=any folders", "4=deny")
			assertSidebarPTYDivider(t, term, colored, plain)
			resizeSidebarPTYColor(term, colored, 72, 40)
			term.wait("Folder access required", "4=deny")
			assertCodeThemePTYCanvas(t, term, colored, plain)
			resizeSidebarPTYColor(term, colored, 120, 65)
			waitPlanPTYRegion(t, term, 92, 120, "COMPLETED", "Results recorded", "PHASES", "╰")
			term.wait("Folder access required", "❯ 1  Allow once", "4  Deny", "3=any folders", "4=deny")
			assertSidebarPTYDivider(t, term, colored, plain)
			term.send("1")
			f.turn("", nil)
			term.wait("SIDEBAR EXECUTION FINISHED", "Enter send")
			scrollCodeThemePTYHistory(t, term)
			waitPlanPTYRegion(t, term, 0, 91, "HISTORY MARKER 00")
			waitPlanPTYRegion(t, term, 92, 120, "COMPLETED", "100%")
			assertSidebarPTYDivider(t, term, colored, plain)
		})
	}
}

func scrollCodeThemePTYHistory(t *testing.T, term *handoffPTY) {
	t.Helper()
	deadline := time.NewTimer(30 * time.Second)
	defer deadline.Stop()
	// Large concatenated wheel bursts can be coalesced by the input reader.
	// Keep sending bounded real mouse input until the current transcript,
	// rather than the raw stream, reaches its oldest retained marker.
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		if strings.Contains(planPTYColumns(term.frame(), 0, 91), "HISTORY MARKER 00") {
			return
		}
		term.send("\x1b[<64;10;8M")
		select {
		case <-tick.C:
		case <-term.done:
			t.Fatalf("Code exited while scrolling history:\n%s", term.frame())
		case <-deadline.C:
			t.Fatalf("transcript did not reach oldest history marker:\n%s", term.frame())
		}
	}
}

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
	for _, noColor := range []bool{false, true} {
		t.Run(fmt.Sprintf("no-color=%v", noColor), func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=^TestBotCLIProcess$", "--", "code", "--model", "fixture:pty-model", "--dir", f.work, "--resume", f.sessionID)
			cmd.Dir = f.work
			for _, env := range os.Environ() {
				if !strings.HasPrefix(env, "NO_COLOR=") && !strings.HasPrefix(env, "TERM=") && !strings.HasPrefix(env, "COLORTERM=") {
					cmd.Env = append(cmd.Env, env)
				}
			}
			plain := ""
			if noColor {
				plain = "1"
			}
			cmd.Env = append(cmd.Env, "ASTONISH_BOT_CLI_CHILD=1", "TERM=xterm-256color", "COLORTERM=truecolor", "NO_COLOR="+plain, "PATH="+f.bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			term := newHandoffPTYSize(t, cmd, 120, 65)
			term.mu.Lock()
			colored := newSidebarPTYColorScreen(120, 65)
			colored.screen.p.Parse([]byte(term.raw.String()))
			term.screen = colored.screen
			term.mu.Unlock()
			frame := waitPlanPTYRegion(t, term, 92, 120, "PLAN PROGRESS", "Ctrl+G", "✓ COMPLETED", "Results recorded", "3/3 phases complete", "100%", "━━━", "PHASES", "Group: wave-3", "╰")
			panel := planPTYColumns(frame, 92, 120)
			if !strings.Contains(panel, "╭") || strings.Contains(panel, "[===") || strings.Contains(panel, "CURRENT") || strings.Contains(panel, "Current:") {
				t.Fatalf("invalid simplified layout: %s", panel)
			}
			assertSidebarPTYDivider(t, term, colored, noColor)
			term.mu.Lock()
			coloredOutput := term.raw.String()
			term.mu.Unlock()
			if !noColor {
				for _, bg := range []string{"48;2;16;25;34", "48;2;24;35;46"} {
					if !strings.Contains(coloredOutput, bg) {
						t.Fatalf("real CLI did not emit surface background %s", bg)
					}
				}
			}
			t.Log("Terminal-native reference layout:\n" + panel)
			term.send("\x07")
			term.wait("Plan progress · esc back")
			term.send("\x1b")
			waitPlanPTYRegion(t, term, 92, 120, "PLAN PROGRESS", "100%")
			resizeSidebarPTYColor(term, colored, 80, 24)
			term.wait("Plan · 3/3 phases complete · 100%", "Current: none")
			resizeSidebarPTYColor(term, colored, 120, 30)
			waitPlanPTYRegion(t, term, 92, 120, "PLAN PROGRESS", "100%", "PHASES", "╰")
			assertSidebarPTYDivider(t, term, colored, noColor)
			resizeSidebarPTYColor(term, colored, 120, 65)
			waitPlanPTYRegion(t, term, 92, 120, "Group: wave-3", "PHASES", "╰")
			assertSidebarPTYDivider(t, term, colored, noColor)
			// The same restored plan must retain matching separators while a
			// real out-of-project read is blocked on folder authorization.
			term.send("Inspect the fixture helper outside this project.\r")
			f.turn("read_file", map[string]any{"path": filepath.Join(f.bin, "npx")})
			term.wait("Folder access required", "❯ 1  Allow once", "4  Deny", "3=any folders", "4=deny")
			assertSidebarPTYDivider(t, term, colored, noColor)
			resizeSidebarPTYColor(term, colored, 80, 24)
			term.wait("Folder access required", "Current: none")
			resizeSidebarPTYColor(term, colored, 120, 30)
			waitPlanPTYRegion(t, term, 92, 120, "100%", "Results recorded")
			term.wait("Folder access required")
			assertSidebarPTYDivider(t, term, colored, noColor)
			term.close()
		})
	}
}

func TestCodePlanSidebarPlanApprovalStyledPTY(t *testing.T) {
	for _, noColor := range []bool{false, true} {
		t.Run(fmt.Sprintf("plain=%v", noColor), func(t *testing.T) {
			f := newSidebarPTYFixture(t)
			cmd := exec.Command(os.Args[0], "-test.run=^TestBotCLIProcess$", "--", "code", "--model", "fixture:pty-model", "--dir", f.work, "--resume", f.sessionID)
			cmd.Dir = f.work
			plain := ""
			if noColor {
				plain = "1"
			}
			cmd.Env = append(os.Environ(), "ASTONISH_BOT_CLI_CHILD=1", "TERM=xterm-256color", "COLORTERM=truecolor", "NO_COLOR="+plain, "PATH="+f.bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			term := newHandoffPTYSize(t, cmd, 120, 65)
			defer term.close()
			term.mu.Lock()
			colored := newSidebarPTYColorScreen(120, 65)
			colored.screen.p.Parse([]byte(term.raw.String()))
			term.screen = colored.screen
			term.mu.Unlock()
			f.announce(term, false)
			waitPlanPTYRegion(t, term, 92, 120, "PHASES", "╰")
			assertSidebarPTYDivider(t, term, colored, noColor)
			resizeSidebarPTYColor(term, colored, 80, 24)
			term.wait("Current: none", "Approve and run autonomously until done")
			resizeSidebarPTYColor(term, colored, 120, 30)
			waitPlanPTYRegion(t, term, 92, 120, "Awaiting approval", "PHASES", "╰")
			assertSidebarPTYDivider(t, term, colored, noColor)
		})
	}
}

// Keep a parallel current-cell foreground plane. Both planes use the same
// cursor, erase, insertion/deletion and scroll emulator; SGR changes only the
// foreground plane's next printed cell, never the text assertions.
type sidebarPTYColorScreen struct {
	screen, foreground, background *revisionScreen
	palette                        map[string]rune
	colorNames                     map[rune]string
}

func newSidebarPTYColorScreen(width, height int) *sidebarPTYColorScreen {
	s := &sidebarPTYColorScreen{screen: newSidebarPTYScreen(width, height), foreground: newSidebarPTYScreen(width, height), background: newSidebarPTYScreen(width, height), palette: map[string]rune{"default": ' '}, colorNames: map[rune]string{' ': "default", 'C': "#101922", 'K': "#18232e"}}
	text, colors, backgrounds := s.screen.p, s.foreground.p, s.background.p
	bg := rune(' ')
	fg := rune(' ')
	palette := s.palette
	colorCell := func(key string) rune {
		if key == "#101922" {
			return 'C'
		}
		if key == "#18232e" {
			return 'K'
		}
		if v, ok := palette[key]; ok {
			return v
		}
		v := rune(0xe000 + len(palette))
		palette[key] = v
		s.colorNames[v] = key
		return v
	}
	p := ansi.NewParser()
	p.SetHandler(ansi.Handler{
		Print: func(r rune) {
			text.Parse([]byte(string(r)))
			for range ansi.StringWidth(string(r)) {
				colors.Parse([]byte(string(fg)))
				backgrounds.Parse([]byte(string(bg)))
			}
		},
		Execute: func(b byte) { text.Parse([]byte{b}); colors.Parse([]byte{b}); backgrounds.Parse([]byte{b}) },
		HandleEsc: func(cmd ansi.Cmd) {
			seq := []byte{0x1b}
			if cmd.Intermediate() != 0 {
				seq = append(seq, cmd.Intermediate())
			}
			seq = append(seq, cmd.Final())
			text.Parse(seq)
			colors.Parse(seq)
			backgrounds.Parse(seq)
		},
		HandleCsi: func(cmd ansi.Cmd, params ansi.Params) {
			var seq strings.Builder
			seq.WriteString("\x1b[")
			if cmd.Prefix() != 0 {
				seq.WriteByte(cmd.Prefix())
			}
			for i := range params {
				v, more, _ := params.Param(i, 0)
				fmt.Fprint(&seq, v)
				if i+1 < len(params) {
					if more {
						seq.WriteByte(':')
					} else {
						seq.WriteByte(';')
					}
				}
			}
			if cmd.Intermediate() != 0 {
				seq.WriteByte(cmd.Intermediate())
			}
			seq.WriteByte(cmd.Final())
			if cmd.Final() != 'm' {
				// Mark pre-existing default cells so BCE changes only cells
				// actually erased/inserted/scrolled by this operation.
				for _, row := range s.background.cells {
					for x, v := range row {
						if v == ' ' {
							row[x] = '\uefff'
						}
					}
				}
				text.Parse([]byte(seq.String()))
				colors.Parse([]byte(seq.String()))
				backgrounds.Parse([]byte(seq.String()))
				for _, row := range s.background.cells {
					for x, v := range row {
						if v == '\uefff' {
							row[x] = ' '
						} else if v == ' ' {
							row[x] = bg
						}
					}
				}
				return
			}
			if len(params) == 0 {
				bg = ' '
				fg = ' '
			}
			for i := 0; i < len(params); i++ {
				n, _, _ := params.Param(i, 0)
				switch {
				case n == 0:
					bg, fg = ' ', ' '
				case n == 49:
					bg = ' '
				case n == 39:
					fg = ' '
				case n == 38 || n == 48:
					mode, _, _ := params.Param(i+1, 0)
					key := ""
					if mode == 2 {
						r, _, _ := params.Param(i+2, 0)
						g, _, _ := params.Param(i+3, 0)
						b, _, _ := params.Param(i+4, 0)
						key = fmt.Sprintf("#%02x%02x%02x", r, g, b)
						i += 4
					} else if mode == 5 {
						index, _, _ := params.Param(i+2, 0)
						key = sidebarPTYIndexedRGB(index)
						i += 2
					}
					if key != "" {
						if n == 38 {
							fg = colorCell(key)
						} else {
							bg = colorCell(key)
						}
					}
				case n >= 30 && n <= 37:
					fg = colorCell(sidebarPTYIndexedRGB(n - 30))
				case n >= 90 && n <= 97:
					fg = colorCell(sidebarPTYIndexedRGB(n - 90 + 8))
				case n >= 40 && n <= 47:
					bg = colorCell(sidebarPTYIndexedRGB(n - 40))
				case n >= 100 && n <= 107:
					bg = colorCell(sidebarPTYIndexedRGB(n - 100 + 8))
				}
			}
		},
	})
	s.screen.p = p
	return s
}

func sidebarPTYIndexedRGB(index int) string {
	basic := []string{"#000000", "#800000", "#008000", "#808000", "#000080", "#800080", "#008080", "#c0c0c0", "#808080", "#ff0000", "#00ff00", "#ffff00", "#0000ff", "#ff00ff", "#00ffff", "#ffffff"}
	if index < 16 {
		return basic[max(0, index)]
	}
	if index >= 232 {
		v := 8 + (index-232)*10
		return fmt.Sprintf("#%02x%02x%02x", v, v, v)
	}
	cube := []int{0, 95, 135, 175, 215, 255}
	n := index - 16
	return fmt.Sprintf("#%02x%02x%02x", cube[n/36], cube[n/6%6], cube[n%6])
}

func TestSidebarPTYColorScreen(t *testing.T) {
	s := newSidebarPTYColorScreen(12, 4)
	s.screen.p.Parse([]byte("\x1b[38;2;90;91;92m─│\x1b[0mX\x1b[2;1H\x1b[38;5;240mtest"))
	if s.foreground.cells[0][0] == ' ' || s.foreground.cells[0][0] != s.foreground.cells[0][1] || s.foreground.cells[0][2] != ' ' {
		t.Fatal("foreground/reset decoding failed")
	}
	if s.foreground.cells[1][0] == s.foreground.cells[0][0] {
		t.Fatal("distinct colors collapsed")
	}
	s.screen.p.Parse([]byte("\x1b[1;1H\x1b[P"))
	if s.screen.cells[0][0] != '│' || s.foreground.cells[0][0] == ' ' || s.foreground.cells[0][1] != ' ' {
		t.Fatal("delete did not move color cells with text")
	}
	s.screen.p.Parse([]byte("\x1b[1;1H\x1b[M"))
	if s.screen.cells[0][0] != 't' || s.foreground.cells[0][0] == ' ' {
		t.Fatal("line deletion lost foreground")
	}
}

func resizeSidebarPTYColor(term *handoffPTY, colored *sidebarPTYColorScreen, width, height int) {
	term.mu.Lock()
	before := term.raw.Len()
	for _, s := range []*revisionScreen{colored.foreground, colored.background} {
		old := s.cells
		s.w, s.h = width, height
		s.clear()
		for y := 0; y < min(len(old), height); y++ {
			copy(s.cells[y], old[y])
		}
		s.x, s.y = min(s.x, width-1), min(s.y, height-1)
		s.top, s.bottom = 0, height-1
	}
	term.mu.Unlock()
	term.resize(width, height)
	// Resize preserves overlapping old cells; generic text can match those
	// cells before SIGWINCH has repainted the expanded canvas. Require the
	// new-width header rule and controls on the new bottom row together.
	waitSidebarPTYChrome(term, before)
}

func waitSidebarPTYChrome(term *handoffPTY, before int) {
	term.t.Helper()
	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()
	for {
		term.mu.Lock()
		s := term.screen
		bottom := string(s.cells[s.h-1])
		ready := term.raw.Len() > before && strings.HasPrefix(string(s.cells[1]), strings.Repeat("─", s.w)) &&
			strings.TrimSpace(bottom) != ""
		term.mu.Unlock()
		if ready {
			return
		}
		select {
		case <-term.changed:
		case <-term.done:
			term.t.Fatalf("Code exited before resized chrome completed:\n%s", term.frame())
		case <-timer.C:
			term.t.Fatalf("resized chrome incomplete:\n%s", term.frame())
		}
	}
}

func assertSidebarPTYDivider(t *testing.T, term *handoffPTY, colored *sidebarPTYColorScreen, noColor bool) {
	t.Helper()
	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()
	for {
		term.mu.Lock()
		err := sidebarPTYDividerError(colored, noColor)
		term.mu.Unlock()
		if err == nil {
			return
		}
		select {
		case <-term.changed:
		case <-term.done:
			t.Fatalf("Code exited before complete sidebar frame: %v\n%s", err, term.frame())
		case <-timer.C:
			t.Fatalf("incomplete sidebar frame: %v\n%s", err, term.frame())
		}
	}
}

// Check text and both color planes atomically. A PTY read may contain only part
// of a repaint, so the caller waits for every original guarantee together.
func sidebarPTYDividerError(colored *sidebarPTYColorScreen, noColor bool) error {
	text, fg := colored.screen, colored.foreground
	if !noColor {
		for y, row := range colored.background.cells {
			for x, bg := range row {
				if bg != 'C' && bg != 'K' {
					return fmt.Errorf("non-theme current-cell background %q at %d,%d glyph=%q", bg, x, y, text.cells[y][x])
				}
			}
		}
	}
	divider := text.w - 29
	var separators []int
	for y, row := range text.cells {
		if strings.HasPrefix(string(row), strings.Repeat("─", text.w)) {
			separators = append(separators, y)
		}
	}
	planApproval := strings.Contains(text.text(), "Approve and run autonomously until done")
	wantSeparators := 2
	if planApproval {
		wantSeparators = 1 // pending plan suppresses the composer/footer separator
	}
	if len(separators) != wantSeparators {
		return fmt.Errorf("missing chrome separators: %v\n%s", separators, text.text())
	}
	want := fg.cells[separators[0]][divider]
	if (want == ' ') != noColor {
		return fmt.Errorf("unexpected default chrome color (plain=%v)", noColor)
	}
	end := text.h - 1 // plan approval retains one full-width controls row
	if !planApproval {
		end = separators[1]
		if fg.cells[end][divider] != want {
			return fmt.Errorf("horizontal separators differ")
		}
	}
	for y := separators[0] + 1; y < end; y++ {
		if text.cells[y][divider] != '│' || fg.cells[y][divider] != want {
			return fmt.Errorf("divider row %d glyph=%q foreground=%U chrome=%U", y, text.cells[y][divider], fg.cells[y][divider], want)
		}
	}
	return nil
}
