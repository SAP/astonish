package launcher

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

func TestBotCurrentControlContextPausedConversation(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		for _, mode := range []execution.BotExecutionMode{execution.BotExecutionModeLegacy, execution.BotExecutionModeChatInteractive} {
			t.Run(string(scope)+"/"+string(mode), func(t *testing.T) {
				f, repo := controlContextFixture(t)
				r := botMemoryTargetRepo{repo}
				f.services.Bots, f.services.PersonalBots = r, r
				f.services.Memory = &botPromptMemoryStore{rows: []store.MemorySearchResult{{ID: f.req.Run.ID.String(), Snippet: "Prefers Celsius", Category: "facts", SessionID: f.req.Run.SessionID.String(), Scope: string(scope)}}}
				f.req.Bot.Scope = scope
				f.req.Bot.Status = store.BotPaused
				f.req.Run.Intent = store.BotIntentConversation
				f.req.Run.ExecutionMode = mode
				f.req.Authorization = interactiveFactoryAuthorization{}
				f.req.Run.UserEventID = "current-question"
				owner := store.WithServices(botFactoryOwnerContext(f.ctx, f.req), f.services)
				binding := &botFactoryBinding{owner: owner, req: f.req, verifyIsolation: func(context.Context) error { return nil }}
				req := &model.LLMRequest{Contents: []*genai.Content{genai.NewContentFromText("Earlier: pause independent work", "user"), genai.NewContentFromText("Which temperature units do I prefer?", "user")}}
				before, _ := json.Marshal(req)
				got, err := binding.withCurrentControlContext(f.ctx, req)
				if err != nil {
					t.Fatal(err)
				}
				text := providerSystem(got)
				for _, want := range []string{"Prefers Celsius", `"lifecycle_status":"paused"`, `"run_id":"` + f.req.Run.ID.String() + `"`, `"user_event_id":"current-question"`, `"execution_mode":"` + string(mode) + `"`, `"intent":"conversation"`, "Do not repeat priority_pause", "does not stop this explicit conversation", "use resume_tasks with resume_targets.schedule_ids", "triggers[].schedule_id (canonical scheduler job IDs), never triggers[].id", "its legacy id is a trigger ID, not a resume target", "Do not use legacy resume or activate", "leave all other disabled schedules inert", "require human authorization only when the returned decision requires it"} {
					if !strings.Contains(text, want) {
						t.Errorf("missing %q", want)
					}
				}
				after, _ := json.Marshal(req)
				if string(before) != string(after) {
					t.Fatal("history changed")
				}
			})
		}
	}
}
