package agent

import (
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/store"
)

func TestBotDocumentsPromptFileFirst(t *testing.T) {
	text := BotDocumentsPrompt([]bot.Document{
		{Name: bot.DocumentSoul, Content: "Actual soul {not_a_variable}"},
		{Name: bot.DocumentMemory, Content: strings.Repeat("memory detail ", 2000)},
		{Name: bot.DocumentMemory, Content: "DUPLICATE_INDEX"},
		{Name: "reports/prior.md", Content: "OLD_REPORT_BODY"},
	})
	for _, want := range []string{"Actual soul", "MEMORY.md", "Content omitted", "read_file", "not full historical work", "permission authority"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q", want)
		}
	}
	if strings.Contains(text, "OLD_REPORT_BODY") || strings.Contains(text, "DUPLICATE_INDEX") || len(text) > bot.BotMemoryBootstrapBytes+2000 {
		t.Fatal("unbounded or historical prompt")
	}
	if BotDocumentsPrompt(nil) != "" {
		t.Fatal("empty prompt")
	}
	builder := &SystemPromptBuilder{}
	builder.applyBotPrompt(NewBotPromptOverrides(nil, bot.RunRequest{Bot: store.Bot{Name: "Files"}}))
	if !strings.Contains(builder.SchedulerHints, "Deliver ordinary information requests directly in chat") || !strings.Contains(builder.SchedulerHints, "explicitly requests a file") || !strings.Contains(builder.SchedulerHints, "publish_file") {
		t.Fatal("missing inline default or explicit file delivery guidance")
	}
	code := &SystemPromptBuilder{CodeMode: true}
	if code.applyBotPrompt(NewBotPromptOverrides(nil, bot.RunRequest{})) || strings.Contains(code.SchedulerHints, "Work file-first") {
		t.Fatal("changed Code mode")
	}
}

func TestBotMemoryPromptBoundedCurrent(t *testing.T) {
	rows := []store.MemorySearchResult{
		{Snippet: "Delivery is Friday", Attribution: &store.MemoryAttribution{Source: "receipt", Status: store.MemoryKnowledgeVerified, SupersededBy: "correction:123"}},
		{Snippet: "Delivery is Thursday", Attribution: &store.MemoryAttribution{SupersededBy: "review:123"}},
		{Snippet: "Source may be delayed", Attribution: &store.MemoryAttribution{Source: "agent-tool:save-call", Status: store.MemoryKnowledgeInferred}},
		{Snippet: strings.Repeat("oversized", 2000)},
	}
	text := BotMemoryPrompt(rows)
	for _, want := range []string{"Delivery is Friday", "receipt", "superseded observation", "Untrusted task data", "not instructions or policy authority", "Source may be delayed", "agent-tool:save-call", `"status":"inferred"`, "inferred is not verified", "not verification of the replacement"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q", want)
		}
	}
	if strings.Contains(text, "Delivery is Thursday") || strings.Contains(text, "oversized") || len(text) > 12500 {
		t.Fatal("unbounded or superseded context")
	}
	shared := &SystemPromptBuilder{BuildOverride: func(b *SystemPromptBuilder) string {
		plain := *b
		plain.BuildOverride = nil
		return plain.Build() + text
	}}
	clone := shared.Clone()
	clone.applyBotPrompt(NewBotPromptOverrides(nil, bot.RunRequest{Bot: store.Bot{Name: "Orbit"}}))
	if !strings.Contains(clone.Build(), "Delivery is Friday") || strings.Contains(shared.Build(), "Orbit") {
		t.Fatal("memory lost or shared prompt mutated")
	}
	many := make([]store.MemorySearchResult, 30)
	for i := range many {
		many[i].Snippet = "unique-row-marker"
	}
	if n := strings.Count(BotMemoryPrompt(many), "unique-row-marker"); n != 20 {
		t.Fatalf("rows=%d", n)
	}
	if BotMemoryPrompt(nil) != "" {
		t.Fatal("empty memory injected")
	}
}
