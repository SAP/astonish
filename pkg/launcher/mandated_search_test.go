package launcher

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/tools"
	"github.com/google/uuid"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
)

type searchTestVault struct {
	store.CredentialStore
	credential *store.Credential
	gets       int
}

func (v *searchTestVault) Get(context.Context, string) *store.Credential {
	v.gets++
	return v.credential
}
func TestBotMandatedConfiguredSearch(t *testing.T) {
	b := tools.ConfiguredSearchBackend{Provider: "configured", Model: "sonar", Endpoint: "https://search.example.org/chat/completions", Credential: "search"}
	resources, err := BotConfiguredSearchResources(b, []string{"public weather"})
	if err != nil {
		t.Fatal(err)
	}
	id := execution.BotRunIdentity{BotID: uuid.New(), ActorID: uuid.New().String(), OrgSlug: "org", TeamSlug: "team"}
	runtime := &factoryMandateRuntime{saved: execution.BotMandate{Ref: execution.BotMandateRef{ID: uuid.New(), BotID: id.BotID, OwnerID: id.ActorID, OrgSlug: id.OrgSlug, TeamSlug: id.TeamSlug, Revision: 1}, Purpose: execution.BotMandatePurposeTask, ExpiresAt: time.Now().Add(time.Hour), Scopes: []string{"tool:execute"}, Constraints: []execution.BotActionResourceConstraint{{Action: execution.CapabilityToolExecute, Resources: resources}}, Limits: execution.BotMandateLimits(config.DefaultBotMandateConfig())}}
	auth, err := execution.VerifyBotMandateAuthorization(context.Background(), runtime, runtime.saved.Ref, execution.BotMandatePurposeTask)
	if err != nil {
		t.Fatal(err)
	}
	vault := &searchTestVault{credential: &store.Credential{Type: store.CredAPIKey, Value: "scoped-test-secret"}}
	current := b
	source := func(context.Context) (tools.ConfiguredSearchBackend, store.CredentialStore, error) {
		return current, vault, nil
	}
	adapter, err := BotMandatedConfiguredSearchTool(&bot.MandateExecution{Authorization: auth, Runtime: runtime, Identity: id}, source, tools.RoutineReadPolicy{Authorize: func(context.Context, string) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}
	classifier := WithBotMandatedReadClassifier(BotCatalogueClassifier{}, []tool.Tool{adapter})
	raw := json.RawMessage(`{"query":"public weather"}`)
	action, err := classifier.DescribeAction(context.Background(), BotConfiguredSearchToolName, raw)
	if err != nil || action.Effect != execution.EffectRead || !action.ConstraintsVerified {
		t.Fatalf("%+v %v", action, err)
	}
	for _, bad := range []string{`{"query":"private task memory"}`, `{"query":"public weather","credential":"other"}`, `{"query":"public weather","url":"https://evil.example"}`} {
		if _, err := classifier.DescribeAction(context.Background(), BotConfiguredSearchToolName, json.RawMessage(bad)); err == nil {
			t.Fatal(bad)
		}
	}
	before := vault.gets
	current.Endpoint = "https://changed.example/chat/completions"
	if _, err := classifier.DescribeAction(context.Background(), BotConfiguredSearchToolName, raw); err == nil || vault.gets != before {
		t.Fatal("changed backend reached credential")
	}
	current = b
	vault.credential = nil
	if _, err := classifier.DescribeAction(context.Background(), BotConfiguredSearchToolName, raw); err == nil {
		t.Fatal("missing credential accepted")
	}
	native, err := functiontool.New(functiontool.Config{Name: BotConfiguredSearchToolName, Description: "fake"}, func(tool.Context, botConfiguredSearchArgs) (struct{}, error) { return struct{}{}, nil })
	if err != nil {
		t.Fatal(err)
	}
	for _, catalog := range [][]tool.Tool{{adapter, native}, {native, adapter}} {
		duplicate := WithBotMandatedReadClassifier(BotCatalogueClassifier{}, catalog)
		d, err := duplicate.DescribeAction(context.Background(), BotConfiguredSearchToolName, raw)
		if err != nil || d.Effect != execution.EffectUnknown {
			t.Fatal("duplicate name trusted")
		}
	}
	fake := WithBotMandatedReadClassifier(BotCatalogueClassifier{}, []tool.Tool{native})
	a, err := fake.DescribeAction(context.Background(), BotConfiguredSearchToolName, raw)
	if err != nil || a.Effect != execution.EffectUnknown {
		t.Fatalf("fake trusted: %+v %v", a, err)
	}
}
