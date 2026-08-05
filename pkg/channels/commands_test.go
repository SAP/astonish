package channels

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func TestParseCommandName(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"simple command", "/status", "status"},
		{"command with args", "/status args here", "status"},
		{"not a command", "not a command", ""},
		{"slash space", "/ ", ""},
		{"uppercase lowered", "/UPPER", "upper"},
		{"leading whitespace", "  /hello", "hello"},
		{"trailing whitespace", "/hello  ", "hello"},
		{"both whitespace", "  /hello  ", "hello"},
		{"just slash", "/", ""},
		{"empty string", "", ""},
		{"mixed case", "/HeLLo world", "hello"},
		{"slash with tab arg", "/cmd\targ", "cmd\targ"}, // tab is not space, no split by IndexByte(' ')
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseCommandName(tt.input)
			if got != tt.want {
				t.Errorf("parseCommandName(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestNewCommandRegistry(t *testing.T) {
	r := NewCommandRegistry()
	if r == nil {
		t.Fatal("NewCommandRegistry() returned nil")
	}
	if cmds := r.List(); len(cmds) != 0 {
		t.Errorf("NewCommandRegistry() should start empty, got %d commands", len(cmds))
	}
}

func TestCommandRegistry_Register(t *testing.T) {
	r := NewCommandRegistry()
	cmd := &Command{
		Name:        "test",
		Description: "A test command",
		Handler:     func(ctx context.Context, cc CommandContext) (string, error) { return "ok", nil },
	}
	r.Register(cmd)

	if got := r.Lookup("test"); got == nil {
		t.Fatal("Register did not add the command")
	}
	if got := r.Lookup("test"); got.Name != "test" {
		t.Errorf("Lookup returned wrong command: got %q", got.Name)
	}
}

func TestCommandRegistry_Register_PanicsOnDuplicate(t *testing.T) {
	r := NewCommandRegistry()
	cmd := &Command{Name: "dup", Description: "first", Handler: func(ctx context.Context, cc CommandContext) (string, error) { return "", nil }}
	r.Register(cmd)

	defer func() {
		rec := recover()
		if rec == nil {
			t.Fatal("expected panic on duplicate registration, got none")
		}
		msg := fmt.Sprintf("%v", rec)
		if !strings.Contains(msg, "dup") {
			t.Errorf("panic message should mention command name 'dup', got: %s", msg)
		}
	}()

	cmd2 := &Command{Name: "dup", Description: "second", Handler: func(ctx context.Context, cc CommandContext) (string, error) { return "", nil }}
	r.Register(cmd2)
}

func TestCommandRegistry_Lookup(t *testing.T) {
	r := NewCommandRegistry()
	cmd := &Command{Name: "find", Description: "findable", Handler: func(ctx context.Context, cc CommandContext) (string, error) { return "", nil }}
	r.Register(cmd)

	if got := r.Lookup("find"); got == nil {
		t.Error("Lookup should find registered command")
	}
	if got := r.Lookup("missing"); got != nil {
		t.Errorf("Lookup should return nil for missing command, got %v", got)
	}
}

func TestCommandRegistry_IsCommand(t *testing.T) {
	r := NewCommandRegistry()
	r.Register(&Command{Name: "status", Description: "status", Handler: func(ctx context.Context, cc CommandContext) (string, error) { return "", nil }})

	tests := []struct {
		text string
		want bool
	}{
		{"/status", true},
		{"/status args", true},
		{"/STATUS", true}, // case insensitive
		{"/missing", false},
		{"not a command", false},
		{"", false},
		{"/", false},
	}

	for _, tt := range tests {
		t.Run(tt.text, func(t *testing.T) {
			if got := r.IsCommand(tt.text); got != tt.want {
				t.Errorf("IsCommand(%q) = %v, want %v", tt.text, got, tt.want)
			}
		})
	}
}

func TestCommandRegistry_Execute(t *testing.T) {
	r := NewCommandRegistry()
	r.Register(&Command{
		Name:        "ping",
		Description: "ping command",
		Handler: func(ctx context.Context, cc CommandContext) (string, error) {
			return "pong", nil
		},
	})

	ctx := context.Background()

	t.Run("dispatches to registered handler", func(t *testing.T) {
		resp, err := r.Execute(ctx, "/ping", CommandContext{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resp != "pong" {
			t.Errorf("Execute(/ping) = %q, want %q", resp, "pong")
		}
	})

	t.Run("sets RawText on CommandContext", func(t *testing.T) {
		var capturedRaw string
		r2 := NewCommandRegistry()
		r2.Register(&Command{
			Name:        "raw",
			Description: "captures raw",
			Handler: func(ctx context.Context, cc CommandContext) (string, error) {
				capturedRaw = cc.RawText
				return "", nil
			},
		})
		_, _ = r2.Execute(ctx, "/raw some args", CommandContext{})
		if capturedRaw != "/raw some args" {
			t.Errorf("RawText = %q, want %q", capturedRaw, "/raw some args")
		}
	})

	t.Run("returns message for unknown command", func(t *testing.T) {
		resp, err := r.Execute(ctx, "/unknown", CommandContext{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(resp, "Unknown command") {
			t.Errorf("expected 'Unknown command' in response, got %q", resp)
		}
	})

	t.Run("returns error for non-command text", func(t *testing.T) {
		_, err := r.Execute(ctx, "not a command", CommandContext{})
		if err == nil {
			t.Fatal("expected error for non-command text")
		}
	})
}

func TestCommandRegistry_List(t *testing.T) {
	r := NewCommandRegistry()
	names := []string{"alpha", "beta", "gamma"}
	for _, name := range names {
		n := name
		r.Register(&Command{
			Name:        n,
			Description: n + " desc",
			Handler:     func(ctx context.Context, cc CommandContext) (string, error) { return "", nil },
		})
	}

	cmds := r.List()
	if len(cmds) != len(names) {
		t.Fatalf("List() returned %d commands, want %d", len(cmds), len(names))
	}
	for i, cmd := range cmds {
		if cmd.Name != names[i] {
			t.Errorf("List()[%d].Name = %q, want %q (order not preserved)", i, cmd.Name, names[i])
		}
	}
}

func TestWelcomeMessage(t *testing.T) {
	t.Run("with name", func(t *testing.T) {
		msg := welcomeMessage("Alice")
		if !strings.Contains(msg, "Alice") {
			t.Errorf("welcomeMessage(\"Alice\") should contain 'Alice', got %q", msg)
		}
	})

	t.Run("empty name defaults to there", func(t *testing.T) {
		msg := welcomeMessage("")
		if !strings.Contains(msg, "there") {
			t.Errorf("welcomeMessage(\"\") should contain 'there', got %q", msg)
		}
	})
}

func TestHelpCommand(t *testing.T) {
	r := NewCommandRegistry()
	r.Register(&Command{
		Name:        "foo",
		Description: "does foo things",
		Handler:     func(ctx context.Context, cc CommandContext) (string, error) { return "", nil },
	})
	r.Register(&Command{
		Name:          "bar",
		Description:   "does bar things",
		SessionScoped: true,
		Handler:       func(ctx context.Context, cc CommandContext) (string, error) { return "", nil },
	})
	r.Register(helpCommand(r))

	ctx := context.Background()
	resp, err := r.Execute(ctx, "/help", CommandContext{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify output contains command names and descriptions
	for _, want := range []string{"/foo", "does foo things", "/bar", "does bar things", "/help", "Show available commands"} {
		if !strings.Contains(resp, want) {
			t.Errorf("help output should contain %q, got:\n%s", want, resp)
		}
	}
}

func TestHelpCommandUsesPresenter(t *testing.T) {
	r := NewCommandRegistry()
	r.Register(&Command{
		Name:        "foo",
		Description: "does foo things",
		Handler:     func(ctx context.Context, cc CommandContext) (string, error) { return "", nil },
	})
	r.Register(&Command{
		Name:          "bar",
		Description:   "does bar things",
		SessionScoped: true,
		Handler:       func(ctx context.Context, cc CommandContext) (string, error) { return "", nil },
	})
	r.Register(helpCommand(r))

	resp, err := r.Execute(context.Background(), "/help", CommandContext{Presenter: testPresenter{}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(resp, "/test-foo") {
		t.Fatalf("presented command name missing from help output:\n%s", resp)
	}
	if strings.Contains(resp, "/test-bar") || strings.Contains(resp, "does bar things") {
		t.Fatalf("session-scoped command should be hidden by presenter:\n%s", resp)
	}
}

type testPresenter struct{}

func (testPresenter) FormatCommandName(name string) string { return "/test-" + name }

func (testPresenter) ShouldExposeCommand(cmd *Command) bool { return cmd != nil && !cmd.SessionScoped }
