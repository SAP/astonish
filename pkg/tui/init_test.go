package tui

import (
	"context"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/tui/backend"
	"github.com/SAP/astonish/pkg/tui/events"
)

// initBackendStub is a staticBackend that also implements backend.InitBackend.
// supports controls the SupportsInit() return so the gating test can exercise
// both the available and unavailable paths.
type initBackendStub struct {
	staticBackend
	supports bool
}

func (b initBackendStub) SupportsInit() bool { return b.supports }

type initDispatchBackend struct {
	staticBackend
	message string
	opts    backend.TurnOptions
}

func (b *initDispatchBackend) SupportsInit() bool { return true }

func (b *initDispatchBackend) RunTurn(_ context.Context, message string, opts backend.TurnOptions) (<-chan events.Event, error) {
	b.message = message
	b.opts = opts
	ch := make(chan events.Event)
	close(ch)
	return ch, nil
}

func TestRunInit_DispatchesUnrestrictedPrompt(t *testing.T) {
	b := &initDispatchBackend{}
	m := newModel(context.Background(), Config{Backend: b, Width: 100, Height: 30})
	m.graphPlanMode = true
	m.askMode = true
	m.planMode = true

	next, cmd := m.runInit(initDeepSystemContext, "Generate project guidance.")
	if cmd == nil {
		t.Fatal("runInit should return a command that waits for backend events")
	}
	if b.message != "Generate project guidance." {
		t.Fatalf("RunTurn message = %q, want visible init marker", b.message)
	}
	if b.opts.SystemContext != initDeepSystemContext {
		t.Fatal("RunTurn did not receive the deep-init system context")
	}
	if b.opts.PlanMode || b.opts.GraphPlanMode || b.opts.AskMode {
		t.Fatalf("init turn must be unrestricted, got options %+v", b.opts)
	}
	got := next.(model)
	if got.eventCh == nil || got.turnCancel == nil {
		t.Fatal("runInit should wire the event channel and turn cancellation")
	}
	got.turnCancel()
}

func TestInitCapabilityGating(t *testing.T) {
	// A plain backend that does not implement backend.InitBackend.
	plain := newModel(context.Background(), Config{Backend: staticBackend{}, Width: 100, Height: 30})
	if plain.initCap() != nil {
		t.Fatal("plain backend must not expose the init capability")
	}

	// Implements the interface but reports no working directory.
	unavailable := newModel(context.Background(), Config{Backend: initBackendStub{supports: false}, Width: 100, Height: 30})
	if unavailable.initCap() != nil {
		t.Fatal("init-capable backend without a working dir must not expose the capability")
	}

	// Implements the interface and reports a working directory.
	available := newModel(context.Background(), Config{Backend: initBackendStub{supports: true}, Width: 100, Height: 30})
	if available.initCap() == nil {
		t.Fatal("init-capable backend with a working dir must expose the capability")
	}
}

// TestFilterSlashCommands_IncludesInit verifies /init and /init-deep are
// capability-gated extras (not part of the always-on palette) and appear when
// their commands are passed as extras.
func TestFilterSlashCommands_IncludesInit(t *testing.T) {
	// Without the extras, /init must not surface from the built-in palette.
	base := filterSlashCommands("init")
	for _, c := range base {
		if c.Name == "init" || c.Name == "init-deep" {
			t.Fatalf("/init must be capability-gated, not built-in: got %q", c.Name)
		}
	}

	// With the extras, both must be offered for the "init" prefix.
	got := filterSlashCommands("init", initSlashCommand, initDeepSlashCommand)
	var haveInit, haveDeep bool
	for _, c := range got {
		switch c.Name {
		case "init":
			haveInit = true
		case "init-deep":
			haveDeep = true
		}
	}
	if !haveInit || !haveDeep {
		t.Fatalf("expected /init and /init-deep in matches, got %+v", got)
	}
}

// TestInitSystemContext_MentionsCodegraph guards that the injected prompts steer
// the agent toward codegraph-first exploration and AGENTS.md output, and that
// the deep variant covers sub-folder generation.
func TestInitSystemContext_MentionsCodegraph(t *testing.T) {
	for name, ctx := range map[string]string{
		"initSystemContext":     initSystemContext,
		"initDeepSystemContext": initDeepSystemContext,
	} {
		if !strings.Contains(ctx, "codegraph") {
			t.Errorf("%s should instruct the agent to use codegraph", name)
		}
		if !strings.Contains(ctx, "AGENTS.md") {
			t.Errorf("%s should mention AGENTS.md", name)
		}
		if !strings.Contains(ctx, "write_file") {
			t.Errorf("%s should tell the agent to write the file(s)", name)
		}
	}
	if !strings.Contains(initDeepSystemContext, "sub-folder") {
		t.Error("initDeepSystemContext should describe per-sub-folder AGENTS.md generation")
	}
}
