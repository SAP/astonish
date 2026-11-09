package launcher

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/fleet"
	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/sandbox/sessioncreds"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"google.golang.org/adk/tool"
)

type drillFlowStore struct {
	store.FlowStore
	data map[string]string
}

func (s drillFlowStore) GetFlow(ctx context.Context, name string) (string, error) {
	if store.TeamSlugFromContext(ctx) != "owner-team" {
		return "", store.ErrBotForbidden
	}
	if text, ok := s.data[name]; ok {
		return text, nil
	}
	return "", fmt.Errorf("missing")
}
func (s drillFlowStore) ListFlowsByType(context.Context, []string) []store.FlowSummary {
	return []store.FlowSummary{{Name: "first", Suite: "suite"}, {Name: "second", Suite: "suite"}}
}

type drillBackend struct {
	sandbox.Backend
	ids, paths, contents []string
}

func (b *drillBackend) PushFile(_ context.Context, id, path string, r io.Reader, _ os.FileMode) error {
	data, err := io.ReadAll(r)
	b.ids = append(b.ids, id)
	b.paths = append(b.paths, path)
	b.contents = append(b.contents, string(data))
	return err
}

type drillClient struct {
	catalogueClient
	names  []string
	after  func()
	secret string
	err    error
}

func (c *drillClient) Call(id, name string, args map[string]any) (json.RawMessage, error) {
	c.calls++
	c.session = id
	c.names = append(c.names, name)
	if c.after != nil {
		c.after()
	}
	if c.err != nil {
		return nil, c.err
	}
	return json.Marshal(map[string]any{"stdout": "ok " + c.secret, "exit_code": 0})
}

type drillGuard struct {
	seen     []string
	consumed map[uuid.UUID]bool
}

func (g *drillGuard) Authorize(ctx context.Context, action execution.ActionDescriptor) (execution.PolicyDecision, error) {
	d := execution.EvaluateRunPolicy(ctx, action, time.Now())
	if d.Kind == execution.PolicyConsumeGrant {
		if g.consumed[d.GrantID] {
			return d, store.ErrBotForbidden
		}
		g.consumed[d.GrantID] = true
		d.Kind = execution.PolicyAllow
	}
	g.seen = append(g.seen, action.Tool)
	return d, nil
}

func drillGrants(t *testing.T, f *botFactoryFixture, actions map[string][]map[string]any) {
	t.Helper()
	var grants []execution.ScopedGrant
	var names []string
	for name, calls := range actions {
		names = append(names, name)
		for _, args := range calls {
			raw, _ := json.Marshal(args)
			a, err := (BotCatalogueClassifier{}).DescribeAction(f.ctx, name, raw)
			if err != nil {
				t.Fatal(err)
			}
			digest, err := a.Digest()
			if err != nil {
				t.Fatal(err)
			}
			grants = append(grants, execution.ScopedGrant{ID: uuid.New(), Identity: f.req.Policy.Identity(), ActionDigest: digest, ExpiresAt: time.Now().Add(time.Hour)})
		}
	}
	var err error
	f.req.Policy, err = execution.NewRunPolicy(execution.RunPolicySpec{Identity: f.req.Policy.Identity(), Level: execution.AutonomySupervised, AllowedTools: names, Grants: grants})
	if err != nil {
		t.Fatal(err)
	}
}

func drillFactory(t *testing.T, f *botFactoryFixture, b *drillBackend, c *drillClient, classifier agent.BotActionClassifier) (map[string]botRunnableTool, tool.Context, *drillGuard) {
	t.Helper()
	guard := &drillGuard{consumed: map[uuid.UUID]bool{}}
	f.cfg.Prepare = func(ctx context.Context, _ bot.RunRequest, _ *BotChatScope) (*BotChatSafeResources, error) {
		ds, err := BotDrillTools(b, "physical-run")
		if err != nil {
			return nil, err
		}
		ts, err := BotCatalogueTools(c, "physical-run", ds...)
		if err != nil {
			return nil, err
		}
		return &BotChatSafeResources{Tools: ts, WorkspaceDir: "/workspace", PolicyRuntime: agent.BotPolicyRuntime{Guard: guard, Classifier: classifier}, VerifyIsolation: func(context.Context) error { return nil }, Cleanup: func() {}}, nil
	}
	r, err := f.construct(t)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(r.Cleanup)
	result := map[string]botRunnableTool{}
	for _, x := range r.Agent.Tools {
		if v, ok := x.(botRunnableTool); ok {
			result[x.Name()] = v
		}
	}
	ctx := execution.WithRunPolicy(execution.WithBotRunIdentity(f.ctx, f.req.Policy.Identity()), f.req.Policy)
	return result, botFactoryHandoffContext{base: ctx, session: "child-session"}, guard
}

