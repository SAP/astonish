package launcher

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/common"
	"github.com/SAP/astonish/pkg/credentials"
	"github.com/SAP/astonish/pkg/drill"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/fleet"
	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/tools"
	"google.golang.org/adk/model"
	"google.golang.org/adk/tool"
)

// BotDrillTools borrows Chat's schemas, never its host-fallback executor. The
// factory completes dispatch with only its terminally bound catalogue. backend
// and physicalID are the already provisioned run sandbox, not a Chat/child ID.
func BotDrillTools(backend sandbox.Backend, physicalID string) ([]tool.Tool, error) {
	if backend == nil || physicalID == "" {
		return nil, store.ErrBotUnsupported
	}
	run, err := tools.NewRunDrillTool(nil, nil, nil, nil)
	if err != nil {
		return nil, err
	}
	inject, err := tools.NewInjectDrillCredentialsTool(nil, nil, nil)
	if err != nil {
		return nil, err
	}
	return []tool.Tool{
		&botDrillTool{botRunnableTool: run.(botRunnableTool), backend: backend, physicalID: physicalID},
		&botDrillTool{botRunnableTool: inject.(botRunnableTool), backend: backend, physicalID: physicalID},
	}, nil
}

type botDrillTool struct {
	botRunnableTool
	backend    sandbox.Backend
	physicalID string
	binding    *botFactoryBinding
	tools      map[string]botRunnableTool
	llm        model.LLM
}

func (t *botDrillTool) configure(binding *botFactoryBinding, ts []tool.Tool, llm model.LLM) {
	t.binding, t.llm = binding, llm
	t.tools = map[string]botRunnableTool{}
	for _, candidate := range ts {
		// Only terminal wrappers from this factory are eligible. A drill cannot
		// recursively launch another composite and acquire a fresh execution latch.
		if bound, ok := candidate.(*botBoundTool); ok && bound.binding == binding {
			switch candidate.Name() {
			case "run_drill", "run_flow", "inject_drill_credentials":
				continue
			}
			t.tools[candidate.Name()] = bound
		}
	}
}

func (t *botDrillTool) Run(ctx tool.Context, args any) (map[string]any, error) {
	if t.binding == nil {
		return nil, store.ErrBotUnsupported
	}
	if _, ok := ctx.(botBoundToolContext); !ok {
		return nil, store.ErrBotForbidden
	}
	p := execution.RunPolicyFromContext(ctx)
	if p == nil || p.Identity() != t.binding.req.Policy.Identity() {
		return nil, store.ErrBotForbidden
	}
	if err := t.binding.check(ctx); err != nil {
		return nil, err
	}
	redactor := credentials.RedactorFromContext(ctx)
	if redactor == nil {
		return nil, store.ErrBotForbidden
	}
	out, err := t.run(ctx, args, redactor)
	if err != nil {
		// Preserve policy/budget error types for the Bot runtime's blocked outcome.
		// Those errors contain no secret values; all injector errors are sanitized.
		return nil, botDrillError{cause: err, message: redactor.Redact(err.Error())}
	}
	return redactor.RedactMap(out), nil
}

type botDrillError struct {
	cause   error
	message string
}

func (e botDrillError) Error() string { return e.message }
func (e botDrillError) Unwrap() error { return e.cause }

