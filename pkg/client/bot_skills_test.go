package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/store"
)

func TestBotSkillsReadRoutes(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Query().Get("scope") != "team" || r.Header.Get("Authorization") != "Bearer token" {
			t.Errorf("invalid inspection request %s", r.URL)
		}
		paths = append(paths, r.URL.EscapedPath())
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"skills":[],"revisions":[],"runs":[],"schema_version":99}`))
	}))
	defer server.Close()
	c := NewWithConfig(server.URL)
	c.cached = &Tokens{AccessToken: "token"}
	b := Bots{Client: c, Scope: store.BotScopeTeam}
	ctx := context.Background()
	page := store.BotSkillPage{Limit: 50}
	if _, err := b.Skills(ctx, "bot", page); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Skill(ctx, "bot", "skill"); err != nil {
		t.Fatal(err)
	}
	if _, err := b.SkillRevision(ctx, "bot", "skill", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := b.SkillRevision(ctx, "bot", "skill", 2); err != nil {
		t.Fatal(err)
	}
	if _, err := b.SkillRevisions(ctx, "bot", "skill", page); err != nil {
		t.Fatal(err)
	}
	if _, err := b.SkillRuns(ctx, "bot", "skill", page); err != nil {
		t.Fatal(err)
	}
	if len(paths) != 6 || !strings.HasSuffix(paths[2], "/head") || !strings.HasSuffix(paths[3], "/revisions/2") {
		t.Fatal(paths)
	}
}
func TestBotSkillsErrorAndCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	b := Bots{Client: NewWithConfig(server.URL), Scope: store.BotScopePersonal}
	if _, err := b.Skills(context.Background(), "bot", store.BotSkillPage{Limit: 50}); err == nil {
		t.Fatal("error swallowed")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := b.Skills(ctx, "bot", store.BotSkillPage{Limit: 50}); err == nil {
		t.Fatal("cancel ignored")
	}
}