func drillData(mode string) map[string]string {
	return map[string]string{
		"suite":  "name: suite\ntype: drill_suite\nsuite_config: {}\n",
		"first":  "name: first\ntype: drill\nsuite: suite\ndrill_config:\n  mode: " + mode + "\n  on_fail: continue\n  max_retries: 2\nnodes:\n  - name: one\n    type: tool\n    args: {tool: shell_command, command: 'printf one'}\n  - name: two\n    type: tool\n    args: {tool: read_file, path: /workspace/second}\n",
		"second": "name: second\ntype: drill\nsuite: suite\nnodes:\n  - name: three\n    type: tool\n    args: {tool: shell_command, command: 'printf three'}\n",
	}
}

func TestBotDrillImplicitBrowserStepDeniedBeforeNative(t *testing.T) {
	f := newBotFactoryFixture(t)
	data := drillData("tutorial")
	data["first"] = "name: first\ntype: drill\nsuite: suite\ndrill_config: {mode: tutorial, auto_wait: true, on_fail: continue}\nnodes:\n  - name: click\n    type: tool\n    args: {tool: browser_click, selector: '#target'}\n"
	f.services.Flows = drillFlowStore{data: data}
	drillGrants(t, f, map[string][]map[string]any{"run_drill": {{"suite_name": "suite"}}, "browser_click": {{"selector": "#target"}}, "browser_wait_for": nil})
	c := &drillClient{}
	ts, ctx, g := drillFactory(t, f, &drillBackend{}, c, BotCatalogueClassifier{})
	_, err := ts["run_drill"].Run(ctx, map[string]any{"suite_name": "suite"})
	var blocked *agent.BotPolicyError
	if !errors.As(err, &blocked) || c.calls != 0 || !reflect.DeepEqual(g.seen, []string{"run_drill"}) {
		t.Fatalf("implicit browser wait bypass: %v calls=%v guards=%v", err, c.names, g.seen)
	}
}

func TestBotDrillHostBaselineAndDetachedTakeoverBlocked(t *testing.T) {
	for _, visual := range []bool{false, true} {
		t.Run(fmt.Sprint(visual), func(t *testing.T) {
			f := newBotFactoryFixture(t)
			data := drillData("test")
			if visual {
				data["first"] = "name: first\ntype: drill\nsuite: suite\nnodes:\n  - name: screenshot\n    type: tool\n    args: {tool: browser_take_screenshot}\n    assert: {type: visual_match, expected: /host/baseline}\n"
			} else {
				data["first"] = "name: first\ntype: drill\nsuite: suite\nnodes:\n  - name: takeover\n    type: tool\n    args: {tool: browser_request_human, reason: login}\n"
			}
			f.services.Flows = drillFlowStore{data: data}
			drillGrants(t, f, map[string][]map[string]any{"run_drill": {{"suite_name": "suite"}}, "browser_request_human": {{"reason": "login"}}})
			c := &drillClient{}
			ts, ctx, _ := drillFactory(t, f, &drillBackend{}, c, BotCatalogueClassifier{})
			got, err := ts["run_drill"].Run(ctx, map[string]any{"suite_name": "suite"})
			if !errors.Is(err, store.ErrBotUnsupported) || c.calls != 0 || got != nil {
				t.Fatalf("host fallback/wait: %v %v", got, err)
			}
		})
	}
}

