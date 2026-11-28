package entstore

import (
	"context"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/store"
)

func TestBotPolicyTeamSettingsTransaction(t *testing.T) {
	client := newTeamClient(t)
	settings := &teamSettingsStore{client: client}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := settings.Save(ctx, &store.TeamSettings{WebSearchTool: "perplexity:perplexity_web_search"}); err != nil {
		t.Fatal(err)
	}
	tx, err := client.Tx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	inside := &teamSettingsStore{client: tx.Client()}
	if err := inside.Save(ctx, &store.TeamSettings{WebSearchTool: "tavily:tavily_search"}); err != nil {
		t.Fatal(err)
	}
	routed := botPolicyContext(ctx, teamBotDB{client: tx.Client(), policyOuter: client})
	current, err := settings.Get(routed)
	if err != nil || current.WebSearchTool != "tavily:tavily_search" {
		t.Fatalf("policy read must use same transaction: %+v %v", current, err)
	}
	foreign := newTeamClient(t)
	other := &teamSettingsStore{client: foreign}
	if err := other.Save(ctx, &store.TeamSettings{WebSearchTool: "foreign"}); err != nil {
		t.Fatal(err)
	}
	current, err = other.Get(routed)
	if err != nil || current.WebSearchTool != "foreign" {
		t.Fatalf("transaction crossed team client: %+v %v", current, err)
	}
}
