package astonish

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// A current-cell screen for the renderer's cursor/erase protocol, not a log.
// ansi.Parser preserves partial CSI and UTF-8 across PTY reads. Styling and
// terminal queries do not affect cells (botPTYTerminal handles query replies).
type revisionScreen struct {
	p           *ansi.Parser
	cells       [][]rune
	x, y, w, h  int
	top, bottom int
	autowrap    bool
}

func newRevisionScreen(w, h int) *revisionScreen {
	s := &revisionScreen{w: w, h: h, bottom: h - 1, autowrap: true}
	s.clear()
	s.p = ansi.NewParser()
	s.p.SetHandler(ansi.Handler{
		Print: func(r rune) {
			width := ansi.StringWidth(string(r))
			if width == 0 {
				return
			}
			if s.x >= s.w {
				if s.autowrap {
					s.x = 0
					s.down()
				} else {
					s.x = s.w - 1
				}
			}
			s.cells[s.y][s.x] = r
			for i := 1; i < width && s.x+i < s.w; i++ {
				s.cells[s.y][s.x+i] = ' '
			}
			s.x += width
		},
		Execute: func(b byte) {
			switch b {
			case '\r':
				s.x = 0
			case '\n':
				s.down()
			case '\b':
				s.x = max(0, s.x-1)
			case '\t':
				s.x = min(s.w-1, (s.x/8+1)*8)
			}
		},
		HandleEsc: func(cmd ansi.Cmd) {
			switch cmd.Final() {
			case 'M': // Reverse index, used for a one-row cursor move by ultraviolet.
				if s.y == s.top {
					s.scrollDown(1)
				} else {
					s.y = max(0, s.y-1)
				}
			case 'D':
				s.down()
			case 'E':
				s.x = 0
				s.down()
			}
		},
		HandleCsi: func(cmd ansi.Cmd, p ansi.Params) {
			param := func(i, def int) int {
				v, _, ok := p.Param(i, def)
				if !ok {
					return def
				}
				return v
			}
			n := max(1, param(0, 1))
			switch cmd.Final() {
			case 'H', 'f':
				s.y = min(s.h-1, max(0, param(0, 1)-1))
				s.x = min(s.w-1, max(0, param(1, 1)-1))
			case 'A':
				s.y = max(0, s.y-n)
			case 'B':
				s.y = min(s.h-1, s.y+n)
			case 'C':
				s.x = min(s.w-1, s.x+n)
			case 'D':
				s.x = max(0, s.x-n)
			case 'G':
				s.x = min(s.w-1, n-1)
			case 'd':
				s.y = min(s.h-1, n-1)
			case 'E':
				s.y = min(s.h-1, s.y+n)
				s.x = 0
			case 'F':
				s.y = max(0, s.y-n)
				s.x = 0
			case 'J':
				switch param(0, 0) {
				case 2, 3:
					s.clear()
				case 0:
					s.erase(s.y, s.x, s.w)
					for y := s.y + 1; y < s.h; y++ {
						s.erase(y, 0, s.w)
					}
				case 1:
					for y := 0; y < s.y; y++ {
						s.erase(y, 0, s.w)
					}
					s.erase(s.y, 0, s.x+1)
				}
			case 'K':
				switch param(0, 0) {
				case 0:
					s.erase(s.y, s.x, s.w)
				case 1:
					s.erase(s.y, 0, s.x+1)
				case 2:
					s.erase(s.y, 0, s.w)
				}
			case 'X':
				s.erase(s.y, s.x, min(s.w, s.x+n))
			case 'r':
				s.top = min(s.h-1, max(0, param(0, 1)-1))
				s.bottom = min(s.h-1, max(s.top, param(1, s.h)-1))
				s.x, s.y = 0, 0
			case 'S':
				s.scrollUp(n)
			case 'T':
				s.scrollDown(n)
			case 'h', 'l':
				if cmd.Prefix() == '?' && param(0, 0) == 7 {
					s.autowrap = cmd.Final() == 'h'
				}
				if cmd.Final() == 'h' && cmd.Prefix() == '?' && param(0, 0) == 1049 {
					s.clear()
					s.x = 0
					s.y = 0
				}
			}
		},
	})
	return s
}
func (s *revisionScreen) erase(y, a, b int) {
	for x := max(0, a); x < min(s.w, b); x++ {
		s.cells[y][x] = ' '
	}
}
func (s *revisionScreen) clear() {
	s.cells = make([][]rune, s.h)
	for y := range s.cells {
		s.cells[y] = []rune(strings.Repeat(" ", s.w))
	}
}
func (s *revisionScreen) down() {
	if s.y == s.bottom {
		s.scrollUp(1)
	} else {
		s.y = min(s.h-1, s.y+1)
	}
}
func (s *revisionScreen) scrollUp(n int) {
	for range min(n, s.bottom-s.top+1) {
		copy(s.cells[s.top:s.bottom], s.cells[s.top+1:s.bottom+1])
		s.cells[s.bottom] = []rune(strings.Repeat(" ", s.w))
	}
}
func (s *revisionScreen) scrollDown(n int) {
	for range min(n, s.bottom-s.top+1) {
		copy(s.cells[s.top+1:s.bottom+1], s.cells[s.top:s.bottom])
		s.cells[s.top] = []rune(strings.Repeat(" ", s.w))
	}
}
func (s *revisionScreen) text() string {
	var lines []string
	for _, row := range s.cells {
		lines = append(lines, string(row))
	}
	return strings.Join(lines, "\n")
}

func TestPlanRevisionScreenScrollAndReverseIndex(t *testing.T) {
	s := newRevisionScreen(30, 6)
	s.p.Parse([]byte("pinned\r\nold\r\nnew\r\nspare\r\n› Approve revision\r\n  Reject revision"))
	s.p.Parse([]byte("\x1b[2;4r\x1b[1S\x1b[1;6r"))
	if string(s.cells[0][:6]) != "pinned" || string(s.cells[1][:3]) != "new" || strings.Contains(s.text(), "old") {
		t.Fatalf("scroll region left stale cells: %s", s.text())
	}
	// Actual renderer selection update: reverse index followed by a cursor
	// relative update must clear the old marker, not modify the next action.
	s.p.Parse([]byte("\x1b[6;1H\x1bM  \r\n› "))
	if strings.Contains(s.text(), "› Approve") || !strings.Contains(s.text(), "› Reject revision") {
		t.Fatal(s.text())
	}
}

func TestPlanRevisionScreen(t *testing.T) {
	s := newRevisionScreen(30, 4)
	for _, part := range []string{"old proposal\r\nchoices", "\x1b[", "2J\x1b[Hnew", "\x1b[2;1H› Re", "ject revision"} {
		s.p.Parse([]byte(part))
	}
	got := s.text()
	if strings.Contains(got, "old proposal") || strings.Contains(got, "choices") || !strings.Contains(got, "› Reject revision") {
		t.Fatal(got)
	}
	s.p.Parse([]byte("\x1b[2;1H\x1b[2Kaccepted"))
	if strings.Contains(s.text(), "Reject") {
		t.Fatal("stale cells survived erase")
	}
}
