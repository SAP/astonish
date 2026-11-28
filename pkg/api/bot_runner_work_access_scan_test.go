package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"google.golang.org/adk/model"
	"google.golang.org/adk/session"
	"google.golang.org/genai"
)

// Count reads independently of timing: a captured response must not be loaded
// again just to bind the reference that evidence() already verified.
type accessScanReader struct {
	agentEvidenceReader
	reads int
}

func (r *accessScanReader) ReadBotTranscript(ctx context.Context, id uuid.UUID, cursor int64, limit int) ([]store.BotTranscriptEvent, error) {
	r.reads++
	return r.agentEvidenceReader.ReadBotTranscript(ctx, id, cursor, limit)
}

func accessScanFixture(tb testing.TB, count, bodyBytes int) (*botAgentWorkService, *store.BotWorkState, *accessScanReader, map[string][]store.BotSourceAccessFact) {
	tb.Helper()
	botID, runID := uuid.New(), uuid.New()
	w := store.NewBotAgentWorkState(store.BotWorkContract{BotID: botID, RunID: runID, Version: 1, Instructions: "Research the sources"})
	r := &accessScanReader{}
	pending := make(map[string][]store.BotSourceAccessFact)
	for i := 0; i < count; i++ {
		call := fmt.Sprintf("source-%d", i)
		response := map[string]any{"status": 200, "content": strings.Repeat("x", bodyBytes)}
		raw, err := json.Marshal(response)
		if err != nil {
			tb.Fatal(err)
		}
		event := &session.Event{ID: "event-" + call, LLMResponse: model.LLMResponse{Content: &genai.Content{Role: "tool", Parts: []*genai.Part{{FunctionResponse: &genai.FunctionResponse{ID: call, Name: "web_fetch", Response: response}}}}}}
		r.rows = append(r.rows, store.BotTranscriptEvent{Cursor: int64(i + 1), Event: event})
		pending[call] = []store.BotSourceAccessFact{{EvidenceRef: "capture:" + call, CallID: call, OriginalURL: "https://example.test/" + call, HTTPStatus: 200, Status: store.BotSourceAccessReturned, ObservedAt: time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC), BodyDigest: botAgentDigest(raw)}}
	}
	s := &botAgentWorkService{req: bot.RunRequest{Run: store.BotRun{ID: runID, SessionID: uuid.New()}}, resources: &BotChatResources{Work: &agentExecutionWorkStore{state: w}, EvidenceReader: r}}
	return s, w, r, pending
}

func TestBotAgentAccessFlushSingleScan(t *testing.T) {
	s, w, reader, pending := accessScanFixture(t, 12, 256*1024)
	s.accessPending = pending
	next, err := s.flushAccess(context.Background(), w)
	if err != nil {
		t.Fatal(err)
	}
	if len(next.AgentWork.Evidence) != 12 || len(next.AgentWork.SourceAccess) != 12 || s.accessPending != nil {
		t.Fatal("source references and facts were not durably flushed")
	}
	if reader.reads != 12 {
		t.Fatalf("got %d transcript reads, want one per captured source (12)", reader.reads)
	}
	// Re-observing an identical capture must not duplicate metadata or save again.
	s.accessPending = pending
	repeated, err := s.flushAccess(context.Background(), next)
	if err != nil || repeated != next || s.accessPending != nil {
		t.Fatalf("identical facts were not idempotent: %v", err)
	}
}

func TestBotAgentAccessFlushSaveFailureRetainsPending(t *testing.T) {
	s, w, _, pending := accessScanFixture(t, 1, 1024)
	s.resources.Work.(*agentExecutionWorkStore).err = store.ErrBotConflict
	s.accessPending = pending
	if _, err := s.flushAccess(context.Background(), w); !errors.Is(err, store.ErrBotConflict) {
		t.Fatalf("save failure lost: %v", err)
	}
	if s.accessPending == nil || len(w.AgentWork.Evidence) != 0 || len(w.AgentWork.SourceAccess) != 0 {
		t.Fatal("failed save changed input state or discarded pending facts")
	}
}

func TestBotAgentAccessFlushExistingReferenceConflict(t *testing.T) {
	s, w, _, pending := accessScanFixture(t, 1, 1024)
	captured, err := s.evidence(context.Background(), bot.BotEvidenceReadRequest{CallID: "source-0", Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	wrong := captured.Reference
	wrong.EventID = "other-event"
	w.AgentWork.Evidence = append(w.AgentWork.Evidence, wrong)
	s.accessPending = pending
	if _, err := s.flushAccess(context.Background(), w); !errors.Is(err, store.ErrBotConflict) {
		t.Fatalf("conflicting stored evidence accepted: %v", err)
	}
	if s.accessPending == nil {
		t.Fatal("failed flush discarded pending facts")
	}
}

func BenchmarkBotAgentAccessFlush(b *testing.B) {
	for _, size := range []int{16 * 1024, 256 * 1024, 1024 * 1024} {
		b.Run(fmt.Sprintf("12_sources_%dKiB", size/1024), func(b *testing.B) {
			s, w, reader, pending := accessScanFixture(b, 12, size)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				s.resources.Work = &agentExecutionWorkStore{state: w}
				s.accessPending = pending
				if _, err := s.flushAccess(context.Background(), w); err != nil {
					b.Fatal(err)
				}
			}
			b.ReportMetric(float64(reader.reads)/float64(b.N), "transcript_reads/op")
		})
	}
}
