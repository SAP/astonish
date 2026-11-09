package astonish

import (
	"io"
	"strings"
	"testing"
)

// botPTYTerminal supplies the terminal side of startup queries. A PTY supplies
// terminal line discipline, not a terminal emulator: leaving these unanswered
// can block the real launcher's color detection before its first UI frame.
// Keep partial escape sequences across reads, and answer in wire order (termenv
// expects the color response before the subsequent cursor-position response).
type botPTYTerminal struct {
	pending string
}

func (term *botPTYTerminal) respond(w io.Writer, output []byte) error {
	queries := []struct{ request, response string }{
		{"\x1b]10;?\x1b\\", "\x1b]10;rgb:ffff/ffff/ffff\x1b\\"},
		{"\x1b]11;?\x1b\\", "\x1b]11;rgb:0000/0000/0000\x1b\\"},
		{"\x1b]10;?\a", "\x1b]10;rgb:ffff/ffff/ffff\a"},
		{"\x1b]11;?\a", "\x1b]11;rgb:0000/0000/0000\a"},
		{"\x1b[6n", "\x1b[1;1R"},
		// Lip Gloss / Bubble Tea use DA1 as the end-of-query barrier.
		{"\x1b[c", "\x1b[?1;2c"},
		{"\x1b[0c", "\x1b[?1;2c"},
	}
	term.pending += string(output)
	for len(term.pending) > 0 {
		partial, matched := false, false
		for _, q := range queries {
			if strings.HasPrefix(term.pending, q.request) {
				if _, err := io.WriteString(w, q.response); err != nil {
					return err
				}
				term.pending = term.pending[len(q.request):]
				matched = true
				break
			}
			partial = partial || strings.HasPrefix(q.request, term.pending)
		}
		if matched {
			continue
		}
		if partial {
			break
		}
		term.pending = term.pending[1:]
	}
	return nil
}

func TestBotPTYTerminalQueries(t *testing.T) {
	wire := "rendered\x1b[2J\x1b]11;?\x1b\\\x1b[6ntext\x1b]10;?\a\x1b]11;?\a\x1b]10;?\x1b\\\x1b[c\x1b[0c"
	want := "\x1b]11;rgb:0000/0000/0000\x1b\\\x1b[1;1R\x1b]10;rgb:ffff/ffff/ffff\a\x1b]11;rgb:0000/0000/0000\a\x1b]10;rgb:ffff/ffff/ffff\x1b\\\x1b[?1;2c\x1b[?1;2c"
	for size := 1; size <= len(wire); size++ {
		var term botPTYTerminal
		var got strings.Builder
		for start := 0; start < len(wire); start += size {
			if err := term.respond(&got, []byte(wire[start:min(start+size, len(wire))])); err != nil {
				t.Fatal(err)
			}
		}
		if got.String() != want || term.pending != "" {
			t.Fatalf("chunk size %d: responses %q, pending %q", size, got.String(), term.pending)
		}
	}
}
