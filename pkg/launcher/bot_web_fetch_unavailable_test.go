package launcher

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"iter"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/api"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/credentials"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"google.golang.org/adk/model"
	"google.golang.org/adk/session"
	"google.golang.org/adk/tool"
	"google.golang.org/genai"
)

func TestBotWebFetchUnavailableThenAlternateSource(t *testing.T) {
	f := newBotFactoryFixture(t)
	base := execution.WithBotRunIdentity(f.ctx, f.req.Policy.Identity())
	base, _ = bot.WithToolOutcomes(base)
	ctx := proposalTestContext{botFactoryToolContext: botFactoryToolContext{base: base}, sid: f.req.Run.SessionID.String()}
	for _, status := range []float64{429, 401, 429, 200} {
		result := map[string]any{"status": status, "final_url": "https://example.test/news", "content": "response body"}
		recordBuiltinToolObservation(ctx, ctx.SessionID(), "web_fetch", nil, result)
		outcome, ok := bot.TakeToolOutcome(ctx, ctx.FunctionCallID())
		if !ok || !outcome.Verified || len(outcome.Unverified) != 0 || len(outcome.Changed) != 0 || len(outcome.Unchanged) != 1 {
			t.Fatalf("known HTTP response treated as uncertain effect: status=%v outcome=%+v present=%v", status, outcome, ok)
		}
		if status != 200 && !strings.Contains(outcome.Unchanged[0].Summary, "unavailable") {
			t.Fatalf("failed read lost source limitation: %+v", outcome)
		}
	}
}

func TestBotBuiltinSourceAccessFacts(t *testing.T) {
	for _, tc := range []struct {
		name, tool, method string
		result             map[string]any
		status             store.BotSourceAccessStatus
		httpStatus         int
		verified           bool
	}{
		{"footer-only-200", "web_fetch", "", map[string]any{"status": float64(200), "final_url": "https://example.test/final", "content": "Privacy policy | Copyright", "truncated": true}, store.BotSourceAccessReturned, 200, true},
		{"rate-limited", "web_fetch", "", map[string]any{"status": float64(429), "final_url": "https://example.test/final", "content": "Too many requests"}, store.BotSourceAccessFailed, 429, true},
		{"error", "web_fetch", "", map[string]any{"error": "fetch failed: access blocked"}, store.BotSourceAccessFailed, 0, false},
		{"http-get", "http_request", "get", map[string]any{"status_code": float64(200), "body": "Footer only", "final_url": "https://example.test/final"}, store.BotSourceAccessReturned, 200, true},
		{"http-head", "http_request", "HEAD", map[string]any{"status_code": float64(204), "body": ""}, store.BotSourceAccessReturned, 204, true},
		{"http-rate-limited", "http_request", "GET", map[string]any{"status_code": float64(429), "body": "Blocked"}, store.BotSourceAccessFailed, 429, false},
		{"http-error", "http_request", "HEAD", map[string]any{"error": "request timed out"}, store.BotSourceAccessFailed, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base := execution.WithBotRunIdentity(context.Background(), execution.BotRunIdentity{BotID: uuid.New(), RunID: uuid.New(), ActorID: uuid.NewString()})
			base, _ = bot.WithToolOutcomes(base)
			ctx := proposalTestContext{botFactoryToolContext: botFactoryToolContext{base: base}, sid: "source-session"}
			args := map[string]any{"url": "https://example.test/original", "method": tc.method, "call_id": "forged"}
			start := time.Now().UTC()
			recordBuiltinToolObservation(ctx, ctx.SessionID(), tc.tool, args, tc.result)
			outcome, ok := bot.TakeToolOutcome(ctx, ctx.FunctionCallID())
			if !ok || len(outcome.SourceAccess) != 1 {
				t.Fatalf("missing typed source access: %+v, present=%v", outcome, ok)
			}
			fact := outcome.SourceAccess[0]
			raw, err := json.Marshal(tc.result)
			if err != nil {
				t.Fatal(err)
			}
			digest := sha256.Sum256(raw)
			finalURL, _ := tc.result["final_url"].(string)
			truncated, _ := tc.result["truncated"].(bool)
			if fact.EvidenceRef != "capture:"+ctx.FunctionCallID() || fact.CallID != ctx.FunctionCallID() || fact.OriginalURL != args["url"] || fact.FinalURL != finalURL || fact.Status != tc.status || fact.HTTPStatus != tc.httpStatus || fact.Truncated != truncated || fact.BodyDigest != hex.EncodeToString(digest[:]) {
				t.Fatalf("incorrect source access: %+v", fact)
			}
			if fact.ObservedAt.Location() != time.UTC || fact.ObservedAt.Before(start) || fact.ObservedAt.After(time.Now().UTC()) {
				t.Fatalf("invalid observed timestamp: %v", fact.ObservedAt)
			}
			if outcome.Verified != tc.verified || len(outcome.Changed) != 0 || len(outcome.Artifacts) != 0 || (len(outcome.Unchanged) == 1) != tc.verified {
				t.Fatalf("changed legacy receipts: %+v", outcome)
			}
			if _, ok := bot.TakeToolOutcome(ctx, ctx.FunctionCallID()); ok {
				t.Fatal("source fact consumed twice")
			}
		})
	}
}

