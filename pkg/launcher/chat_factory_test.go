package launcher

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/credentials"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
)

func TestLogChatFactoryInitialization(t *testing.T) {
	tests := []struct {
		name     string
		result   *ChatFactoryResult
		err      error
		contains []string
		excludes []string
	}{
		{
			name:   "success",
			result: &ChatFactoryResult{ProviderName: "vertex", ModelName: "gemini"},
			contains: []string{
				"msg=\"chat agent initialized\"", "component=chat-factory", "elapsed=", "platform=true", "code_mode=false", "daemon=true", "provider=vertex", "model=gemini",
			},
		},
		{
			name: "failure",
			err:  errors.New("provider unavailable"),
			contains: []string{
				"msg=\"chat agent initialization failed\"", "component=chat-factory", "elapsed=", "error=\"provider unavailable\"",
			},
			excludes: []string{"provider=", "model="},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			old := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
			defer slog.SetDefault(old)

			logChatFactoryInitialization(time.Now(), &ChatFactoryConfig{PlatformMode: true, IsDaemon: true}, tt.result, tt.err)
			got := buf.String()
			for _, want := range tt.contains {
				if !strings.Contains(got, want) {
					t.Errorf("log %q does not contain %q", got, want)
				}
			}
			for _, unwanted := range tt.excludes {
				if strings.Contains(got, unwanted) {
					t.Errorf("log %q unexpectedly contains %q", got, unwanted)
				}
			}
		})
	}
}

func TestShouldRegisterFactoryPerplexity(t *testing.T) {
	appCfg := &config.AppConfig{}
	appCfg.General.WebSearchTool = "perplexity:perplexity_web_search"
	appCfg.PerplexityWebSearch.Provider = "provider"
	appCfg.PerplexityWebSearch.Model = "sonar"

	if shouldRegisterFactoryPerplexity(&ChatFactoryConfig{AppConfig: appCfg, PlatformMode: true}) {
		t.Fatal("platform shared agent must use request-scoped Perplexity only")
	}
	if shouldRegisterFactoryPerplexity(&ChatFactoryConfig{AppConfig: appCfg}) {
		t.Fatal("Studio personal-mode shared agent must use request-scoped Perplexity only")
	}
	if !shouldRegisterFactoryPerplexity(&ChatFactoryConfig{AppConfig: appCfg, CodeMode: true}) {
		t.Fatal("local Code mode should register its factory-scoped Perplexity tool")
	}
}

func TestSubAgentCredentialStoreWiring(t *testing.T) {
	t.Run("nil store remains a nil interface", func(t *testing.T) {
		var store *credentials.Store
		resolver := optionalCredentialResolver(store)
		if resolver != nil {
			t.Fatalf("optionalCredentialResolver(nil) = %#v, want nil interface", resolver)
		}
	})

	t.Run("available store is wired as resolver", func(t *testing.T) {
		store, err := credentials.Open(t.TempDir())
		if err != nil {
			t.Fatalf("open credential store: %v", err)
		}

		resolver := optionalCredentialResolver(store)
		if resolver == nil {
			t.Fatal("optionalCredentialResolver(store) returned nil")
		}
		if got := resolver.Get("missing"); got != nil {
			t.Fatalf("resolver.Get(missing) = %#v, want nil", got)
		}
		if resolver != store {
			t.Fatal("resolver does not preserve the opened credential store")
		}
	})
}

func TestMainThreadToolAllowlistByMode(t *testing.T) {
	if allow := mainThreadToolAllowlist(false); len(allow) != 0 {
		t.Fatalf("platform/chat main-thread allowlist = %#v, want all domain tools deferred", allow)
	}

	allow := mainThreadToolAllowlist(true)
	for _, name := range []string{"read_file", "write_file", "shell_command", "announce_plan", "codegraph_explore"} {
		if !allow[name] {
			t.Errorf("code-mode main-thread allowlist is missing %q", name)
		}
	}
	for _, name := range []string{"search_tools", "describe_tools", "execute_tool"} {
		if allow[name] {
			t.Errorf("code-mode main-thread allowlist unexpectedly exposes bridge tool %q", name)
		}
	}
}

func TestDirectCodeToolsExposeConcreteToolsWithoutBridge(t *testing.T) {
	newTool := func(name string) tool.Tool {
		t.Helper()
		got, err := functiontool.New(functiontool.Config{Name: name, Description: name}, func(_ tool.Context, _ map[string]any) (map[string]any, error) {
			return nil, nil
		})
		if err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		return got
	}

	readFile := newTool("read_file")
	writeFile := newTool("write_file")
	direct := directCodeTools([]tool.Tool{readFile}, []*agent.ToolGroup{
		{Name: "core", Tools: []tool.Tool{readFile, writeFile}},
	})
	if len(direct) != 2 {
		t.Fatalf("direct code tools count = %d, want 2", len(direct))
	}
	if direct[0].Name() != "read_file" || direct[1].Name() != "write_file" {
		t.Fatalf("direct code tools = [%s %s], want [read_file write_file]", direct[0].Name(), direct[1].Name())
	}
	for _, got := range direct {
		if got.Name() == "execute_tool" {
			t.Fatal("code mode must not expose execute_tool")
		}
	}
}

func TestProviderToolsForCodeModeOmitsBridge(t *testing.T) {
	readFile, err := functiontool.New(functiontool.Config{Name: "read_file", Description: "read"}, func(_ tool.Context, _ map[string]any) (map[string]any, error) {
		return nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	providerTools, err := providerToolsForMode(true, agent.NewLexicalToolIndex(), nil, []*agent.ToolGroup{{Name: "core", Tools: []tool.Tool{readFile}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(providerTools) != 1 || providerTools[0].Name() != "read_file" {
		t.Fatalf("code provider tools = %#v, want only read_file", providerTools)
	}
}

func TestFixedProviderToolsContract(t *testing.T) {
	providerTools, err := providerToolsForMode(false, agent.NewLexicalToolIndex(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"search_tools", "describe_tools", "execute_tool"}
	if len(providerTools) != len(want) {
		t.Fatalf("provider-visible tools = %d, want %d", len(providerTools), len(want))
	}
	for i, name := range want {
		if got := providerTools[i].Name(); got != name {
			t.Fatalf("provider-visible tool %d = %q, want %q", i, got, name)
		}
	}
}

func TestSkillLookupMode(t *testing.T) {
	tests := []struct {
		name         string
		platformMode bool
		codeMode     bool
		want         string
	}{
		{name: "local", want: "local"},
		{name: "code", codeMode: true, want: "code"},
		{name: "platform", platformMode: true, want: "platform"},
		{name: "platform takes precedence", platformMode: true, codeMode: true, want: "platform"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := string(skillLookupMode(tt.platformMode, tt.codeMode)); got != tt.want {
				t.Fatalf("skillLookupMode(%v, %v) = %q, want %q", tt.platformMode, tt.codeMode, got, tt.want)
			}
		})
	}
}
