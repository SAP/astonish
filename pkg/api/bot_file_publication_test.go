package api

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
	"google.golang.org/adk/model"
	"google.golang.org/adk/session"
	"google.golang.org/genai"
)

type filePublicationRepo struct {
	*botSessionFixtureStore
	publication *store.BotFilePublication
}

func (s *filePublicationRepo) ListApprovals(context.Context, store.BotID, store.BotPage) ([]store.BotApproval, error) {
	return nil, nil
}
func (s *filePublicationRepo) ReadFilePublication(_ context.Context, b store.BotID, r store.RunID, seq int64) (*store.BotFilePublication, error) {
	if b != s.publication.BotID || r != s.publication.RunID || seq != s.publication.ReportSequence {
		return nil, store.ErrBotNotFound
	}
	return s.publication, nil
}

type filePublicationSessions struct {
	*botSessionMetaFixture
	mu  sync.Mutex
	raw [][]byte
}

func (s *filePublicationSessions) snapshot() []*session.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	var events []*session.Event
	for _, raw := range s.raw {
		var e session.Event
		if json.Unmarshal(raw, &e) != nil {
			panic("invalid test event")
		}
		events = append(events, &e)
	}
	return events
}
func (s *filePublicationSessions) ReadTranscriptEvents(context.Context, string, string, string) ([]*session.Event, error) {
	return s.snapshot(), nil
}
func (s *filePublicationSessions) ReadBotTranscript(_ context.Context, _ uuid.UUID, after int64, limit int) ([]store.BotTranscriptEvent, error) {
	var rows []store.BotTranscriptEvent
	for i, e := range s.snapshot() {
		if int64(i+1) > after && len(rows) < limit {
			rows = append(rows, store.BotTranscriptEvent{Cursor: int64(i + 1), Event: e})
		}
	}
	return rows, nil
}

// Wire-level HTTP streaming and finite reload exercise the production handlers;
// fixtures represent already committed receipts, not a running model or container.
func TestBotFileFirstPublicationSSE(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			owner, viewer, id, run, sid, home := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
			if scope == store.BotScopePersonal {
				viewer = owner
			}
			p := &store.BotFilePublication{SchemaVersion: store.BotFilePublicationSchemaVersion, BotID: id, RunID: run, HomeSessionID: sid, HomeID: home, ReportSequence: 19, EventID: store.BotPublicationEventID(run, 19), Summary: "The report and editable sources are ready.", Artifacts: []store.BotPublishedArtifact{{ID: uuid.New(), HomeID: home, RelativePath: "reports/answer.md", SHA256: strings.Repeat("a", 64), SizeBytes: 55, MediaType: "text/markdown", Title: "Report", Preview: store.BotFilePreviewText}, {ID: uuid.New(), HomeID: home, RelativePath: "reports/source.zip", SHA256: strings.Repeat("b", 64), SizeBytes: 20 << 20, MediaType: "application/zip", Title: "Sources", Preview: store.BotFilePreviewDownload}}}
			repo := &filePublicationRepo{botSessionFixtureStore: &botSessionFixtureStore{b: store.Bot{ID: id, OwnerID: owner, Scope: scope, HomeSessionID: sid}}, publication: p}
			ss := &filePublicationSessions{botSessionMetaFixture: &botSessionMetaFixture{meta: &store.SessionMeta{ID: sid.String(), UserID: owner.String(), BotID: id.String(), Purpose: store.SessionPurposeBotHome, Retention: store.SessionRetentionBot}}}
			svc := &store.Services{}
			if scope == store.BotScopePersonal {
				svc.PersonalBots = repo
				svc.PersonalSessions = ss
			} else {
				svc.Bots = repo
				svc.Sessions = ss
			}
			router := mux.NewRouter()
			registerBotRoutes(router)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				ctx, err := execution.WithPrincipal(r.Context(), execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: viewer.String(), OrgSlug: "org", TeamSlug: "team", Authenticated: true})
				if err != nil {
					panic(err)
				}
				ctx = store.WithTenantContext(ctx, &store.TenantContext{UserID: viewer.String(), OrgSlug: "org", TeamSlug: "team"})
				ctx = store.WithServices(ctx, svc)
				router.ServeHTTP(w, r.WithContext(ctx))
			}))
			defer server.Close()
			client := server.Client()
			client.Timeout = 5 * time.Second
			base := server.URL + "/api/studio/bots/" + id.String() + "/sessions/" + sid.String()
			get := func(suffix, cursor string) *http.Response {
				t.Helper()
				req, _ := http.NewRequest("GET", base+suffix+"?scope="+string(scope), nil)
				req.Header.Set("Last-Event-ID", cursor)
				res, err := client.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				if res.StatusCode != 200 {
					res.Body.Close()
					t.Fatalf("HTTP %d", res.StatusCode)
				}
				return res
			}
			live := get("/events", "")
			defer live.Body.Close()
			rd := bufio.NewReader(live.Body)
			if f := readBotSSE(t, rd); f.event != botReplayComplete {
				t.Fatal(f)
			}
			raw, _ := json.Marshal(p)
			var envelope map[string]any
			json.Unmarshal(raw, &envelope)
			envelope["private_receipt"] = "PRIVATE_SENTINEL"
			event := &session.Event{ID: p.EventID, InvocationID: "bot-report:" + run.String(), Author: "assistant", Timestamp: time.Now().UTC(), LLMResponse: model.LLMResponse{Content: genai.NewContentFromText(p.Summary, "model")}, Actions: session.EventActions{StateDelta: map[string]any{store.BotFilePublicationKey: envelope, "private_authority": "PRIVATE_SENTINEL"}}}
			raw, _ = json.Marshal(event)
			ss.mu.Lock()
			ss.raw = append(ss.raw, raw, raw)
			ss.mu.Unlock()
			first := readBotSSE(t, rd)
			if first.event != store.BotFilePublicationKey || strings.Contains(first.data, "PRIVATE_SENTINEL") || !strings.Contains(first.data, p.Artifacts[1].ID.String()) {
				t.Fatal(first)
			}
			live.Body.Close()
			// Both copies reduce to exactly one canonical publication in a fresh replay.
			replay := get("/events", "")
			rr := bufio.NewReader(replay.Body)
			again := readBotSSE(t, rr)
			if first.data != again.data || first.id != again.id {
				t.Fatal("live/reload envelope changed")
			}
			duplicate := readBotSSE(t, rr)
			if duplicate.event != botCursorEvent {
				t.Fatal("duplicate publication became visible", duplicate)
			}
			marker := readBotSSE(t, rr)
			if marker.event != botReplayComplete {
				t.Fatal("duplicate publication", marker)
			}
			replay.Body.Close()
			resumed := get("/events", marker.id)
			if f := readBotSSE(t, bufio.NewReader(resumed.Body)); f.event != botReplayComplete {
				t.Fatal("cursor redelivered", f)
			}
			resumed.Body.Close()
			history := get("/stream", "")
			hr := bufio.NewReader(history.Body)
			if f := readBotSSE(t, hr); f.data != first.data {
				t.Fatal("finite reload changed", f)
			}
			if f := readBotSSE(t, hr); f.event != botCursorEvent {
				t.Fatal("finite duplicate became visible", f)
			}
			if f := readBotSSE(t, hr); f.event != botTranscriptDone {
				t.Fatal("finite duplicate", f)
			}
			history.Body.Close()
			if getChatRunnerRegistry().Get(sid.String()) != nil {
				t.Fatal("read started model")
			}
		})
	}
}

