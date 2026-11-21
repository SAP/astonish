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
	persistentsession "github.com/SAP/astonish/pkg/session"
	"github.com/charmbracelet/x/ansi"
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
			term.wait("Folder access required", "Deny")
			assertSidebarPTYDivider(t, term, colored, noColor)
			resizeSidebarPTYColor(term, colored, 80, 24)
			term.wait("Folder access required", "Current: none")
			resizeSidebarPTYColor(term, colored, 120, 30)
			waitPlanPTYRegion(t, term, 92, 120, "PLAN PROGRESS", "100%", "╰")
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
	screen, foreground *revisionScreen
}

func newSidebarPTYColorScreen(width, height int) *sidebarPTYColorScreen {
	s := &sidebarPTYColorScreen{newSidebarPTYScreen(width, height), newSidebarPTYScreen(width, height)}
	text, colors := s.screen.p, s.foreground.p
	fg := rune(' ')
	palette := map[string]rune{"default": ' '}
	p := ansi.NewParser()
	p.SetHandler(ansi.Handler{
		Print: func(r rune) {
			text.Parse([]byte(string(r)))
			for range ansi.StringWidth(string(r)) {
				colors.Parse([]byte(string(fg)))
			}
		},
		Execute: func(b byte) { text.Parse([]byte{b}); colors.Parse([]byte{b}) },
		HandleEsc: func(cmd ansi.Cmd) {
			seq := []byte{0x1b}
			if cmd.Intermediate() != 0 {
				seq = append(seq, cmd.Intermediate())
			}
			seq = append(seq, cmd.Final())
			text.Parse(seq)
			colors.Parse(seq)
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
				text.Parse([]byte(seq.String()))
				colors.Parse([]byte(seq.String()))
				return
			}
			if len(params) == 0 {
				fg = ' '
			}
			for i := 0; i < len(params); i++ {
				n, _, _ := params.Param(i, 0)
				key := ""
				switch {
				case n == 0 || n == 39:
					key = "default"
				case n == 38 || n == 48:
					mode, _, _ := params.Param(i+1, 0)
					count := 2
					if mode == 2 {
						count = 4
					}
					if n == 38 {
						key = fmt.Sprint(params[i:min(len(params), i+count+1)])
					}
					i += count
				case n >= 30 && n <= 37 || n >= 90 && n <= 97:
					key = fmt.Sprint(n)
				}
				if key != "" {
					if _, ok := palette[key]; !ok {
						palette[key] = rune(0xe000 + len(palette))
					}
					fg = palette[key]
				}
			}
		},
	})
	s.screen.p = p
	return s
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
	s := colored.foreground
	old := s.cells
	s.w, s.h = width, height
	s.clear()
	for y := 0; y < min(len(old), height); y++ {
		copy(s.cells[y], old[y])
	}
	s.x, s.y = min(s.x, width-1), min(s.y, height-1)
	s.top, s.bottom = 0, height-1
	term.mu.Unlock()
	term.resize(width, height)
}

func assertSidebarPTYDivider(t *testing.T, term *handoffPTY, colored *sidebarPTYColorScreen, noColor bool) {
	t.Helper()
	term.mu.Lock()
	defer term.mu.Unlock()
	text, fg := colored.screen, colored.foreground
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
		t.Fatalf("missing chrome separators: %v\n%s", separators, text.text())
	}
	want := fg.cells[separators[0]][divider]
	if (want == ' ') != noColor {
		t.Fatalf("unexpected default chrome color (plain=%v)", noColor)
	}
	end := text.h - 1 // plan approval retains one full-width controls row
	if !planApproval {
		end = separators[1]
		if fg.cells[end][divider] != want {
			t.Fatal("horizontal separators differ")
		}
	}
	for y := separators[0] + 1; y < end; y++ {
		if text.cells[y][divider] != '│' || fg.cells[y][divider] != want {
			t.Fatalf("divider row %d glyph=%q foreground=%U chrome=%U", y, text.cells[y][divider], fg.cells[y][divider], want)
		}
	}
}
