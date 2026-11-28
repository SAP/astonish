package launcher

import (
	"context"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/api"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/session"
)

// Exercise the production factory and runtime adapter together, not merely the
// prompt helper: Bot metadata must reach only the provider's system instruction.
func TestBotChatFactoryPromptInjection(t *testing.T) {
	f := newBotFactoryFixture(t)
	f.req.Bot.Name = "Orbit"
	f.req.Version.Charter = "private-charter"
	f.req.Version.Persona = store.BotPersona{Identity: "private-persona", Timezone: "Europe/Berlin"}
	f.req.Instructions = "Report this observation."
	factory, err := NewBotChatAgentFactory(f.cfg)
	if err != nil {
		t.Fatal(err)
	}
	adapter, err := api.NewBotChatRuntimeAdapter(factory)
	if err != nil {
		t.Fatal(err)
	}
	result, err := adapter.Start(f.ctx, f.req, func(context.Context, bot.RuntimeEvent) error { return nil })
	if err != nil || result.Status != store.RunSucceeded {
		t.Fatalf("status=%s err=%v", result.Status, err)
	}
	if len(f.resolver.model.requests) != 1 {
		t.Fatalf("model calls=%d", len(f.resolver.model.requests))
	}
	req := f.resolver.model.requests[0]
	if req.Config == nil || req.Config.SystemInstruction == nil {
		t.Fatal("missing system instruction")
	}
	var system string
	for _, p := range req.Config.SystemInstruction.Parts {
		system += p.Text
	}
	for _, want := range []string{"Orbit", "private-charter", "private-persona", "Europe/Berlin", "background Bot run", "not permission grants", "Your final answer is automatically delivered to this Bot's own Home chat", "Do not search for or invoke a posting tool", "exact destination and action", "at most one tool call per model response"} {
		if !strings.Contains(system, want) {
			t.Errorf("system instruction missing %q", want)
		}
	}
	foundUser := false
	for _, content := range req.Contents {
		for _, p := range content.Parts {
			if strings.Contains(p.Text, "private-charter") || strings.Contains(p.Text, "private-persona") || strings.Contains(p.Text, "Your final answer is automatically delivered") || strings.Contains(p.Text, "at most one tool call per model response") {
				t.Fatal("Bot metadata leaked into provider conversation")
			}
			foundUser = foundUser || content.Role == "user" && p.Text == f.req.Instructions
		}
	}
	if !foundUser {
		t.Fatal("clean user instructions missing")
	}
	saved, err := f.sessions.Get(f.ctx, &session.GetRequest{AppName: "astonish", UserID: f.req.Bot.OwnerID.String(), SessionID: f.req.Run.SessionID.String()})
	if err != nil {
		t.Fatal(err)
	}
	for event := range saved.Session.Events().All() {
		if event.Content == nil {
			continue
		}
		for _, p := range event.Content.Parts {
			if strings.Contains(p.Text, "private-charter") || strings.Contains(p.Text, "private-persona") || strings.Contains(p.Text, "Your final answer is automatically delivered") || strings.Contains(p.Text, "at most one tool call per model response") {
				t.Fatal("Bot metadata persisted in transcript")
			}
		}
	}
}