func TestBotDrillExactPolicyPinnedSandbox(t *testing.T) {
	f := newBotFactoryFixture(t)
	f.services.Flows = drillFlowStore{data: drillData("test")}
	f.services.PersonalCredentials = sessioncreds.NewStore(map[string]*store.Credential{"owner": {Type: store.CredBearer, Token: "drill-output-secret-123456"}})
	drillGrants(t, f, map[string][]map[string]any{
		"run_drill":     {{"suite_name": "suite"}},
		"shell_command": {{"command": "printf one"}, {"command": "printf three"}},
		"read_file":     {{"path": "/workspace/second"}},
	})
	b, c := &drillBackend{}, &drillClient{secret: "drill-output-secret-123456"}
	ts, ctx, g := drillFactory(t, f, b, c, BotCatalogueClassifier{})
	got, err := ts["run_drill"].Run(ctx, map[string]any{"suite_name": "suite"})
	if err != nil {
		t.Fatal(err)
	}
	if got["status"] != "passed" || c.calls != 3 || c.session != "physical-run" || len(b.ids) != 0 {
		t.Fatalf("%v calls=%d id=%s", got, c.calls, c.session)
	}
	if !reflect.DeepEqual(g.seen, []string{"run_drill", "shell_command", "read_file", "shell_command"}) {
		t.Fatalf("per-step guard missing: %v", g.seen)
	}
	raw, _ := json.Marshal(got)
	if strings.Contains(string(raw), c.secret) {
		t.Fatal("secret result")
	}
}

func TestBotDrillDeniedStepStopsTutorialAndSuite(t *testing.T) {
	f := newBotFactoryFixture(t)
	f.services.Flows = drillFlowStore{data: drillData("tutorial")}
	drillGrants(t, f, map[string][]map[string]any{"run_drill": {{"suite_name": "suite"}}, "shell_command": {{"command": "printf one"}, {"command": "printf three"}}, "read_file": nil})
	c := &drillClient{}
	ts, ctx, _ := drillFactory(t, f, &drillBackend{}, c, BotCatalogueClassifier{})
	_, err := ts["run_drill"].Run(ctx, map[string]any{"suite_name": "suite"})
	var blocked *agent.BotPolicyError
	if !errors.As(err, &blocked) || !blocked.ApprovalRequired() || c.calls != 1 {
		t.Fatalf("err=%v calls=%v", err, c.names)
	}
}

func TestBotDrillChildCeilingAndLiveRevocation(t *testing.T) {
	for _, revoke := range []bool{false, true} {
		t.Run(fmt.Sprint(revoke), func(t *testing.T) {
			f := newBotFactoryFixture(t)
			f.services.Flows = drillFlowStore{data: drillData("test")}
			drillGrants(t, f, map[string][]map[string]any{
				"run_drill":     {{"suite_name": "suite"}},
				"shell_command": {{"command": "printf one"}, {"command": "printf three"}},
				"read_file":     {{"path": "/workspace/second"}},
			})
			c := &drillClient{}
			ts, ctx, _ := drillFactory(t, f, &drillBackend{}, c, BotCatalogueClassifier{})
			if revoke {
				c.after = func() { f.req.Bot.Status = store.BotPaused }
			} else {
				p, _ := f.req.Policy.Narrow([]string{"run_drill", "shell_command"})
				ctx = botFactoryHandoffContext{base: execution.WithRunPolicy(ctx, p), session: "child-session"}
			}
			_, err := ts["run_drill"].Run(ctx, map[string]any{"suite_name": "suite"})
			wantCalls := 0 // Production policy prohibits consequential composites in children.
			if revoke {
				wantCalls = 1
			}
			if err == nil || c.calls != wantCalls {
				t.Fatalf("err=%v calls=%v", err, c.names)
			}
		})
	}
}

