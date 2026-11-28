package launcher

import (
	"context"
	"io"
	"os"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/sandbox/sessioncreds"
	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/tool"
)

type emailInterleaveGuard struct {
	execution.PolicyGuard
	after func()
	calls int
}

func (g *emailInterleaveGuard) Authorize(ctx context.Context, a execution.ActionDescriptor) (execution.PolicyDecision, error) {
	d, err := g.PolicyGuard.Authorize(ctx, a)
	g.calls++
	if err == nil && g.after != nil {
		g.after()
	}
	return d, err
}

// Exercise the real progressive resolver/dispatcher, not merely the wrapper.
func TestBotEmailDispatcherTargetBinding(t *testing.T) {
	for _, mode := range []string{"account-swap", "same-name-replacement", "unchanged"} {
		t.Run(mode, func(t *testing.T) {
			f := newBotFactoryFixture(t)
			settings := emailTestSettings()
			f.services.PersonalSettings = &emailPersonalSettings{settings: settings}
			cs := &emailCountingCredentials{CredentialStore: sessioncreds.NewStore(map[string]*store.Credential{"mail": {Type: store.CredPassword, Password: productionTestSecret}})}
			f.services.PersonalCredentials = cs
			b := &productionCapabilityBackend{}
			c := &productionCapabilityClient{backend: b}
			target, ctx := emailTestTarget(t, f, "email_send", b, c)
			args := emailSendArguments()
			ctx = approveMCPTestTool(t, target, ctx, args)
			bound := target.(*botBoundTool)
			runtime := agent.BotPolicyRuntimeFromContext(bound.binding.owner)
			guard := &emailInterleaveGuard{PolicyGuard: runtime.Guard}
			if mode == "account-swap" {
				guard.after = func() { settings.Address = "replacement@example.com" }
			}
			if mode == "same-name-replacement" {
				// Classifier still describes adapter A, but the executable is now adapter B.
				original := bound.botRunnableTool.(*botEmailTool)
				replacement := *original
				services := *f.services
				alternate := *settings
				alternate.Address = "replacement@example.com"
				services.PersonalSettings = &emailPersonalSettings{settings: &alternate}
				replacement.services = &services
				guard.after = func() { bound.botRunnableTool = &replacement }
			}
			// The unchanged control reaches the node but does not contact a real SMTP server.
			if mode == "unchanged" {
				settings.SMTPServer = "127.0.0.1:1"
				ctx = approveMCPTestTool(t, target, ctx, args)
				runtime = agent.BotPolicyRuntimeFromContext(bound.binding.owner)
				guard.PolicyGuard = runtime.Guard
			}
			runtime.Guard = guard
			bound.binding.owner = agent.WithBotPolicyRuntime(bound.binding.owner, runtime)
			ctx = botFactoryHandoffContext{base: agent.WithBotPolicyRuntime(bound.binding.bind(ctx), runtime), session: ctx.SessionID()}
			index := agent.NewLexicalToolIndex()
			if err := index.PrimeTools(ctx, []tool.Tool{target}, nil); err != nil {
				t.Fatal(err)
			}
			executor := agent.NewProgressiveToolExecutor(&agent.ChatAgent{ToolIndex: index})
			cs.reads = 0
			result := executor.Execute(ctx, target.Name(), args)
			if guard.calls != 1 {
				t.Fatalf("guard calls = %d", guard.calls)
			}
			if mode != "unchanged" && (result.Error == "" || c.calls != 0 || cs.reads != 0 || len(b.vault) != 0) {
				t.Fatalf("target swap escaped: %+v calls=%d reads=%d", result, c.calls, cs.reads)
			}
			if mode == "unchanged" && c.calls != 1 {
				t.Fatalf("unchanged target did not reach node: %+v", result)
			}
		})
	}
}

type emailBlockingPush struct {
	*productionCapabilityBackend
	entered, release chan struct{}
}

func (b *emailBlockingPush) PushFile(ctx context.Context, id, path string, r io.Reader, mode os.FileMode) error {
	close(b.entered)
	<-b.release // Deliberately ignores cancellation like a slow external backend.
	return b.productionCapabilityBackend.PushFile(ctx, id, path, r, mode)
}
func TestBotEmailRechecksAfterVaultPush(t *testing.T) {
	for _, mode := range []string{"revocation", "cancellation", "isolation", "account"} {
		t.Run(mode, func(t *testing.T) {
			f := newBotFactoryFixture(t)
			settings := emailTestSettings()
			f.services.PersonalSettings = &emailPersonalSettings{settings: settings}
			f.services.PersonalCredentials = sessioncreds.NewStore(map[string]*store.Credential{"mail": {Type: store.CredPassword, Password: productionTestSecret}})
			b := &productionCapabilityBackend{}
			c := &productionCapabilityClient{backend: b}
			target, ctx := emailTestTarget(t, f, "email_send", b, c)
			ctx = approveMCPTestTool(t, target, ctx, emailSendArguments())
			bound := target.(*botBoundTool)
			blocking := &emailBlockingPush{productionCapabilityBackend: b, entered: make(chan struct{}), release: make(chan struct{})}
			bound.botRunnableTool.(*botEmailTool).backend = blocking
			base, cancel := context.WithCancel(ctx)
			defer cancel()
			ctx = botFactoryHandoffContext{base: base, session: ctx.SessionID()}
			done := make(chan error, 1)
			go func() { _, err := target.Run(ctx, emailSendArguments()); done <- err }()
			select {
			case <-blocking.entered:
			case <-time.After(5 * time.Second):
				t.Fatal("vault push not reached")
			}
			switch mode {
			case "revocation":
				f.req.Bot.Status = store.BotPaused
			case "cancellation":
				cancel()
			case "isolation":
				bound.binding.verifyIsolation = func(context.Context) error { return store.ErrBotForbidden }
			case "account":
				settings.Address = "replacement@example.com"
			}
			close(blocking.release)
			select {
			case err := <-done:
				if err == nil || c.calls != 0 {
					t.Fatalf("post-push fence missed: %v calls=%d", err, c.calls)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("dispatch stuck")
			}
		})
	}
}
