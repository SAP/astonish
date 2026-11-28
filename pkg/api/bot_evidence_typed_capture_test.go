package api

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/tools"
)

// http_request returns a typed ADK result envelope. The live current-session
// optimization must hash the same canonical JSON map as durable restoration.
func TestBotEvidenceTypedHTTPEnvelopeMatchesPersistedCapture(t *testing.T) {
	s, _, reader, _ := accessScanFixture(t, 1, 0)
	response := map[string]any{"result": tools.HttpRequestResult{StatusCode: 200, Status: "200 OK", Body: "source", ContentType: "text/plain"}}
	part := reader.rows[0].Event.Content.Parts[0]
	part.FunctionResponse.Name = "http_request"
	part.FunctionResponse.Response = response
	typed, err := s.evidence(context.Background(), bot.BotEvidenceReadRequest{CallID: "source-0", Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	var persisted map[string]any
	if err := json.Unmarshal(raw, &persisted); err != nil {
		t.Fatal(err)
	}
	part.FunctionResponse.Response = persisted
	restored, err := s.evidence(context.Background(), bot.BotEvidenceReadRequest{CallID: "source-0", Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if typed.Reference.Digest != restored.Reference.Digest {
		t.Fatalf("live typed HTTP response digest differs from persisted capture: live=%s persisted=%s", typed.Reference.Digest, restored.Reference.Digest)
	}
	part.FunctionResponse.Response = response
	w := s.resources.Work.(*agentExecutionWorkStore).state
	fact := store.BotSourceAccessFact{EvidenceRef: restored.Reference.Ref, CallID: "source-0", OriginalURL: "https://example.test/source", HTTPStatus: 200, Status: store.BotSourceAccessReturned, ObservedAt: time.Now(), BodyDigest: restored.Reference.Digest}
	s.captureAccess("source-0", []store.BotSourceAccessFact{fact})
	next, err := s.flushAccess(context.Background(), w)
	if err != nil || len(next.AgentWork.SourceAccess) != 1 {
		t.Fatalf("typed capture cannot flush: %v", err)
	}
	fact.BodyDigest = strings.Repeat("0", 64)
	s.captureAccess("source-0", []store.BotSourceAccessFact{fact})
	if _, err := s.flushAccess(context.Background(), next); !errors.Is(err, store.ErrBotInvalid) || !strings.Contains(err.Error(), "source-access fact") {
		t.Fatalf("digest mismatch not rejected with payload-free context: %v", err)
	}
}