func TestBotBuiltinSourceAccessRejectsUnboundAndNonRead(t *testing.T) {
	identity := execution.BotRunIdentity{BotID: uuid.New(), RunID: uuid.New(), ActorID: uuid.NewString()}
	for _, tc := range []struct {
		name, tool, method string
		bound              bool
		result             map[string]any
	}{
		{"write", "http_request", "POST", true, map[string]any{"status_code": float64(200)}},
		{"unknown-tool", "remote_web_fetch", "GET", true, map[string]any{"status": float64(200)}},
		{"unbound", "web_fetch", "GET", false, map[string]any{"status": float64(200), "final_url": "https://example.test/final", "content": "footer"}},
		{"fractional-status", "web_fetch", "GET", true, map[string]any{"status": float64(200.5)}},
		{"missing-status", "web_fetch", "GET", true, map[string]any{"content": "footer"}},
		{"unserializable", "web_fetch", "GET", true, map[string]any{"status": float64(200), "invalid": make(chan int)}},
		{"nil-result", "web_fetch", "GET", true, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base := context.Background()
			if tc.bound {
				base = execution.WithBotRunIdentity(base, identity)
			}
			base, _ = bot.WithToolOutcomes(base)
			ctx := proposalTestContext{botFactoryToolContext: botFactoryToolContext{base: base}, sid: "source-session"}
			recordBuiltinToolObservation(ctx, ctx.SessionID(), tc.tool, map[string]any{"url": "https://example.test/original", "method": tc.method}, tc.result)
			if outcome, ok := bot.TakeToolOutcome(ctx, ctx.FunctionCallID()); ok {
				t.Fatalf("untrusted access minted outcome: %+v", outcome)
			}
		})
	}
}

func TestBotBuiltinSourceAccessRedactedDigest(t *testing.T) {
	const secret = "source-\"secret\n123456"
	for _, name := range []string{"web_fetch", "http_request"} {
		t.Run(name, func(t *testing.T) {
			redactor := credentials.NewRedactor()
			redactor.AddSecret("source", secret)
			base := execution.WithBotRunIdentity(context.Background(), execution.BotRunIdentity{BotID: uuid.New(), RunID: uuid.New(), ActorID: uuid.NewString()})
			base = credentials.WithRedactor(base, redactor)
			base, _ = bot.WithToolOutcomes(base)
			ctx := botFactoryHandoffContext{base: base, session: "source-session", functionCallID: "trusted-source"}
			result := map[string]any{"status": float64(429), "status_code": float64(429), "content": "blocked " + secret, "headers": map[string]any{"trace": secret}, "nested": []any{secret}}
			recordBuiltinToolObservation(ctx, ctx.SessionID(), name, map[string]any{"url": "https://example.test/source", "method": "GET"}, result)
			outcome, ok := bot.TakeToolOutcome(ctx, ctx.FunctionCallID())
			if !ok || len(outcome.SourceAccess) != 1 {
				t.Fatalf("missing trusted source: %+v", outcome)
			}
			// This is the exact map transform in ChatAgent.AfterToolCallback,
			// not Redact(json.Marshal(result)), which has different escaping.
			journal, err := json.Marshal(redactor.RedactMap(result))
			if err != nil {
				t.Fatal(err)
			}
			digest := sha256.Sum256(journal)
			fact := outcome.SourceAccess[0]
			if fact.BodyDigest != hex.EncodeToString(digest[:]) || fact.CallID != ctx.FunctionCallID() || fact.EvidenceRef != "capture:"+ctx.FunctionCallID() {
				t.Fatalf("source fact does not bind sanitized capture: %+v", fact)
			}
			if strings.Contains(string(journal), secret) || result["content"] != "blocked "+secret {
				t.Fatal("redaction leaked a secret or mutated the transport response")
			}
		})
	}
}

