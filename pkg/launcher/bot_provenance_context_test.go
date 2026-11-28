package launcher

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"google.golang.org/adk/model"
	"google.golang.org/adk/session"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
	"google.golang.org/genai"
)

func provenanceEvent(cursor int64, at time.Time, part *genai.Part) store.BotTranscriptEvent {
	return store.BotTranscriptEvent{Cursor: cursor, Event: &session.Event{ID: uuid.NewString(), Timestamp: at, LLMResponse: model.LLMResponse{Content: &genai.Content{Role: "model", Parts: []*genai.Part{part}}}}}
}

type provenanceSessionStore struct {
	store.SessionStore
	events    []store.BotTranscriptEvent
	sessionID uuid.UUID
	reads     int
}

func (s *provenanceSessionStore) ReadBotTranscript(_ context.Context, id uuid.UUID, after int64, limit int) ([]store.BotTranscriptEvent, error) {
	if id != s.sessionID {
		return nil, store.ErrBotForbidden
	}
	s.reads++
	var out []store.BotTranscriptEvent
	for _, e := range s.events {
		if e.Cursor > after && len(out) < limit {
			out = append(out, e)
		}
	}
	return out, nil
}

type provenanceRunRepo struct {
	botFactoryTestRepo
	previous store.BotRun
	foreign  bool
	rows     []store.BotRun
}

func (r *provenanceRunRepo) ListRuns(_ context.Context, id store.BotID, _ store.BotPage) ([]store.BotRun, error) {
	if id != r.fixture.req.Bot.ID {
		return nil, store.ErrBotForbidden
	}
	if r.rows != nil {
		return r.rows, nil
	}
	return []store.BotRun{r.previous}, nil
}
func (r *provenanceRunRepo) GetRun(ctx context.Context, id store.BotID, runID store.RunID) (*store.BotRun, error) {
	if runID == r.previous.ID {
		v := r.previous
		if r.foreign {
			v.BotID = uuid.New()
		}
		return &v, nil
	}
	return r.botFactoryTestRepo.GetRun(ctx, id, runID)
}

