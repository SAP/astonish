package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

func TestAgentLiveProvenanceNeverPromotesResolverOrUsage(t *testing.T) {
	home := agentHomeModel{Provider: agentProvider, Model: agentModel, Credentials: true}
	run := store.BotRun{Provider: agentProvider, Model: agentModel, Usage: store.BotUsage{InputTokens: 123, OutputTokens: 456}}
	got, err := agentObserveProvenance(home, run)
	if err == nil || got.AdapterVerified || len(got.Missing) != 3 || got.ResolverProvider != agentProvider {
		t.Fatalf("resolver metadata incorrectly certified adapter: %+v %v", got, err)
	}
	home.Provider = "alias"
	if _, err := agentObserveProvenance(home, run); err == nil {
		t.Fatal("accepted alias")
	}
}

func agentTestStream(messages []chatModelMessage) string {
	b, _ := json.Marshal(map[string]any{"messages": messages})
	return string(b)
}
func TestAgentLiveSourcePayloadsRequirePairedFetchedContent(t *testing.T) {
	payload := json.RawMessage(`{"url":"https://example.test/source","status":200,"content":"Retained dated observation: units=17, not 99."}`)
	call := chatModelMessage{Type: "tool_call", ToolName: "web_fetch", ToolID: "fetch-1"}
	result := chatModelMessage{Type: "tool_result", ToolName: "web_fetch", ToolID: "fetch-1", ToolResult: payload}
	got, err := agentSourcePayloads(agentTestStream([]chatModelMessage{call, result}))
	if err != nil || len(got) != 1 || got[0].SHA256 != fmt.Sprintf("%x", sha256.Sum256(payload)) || string(got[0].Payload) != string(payload) {
		t.Fatalf("capture=%+v err=%v", got, err)
	}
	for name, mutate := range map[string]func(*chatModelMessage, *chatModelMessage){
		"mismatched-call":  func(c, r *chatModelMessage) { r.ToolID = "other" },
		"search-not-fetch": func(c, r *chatModelMessage) { c.ToolName = "web_search"; r.ToolName = "web_search" },
		"empty-page": func(c, r *chatModelMessage) {
			r.ToolResult = json.RawMessage(`{"url":"https://example.test/source","status":200,"content":" "}`)
		},
		"access-denied": func(c, r *chatModelMessage) {
			r.ToolResult = json.RawMessage(`{"url":"https://example.test/source","status":403,"content":"Denied; please sign in for full document access."}`)
		},
		"tool-error": func(c, r *chatModelMessage) {
			r.ToolResult = json.RawMessage(`{"url":"https://example.test/source","error":"unavailable","content":"Cached text is not a successful fetch"}`)
		},
		"missing-url": func(c, r *chatModelMessage) {
			r.ToolResult = json.RawMessage(`{"status":200,"content":"A very long text does not establish where it came from"}`)
		},
	} {
		t.Run(name, func(t *testing.T) {
			c, r := call, result
			mutate(&c, &r)
			got, err := agentSourcePayloads(agentTestStream([]chatModelMessage{c, r}))
			if err != nil || len(got) != 0 {
				t.Fatalf("accepted invalid capture: %+v %v", got, err)
			}
		})
	}
	for _, messages := range [][]chatModelMessage{{result}, {call, call, result}, {call, result, result}} {
		got, err := agentSourcePayloads(agentTestStream(messages))
		if err == nil && len(got) != 0 {
			t.Fatal("accepted orphan or duplicate result")
		}
	}
	if _, err := agentSourcePayloads("data: {}\n\n"); err == nil {
		t.Fatal("accepted incomplete SSE")
	}
}