// sourceRedactionRepo retains real validated work transitions in an isolated
// fixture; the production service still binds facts to actual session captures.
type sourceRedactionRepo struct {
	botFactoryTestRepo
	state *store.BotWorkState
}

func (r *sourceRedactionRepo) ListRuns(context.Context, store.BotID, store.BotPage) ([]store.BotRun, error) {
	return nil, nil
}

func (r *sourceRedactionRepo) LoadWork(context.Context, store.BotLease) (*store.BotWorkState, error) {
	return r.state.Clone(), nil
}
func (r *sourceRedactionRepo) SaveWork(_ context.Context, q store.BotWorkUpdate) (*store.BotWorkState, error) {
	if q.ExpectedRevision != r.state.Revision {
		return nil, store.ErrBotConflict
	}
	if err := store.ValidateBotWorkUpdate(r.state, &q.State); err != nil {
		return nil, err
	}
	r.state = q.State.Clone()
	r.state.Revision++
	return r.state.Clone(), nil
}

type sourceRedactionTranscript struct {
	sessions session.Service
	user     string
}

func (r sourceRedactionTranscript) ReadBotTranscript(ctx context.Context, id uuid.UUID, cursor int64, limit int) ([]store.BotTranscriptEvent, error) {
	got, err := r.sessions.Get(ctx, &session.GetRequest{AppName: "astonish", UserID: r.user, SessionID: id.String()})
	if err != nil {
		return nil, err
	}
	var rows []store.BotTranscriptEvent
	var n int64
	for e := range got.Session.Events().All() {
		n++
		if n > cursor && len(rows) < limit {
			rows = append(rows, store.BotTranscriptEvent{Cursor: n, Event: e})
		}
	}
	return rows, nil
}

type sourceRedactionModel struct {
	t      *testing.T
	calls  int
	tool   string
	secret string
	saved  *sourceRedactionRepo
}

func (*sourceRedactionModel) Name() string { return "source-redaction" }
func (m *sourceRedactionModel) GenerateContent(_ context.Context, q *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		m.calls++
		if m.calls > 1 {
			if len(m.saved.state.AgentWork.SourceAccess) != m.calls-1 {
				m.t.Fatalf("source not bound before next provider request: %+v", m.saved.state.AgentWork)
			}
			for _, content := range q.Contents {
				for _, p := range content.Parts {
					if p.FunctionResponse == nil {
						continue
					}
					raw, _ := json.Marshal(p.FunctionResponse.Response)
					if strings.Contains(string(raw), m.secret) {
						m.t.Fatal("secret reached provider")
					}
				}
			}
		}
		content := genai.NewContentFromText("Corrected answer: the alternate source reports clear weather; the first source was unavailable.", "model")
		if m.calls <= 2 {
			path := "blocked"
			if m.calls == 2 {
				path = "alternate"
			}
			args := map[string]any{"url": "http://93.184.216.34/" + path}
			if m.tool == "web_fetch" {
				args["mode"] = "raw"
			} else {
				args["method"] = "GET"
			}
			content = &genai.Content{Role: "model", Parts: []*genai.Part{{FunctionCall: &genai.FunctionCall{ID: path, Name: m.tool, Args: args}}}}
		}
		yield(&model.LLMResponse{Content: content, TurnComplete: true}, nil)
	}
}

