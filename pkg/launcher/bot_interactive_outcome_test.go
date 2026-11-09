package launcher

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/api"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/model"
)

type interactiveOutcomePool struct {
	sandbox.ToolNodePool
	node *fileOutcomeNode
}

func (p *interactiveOutcomePool) GetOrCreate(string) sandbox.ToolNodeClient { return p.node }
func (p *interactiveOutcomePool) SetSessionScope(string, string, string)    {}
func (p *interactiveOutcomePool) GetBackend() sandbox.Backend               { return nil }
func (p *interactiveOutcomePool) Alias(string, string)                      {}

func TestInteractiveChatOutcomeAccountingThroughRunner(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"weather":"clear"}`))
	}))
	defer server.Close()
	// Exercise real WebFetch and HTTP, retaining SSRF validation. Only the test
	// transport routes the public literal to our local server; no production toggle.
	old := http.DefaultTransport
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", server.Listener.Addr().String())
	}}
	http.DefaultTransport = transport
	defer func() { http.DefaultTransport = old; transport.CloseIdleConnections() }()
	for _, tc := range []struct {
		name    string
		strip   bool
		partial bool
	}{
		{name: "web_fetch"}, {name: "read_file"}, {name: "write_file"}, {name: "write_file", strip: true, partial: true}, {name: "shell_command", partial: true},
	} {
		t.Run(tc.name+map[bool]string{true: "_no_receipt"}[tc.strip], func(t *testing.T) {
			f := newBotFactoryFixture(t)
			f.req.Run.ExecutionMode = execution.BotExecutionModeChatInteractive
			f.req.Run.CallerCeiling = &execution.BotCallerCeiling{Principal: f.req.Principal, ExpiresAt: time.Now().Add(time.Hour)}
			f.req.Authorization = interactiveFactoryAuthorization{}
			f.req.Policy = nil
			meter, budgetErr := bot.NewRunBudget(store.BudgetSpec{MaxRuntimeSeconds: 60}, time.Now())
			if budgetErr != nil {
				t.Fatal(budgetErr)
			}
			f.req.Budget = meter
			path := filepath.Join(t.TempDir(), "read.txt")
			if err := os.WriteFile(path, []byte("observed"), 0600); err != nil {
				t.Fatal(err)
			}
			args := map[string]any{"path": path}
			switch tc.name {
			case "web_fetch":
				args = map[string]any{"url": "http://93.184.216.34/weather", "mode": "raw"}
			case "write_file":
				args["content"] = "changed"
			case "shell_command":
				args = map[string]any{"command": "printf observed"}
			}
			node := &fileOutcomeNode{stripReceipt: tc.strip}
			llm := &fileOutcomeModel{name: tc.name, args: args}
			f.cfg.WrapModel = func(context.Context, model.LLM, execution.RunBudget) (model.LLM, error) { return llm, nil }
			f.cfg.Prepare = func(context.Context, bot.RunRequest, *BotChatScope) (*BotChatSafeResources, error) {
				return &BotChatSafeResources{ChatConfig: &config.AppConfig{}, Catalog: &ChatCatalogResources{Pool: &interactiveOutcomePool{node: node}}, WorkspaceDir: filepath.Dir(path), VerifyIsolation: func(context.Context) error { return nil }, Cleanup: func() {}}, nil
			}
			factory, err := NewBotChatAgentFactory(f.cfg)
			if err != nil {
				t.Fatal(err)
			}
			adapter, err := api.NewBotChatRuntimeAdapter(factory)
			if err != nil {
				t.Fatal(err)
			}
			result, err := adapter.Start(f.ctx, f.req, func(context.Context, bot.RuntimeEvent) error { return nil })
			if err != nil {
				t.Fatal(err)
			}
			if node.calls != 1 || result.Usage.ToolCalls != 1 {
				t.Fatalf("executions=%d usage=%+v", node.calls, result.Usage)
			}
			want := store.RunSucceeded
			if tc.partial {
				want = store.RunPartial
			}
			if result.Status != want {
				t.Fatalf("status=%s want=%s report=%+v", result.Status, want, result.Report)
			}
			if !tc.partial && tc.name != "write_file" && len(result.Report.Unchanged) != 1 {
				t.Fatalf("read observation missing: %+v", result.Report)
			}
		})
	}
}