func (t *botDrillTool) run(ctx tool.Context, args any, redactor *credentials.Redactor) (map[string]any, error) {
	raw, err := json.Marshal(args)
	if err != nil {
		return nil, err
	}
	var values map[string]any
	if err := json.Unmarshal(raw, &values); err != nil {
		return nil, err
	}
	if err := common.ValidateToolArguments(t, values); err != nil {
		return nil, err
	}
	var a tools.RunDrillArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return nil, err
	}
	name := strings.TrimSuffix(strings.TrimSuffix(strings.TrimSpace(a.SuiteName), ".yaml"), ".yml")
	svc := store.FromContext(ctx)
	if name == "" {
		return nil, store.ErrBotInvalid
	}
	if svc == nil || svc.Flows == nil {
		return nil, fmt.Errorf("%w: team drill store unavailable; filesystem fallback disabled", store.ErrBotUnsupported)
	}
	suite, err := drill.LoadSuiteFromStore(svc.Flows, ctx, name)
	if err != nil {
		return nil, fmt.Errorf("drill load: %s", redactor.Redact(err.Error()))
	}
	if err := drill.ValidateSuite(suite); err != nil {
		return nil, fmt.Errorf("drill validation: %s", redactor.Redact(err.Error()))
	}
	if err := t.inject(ctx, suite, redactor); err != nil {
		return nil, err
	}
	if t.Name() == "inject_drill_credentials" {
		return map[string]any{"status": "ok", "message": "Suite credential mappings applied to the pinned Bot sandbox (no secrets returned)."}, nil
	}
	tests := suite.Tests
	if a.Tag != "" {
		tags := strings.Split(a.Tag, ",")
		for i := range tags {
			tags[i] = strings.TrimSpace(tags[i])
		}
		tests = drill.FilterTestsByTag(tests, tags)
	}
	if a.TestName != "" {
		match := drill.FilterTestByName(tests, strings.TrimSuffix(strings.TrimSuffix(a.TestName, ".yaml"), ".yml"))
		if match == nil {
			return nil, fmt.Errorf("%w: drill not in selected suite", store.ErrBotInvalid)
		}
		tests = []drill.LoadedTest{*match}
	}
	for _, test := range tests {
		for _, node := range test.Config.Nodes {
			if node.Assert != nil && node.Assert.Type == "visual_match" {
				return nil, fmt.Errorf("%w: visual baselines require a scoped sandbox artifact adapter; daemon filesystem fallback disabled", store.ErrBotUnsupported)
			}
		}
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	executor := &botDrillExecutor{invocation: ctx, tools: t.tools, redactor: redactor, cancel: cancel}
	// No daemon artifact directory or global report writer. Browser recording
	// remains explicitly unsupported until a scoped artifact adapter is supplied.
	runner := drill.NewSuiteRunner(executor, nil, false)
	runner.SetVars(map[string]string{"CONTAINER_IP": "localhost"})
	runner.SetLLMProvider(drill.NewLLMProviderFromModel(t.llm))
	report, err := runner.RunSuite(runCtx, suite, tests)
	if err != nil {
		return nil, err
	}
	var text bytes.Buffer
	drill.PrintReport(report, &text)
	if svc.DrillReports != nil {
		encoded, err := json.Marshal(report)
		if err != nil {
			return nil, err
		}
		// Scrub before persistence, not merely the outer tool response.
		encoded = []byte(redactor.Redact(string(encoded)))
		if err := t.binding.check(ctx); err != nil {
			return nil, err
		}
		if err := svc.DrillReports.SaveReport(ctx, &store.DrillReport{Suite: name, Status: report.Status, Summary: redactor.Redact(report.Summary), DurationMs: report.Duration, ReportData: encoded, StartedAt: report.StartedAt, FinishedAt: report.FinishedAt}); err != nil {
			return nil, fmt.Errorf("drill report: %s", redactor.Redact(err.Error()))
		}
	}
	return map[string]any{"status": report.Status, "summary": report.Summary, "report": text.String()}, nil
}

// botDrillStepContext intentionally exposes only the bound caller's values:
// narrowed child policy survives; a dispatch handoff for run_drill does not.
// Deadline/cancellation, however, come from the runner's per-step context.
type botDrillStepContext struct {
	tool.Context
	step context.Context
}

func (c botDrillStepContext) Deadline() (time.Time, bool) { return c.step.Deadline() }
func (c botDrillStepContext) Done() <-chan struct{}       { return c.step.Done() }
func (c botDrillStepContext) Err() error                  { return c.step.Err() }

type botDrillExecutor struct {
	invocation tool.Context
	tools      map[string]botRunnableTool
	redactor   *credentials.Redactor
	cancel     context.CancelFunc
	terminal   error
}

func (e *botDrillExecutor) TerminalError() error { return e.terminal }
func (e *botDrillExecutor) Execute(ctx context.Context, name string, args map[string]any) (any, error) {
	if e.terminal != nil {
		return nil, e.terminal
	}
	fail := func(err error) (any, error) { e.terminal = err; e.cancel(); return nil, err }
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	target := e.tools[name]
	if target == nil {
		return fail(fmt.Errorf("%w: drill step %s has no bound sandbox adapter", store.ErrBotUnsupported, name))
	}
	result, err := target.Run(botDrillStepContext{Context: e.invocation, step: ctx}, args)
	if err != nil {
		return fail(err)
	}
	return e.redactor.RedactMap(result), nil
}