func TestBotSourceRedactionThroughBothFactories(t *testing.T) {
	const secret = "source-secret-123456"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/blocked" {
			w.WriteHeader(http.StatusTooManyRequests)
		}
		_, _ = w.Write([]byte(`{"weather":"clear","trace":"` + secret + `"}`))
	}))
	defer server.Close()
	old := http.DefaultTransport
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", server.Listener.Addr().String())
	}}
	http.DefaultTransport = transport
	defer func() { http.DefaultTransport = old; transport.CloseIdleConnections() }()
	for _, interactive := range []bool{false, true} {
		for _, name := range []string{"web_fetch"} {
			t.Run(map[bool]string{false: "autonomous", true: "interactive"}[interactive]+"/"+name, func(t *testing.T) {
				f := newBotFactoryFixture(t)
				w := store.NewBotAgentWorkState(store.BotWorkContract{BotID: f.req.Bot.ID, RunID: f.req.Run.ID, Version: f.req.Run.Version, Instructions: f.req.Instructions})
				f.req.Work = w.Clone()
				repo := &sourceRedactionRepo{botFactoryTestRepo: botFactoryTestRepo{fixture: f}, state: w}
				f.services.Bots, f.services.PersonalBots = repo, repo
				node := &fileOutcomeNode{}
				llm := &sourceRedactionModel{t: t, tool: name, secret: secret, saved: repo}
				f.cfg.WrapModel = func(context.Context, model.LLM, execution.RunBudget) (model.LLM, error) { return llm, nil }
				var err error
				f.req.Policy, err = execution.NewRunPolicy(execution.RunPolicySpec{Identity: f.req.Policy.Identity(), Level: execution.AutonomySupervised, AllowedTools: []string{name}})
				if err != nil {
					t.Fatal(err)
				}
				if interactive {
					f.req.Run.ExecutionMode = execution.BotExecutionModeChatInteractive
					f.req.Run.CallerCeiling = &execution.BotCallerCeiling{Principal: f.req.Principal, ExpiresAt: time.Now().Add(time.Hour)}
					f.req.Authorization = interactiveFactoryAuthorization{}
					f.req.Policy = nil
					f.cfg.Prepare = func(context.Context, bot.RunRequest, *BotChatScope) (*BotChatSafeResources, error) {
						return &BotChatSafeResources{ChatConfig: &config.AppConfig{}, Catalog: &ChatCatalogResources{Pool: &interactiveOutcomePool{node: node}}, WorkspaceDir: "/isolated", VerifyIsolation: func(context.Context) error { return nil }, Cleanup: func() {}}, nil
					}
				} else {
					catalogue, err := BotCatalogueTools(node, f.req.Run.SessionID.String())
					if err != nil {
						t.Fatal(err)
					}
					var selected []tool.Tool
					for _, candidate := range catalogue {
						if candidate.Name() == name {
							selected = append(selected, candidate)
						}
					}
					f.cfg.Prepare = func(context.Context, bot.RunRequest, *BotChatScope) (*BotChatSafeResources, error) {
						return &BotChatSafeResources{Tools: selected, WorkspaceDir: "/isolated", PolicyRuntime: agent.BotPolicyRuntime{Guard: botFactoryTestGuard{}, Classifier: botFactoryTestClassifier{}}, VerifyIsolation: func(context.Context) error { return nil }, Cleanup: func() {}}, nil
					}
				}
				factory, err := NewBotChatAgentFactory(f.cfg)
				if err != nil {
					t.Fatal(err)
				}
				reader := sourceRedactionTranscript{sessions: f.sessions, user: f.req.Bot.OwnerID.String()}
				adapter, err := api.NewBotChatRuntimeAdapter(func(ctx context.Context, req bot.RunRequest) (*api.BotChatResources, error) {
					resources, err := factory(ctx, req)
					if err == nil {
						resources.Agent.Redactor.AddSecret("source", secret)
						resources.EvidenceReader = reader
					}
					return resources, err
				})
				if err != nil {
					t.Fatal(err)
				}
				result, err := adapter.Start(f.ctx, f.req, func(context.Context, bot.RuntimeEvent) error { return nil })
				if err != nil || result.Status != store.RunSucceeded || !strings.Contains(result.Report.Summary, "Corrected answer:") || len(result.Report.Unverified) != 0 {
					t.Fatalf("corrected final did not complete: result=%+v err=%v", result, err)
				}
				if llm.calls != 3 || node.calls != 2 || repo.state.Phase != store.BotWorkFinished || len(repo.state.AgentWork.SourceAccess) != 2 {
					t.Fatalf("unexpected execution counts/state: model=%d node=%d work=%+v", llm.calls, node.calls, repo.state)
				}
				rows, err := reader.ReadBotTranscript(f.ctx, f.req.Run.SessionID, 0, 100)
				if err != nil {
					t.Fatal(err)
				}
				captures := map[string]string{}
				for _, row := range rows {
					if row.Event.Content == nil {
						continue
					}
					for _, part := range row.Event.Content.Parts {
						if part.FunctionResponse == nil {
							continue
						}
						raw, _ := json.Marshal(part.FunctionResponse.Response)
						if strings.Contains(string(raw), secret) || !strings.Contains(string(raw), "[REDACTED]") {
							t.Fatalf("unsafe/missing sanitized capture: %s", raw)
						}
						digest := sha256.Sum256(raw)
						captures[part.FunctionResponse.ID] = hex.EncodeToString(digest[:])
					}
				}
				for _, fact := range repo.state.AgentWork.SourceAccess {
					want := store.BotSourceAccessReturned
					if fact.CallID == "blocked" {
						want = store.BotSourceAccessFailed
					}
					if fact.Status != want || fact.EvidenceRef != "capture:"+fact.CallID || fact.BodyDigest != captures[fact.CallID] {
						t.Fatalf("fact not bound to journal: %+v captures=%v", fact, captures)
					}
				}
			})
		}
	}
}

