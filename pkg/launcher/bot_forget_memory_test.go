package launcher

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/api"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

// Uses the routed personal/team SQLite fixture and production factory/tool
// bindings. The provider is controlled; no paid model or live surface is used.
func testBotForgottenMemoryProvider(t *testing.T, f *botFactoryFixture, r *api.BotChatResources, c basicToolContext, sessionID string) {
	t.Helper()
	const forgotten = "forgotten-zircon-preference"
	a := &store.MemoryAttribution{BotID: f.req.Bot.ID, Source: "original-owner-observation", ObservedAt: time.Now(), Status: store.MemoryKnowledgeInferred, SupersededBy: "routine-operation:" + uuid.NewString()}
	if err := f.services.Memory.Add(f.ctx, store.MemoryEntry{Content: forgotten, Category: "facts", SessionID: sessionID, Attribution: a}); err != nil {
		t.Fatal(err)
	}
	history, err := f.services.Memory.ListBySession(f.ctx, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	id := ""
	for _, row := range history {
		if row.Snippet == forgotten {
			id = row.ID
		}
	}
	if id == "" {
		t.Fatal("tombstone history missing")
	}
	found := basicCall(t, r, c, "memory_search", map[string]any{"query": forgotten, "max_results": 1})
	raw, _ := json.Marshal(found)
	if strings.Contains(string(raw), forgotten) {
		t.Fatalf("forgotten search: %s", raw)
	}
	getChecked := false
	for _, entry := range r.Agent.Tools {
		if entry.Name() == "memory_get" {
			getChecked = true
			_, err := entry.(botRunnableTool).Run(c, map[string]any{"path": id})
			if !errors.Is(err, store.ErrBotNotFound) {
				t.Fatalf("stale path recall must fail closed: %v", err)
			}
		}
	}
	if !getChecked {
		t.Fatal("memory_get missing")
	}
	// A new attempt refreshes memory from the real store; old content must not
	// enter the provider's system context, while unrelated active memory survives.
	fresh, err := f.construct(t)
	if err != nil {
		t.Fatal(err)
	}
	defer fresh.Cleanup()
	request := &model.LLMRequest{Contents: []*genai.Content{genai.NewContentFromText("What facts are current?", "user")}}
	for _, err := range fresh.Agent.LLM.GenerateContent(f.ctx, request, false) {
		if err != nil {
			t.Fatal(err)
		}
	}
	requests := f.resolver.model.requests
	if len(requests) == 0 {
		t.Fatal("provider not exercised")
	}
	system := providerSystem(requests[len(requests)-1])
	if strings.Contains(system, forgotten) || !strings.Contains(system, "quasar replacement") {
		t.Fatalf("incorrect current provider context: %s", system)
	}
	after, err := f.services.Memory.Get(f.ctx, id)
	if err != nil || after == nil || after.Snippet != forgotten || after.Attribution.SupersededBy != a.SupersededBy {
		t.Fatalf("recall destroyed history: %+v %v", after, err)
	}
}