func TestBotFilePublicationBindingAndAllowlist(t *testing.T) {
	b, r, s, h := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	p := store.BotFilePublication{SchemaVersion: 1, BotID: b, RunID: r, HomeSessionID: s, HomeID: h, ReportSequence: 1, EventID: store.BotPublicationEventID(r, 1), Summary: "Ready", Artifacts: []store.BotPublishedArtifact{{ID: uuid.New(), HomeID: h, RelativePath: "answer.md", SHA256: strings.Repeat("a", 64), Preview: store.BotFilePreviewText}}}
	event := &session.Event{ID: p.EventID, InvocationID: "bot-report:" + r.String(), Author: "assistant", LLMResponse: model.LLMResponse{Content: genai.NewContentFromText(p.Summary, "model")}, Actions: session.EventActions{StateDelta: map[string]any{store.BotFilePublicationKey: p}}}
	for _, mutate := range []func(*store.BotFilePublication){func(p *store.BotFilePublication) { p.BotID = uuid.New() }, func(p *store.BotFilePublication) { p.RunID = uuid.New() }, func(p *store.BotFilePublication) { p.HomeSessionID = uuid.New() }, func(p *store.BotFilePublication) { p.HomeID = uuid.New() }, func(p *store.BotFilePublication) { p.ReportSequence++ }, func(p *store.BotFilePublication) { p.Artifacts = append(p.Artifacts, p.Artifacts[0]) }} {
		changed := p
		mutate(&changed)
		event.Actions.StateDelta[store.BotFilePublicationKey] = changed
		if _, err := marshalBotTranscriptEvent(event, b, s); err == nil {
			t.Fatal("mismatched binding accepted")
		}
	}
	// Structurally valid metadata still needs an exact committed receipt match.
	repo := &filePublicationRepo{publication: &p}
	for name, mutate := range map[string]func(*store.BotFilePublication){
		"artifact": func(p *store.BotFilePublication) { p.Artifacts[0].ID = uuid.New() },
		"digest":   func(p *store.BotFilePublication) { p.Artifacts[0].SHA256 = strings.Repeat("b", 64) },
		"path":     func(p *store.BotFilePublication) { p.Artifacts[0].RelativePath = "other.md" },
		"home":     func(p *store.BotFilePublication) { p.HomeID = uuid.New(); p.Artifacts[0].HomeID = p.HomeID },
		"summary":  func(p *store.BotFilePublication) { p.Summary = "Uncommitted summary" },
	} {
		t.Run(name, func(t *testing.T) {
			changed := p
			changed.Artifacts = append([]store.BotPublishedArtifact(nil), p.Artifacts...)
			mutate(&changed)
			event.Content = genai.NewContentFromText(changed.Summary, "model")
			event.Actions.StateDelta[store.BotFilePublicationKey] = changed
			if _, err := marshalBotTranscriptEvent(event, b, s); err != nil {
				t.Fatalf("fixture must be structurally valid: %v", err)
			}
			if _, err := marshalCommittedBotTranscriptEvent(context.Background(), repo, event, b, s); err == nil {
				t.Fatal("uncommitted delivery accepted")
			}
		})
	}
	event.Content = genai.NewContentFromText(p.Summary, "model")
	event.Actions.StateDelta[store.BotFilePublicationKey] = p
	if _, err := marshalCommittedBotTranscriptEvent(context.Background(), &botSessionFixtureStore{}, event, b, s); err == nil {
		t.Fatal("missing committed publication reader accepted")
	}
	// Ordinary Chat content and its artifact/report markers remain byte-for-byte.
	ordinary := &session.Event{ID: "ordinary", LLMResponse: model.LLMResponse{Content: genai.NewContentFromText("# Long ordinary Chat report", "model")}}
	want, _ := json.Marshal(ordinary)
	got, err := marshalBotTranscriptEvent(ordinary, b, s)
	if err != nil || string(got) != string(want) {
		t.Fatal("ordinary Chat changed")
	}
}
