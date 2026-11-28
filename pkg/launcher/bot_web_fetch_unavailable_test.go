package launcher

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/credentials"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
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
