package launcher

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/SAP/astonish/pkg/agent"
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

// BotDrillTools uses Chat's ordinary run_drill handler with scoped execution
// dependencies. The factory supplies only its terminally bound catalogue. backend
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
	deps := &tools.ScopedDrillExecution{
		NewExecutor: func(invocation tool.Context) (drill.ToolExecutor, error) {
			return &botDrillExecutor{invocation: invocation, tools: t.tools, redactor: redactor}, nil
		},
		LLM: drill.NewLLMProviderFromModel(t.llm),
		InjectCredentials: func(invocation tool.Context, suite *drill.LoadedSuite) error {
			return t.inject(invocation, suite, redactor)
		},
		SaveReport: func(invocation tool.Context, report *drill.SuiteReport) error {
			svc := store.FromContext(invocation)
			if svc == nil || svc.DrillReports == nil {
				return nil
			}
			encoded, err := json.Marshal(report)
			if err != nil {
				return err
			}
			if err := t.binding.check(invocation); err != nil {
				return err
			}
			return svc.DrillReports.SaveReport(invocation, &store.DrillReport{Suite: report.Suite, Status: report.Status, Summary: redactor.Redact(report.Summary), DurationMs: report.Duration, ReportData: []byte(redactor.Redact(string(encoded))), StartedAt: report.StartedAt, FinishedAt: report.FinishedAt})
		},
	}
	bound := botDrillExecutionContext{Context: ctx, scoped: tools.WithDrillExecutionContext(ctx, deps)}
	return t.botRunnableTool.Run(bound, args)
}

type botDrillExecutionContext struct {
	tool.Context
	scoped context.Context
}

func (c botDrillExecutionContext) Value(key any) any { return c.scoped.Value(key) }

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
}

func (e *botDrillExecutor) Execute(ctx context.Context, name string, args map[string]any) (any, error) {
	target := e.tools[name]
	if target == nil {
		return nil, fmt.Errorf("%w: drill step %s has no bound sandbox adapter", store.ErrBotUnsupported, name)
	}
	result, err := target.Run(botDrillStepContext{Context: e.invocation, step: ctx}, args)
	if err != nil {
		return nil, err
	}
	return e.redactor.RedactMap(result), nil
}
func (t *botDrillTool) inject(ctx tool.Context, suite *drill.LoadedSuite, redactor *credentials.Redactor) error {
	return injectScopedDrillCredentials(ctx, suite, redactor, t.backend, t.physicalID, t.binding.check, func(ctx context.Context, action map[string]any) error {
		if err := agent.AuthorizeBotTool(ctx, "inject_drill_credentials", action); err != nil {
			return err
		}
		if t.Name() != "inject_drill_credentials" {
			return t.binding.req.Budget.ReserveTool(ctx, "inject_drill_credentials")
		}
		return nil
	})
}

// injectScopedDrillCredentials shares the static, exact-payload materialization
// boundary with Chat. check and authorize are trusted runtime hooks, never YAML.
func injectScopedDrillCredentials(ctx tool.Context, suite *drill.LoadedSuite, redactor *credentials.Redactor, target sandbox.Backend, physicalID string, check func(context.Context) error, authorize func(context.Context, map[string]any) error) error {
	if suite == nil || suite.Config == nil {
		return nil
	}
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
	if target == nil || physicalID == "" || check == nil || authorize == nil || redactor == nil {
		return fmt.Errorf("%w: scoped drill credential injector is unavailable", store.ErrBotUnsupported)
	}
	if err := check(ctx); err != nil {
		return err
	}
	// Before reading secret material enforce the child's identity/tool ceiling.
	if _, botRun := execution.BotRunIdentityFromContext(ctx); botRun || execution.RunPolicyFromContext(ctx) != nil {
		ceiling := execution.EvaluateRunPolicy(ctx, execution.ActionDescriptor{Tool: "inject_drill_credentials", Effect: execution.EffectRead, Arguments: json.RawMessage(`{}`)}, time.Now())
		if ceiling.Kind == execution.PolicyDeny {
			return &agent.BotPolicyError{Decision: ceiling}
		}
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
	action := map[string]any{"suite_name": suite.Name, "credentials": spec.Credentials, "credential_injection": spec.Injection, "sandbox_id": physicalID, "payload_ref": payloadRef}
	if err := authorize(ctx, action); err != nil {
		return err
	}
	backend := botDrillInjectionBackend{Backend: target, check: check, physicalID: physicalID}
	// Passing nil cs is intentional: the materializer uses only the resolved
	// static snapshot; it cannot refresh credentials on the daemon.
	if err := fleet.MaterializeInjectionFilesFromSpec(ctx, backend, physicalID, spec.Credentials, spec.Injection, resolved, nil, spec.OwnerKey); err != nil {
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
	check      func(context.Context) error
	physicalID string
}

func (b botDrillInjectionBackend) PushFile(ctx context.Context, id, path string, content io.Reader, mode os.FileMode) error {
	if id != b.physicalID {
		return store.ErrBotForbidden
	}
	if err := b.check(ctx); err != nil {
		return err
	}
	return b.Backend.PushFile(ctx, b.physicalID, path, content, mode)
}
