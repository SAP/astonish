package launcher

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/sandbox/sessioncreds"
	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/tool"
)

// Physical acceptance runs the freshly built candidate's existing node command,
// not tools.ExecuteTool in the host. Container and files belong only to this test.
type researchDockerBackend struct {
	sandbox.Backend
	container string
}

func (b *researchDockerBackend) Kind() sandbox.BackendKind { return sandbox.BackendKindDocker }
func (b *researchDockerBackend) PushFile(ctx context.Context, _ string, path string, r io.Reader, mode os.FileMode) error {
	cmd := exec.CommandContext(ctx, "docker", "exec", "-i", b.container, "/bin/sh", "-c", fmt.Sprintf("mkdir -p %q && umask 077 && /bin/cat > %q && chmod %o %q", filepath.Dir(path), path, mode, path))
	cmd.Stdin = r
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("vault push: %v %s", err, out)
	}
	return nil
}

type researchDockerClient struct {
	sandbox.ToolNodeClient
	backend *researchDockerBackend
	calls   int
}

func (c *researchDockerClient) Call(_ string, name string, args map[string]any) (json.RawMessage, error) {
	c.calls++
	raw, _ := json.Marshal(map[string]any{"id": "1", "tool": name, "args": args})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", "exec", "-i", c.backend.container, "/tmp/astonish-search-candidate", "node")
	cmd.Stdin = bytes.NewReader(append(raw, '\n'))
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("node: %v %s", err, out)
	}
	for _, line := range bytes.Split(out, []byte("\n")) {
		var r struct {
			ID     string          `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  string          `json:"error"`
		}
		if json.Unmarshal(line, &r) == nil && r.ID == "1" {
			if r.Error != "" {
				return nil, fmt.Errorf("node: %s", r.Error)
			}
			return r.Result, nil
		}
	}
	return nil, fmt.Errorf("missing physical node receipt: %s", out)
}

func TestBotConfiguredSearchPhysicalMCP(t *testing.T) {
	if os.Getenv("ASTONISH_TEST_CONFIGURED_SEARCH") != "1" {
		t.Skip("explicit owned physical search acceptance")
	}
	binary := os.Getenv("ASTONISH_TEST_SEARCH_NODE")
	if binary == "" {
		t.Fatal("fresh candidate node binary required")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	command := func(args ...string) string {
		t.Helper()
		out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput()
		if err != nil {
			t.Fatalf("docker %v: %v %s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	image := command("image", "inspect", os.Getenv("ASTONISH_SANDBOX_IMAGE"), "--format", "{{.Id}}")
	container := command("run", "-d", "--network", "none", "--entrypoint", "/bin/sh", image, "-c", "sleep 120")
	defer func() {
		out, err := exec.Command("docker", "rm", "-fv", container).CombinedOutput()
		if err != nil {
			t.Errorf("cleanup: %v %s", err, out)
		}
	}()
	command("cp", binary, container+":/tmp/astonish-search-candidate")
	command("exec", container, "chmod", "0755", "/tmp/astonish-search-candidate")
	// Fixture executable implements real MCP stdio; no internet or paid credentials.
	script := `while IFS= read -r line; do
 id=$(printf '%s' "$line" | sed -n 's/.*"id"[[:space:]]*:[[:space:]]*\([^,}]*\).*/\1/p')
 case "$line" in
 *'"server/discover"'*) printf '{"jsonrpc":"2.0","id":%s,"result":{"supportedVersions":["2026-07-28"],"capabilities":{"tools":{}},"serverInfo":{"name":"owned-tavily","version":"1"}}}\n' "$id";;
 *'"initialize"'*) printf '{"jsonrpc":"2.0","id":%s,"result":{"protocolVersion":"2025-06-18","capabilities":{"tools":{}},"serverInfo":{"name":"owned-tavily","version":"1"}}}\n' "$id";;
 *'"tools/call"'*) printf '{"jsonrpc":"2.0","id":%s,"result":{"content":[{"type":"text","text":"{\\"results\\":[{\\"title\\":\\"Decisive fixture\\",\\"url\\":\\"https://example.com/decisive\\"}]}"}]}}\n' "$id"; exit 0;;
 esac
 done`
	backend := &researchDockerBackend{container: container}
	if err := backend.PushFile(ctx, "", "/tmp/tavily-fixture.sh", strings.NewReader(script), 0700); err != nil {
		t.Fatal(err)
	}
	f, runtime, episode := researchFixture(t)
	server := store.MCPServer{Name: "owned", Command: "/bin/sh", Args: []string{"/tmp/tavily-fixture.sh"}, Env: map[string]string{"TAVILY_API_KEY": "{{CREDENTIAL:owner:token}}"}, CachedTools: json.RawMessage(`[{"name":"tavily_search","inputSchema":{"type":"object","properties":{"query":{"type":"string"},"max_results":{"type":"integer"}},"required":["query"]}}]`)}
	f.services.TeamMCPServers = newMemMCPStore(server)
	f.services.Settings = botKnowledgeTeamSettings{row: &store.TeamSettings{WebSearchTool: "owned:tavily_search"}}
	f.services.Credentials = sessioncreds.NewStore(map[string]*store.Credential{"owner": {Type: store.CredBearer, Token: productionTestSecret}})
	route, err := BotEffectiveConfiguredSearchRoute(f.ctx, f.services)
	if err != nil {
		t.Fatal(err)
	}
	runtime.m.PublicResearch.Route = route
	// Reverify after the fixture selection changes, rather than mutating authorization.
	auth, err := execution.VerifyBotMandateAuthorization(f.ctx, runtime, runtime.m.Ref, runtime.m.Purpose)
	if err != nil {
		t.Fatal(err)
	}
	f.req.Mandate.Authorization = auth
	client := &researchDockerClient{backend: backend}
	c := store.WithCredentialStore(f.ctx, f.services.Credentials)
	mcp, cleanup, err := BotMCPTools(c, productionCapabilityPool{backend: backend, client: client}, f.req.Run.SessionID.String(), f.services.TeamMCPServers)
	defer cleanup()
	if err != nil {
		t.Fatal(err)
	}
	search, err := BotConfiguredResearchTool(c, f.req, knowledgeScope(f), nil, mcp, episode)
	if err != nil {
		t.Fatal(err)
	}
	if client.calls != 0 {
		t.Fatal("eager MCP dispatch")
	}
	tc := knowledgeCtx(f)
	tc.base = c
	out, err := search.(botRunnableTool).Run(tc, map[string]any{"facet": "overview"})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(out)
	if client.calls != 1 || episode.State() != BotResearchSearched || !strings.Contains(string(raw), "https://example.com/decisive") || strings.Contains(string(raw), productionTestSecret) {
		t.Fatalf("physical search receipt: %s state=%s calls=%d", raw, episode.State(), client.calls)
	}
	if _, err := mcp[0].(tool.Tool).(botRunnableTool).Run(tc, map[string]any{"query": "private"}); err == nil || client.calls != 1 {
		t.Fatal("raw sibling authority bypass")
	}
}