func TestBotDrillSharedBudget(t *testing.T) {
	f := newBotFactoryFixture(t)
	f.services.Flows = drillFlowStore{data: drillData("test")}
	limit := int64(2)
	meter, err := bot.NewRunBudget(store.BudgetSpec{MaxRuntimeSeconds: 60, MaxToolCalls: &limit}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	f.req.Budget = meter
	f.req.Policy, _ = execution.NewRunPolicy(execution.RunPolicySpec{Identity: f.req.Policy.Identity(), Level: execution.AutonomySupervised, AllowedTools: []string{"run_drill", "shell_command", "read_file"}})
	c := &drillClient{}
	ts, ctx, _ := drillFactory(t, f, &drillBackend{}, c, botFactoryTestClassifier{})
	_, err = ts["run_drill"].Run(ctx, map[string]any{"suite_name": "suite"})
	if !errors.Is(err, bot.ErrBudgetExceeded) || c.calls != 1 {
		t.Fatalf("err=%v calls=%v", err, c.names)
	}
}

func TestBotDrillSandboxFailureNeverFallsBack(t *testing.T) {
	f := newBotFactoryFixture(t)
	marker := filepath.Join(t.TempDir(), "host-executed")
	command := fmt.Sprintf("printf unsafe > %q", marker)
	data := drillData("tutorial")
	data["first"] = fmt.Sprintf("name: first\ntype: drill\nsuite: suite\ndrill_config: {mode: tutorial, on_fail: continue}\nnodes:\n  - name: failed\n    type: tool\n    args: {tool: shell_command, command: %q}\n", command)
	f.services.Flows = drillFlowStore{data: data}
	drillGrants(t, f, map[string][]map[string]any{
		"run_drill":     {{"suite_name": "suite"}},
		"shell_command": {{"command": command}, {"command": "printf three"}},
	})
	unavailable := errors.New("sandbox unavailable")
	c := &drillClient{err: unavailable}
	ts, ctx, _ := drillFactory(t, f, &drillBackend{}, c, BotCatalogueClassifier{})
	got, err := ts["run_drill"].Run(ctx, map[string]any{"suite_name": "suite"})
	if !errors.Is(err, unavailable) || got != nil || c.calls != 1 || c.session != "physical-run" {
		t.Fatalf("sandbox failure bypassed: result=%v err=%v calls=%v id=%s", got, err, c.names, c.session)
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("host command executed: %v", err)
	}
}

func TestBotDrillStoreUnavailableNeverFallsBack(t *testing.T) {
	f := newBotFactoryFixture(t)
	f.services.Flows = nil
	drillGrants(t, f, map[string][]map[string]any{"run_drill": {{"suite_name": "suite"}}})
	b, c := &drillBackend{}, &drillClient{}
	ts, ctx, _ := drillFactory(t, f, b, c, BotCatalogueClassifier{})
	got, err := ts["run_drill"].Run(ctx, map[string]any{"suite_name": "suite"})
	if !errors.Is(err, store.ErrBotUnsupported) || got != nil || c.calls != 0 || len(b.ids) != 0 {
		t.Fatalf("missing scoped store did not fail closed: %v %v", got, err)
	}
}

func TestBotDrillInjectionMappingMutationBlocked(t *testing.T) {
	for _, change := range []string{"credential", "path", "mode", "sandbox"} {
		t.Run(change, func(t *testing.T) {
			f := newBotFactoryFixture(t)
			data := drillData("test")
			data["suite"] = "name: suite\ntype: drill_suite\nsuite_config:\n  credentials: {app: owner}\n  credential_injection:\n    files:\n      - {credential: app, path: /workspace/token, field: token, mode: '0600'}\n"
			f.services.Flows = drillFlowStore{data: data}
			f.services.PersonalCredentials = sessioncreds.NewStore(map[string]*store.Credential{"owner": {Type: store.CredBearer, Token: "mapping-secret-123456"}})
			file := map[string]any{"credential": "app", "path": "/workspace/token", "field": "token", "mode": "0600"}
			mapping := map[string]any{"suite_name": "suite", "credentials": map[string]string{"app": "owner"}, "credential_injection": map[string]any{"files": []any{file}}, "sandbox_id": "physical-run"}
			switch change {
			case "credential":
				mapping["credentials"] = map[string]string{"app": "other-owner"}
			case "path":
				file["path"] = "/workspace/approved-token"
			case "mode":
				file["mode"] = "0400"
			case "sandbox":
				mapping["sandbox_id"] = "other-run"
			}
			drillGrants(t, f, map[string][]map[string]any{"inject_drill_credentials": {{"suite_name": "suite"}, mapping}})
			b, c := &drillBackend{}, &drillClient{}
			ts, ctx, _ := drillFactory(t, f, b, c, BotCatalogueClassifier{})
			got, err := ts["inject_drill_credentials"].Run(ctx, map[string]any{"suite_name": "suite"})
			var blocked *agent.BotPolicyError
			if !errors.As(err, &blocked) || !blocked.ApprovalRequired() || got != nil || len(b.ids) != 0 || c.calls != 0 {
				t.Fatalf("changed mapping escaped approval: %v %v writes=%v", got, err, b.ids)
			}
		})
	}
}

func TestBotDrillImplicitInjectionRequiresChildAuthority(t *testing.T) {
	f := newBotFactoryFixture(t)
	data := drillData("test")
	data["suite"] = "name: suite\ntype: drill_suite\nsuite_config:\n  credentials: {app: owner}\n  credential_injection:\n    files:\n      - {credential: app, path: /workspace/token, field: token, mode: '0600'}\n"
	f.services.Flows = drillFlowStore{data: data}
	f.services.PersonalCredentials = sessioncreds.NewStore(map[string]*store.Credential{"owner": {Type: store.CredBearer, Token: "child-secret-123456"}})
	mapping := map[string]any{"suite_name": "suite", "credentials": map[string]string{"app": "owner"}, "credential_injection": map[string]any{"files": []any{map[string]any{"credential": "app", "path": "/workspace/token", "field": "token", "mode": "0600"}}}, "sandbox_id": "physical-run"}
	drillGrants(t, f, map[string][]map[string]any{"run_drill": {{"suite_name": "suite"}}, "inject_drill_credentials": {mapping}})
	b, c := &drillBackend{}, &drillClient{}
	ts, ctx, _ := drillFactory(t, f, b, c, BotCatalogueClassifier{})
	narrowed, err := f.req.Policy.Narrow([]string{"run_drill"})
	if err != nil {
		t.Fatal(err)
	}
	ctx = botFactoryHandoffContext{base: execution.WithRunPolicy(ctx, narrowed), session: "child-session"}
	got, err := ts["run_drill"].Run(ctx, map[string]any{"suite_name": "suite"})
	var blocked *agent.BotPolicyError
	if !errors.As(err, &blocked) || blocked.ApprovalRequired() || got != nil || len(b.ids) != 0 || c.calls != 0 {
		t.Fatalf("implicit injection widened child authority: %v %v writes=%v", got, err, b.ids)
	}
}

func TestBotDrillInjectionExactMapping(t *testing.T) {
	for _, approved := range []bool{false, true} {
		t.Run(fmt.Sprint(approved), func(t *testing.T) {
			f := newBotFactoryFixture(t)
			data := drillData("test")
			data["suite"] = "name: suite\ntype: drill_suite\nsuite_config:\n  credentials: {app: owner}\n  credential_injection:\n    files:\n      - {credential: app, path: /workspace/token, field: token, mode: '0600'}\n"
			f.services.Flows = drillFlowStore{data: data}
			f.services.PersonalCredentials = sessioncreds.NewStore(map[string]*store.Credential{"owner": {Type: store.CredBearer, Token: "drill-injected-secret-123456"}})
			calls := []map[string]any{{"suite_name": "suite"}}
			if approved {
				calls = append(calls, map[string]any{"suite_name": "suite", "credentials": map[string]string{"app": "owner"}, "credential_injection": map[string]any{"files": []any{map[string]any{"credential": "app", "path": "/workspace/token", "field": "token", "mode": "0600"}}}, "sandbox_id": "physical-run"})
			}
			if approved {
				resolved, err := fleet.ResolveCredentialsPlatformMap(f.ctx, map[string]string{"app": "owner"}, f.services.PersonalCredentials)
				if err != nil {
					t.Fatal(err)
				}
				ref, err := botDrillPayloadReference(resolved)
				if err != nil {
					t.Fatal(err)
				}
				calls[len(calls)-1]["payload_ref"] = ref
			}
			drillGrants(t, f, map[string][]map[string]any{"inject_drill_credentials": calls})
			b, c := &drillBackend{}, &drillClient{}
			ts, ctx, _ := drillFactory(t, f, b, c, BotCatalogueClassifier{})
			got, err := ts["inject_drill_credentials"].Run(ctx, map[string]any{"suite_name": "suite"})
			if !approved {
				if err == nil || len(b.ids) != 0 {
					t.Fatalf("name-only approval injected: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(b.ids) != 1 || b.ids[0] != "physical-run" || b.contents[0] != "drill-injected-secret-123456" || c.calls != 0 {
				t.Fatalf("wrong injection target %v", b.ids)
			}
			raw, _ := json.Marshal(got)
			if strings.Contains(string(raw), b.contents[0]) {
				t.Fatal("secret returned")
			}
		})
	}
}
