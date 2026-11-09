package drill

import (
	"context"
	"errors"
	"testing"

	"github.com/SAP/astonish/pkg/config"
)

type terminalExecutor struct {
	calls    int
	terminal error
}

func (e *terminalExecutor) Execute(context.Context, string, map[string]any) (any, error) {
	e.calls++
	e.terminal = errors.New("authority revoked")
	return nil, e.terminal
}
func (e *terminalExecutor) TerminalError() error { return e.terminal }

type ordinaryWaitExecutor struct{ calls []string }

func (e *ordinaryWaitExecutor) Execute(_ context.Context, name string, _ map[string]any) (any, error) {
	e.calls = append(e.calls, name)
	if name == "browser_wait_for" {
		return nil, errors.New("ordinary wait timeout")
	}
	return map[string]any{"ok": true}, nil
}

func TestAutoWaitTerminalOptional(t *testing.T) {
	node := config.Node{Name: "click", Type: "tool", Args: map[string]any{"tool": "browser_click", "selector": "#button"}}
	terminal := &terminalExecutor{}
	got := NewSuiteRunner(terminal, nil, false).executeStep(context.Background(), node, 5, true, 100, false, nil)
	if got.Status != "error" || terminal.calls != 1 {
		t.Fatalf("terminal wait proceeded: %+v calls=%d", got, terminal.calls)
	}
	ordinary := &ordinaryWaitExecutor{}
	got = NewSuiteRunner(ordinary, nil, false).executeStep(context.Background(), node, 5, true, 100, false, nil)
	if got.Status != "passed" || len(ordinary.calls) != 2 || ordinary.calls[1] != "browser_click" {
		t.Fatalf("ordinary wait semantics changed: %+v calls=%v", got, ordinary.calls)
	}
}

func TestTerminalExecutorStopsContinueRetriesAndSuite(t *testing.T) {
	for _, mode := range []string{"test", "tutorial"} {
		t.Run(mode, func(t *testing.T) {
			e := &terminalExecutor{}
			suite := &LoadedSuite{Name: "suite", Config: &config.AgentConfig{SuiteConfig: &config.DrillSuiteConfig{}}}
			cfg := &config.AgentConfig{Suite: "suite", DrillConfig: &config.DrillConfig{Mode: mode, OnFail: "continue", MaxRetries: 3}, Nodes: []config.Node{
				{Name: "first", Type: "tool", Args: map[string]any{"tool": "shell_command", "command": "one"}},
				{Name: "second", Type: "tool", Args: map[string]any{"tool": "shell_command", "command": "two"}},
			}}
			report, err := NewSuiteRunner(e, nil, false).RunSuite(context.Background(), suite, []LoadedTest{{Name: "one", Config: cfg}, {Name: "two", Config: cfg}})
			if !errors.Is(err, e.terminal) || e.calls != 1 || len(report.Tests) != 1 || len(report.Tests[0].Steps) != 1 {
				t.Fatalf("err=%v calls=%d report=%+v", err, e.calls, report)
			}
		})
	}
}