func TestAgentLiveComparisonRejectsSourceContradictionsWithoutTopicHeuristics(t *testing.T) {
	// Deliberately unrelated IDs and values: the oracle is source content, not topic words.
	sources := []agentComparisonSource{{ID: "x-782", AsOf: "2026-03-02T00:30:00+03:00", Units: 11}, {ID: "y-415", AsOf: "2026-03-01T22:00:00Z", Units: 83}, {ID: "z-992", AsOf: "2025-01-01T00:00:00Z", Units: 42}}
	correct := agentComparisonAnswer{SelectedSource: "y-415", AsOf: sources[1].AsOf, Units: 83, Conflict: true, StaleSources: []string{"x-782", "z-992"}, Citations: []string{"z-992", "y-415", "x-782"}}
	encode := func(a agentComparisonAnswer) string { raw, _ := json.Marshal(a); return string(raw) }
	if err := agentValidateComparison(encode(correct), sources); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*agentComparisonAnswer){
		"contradicted-units": func(a *agentComparisonAnswer) { a.Units = 999 },
		"average":            func(a *agentComparisonAnswer) { a.Units = 45 },
		"stale-selection":    func(a *agentComparisonAnswer) { a.SelectedSource = "x-782"; a.Units = 11; a.AsOf = sources[0].AsOf },
		"denied-conflict":    func(a *agentComparisonAnswer) { a.Conflict = false },
		"omitted-staleness":  func(a *agentComparisonAnswer) { a.StaleSources = nil },
		"unknown-citation":   func(a *agentComparisonAnswer) { a.Citations = []string{"unknown", "y-415", "x-782"} },
		"repeated-citation":  func(a *agentComparisonAnswer) { a.Citations = []string{"x-782", "y-415", "x-782"} },
	} {
		t.Run(name, func(t *testing.T) {
			a := correct
			mutate(&a)
			if agentValidateComparison(encode(a), sources) == nil {
				t.Fatal("accepted unsupported comparison")
			}
		})
	}
	for _, bad := range []string{encode(correct) + " garbage", encode(correct) + " {}", strings.TrimSuffix(encode(correct), "}") + `,"extra_claim":"invented"}`, strings.TrimSuffix(encode(correct), "}") + `,"units":83}`, strings.Replace(encode(correct), `"conflict":true`, `"conflict":null`, 1), strings.Replace(encode(correct), `"conflict":true,`, ``, 1), "null"} {
		if agentValidateComparison(bad, sources) == nil {
			t.Fatalf("accepted extra claims: %s", bad)
		}
	}
	for _, bad := range [][]agentComparisonSource{nil, {sources[0]}, {sources[0], sources[0]}, {{ID: "x", AsOf: "invalid", Units: 1}, sources[1]}, {{ID: "x", AsOf: sources[1].AsOf, Units: 1}, sources[1]}} {
		if agentValidateComparison(encode(correct), bad) == nil {
			t.Fatal("accepted ambiguous source dossier")
		}
	}
}

func TestAgentLiveRevisionRequiresExistingDocumentPreservation(t *testing.T) {
	before := "# Brief\n\nWindow: 14 days.\n\nDo not modify this paragraph.\n"
	after := strings.Replace(before, "14 days", "21 days", 1)
	if err := agentValidateRevision(before, after, "14 days", "21 days"); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{before, "Window: 21 days.", after + "Extra change.", strings.Replace(after, "paragraph", "section", 1)} {
		if agentValidateRevision(before, bad, "14 days", "21 days") == nil {
			t.Fatal("accepted unsaved or extraneous revision")
		}
	}
	if agentValidateRevision(before+before, after+after, "14 days", "21 days") == nil {
		t.Fatal("accepted ambiguous edit")
	}
}

func TestAgentLiveArtifactRequiresRetainedDigestAndHome(t *testing.T) {
	body := "# Saved artifact\n"
	a := store.BotArtifact{ID: uuid.New(), HomeID: uuid.New(), RelativePath: "reports/test.md", SHA256: fmt.Sprintf("%x", sha256.Sum256([]byte(body)))}
	run := store.BotRun{Report: store.BotReport{Artifacts: []store.BotArtifact{a}}}
	if got, err := agentRetainedArtifact(run, a.RelativePath, body); err != nil || got != a {
		t.Fatalf("artifact=%+v %v", got, err)
	}
	for _, mutate := range []func(*store.BotArtifact){func(a *store.BotArtifact) { a.SHA256 = strings.Repeat("0", 64) }, func(a *store.BotArtifact) { a.HomeID = uuid.Nil }, func(a *store.BotArtifact) { a.ID = uuid.Nil }, func(a *store.BotArtifact) { a.RelativePath = "other.md" }} {
		b := a
		mutate(&b)
		r := run
		r.Report.Artifacts = []store.BotArtifact{b}
		if _, err := agentRetainedArtifact(r, a.RelativePath, body); err == nil {
			t.Fatal("accepted unbound artifact")
		}
	}
	if _, err := agentRetainedArtifact(run, a.RelativePath, body+"tampered"); err == nil {
		t.Fatal("accepted changed bytes")
	}
}