// Execute a real tool and retain its call/result events, then verify that the
// next employee's bootstrap does not hydrate them. Historical evidence requires
// explicit retrieval; the prompt must not infer a method from a summary or URL.
func TestBotPriorRetrievalProvenanceFollowup(t *testing.T) {
	for _, interactive := range []bool{false, true} {
		t.Run(map[bool]string{false: "autonomous", true: "interactive"}[interactive], func(t *testing.T) {
			f := newBotFactoryFixture(t)
			previous := store.BotRun{ID: uuid.New(), BotID: f.req.Bot.ID, SessionID: uuid.New(), CreatedAt: time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC), Status: store.RunFailed}
			repo := &provenanceRunRepo{botFactoryTestRepo: botFactoryTestRepo{fixture: f}, previous: previous}
			f.services.PersonalBots = repo
			fetch, err := functiontool.New(functiontool.Config{Name: "web_fetch", Description: "Controlled fetch"}, func(_ tool.Context, _ struct {
				URL     string         `json:"url"`
				Headers map[string]any `json:"headers"`
			}) (map[string]any, error) {
				return map[string]any{"error": "403 forbidden secret-result", "url": "https://news.example/story?api_key=secret-query", "content": "private source body", "authorization": "Bearer secret-header"}, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			args := map[string]any{"url": "https://user:password@news.example/story?api_key=secret-query", "headers": map[string]any{"Authorization": "Bearer secret-header"}}
			result, err := fetch.(botRunnableTool).Run(botFactoryHandoffContext{base: f.ctx, session: previous.SessionID.String(), functionCallID: "fetch-1"}, args)
			if err != nil {
				t.Fatal(err)
			}
			reader := &provenanceSessionStore{sessionID: previous.SessionID, events: []store.BotTranscriptEvent{
				provenanceEvent(1, previous.CreatedAt, &genai.Part{FunctionCall: &genai.FunctionCall{ID: "fetch-1", Name: "web_fetch", Args: args}}),
				provenanceEvent(2, previous.CreatedAt.Add(time.Second), &genai.Part{FunctionResponse: &genai.FunctionResponse{ID: "fetch-1", Name: "web_fetch", Response: result}}),
			}}
			f.services.PersonalSessions = reader
			f.req.Instructions = "How did you research the previous answer?"
			if interactive {
				f.req.Run.ExecutionMode = execution.BotExecutionModeChatInteractive
				f.req.Run.CallerCeiling = &execution.BotCallerCeiling{Principal: f.req.Principal, ExpiresAt: time.Now().Add(time.Hour)}
				f.req.Authorization = interactiveFactoryAuthorization{}
				f.req.Policy = nil
				f.cfg.Prepare = func(context.Context, bot.RunRequest, *BotChatScope) (*BotChatSafeResources, error) {
					return &BotChatSafeResources{ChatConfig: &config.AppConfig{}, Catalog: &ChatCatalogResources{Pool: &catalogTestPool{}}, WorkspaceDir: "/isolated", VerifyIsolation: func(context.Context) error { return nil }, Cleanup: func() {}}, nil
				}
			}
			resources, err := f.constructWorker(t)
			if err != nil {
				t.Fatal(err)
			}
			defer resources.Cleanup()
			data, _ := json.Marshal(resources.Agent.SystemPrompt.Build())
			prompt := string(data)
			for _, want := range []string{"MEMORY.md", "Read referenced files explicitly", "Do not infer a research tool"} {
				if !strings.Contains(prompt, want) {
					t.Errorf("missing %q in employee prompt", want)
				}
			}
			for _, secret := range []string{previous.ID.String(), "2026-11-13", "https://news.example/story", "secret-query", "secret-header", "secret-result", "private source body", "password", "api_key"} {
				if strings.Contains(prompt, secret) {
					t.Errorf("leaked %q", secret)
				}
			}
			if reader.reads != 0 {
				t.Error("bootstrap hydrated prior transcript")
			}
		})
	}
}

func TestBotPriorProvenanceScopeAndBounds(t *testing.T) {
	for _, mode := range []string{"foreign-run", "foreign-list", "home", "revoked", "bounded", "url-only"} {
		t.Run(mode, func(t *testing.T) {
			f := newBotFactoryFixture(t)
			previous := store.BotRun{ID: uuid.New(), BotID: f.req.Bot.ID, SessionID: uuid.New(), CreatedAt: time.Now().Add(-time.Hour), Status: store.RunFailed}
			repo := &provenanceRunRepo{botFactoryTestRepo: botFactoryTestRepo{fixture: f}, previous: previous}
			reader := &provenanceSessionStore{sessionID: previous.SessionID}
			f.services.PersonalBots = repo
			f.services.PersonalSessions = reader
			switch mode {
			case "foreign-run":
				repo.foreign = true
			case "foreign-list":
				repo.previous.BotID = uuid.New()
			case "home":
				f.req.Bot.HomeSessionID = previous.SessionID
			case "bounded":
				for i := int64(1); i <= 301; i++ {
					reader.events = append(reader.events, provenanceEvent(i, previous.CreatedAt, &genai.Part{FunctionCall: &genai.FunctionCall{ID: "same-call", Name: "web_fetch", Args: map[string]any{"url": "https://news.example/story"}}}))
				}
			case "url-only":
				reader.events = []store.BotTranscriptEvent{provenanceEvent(1, previous.CreatedAt, &genai.Part{Text: "https://news.example/story web_fetch"})}
			}
			resources, err := f.constructWorker(t)
			if err != nil {
				t.Fatal(err)
			}
			defer resources.Cleanup()
			prompt := resources.Agent.SystemPrompt.Build()
			switch mode {
			case "home", "foreign-run", "foreign-list", "bounded":
				if reader.reads != 0 {
					t.Fatal("read Home as run transcript")
				}
			case "url-only":
				if strings.Contains(prompt, "Recorded prior Bot tool provenance") {
					t.Fatal("inferred tool from text URL")
				}
			case "revoked":
				entry := resources.Agent.ToolIndex.GetToolEntry("read_file")
				if entry == nil {
					t.Fatal("no shared read_file tool")
				}
				f.req.Run.Lease.Fence++
				before := reader.reads
				ctx := sharedBotToolContext{proposalTestContext{botFactoryToolContext: botFactoryToolContext{base: f.ctx}, sid: f.req.Run.SessionID.String()}}
				if _, err := entry.Tool.(botRunnableTool).Run(ctx, map[string]any{"path": "outputs/report.md"}); !errors.Is(err, store.ErrBotConflict) || reader.reads != before {
					t.Fatalf("revoked lease read history: err=%v reads=%d before=%d", err, reader.reads, before)
				}
			}
		})
	}
}
