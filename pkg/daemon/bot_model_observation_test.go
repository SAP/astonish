package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"iter"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"google.golang.org/adk/model"
)

type observationScopeKey struct{}

type observationTestModel struct {
	generate func(context.Context, *model.LLMRequest, bool) iter.Seq2[*model.LLMResponse, error]
}

func (*observationTestModel) Name() string { return "actual-model" }
func (m *observationTestModel) GenerateContent(ctx context.Context, r *model.LLMRequest, s bool) iter.Seq2[*model.LLMResponse, error] {
	return m.generate(ctx, r, s)
}
func observationTestRequest() bot.RunRequest {
	return bot.RunRequest{Bot: store.Bot{ID: uuid.New(), OwnerID: uuid.New(), Scope: store.BotScopePersonal}, Run: store.BotRun{ID: uuid.New(), SessionID: uuid.New()}}
}
func observationLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var out bytes.Buffer
	old := log.Writer()
	log.SetOutput(&out)
	t.Cleanup(func() { log.SetOutput(old) })
	return &out
}
func observationRecords(t *testing.T, out *bytes.Buffer) []botModelObservation {
	t.Helper()
	records := []botModelObservation{}
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		_, raw, ok := strings.Cut(line, botModelObservationMarker)
		if !ok {
			t.Fatalf("missing marker")
		}
		var r botModelObservation
		if err := json.Unmarshal([]byte(raw), &r); err != nil {
			t.Fatal(err)
		}
		records = append(records, r)
	}
	return records
}
func TestBotModelObservationActualInstance(t *testing.T) {
	out := observationLogs(t)
	r := observationTestRequest()
	inner := &observationTestModel{}
	wrapped := observeBotResolvedModel(r, "worker", "resolver-alias", "resolver-model", inner).(*botObservedModel)
	if wrapped.inner != inner || wrapped.Name() != inner.Name() {
		t.Fatal("replaced model")
	}
	records := observationRecords(t, out)
	got := records[0]
	if len(records) != 1 || got.AdapterName != "actual-model" || got.AdapterType != "*daemon.observationTestModel" || got.Model != "resolver-model" || got.RunID != r.Run.ID || got.BotID != r.Bot.ID || got.SessionID != r.Run.SessionID || got.OwnerID != r.Bot.OwnerID || got.WorkerID != "worker" || got.AdapterID == uuid.Nil || got.WireVerified {
		t.Fatalf("wrong observation: %+v", got)
	}
}
func TestBotModelObservationRejectsNil(t *testing.T) {
	out := observationLogs(t)
	r := observationTestRequest()
	if observeBotResolvedModel(r, "w", "p", "m", nil) != nil {
		t.Fatal("nil changed")
	}
	var typed *observationTestModel
	if observeBotResolvedModel(r, "w", "p", "m", typed) != typed {
		t.Fatal("typed nil changed")
	}
	if out.Len() != 0 {
		t.Fatal("nil emitted success")
	}
}
func TestBotModelObservationIteratorTransparency(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "batch", true: "stream"}[stream], func(t *testing.T) {
			out := observationLogs(t)
			r := observationTestRequest()
			ctx := context.WithValue(context.Background(), observationScopeKey{}, "scope-marker")
			req := &model.LLMRequest{Model: "selected"}
			responses := []*model.LLMResponse{{}, nil, {}}
			calls, iterations := 0, 0
			inner := &observationTestModel{generate: func(c context.Context, q *model.LLMRequest, s bool) iter.Seq2[*model.LLMResponse, error] {
				calls++
				if c != ctx || q != req || s != stream {
					t.Fatal("arguments changed")
				}
				return func(yield func(*model.LLMResponse, error) bool) {
					iterations++
					for _, v := range responses {
						if !yield(v, nil) {
							return
						}
					}
				}
			}}
			llm := observeBotResolvedModel(r, "w", "p", "m", inner)
			seq := llm.GenerateContent(ctx, req, stream)
			if calls != 1 || iterations != 0 {
				t.Fatal("iterator timing changed")
			}
			i := 0
			for v, err := range seq {
				if v != responses[i] || err != nil {
					t.Fatal("response changed")
				}
				i++
			}
			if i != 3 || calls != 1 || iterations != 1 {
				t.Fatal("extra/missing calls")
			}
			records := observationRecords(t, out)
			if len(records) != 3 || records[2].Outcome != botModelComplete || records[2].Responses != 2 || records[1].CallID != records[2].CallID || records[1].RequestModel != "selected" {
				t.Fatal("incorrect generation observations")
			}
		})
	}
}
func TestBotModelObservationEarlyStopAndError(t *testing.T) {
	for _, kind := range []string{"stop", "error", "cancel", "nil-request"} {
		t.Run(kind, func(t *testing.T) {
			out := observationLogs(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			boom := errors.New("SECRET payload must not appear")
			yielded := 0
			response := &model.LLMResponse{}
			inner := &observationTestModel{generate: func(c context.Context, q *model.LLMRequest, s bool) iter.Seq2[*model.LLMResponse, error] {
				return func(yield func(*model.LLMResponse, error) bool) {
					for i := 0; i < 3; i++ {
						yielded++
						var err error
						if kind == "error" {
							err = boom
						}
						if kind == "cancel" {
							cancel()
							err = context.Canceled
						}
						if !yield(response, err) {
							return
						}
					}
				}
			}}
			llm := observeBotResolvedModel(observationTestRequest(), "w", "p", "m", inner)
			for got, err := range llm.GenerateContent(ctx, nil, true) {
				if got != response {
					t.Fatal("response changed")
				}
				if kind == "error" && err != boom {
					t.Fatal("error changed")
				}
				if kind == "stop" {
					break
				}
			}
			end := observationRecords(t, out)[2]
			want := map[string]botModelObservationOutcome{"stop": botModelStopped, "error": botModelError, "cancel": botModelCancelled, "nil-request": botModelComplete}[kind]
			if end.Outcome != want || end.RequestModel != "" || strings.Contains(out.String(), "SECRET") {
				t.Fatal("outcome/payload invalid")
			}
			if kind == "stop" && yielded != 1 {
				t.Fatal("early stop lost")
			}
		})
	}
}
func TestBotModelObservationConcurrentRunIsolation(t *testing.T) {
	out := observationLogs(t)
	var wg sync.WaitGroup
	runs := map[uuid.UUID]uuid.UUID{}
	for i := 0; i < 8; i++ {
		r := observationTestRequest()
		runs[r.Run.ID] = r.Bot.ID
		inner := &observationTestModel{generate: func(context.Context, *model.LLMRequest, bool) iter.Seq2[*model.LLMResponse, error] {
			return func(yield func(*model.LLMResponse, error) bool) { yield(&model.LLMResponse{}, nil) }
		}}
		llm := observeBotResolvedModel(r, "w", "p", "m", inner)
		for j := 0; j < 3; j++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for range llm.GenerateContent(context.Background(), nil, true) {
				}
			}()
		}
	}
	wg.Wait()
	seen := map[uuid.UUID]bool{}
	ends := 0
	for _, r := range observationRecords(t, out) {
		if runs[r.RunID] != r.BotID {
			t.Fatal("cross-run metadata")
		}
		if r.Phase == botModelEnded {
			ends++
			if seen[r.CallID] {
				t.Fatal("duplicate call")
			}
			seen[r.CallID] = true
		}
	}
	if ends != 24 {
		t.Fatalf("ends %d", ends)
	}
}

