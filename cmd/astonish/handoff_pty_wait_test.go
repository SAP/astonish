package astonish

import (
	"strings"
	"testing"
	"time"
)

func TestHandoffPTYSettledFrame(t *testing.T) {
	p := &handoffPTY{screen: newRevisionScreen(30, 4)}
	p.screen.p.Parse([]byte("Message Astonish Enter send"))
	past := time.Now().Add(-time.Second)
	p.lastPaint, p.lastInput = past, past
	if frame, settled := p.settledFrame(); !settled || !strings.Contains(frame, "Enter send") {
		t.Fatalf("settled current frame: %t %q", settled, frame)
	}
	p.lastInput = time.Now()
	if _, settled := p.settledFrame(); settled {
		t.Fatal("pre-input composer must not satisfy the next transition")
	}
	p.lastInput, p.lastPaint = past, time.Now()
	p.screen.p.Parse([]byte("\x1b[2J\x1b[HSessions enter open"))
	if _, settled := p.settledFrame(); settled {
		t.Fatal("partial transition must not satisfy the next action")
	}
	p.lastPaint = past
	if frame, settled := p.settledFrame(); !settled || strings.Contains(frame, "Enter send") || !strings.Contains(frame, "Sessions") {
		t.Fatalf("must inspect current cells, not earlier matching output: %t %q", settled, frame)
	}
}
