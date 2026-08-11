package tui

import (
	"testing"

	"github.com/SAP/astonish/pkg/tui/backend"
)

func TestWebSearchPicker_SlashCommandAppears(t *testing.T) {
	// Without the websearch extra command, /websearch should not appear.
	without := filterSlashCommands("web")
	for _, cmd := range without {
		if cmd.Name == "websearch" {
			t.Fatal("expected /websearch not in base commands")
		}
	}

	// With the extra command, it should match.
	withCmd := filterSlashCommands("web", webSearchSlashCommand)
	found := false
	for _, cmd := range withCmd {
		if cmd.Name == "websearch" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected /websearch in filtered commands with extra, got: %v", withCmd)
	}
}

func TestWebSearchPicker_RebuildItemsList(t *testing.T) {
	state := webSearchPickerState{
		step: "list",
		providers: []backend.WebSearchProvider{
			{ID: "tavily", DisplayName: "Tavily", Active: true},
			{ID: "brave-search", DisplayName: "Brave Search"},
			{ID: "perplexity", DisplayName: "Perplexity / Sonar"},
		},
	}
	state.rebuildItems()

	// Should have all providers + "(None — disable web search)"
	if len(state.items) != 4 {
		t.Fatalf("expected 4 items, got %d: %v", len(state.items), state.items)
	}

	// Filter to "tav"
	state.filter = "tav"
	state.rebuildItems()
	if len(state.items) != 1 || state.items[0] != "Tavily" {
		t.Errorf("expected [Tavily] for filter 'tav', got: %v", state.items)
	}
}

func TestWebSearchPicker_RebuildItemsPerplexityModels(t *testing.T) {
	state := webSearchPickerState{
		step:             "perplexity-model",
		perplexityModels: []string{"sonar-pro", "sonar-small", "sonar-medium"},
	}
	state.rebuildItems()
	if len(state.items) != 3 {
		t.Fatalf("expected 3 items, got %d", len(state.items))
	}

	state.filter = "pro"
	state.rebuildItems()
	if len(state.items) != 1 || state.items[0] != "sonar-pro" {
		t.Errorf("expected [sonar-pro], got: %v", state.items)
	}
}