func TestBotModelObservationPrivateLoggerRoute(t *testing.T) {
	t.Setenv(logDestinationEnv, "file")
	root := t.TempDir()
	logger, err := newLoggerForMode("", root)
	if err != nil {
		t.Fatal(err)
	}
	old := log.Writer()
	log.SetOutput(logger)
	defer log.SetOutput(old)
	observeBotResolvedModel(observationTestRequest(), "worker", "provider", "model", &observationTestModel{})
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(root, "daemon.log"))
	if err != nil {
		t.Fatal(err)
	}
	records := observationRecords(t, bytes.NewBuffer(raw))
	if len(records) != 1 || records[0].Phase != botModelResolved {
		t.Fatal("private logger lost observation")
	}
}

type panicObservationWriter struct{}

func (panicObservationWriter) Write([]byte) (int, error) { panic("writer unavailable") }
func TestBotModelObservationLogFailureDoesNotGate(t *testing.T) {
	old := log.Writer()
	log.SetOutput(panicObservationWriter{})
	defer log.SetOutput(old)
	inner := &observationTestModel{generate: func(context.Context, *model.LLMRequest, bool) iter.Seq2[*model.LLMResponse, error] {
		return func(yield func(*model.LLMResponse, error) bool) { yield(nil, nil) }
	}}
	llm := observeBotResolvedModel(observationTestRequest(), "w", "p", "m", inner)
	n := 0
	for range llm.GenerateContent(context.Background(), nil, false) {
		n++
	}
	if n != 1 {
		t.Fatal("logging gated model")
	}
}
