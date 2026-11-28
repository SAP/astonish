package launcher

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/agent"
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
		if code, err := strconv.Atoi(r.URL.Query().Get("status")); err == nil && code >= 100 {
			w.WriteHeader(code)
		}
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
		status  int
	}{
		{name: "web_fetch", status: 401}, {name: "web_fetch", status: 429},
		{name: "web_fetch"}, {name: "read_file"}, {name: "write_file"}, {name: "write_file", strip: true, partial: true}, {name: "shell_command", partial: true},
	} {
		t.Run(tc.name+strconv.Itoa(tc.status)+map[bool]string{true: "_no_receipt"}[tc.strip], func(t *testing.T) {
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
			path := "outputs/read.txt"
			if err := os.MkdirAll(filepath.Join(f.files.root, "outputs"), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(f.files.root, path), []byte("observed"), 0600); err != nil {
				t.Fatal(err)
			}
			args := map[string]any{"path": filepath.Join(f.files.root, path)}
			switch tc.name {
			case "web_fetch":
				args = map[string]any{"url": "http://93.184.216.34/weather?status=" + strconv.Itoa(tc.status), "mode": "raw"}
			case "write_file":
				args["content"] = "changed"
				entry, err := f.files.entry(path)
				if err != nil {
					t.Fatal(err)
				}
				args["expected_sha256"] = entry.SHA256
				f.services.BotFilesFactory = func(context.Context, *store.Services, store.BotScope) (store.BotFiles, error) {
					return fileOutcomeFiles{BotFiles: f.files, strip: tc.strip}, nil
				}
			case "shell_command":
				args = map[string]any{"command": "printf observed"}
			}
			node := &fileOutcomeNode{stripReceipt: tc.strip}
			llm := &fileOutcomeModel{name: tc.name, args: args}
			f.cfg.WrapModel = func(context.Context, model.LLM, execution.RunBudget) (model.LLM, error) { return llm, nil }
			f.cfg.Prepare = func(context.Context, bot.RunRequest, *BotChatScope) (*BotChatSafeResources, error) {
				return &BotChatSafeResources{PolicyRuntime: agent.BotPolicyRuntime{Guard: workCapabilityFixtureGuard{}, Classifier: botFactoryTestClassifier{}}, ChatConfig: &config.AppConfig{}, Catalog: &ChatCatalogResources{Pool: &interactiveOutcomePool{node: node}}, WorkspaceDir: f.files.root, VerifyIsolation: func(context.Context) error { return nil }, Cleanup: func() {}}, nil
			}
			factory, err := NewBotChatAgentFactory(f.cfg)
			if err != nil {
				t.Fatal(err)
			}
			adapter, err := api.NewBotChatRuntimeAdapter(outcomeWorkerFactory(factory))
			if err != nil {
				t.Fatal(err)
			}
			writesBefore := f.files.writes
			result, err := adapter.Start(f.ctx, f.req, func(context.Context, bot.RuntimeEvent) error { return nil })
			if tc.strip {
				if err == nil || result.Status != store.RunFailed || result.Report.Failure == nil || !strings.Contains(llm.toolResponses, store.ErrBotConflict.Error()) {
					t.Fatalf("missing retained receipt did not fail closed: %v responses=%s", err, llm.toolResponses)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			wantNode, wantWrites := 1, 0
			// Ordinary file tools now execute through Chat's sandbox node; the
			// retained-file service is reserved for publication and internal state.
			if node.calls != wantNode || f.files.writes-writesBefore != wantWrites || result.Usage.ToolCalls != 1 || meter.Usage().ToolCalls != 1 {
				t.Fatalf("executions=%d usage=%+v", node.calls, result.Usage)
			}
			want := store.RunSucceeded
			if tc.partial {
				want = store.RunPartial
			}
			if tc.strip {
				want = store.RunFailed
				if len(result.Report.Unverified) != 1 || len(result.Report.Changed)+len(result.Report.Unchanged) != 0 {
					t.Fatalf("missing receipt upgraded: %+v", result.Report)
				}
			}
			if result.Status != want {
				t.Fatalf("status=%s want=%s report=%+v", result.Status, want, result.Report)
			}
			if tc.status >= 300 && (len(result.Report.Unverified) != 0 || len(result.Report.Unchanged) != 1 || !strings.Contains(result.Report.Unchanged[0].Summary, "source unavailable")) {
				t.Fatalf("known unavailable source became uncertain effect: %+v", result.Report)
			}
			if !tc.partial && tc.name != "write_file" && len(result.Report.Unchanged) != 1 {
				t.Fatalf("read observation missing: %+v", result.Report)
			}
		})
	}
}
