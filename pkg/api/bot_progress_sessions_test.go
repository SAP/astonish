package api

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/entstore"
	"google.golang.org/adk/model"
	"google.golang.org/adk/session"
	"google.golang.org/genai"
)

type progressCountingSessions struct {
	session.Service
	gets int
	fail error
}

func (s *progressCountingSessions) Get(c context.Context, q *session.GetRequest) (*session.GetResponse, error) {
	s.gets++
	return s.Service.Get(c, q)
}
func (s *progressCountingSessions) AppendEvent(c context.Context, v session.Session, e *session.Event) error {
	if s.fail != nil {
		return s.fail
	}
	return s.Service.AppendEvent(c, v, e)
}

func TestBotProgressStartupFailureObserver(t *testing.T) {
	var events []entstore.BotExecutionTiming
	ctx := entstore.WithBotExecutionObserver(t.Context(), func(v entstore.BotExecutionTiming) { events = append(events, v) })
	a, err := NewBotChatRuntimeAdapter(func(context.Context, bot.RunRequest) (*BotChatResources, error) {
		t.Fatal("invalid request reached factory")
		return nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = a.Start(ctx, bot.RunRequest{}, func(context.Context, bot.RuntimeEvent) error { return nil })
	if err == nil || len(events) != 2 || events[0].Stage != entstore.BotStageOwnerSetup || events[1].Stage != entstore.BotStageStartup {
		t.Fatalf("err=%v events=%+v", err, events)
	}
	for _, e := range events {
		if e.Result != entstore.BotStageFailed || e.Duration < 0 {
			t.Fatalf("event=%+v", e)
		}
	}
}

func TestBotProgressCurrentSessionIncremental(t *testing.T) {
	ctx, p, c, ss, _ := progressContextFixture(t)
	q := &session.GetRequest{AppName: studioChatAppName, UserID: p.req.Bot.OwnerID.String(), SessionID: p.req.Run.SessionID.String()}
	initial, err := ss.Get(ctx, q)
	if err != nil {
		t.Fatal(err)
	}
	counted := &progressCountingSessions{Service: ss}
	resources := &BotChatResources{ProgressDispatch: p, Agent: &agent.ChatAgent{}, SessionService: counted, AppName: studioChatAppName}
	if _, err = p.compose(ctx, resources, initial.Session); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 1000; i++ {
		e := &session.Event{ID: fmt.Sprint(i), Author: "model", LLMResponse: model.LLMResponse{Content: genai.NewContentFromText("irrelevant old model text", "model")}}
		if i%10 == 0 {
			e.Content = &genai.Content{Parts: []*genai.Part{{FunctionResponse: &genai.FunctionResponse{ID: fmt.Sprint(i), Name: "read_file", Response: map[string]any{"text": fmt.Sprint(i)}}}}}
		}
		if i%111 == 0 {
			e.Author = "user"
			e.Content.Parts = append(e.Content.Parts, &genai.Part{Text: fmt.Sprint("owner", i)}, &genai.Part{Text: "second owner part"})
		}
		if err = resources.SessionService.AppendEvent(ctx, initial.Session, e); err != nil {
			t.Fatal(err)
		}
	}
	verify := func() {
		t.Helper()
		expected, err := p.observeContext(ctx, resources, initial.Session)
		if err != nil {
			t.Fatal(err)
		}
		actual, err := c.Observe(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(expected, actual) {
			t.Fatalf("incremental observation changed references, fingerprint or excerpts\nwant=%+v\ngot=%+v", expected, actual)
		}
	}
	verify()
	tracker := resources.SessionService.(*botProgressSessions)
	cursor := tracker.cursor
	for i := 0; i < 10; i++ {
		verify()
	}
	if counted.gets != 0 || tracker.cursor != cursor {
		t.Fatalf("boundary reread persisted history: gets=%d cursor=%d/%d", counted.gets, tracker.cursor, cursor)
	}
	fresh := &session.Event{ID: "last", Author: "model", LLMResponse: model.LLMResponse{Content: &genai.Content{Parts: []*genai.Part{{FunctionResponse: &genai.FunctionResponse{ID: "latest", Name: "web_fetch", Response: map[string]any{"text": "latest source"}}}}}}}
	counted.fail = store.ErrBotConflict
	if err = resources.SessionService.AppendEvent(ctx, initial.Session, fresh); !errors.Is(err, store.ErrBotConflict) {
		t.Fatal(err)
	}
	verify()
	counted.fail = nil
	if err = resources.SessionService.AppendEvent(ctx, initial.Session, fresh); err != nil {
		t.Fatal(err)
	}
	verify()
	if tracker.cursor != cursor+1 {
		t.Fatal("new persisted output missing")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err = c.Observe(cancelled); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel lost: %v", err)
	}
	// Explicit Get remains the complete durable transcript, not the progress tail.
	restored, err := resources.SessionService.Get(ctx, q)
	if err != nil {
		t.Fatal(err)
	}
	if restored.Session.Events().Len() != cursor+1 {
		t.Fatal("exact resume transcript shortened")
	}
}

func TestBotConversationEventsIndexedView(t *testing.T) {
	a, b, c := &session.Event{ID: "prior"}, &session.Event{ID: "current"}, &session.Event{ID: "appended"}
	base := botProgressSessionSnapshot{events: botConversationEvents{prior: []*session.Event{b}}}
	s := botConversationSession{Session: base, prior: []*session.Event{a}, appended: []*session.Event{c}}
	events := s.Events()
	if events.Len() != 3 {
		t.Fatal(events.Len())
	}
	var ids []string
	for e := range events.All() {
		ids = append(ids, e.ID)
	}
	if !reflect.DeepEqual(ids, []string{"prior", "current", "appended"}) {
		t.Fatal(ids)
	}
	for i, id := range ids {
		if events.At(i).ID != id {
			t.Fatal("indexed order changed")
		}
	}
	for _, i := range []int{-1, 3, 100} {
		if events.At(i) != nil {
			t.Fatalf("out-of-range event %d", i)
		}
	}
}

// This counter counts actual indexed event access, not merely service calls.
// A progress boundary must not walk a long transcript after initialization.
type progressIndexedEvents struct {
	events []*session.Event
	reads  int
}

func (e *progressIndexedEvents) Len() int                { return len(e.events) }
func (e *progressIndexedEvents) At(i int) *session.Event { e.reads++; return e.events[i] }
func (e *progressIndexedEvents) All() iter.Seq[*session.Event] {
	return func(yield func(*session.Event) bool) {
		for i := range e.events {
			if !yield(e.At(i)) {
				return
			}
		}
	}
}

type progressIndexedSession struct {
	session.Session
	events        *progressIndexedEvents
	app, user, id string
}

func (s *progressIndexedSession) ID() string             { return s.id }
func (s *progressIndexedSession) AppName() string        { return s.app }
func (s *progressIndexedSession) UserID() string         { return s.user }
func (s *progressIndexedSession) Events() session.Events { return s.events }

type progressIndexedService struct {
	session.Service
	current *progressIndexedSession
	gets    int
}

func (s *progressIndexedService) Get(context.Context, *session.GetRequest) (*session.GetResponse, error) {
	s.gets++
	return &session.GetResponse{Session: s.current}, nil
}
func (s *progressIndexedService) AppendEvent(_ context.Context, v session.Session, e *session.Event) error {
	v.(*progressIndexedSession).events.events = append(v.(*progressIndexedSession).events.events, e)
	return nil
}
func TestBotProgressSessionBoundedIndexedReads(t *testing.T) {
	events := &progressIndexedEvents{}
	for i := 0; i < 10000; i++ {
		events.events = append(events.events, &session.Event{ID: fmt.Sprint(i)})
	}
	v := &progressIndexedSession{events: events, app: "app", user: "owner", id: "run"}
	svc := &progressIndexedService{current: v}
	tracker := &botProgressSessions{Service: svc, app: v.app, user: v.user, id: v.id}
	if _, n, err := tracker.snapshot(context.Background()); err != nil || n != 10000 {
		t.Fatalf("initial: %d %v", n, err)
	}
	initialReads := events.reads
	for i := 0; i < 20; i++ {
		if _, _, err := tracker.snapshot(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if events.reads != initialReads || svc.gets != 1 {
		t.Fatalf("reread history: indexed=%d initial=%d gets=%d", events.reads, initialReads, svc.gets)
	}
	if err := tracker.AppendEvent(context.Background(), v, &session.Event{ID: "fresh"}); err != nil {
		t.Fatal(err)
	}
	if _, n, err := tracker.snapshot(context.Background()); err != nil || n != 10001 {
		t.Fatalf("append: %d %v", n, err)
	}
	if events.reads != initialReads+1 {
		t.Fatalf("tail read=%d want=1", events.reads-initialReads)
	}
}

func TestBotConversationCanonicalAppendBoundedReads(t *testing.T) {
	events := &progressIndexedEvents{}
	for i := 0; i < 10000; i++ {
		events.events = append(events.events, &session.Event{ID: fmt.Sprint(i)})
	}
	v := &progressIndexedSession{events: events}
	s := &botConversationSession{Session: v}
	s.appendCanonical(&session.Event{ID: "0"})
	if len(s.appended) != 0 {
		t.Fatal("canonical owner duplicated")
	}
	initial := events.reads
	for i := 0; i < 100; i++ {
		e := &session.Event{ID: fmt.Sprintf("new-%d", i)}
		s.appendCanonical(e)
		s.appendCanonical(e)
	}
	if len(s.appended) != 100 || events.reads != initial {
		t.Fatalf("append count=%d history rereads=%d", len(s.appended), events.reads-initial)
	}
}

func TestBotProgressSessionScopeIsolation(t *testing.T) {
	own := &progressIndexedSession{events: &progressIndexedEvents{}, app: "app", user: "owner", id: "run"}
	other := &progressIndexedSession{events: &progressIndexedEvents{}, app: "app", user: "other", id: "run"}
	svc := &progressIndexedService{current: other}
	tracker := &botProgressSessions{Service: svc, current: own, app: own.app, user: own.user, id: own.id}
	ctx := context.Background()
	for _, q := range []*session.GetRequest{
		{AppName: "app", UserID: "other", SessionID: "run"},
		{AppName: "app", UserID: "owner", SessionID: "run"}, // mismatched store response must also be rejected
	} {
		if _, err := tracker.Get(ctx, q); err != nil {
			t.Fatal(err)
		}
		if tracker.current != own {
			t.Fatal("foreign scope replaced current transcript")
		}
	}
	if err := tracker.AppendEvent(ctx, other, &session.Event{ID: "foreign"}); err != nil {
		t.Fatal(err)
	}
	if _, n, err := tracker.snapshot(ctx); err != nil || n != 0 {
		t.Fatalf("foreign event leaked: %d %v", n, err)
	}
	tracker.current = nil
	if _, _, err := tracker.snapshot(ctx); !errors.Is(err, store.ErrBotForbidden) {
		t.Fatalf("foreign initialization accepted: %v", err)
	}
}

func TestBotProgressSessionConcurrentAppendSnapshot(t *testing.T) {
	v := &progressIndexedSession{events: &progressIndexedEvents{}, app: "app", user: "owner", id: "run"}
	svc := &progressIndexedService{current: v}
	tracker := &botProgressSessions{Service: svc, app: v.app, user: v.user, id: v.id}
	var wg sync.WaitGroup
	for g := 0; g < 4; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				if g%2 == 0 {
					if err := tracker.AppendEvent(context.Background(), v, &session.Event{ID: fmt.Sprintf("%d-%d", g, i)}); err != nil {
						t.Error(err)
					}
				} else if _, _, err := tracker.snapshot(context.Background()); err != nil {
					t.Error(err)
				}
			}
		}(g)
	}
	wg.Wait()
	if _, n, err := tracker.snapshot(context.Background()); err != nil || n != 200 {
		t.Fatalf("lost append: %d %v", n, err)
	}
}

func TestBotProgressMultipleEvaluationsUseCurrentTail(t *testing.T) {
	ctx, p, c, ss, llm := progressContextFixture(t)
	initial, err := ss.Get(ctx, &session.GetRequest{AppName: studioChatAppName, UserID: p.req.Bot.OwnerID.String(), SessionID: p.req.Run.SessionID.String()})
	if err != nil {
		t.Fatal(err)
	}
	counted := &progressCountingSessions{Service: ss}
	resources := &BotChatResources{ProgressDispatch: p, Agent: &agent.ChatAgent{LLM: llm}, SessionService: counted, AppName: studioChatAppName}
	if _, err := p.compose(ctx, resources, initial.Session); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	c.Now = func() time.Time { return now }
	c.Commit = func(context.Context, bot.ProgressObservation, bot.ProgressDecision, time.Time) error { return nil }
	if _, err := c.Check(ctx); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	for i := 0; i < 3; i++ {
		e := &session.Event{ID: fmt.Sprintf("response-%d", i), LLMResponse: model.LLMResponse{Content: &genai.Content{Parts: []*genai.Part{{FunctionResponse: &genai.FunctionResponse{ID: fmt.Sprintf("call-%d", i), Name: "read_file", Response: map[string]any{"text": fmt.Sprintf("source-%d", i)}}}}}}}
		if err := resources.SessionService.AppendEvent(ctx, initial.Session, e); err != nil {
			t.Fatal(err)
		}
		now = now.Add(c.Interval)
		if d, err := c.Check(ctx); err != nil || d == nil {
			t.Fatalf("evaluation %d: %v %v", i, d, err)
		}
		if !strings.Contains(llm.raw, fmt.Sprintf("source-%d", i)) {
			t.Fatalf("evaluation %d omitted newly persisted tool evidence", i)
		}
	}
	t.Logf("three persisted-tool/evaluation cycles: elapsed=%s model_calls=%d durable_gets=%d", time.Since(start), llm.calls, counted.gets)
	if llm.calls != 3 || counted.gets != 0 {
		t.Fatalf("calls=%d durable gets=%d", llm.calls, counted.gets)
	}
}

type progressOperationWork struct {
	store.BotWorkStore
	work *store.BotWorkState
	seen []context.Context
	err  error
}

func (w *progressOperationWork) LoadWork(ctx context.Context, _ store.BotLease) (*store.BotWorkState, error) {
	w.seen = append(w.seen, ctx)
	if entstore.WithBotContentReadOperationFromContext(context.Background(), ctx) == context.Background() {
		return nil, fmt.Errorf("missing synchronous read operation")
	}
	return w.work, w.err
}
func TestBotProgressObservationCommitReadOperationLifetime(t *testing.T) {
	ctx, p, c, ss, llm := progressContextFixture(t)
	w := &progressOperationWork{work: p.req.Work}
	resources := &BotChatResources{ProgressDispatch: p, Agent: &agent.ChatAgent{LLM: llm}, Work: w, SessionService: ss, AppName: studioChatAppName}
	if _, err := p.compose(ctx, resources); err != nil {
		t.Fatal(err)
	}
	if c.BeginBookkeeping == nil {
		t.Fatal("composition omitted synchronous bookkeeping capability")
	}
	if _, err := c.Observe(ctx); err != nil {
		t.Fatal(err)
	}
	// Revocation must reach the store again even after a successful observation.
	w.err = store.ErrBotForbidden
	if _, err := c.Observe(ctx); !errors.Is(err, store.ErrBotForbidden) {
		t.Fatalf("observe permission cached: %v", err)
	}
	if err := c.Commit(ctx, bot.ProgressObservation{}, bot.ProgressDecision{}, time.Now()); !errors.Is(err, store.ErrBotForbidden) {
		t.Fatalf("commit permission cached: %v", err)
	}
	if len(w.seen) != 3 {
		t.Fatalf("work calls=%d", len(w.seen))
	}
	for _, retained := range w.seen {
		if entstore.WithBotContentReadOperationFromContext(context.Background(), retained) != context.Background() {
			t.Fatal("read operation outlived synchronous callback")
		}
	}
}
