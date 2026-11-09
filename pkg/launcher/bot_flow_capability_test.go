package launcher

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/SAP/astonish/pkg/sandbox/sessioncreds"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/tools"
	"google.golang.org/adk/tool"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type productionFlowStore struct {
	store.FlowStore
	yaml  string
	calls int
}

func (s *productionFlowStore) GetFlow(context.Context, string) (string, error) {
	s.calls++
	return s.yaml, nil
}
func TestBotFlowFactoryExecutesEngine(t *testing.T) {
	f := newBotFactoryFixture(t)
	fs := &productionFlowStore{yaml: `description: controlled
nodes:
  - name: hello
    type: output
    prompt: Hello owner
flow:
  - from: START
    to: hello
  - from: hello
    to: END
`}
	f.services.PersonalFlows = fs
	target, ctx := productionFactoryTool(t, f, "run_flow", func(context.Context, string) ([]tool.Tool, func(), error) {
		x, e := BotRunFlowTool()
		return []tool.Tool{x}, func() {}, e
	})
	out, err := target.Run(ctx, map[string]any{"flow_name": "owner-flow"})
	if err != nil {
		t.Fatal(err)
	}
	if out["status"] != "completed" || fs.calls != 1 {
		t.Fatalf("engine did not complete: %#v", out)
	}
}

func TestBotFlowFactoryChildHTTPPolicy(t *testing.T) {
	tools.SetAllowPrivateNetworks(true)
	defer tools.SetAllowPrivateNetworks(false)
	for _, allowed := range []bool{false, true} {
		t.Run(fmt.Sprint(allowed), func(t *testing.T) {
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.Header.Get("Authorization") != "Bearer "+productionTestSecret {
					t.Error("missing owner auth")
				}
				fmt.Fprint(w, productionTestSecret)
			}))
			defer srv.Close()
			f := newBotFactoryFixture(t)
			f.services.PersonalCredentials = sessioncreds.NewStore(map[string]*store.Credential{"owner": {Type: store.CredBearer, Token: productionTestSecret}})
			f.services.PersonalFlows = &productionFlowStore{yaml: fmt.Sprintf(`description: controlled child
nodes:
  - name: request
    type: tool
    tools_selection: [http_request]
    args:
      url: %q
      credential: owner
flow:
  - from: START
    to: request
  - from: request
    to: END
`, srv.URL)}
			b := &productionCapabilityBackend{}
			client := &productionCapabilityClient{backend: b}
			var allow []string
			if allowed {
				allow = []string{"http_request"}
			}
			target, ctx := productionFactoryTool(t, f, "run_flow", func(_ context.Context, id string) ([]tool.Tool, func(), error) {
				flow, err := BotRunFlowTool()
				if err != nil {
					return nil, nil, err
				}
				h, err := BotHTTPTool(b, client, id)
				return []tool.Tool{flow, h}, func() {}, err
			}, allow...)
			out, err := target.Run(ctx, map[string]any{"flow_name": "owner-child-flow"})
			raw, _ := json.Marshal(out)
			if strings.Contains(string(raw), productionTestSecret) {
				t.Fatal("flow leaked token")
			}
			if allowed {
				if err != nil || calls != 1 || out["status"] != "completed" {
					t.Fatalf("child failed: %v calls=%d out=%s", err, calls, raw)
				}
			} else if calls != 0 || err == nil && out["status"] == "completed" {
				t.Fatalf("unauthorized child ran: %v calls=%d out=%s", err, calls, raw)
			}
		})
	}
}