func (t *botDrillTool) inject(ctx tool.Context, suite *drill.LoadedSuite, redactor *credentials.Redactor) error {
	// No fleet-plan/template fallback: only this owner-scoped suite's explicit
	// mapping is eligible. Resolve a static snapshot before payload approval;
	// only its keyed reference enters the reviewed action, never secret bytes.
	spec, err := drill.SpecFromSuite(suite.Name, suite.Config.SuiteConfig)
	if err != nil {
		return fmt.Errorf("drill injection: %s", redactor.Redact(err.Error()))
	}
	if spec == nil || !spec.HasWork() {
		return nil
	}
	if len(spec.Injection.Env) != 0 {
		return fmt.Errorf("%w: drill environment injection needs a pinned node environment adapter; use explicit credential files", store.ErrBotUnsupported)
	}
	for _, file := range spec.Injection.Files {
		switch file.Field {
		case "token", "value", "password", "username", "content":
		default:
			return fmt.Errorf("%w: static drill credential field required", store.ErrBotUnsupported)
		}
	}
	// Before reading secret material enforce the child's identity/tool ceiling.
	ceiling := execution.EvaluateRunPolicy(ctx, execution.ActionDescriptor{Tool: "inject_drill_credentials", Effect: execution.EffectRead, Arguments: json.RawMessage(`{}`)}, time.Now())
	if ceiling.Kind == execution.PolicyDeny {
		return &agent.BotPolicyError{Decision: ceiling}
	}
	if err := t.binding.check(ctx); err != nil {
		return err
	}
	cs := store.CredentialStoreFromContext(ctx)
	if cs == nil {
		return store.ErrBotUnsupported
	}
	// The existing resolver reads stored fields only (no OAuth/network refresh).
	resolved, err := fleet.ResolveCredentialsPlatformMap(ctx, spec.Credentials, cs)
	if err != nil {
		return fmt.Errorf("drill injection: %s", redactor.Redact(err.Error()))
	}
	for _, c := range resolved {
		for _, value := range []string{c.Token, c.Password, c.Username, c.Content} {
			if value != "" {
				redactor.AddSecret("drill-injection", value)
			}
		}
	}
	payloadRef, err := botDrillPayloadReference(resolved)
	if err != nil {
		return store.ErrBotUnsupported
	}
	action := map[string]any{"suite_name": suite.Name, "credentials": spec.Credentials, "credential_injection": spec.Injection, "sandbox_id": t.physicalID, "payload_ref": payloadRef}
	if err := agent.AuthorizeBotTool(ctx, "inject_drill_credentials", action); err != nil {
		return err
	}
	if t.Name() != "inject_drill_credentials" {
		if err := t.binding.req.Budget.ReserveTool(ctx, "inject_drill_credentials"); err != nil {
			return err
		}
	}
	backend := botDrillInjectionBackend{Backend: t.backend, binding: t.binding, physicalID: t.physicalID}
	// Passing nil cs is intentional: the materializer uses only the resolved
	// static snapshot; it cannot refresh credentials on the daemon.
	if err := fleet.MaterializeInjectionFilesFromSpec(ctx, backend, t.physicalID, spec.Credentials, spec.Injection, resolved, nil, spec.OwnerKey); err != nil {
		return fmt.Errorf("drill injection: %s", redactor.Redact(err.Error()))
	}
	return nil
}

// The key never leaves this process. Restarting invalidates pending payload
// approvals (fail closed), rather than exposing an offline dictionary oracle for
// low-entropy passwords. Only the HMAC reference may be persisted, never bytes.
var botDrillPayloadKey = func() []byte {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil
	}
	return key
}()

func botDrillPayloadReference(resolved map[string]*fleet.ResolvedCredential) (string, error) {
	if len(botDrillPayloadKey) != 32 {
		return "", store.ErrBotUnsupported
	}
	raw, err := json.Marshal(resolved)
	if err != nil {
		return "", err
	}
	defer clear(raw)
	mac := hmac.New(sha256.New, botDrillPayloadKey)
	mac.Write([]byte("bot-drill-payload-v1\x00"))
	mac.Write(raw)
	return "drill-payload-v1:" + hex.EncodeToString(mac.Sum(nil)), nil
}

type botDrillInjectionBackend struct {
	sandbox.Backend
	binding    *botFactoryBinding
	physicalID string
}

func (b botDrillInjectionBackend) PushFile(ctx context.Context, id, path string, content io.Reader, mode os.FileMode) error {
	if id != b.physicalID {
		return store.ErrBotForbidden
	}
	if err := b.binding.check(ctx); err != nil {
		return err
	}
	return b.Backend.PushFile(ctx, b.physicalID, path, content, mode)
}