func TestBotSourceAccessLedgerSnapshot(t *testing.T) {
	ctx, _ := bot.WithToolOutcomes(context.Background())
	facts := []store.BotSourceAccessFact{{CallID: "original"}}
	bot.RecordToolOutcome(ctx, "call", bot.ToolOutcome{SourceAccess: facts})
	facts[0].CallID = "mutated"
	out, ok := bot.TakeToolOutcome(ctx, "call")
	if !ok || len(out.SourceAccess) != 1 || out.SourceAccess[0].CallID != "original" {
		t.Fatalf("source facts not snapshotted: %+v", out)
	}
}

func TestBotWebFetchUnknownResponseRemainsUnverified(t *testing.T) {
	f := newBotFactoryFixture(t)
	base := execution.WithBotRunIdentity(f.ctx, f.req.Policy.Identity())
	base, _ = bot.WithToolOutcomes(base)
	ctx := proposalTestContext{botFactoryToolContext: botFactoryToolContext{base: base}, sid: f.req.Run.SessionID.String()}
	for _, result := range []map[string]any{
		{"status": float64(0), "final_url": "https://example.test", "content": ""},
		{"status": float64(429), "content": "unavailable"},
		{"status": float64(429), "final_url": "https://example.test"},
		{"status": float64(429.5), "final_url": "https://example.test", "content": "unavailable"},
		{"status": float64(429), "final_url": "https://example.test", "content": "unavailable", "error": "transport failure"},
	} {
		recordBuiltinToolObservation(ctx, ctx.SessionID(), "web_fetch", nil, result)
		if outcome, ok := bot.TakeToolOutcome(ctx, ctx.FunctionCallID()); ok {
			t.Fatalf("unknown response minted observation: %+v => %+v", result, outcome)
		}
	}
}
