package main

// These observers inspect production records. Resolver identity, successful tool
// transport, and agent confidence are deliberately not adapter/grounding proof.
import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"

	"github.com/SAP/astonish/pkg/store"
	"github.com/go-rod/rod"
	"github.com/google/uuid"
)

type agentSourcePayload struct {
	CallID  string          `json:"call_id"`
	Tool    string          `json:"tool"`
	URL     string          `json:"url"`
	SHA256  string          `json:"sha256"`
	Payload json.RawMessage `json:"payload"`
}

// Retain only paired successful retrieval results, not URLs mentioned by an
// assistant or a search snippet misrepresented as a fetched document.
func agentSourcePayloads(stream string) ([]agentSourcePayload, error) {
	messages, err := chatModelMessages(stream)
	if err != nil {
		return nil, err
	}
	calls := map[string]string{}
	seen := map[string]bool{}
	out := []agentSourcePayload{}
	for _, m := range messages {
		if m.Type == "tool_call" && m.ToolID != "" && (m.ToolName == "web_fetch" || m.ToolName == "browser_navigate") {
			if _, exists := calls[m.ToolID]; exists {
				return nil, fmt.Errorf("duplicate retrieval call identity")
			}
			calls[m.ToolID] = m.ToolName
		}
		if m.Type != "tool_result" || m.ToolID == "" || calls[m.ToolID] == "" || calls[m.ToolID] != m.ToolName || !successfulWebResult(m.ToolResult) {
			continue
		}
		if seen[m.ToolID] {
			return nil, fmt.Errorf("duplicate retrieval result identity")
		}
		seen[m.ToolID] = true
		var result struct {
			URL      string `json:"url"`
			Content  string `json:"content"`
			Text     string `json:"text"`
			Markdown string `json:"markdown"`
		}
		if json.Unmarshal(m.ToolResult, &result) != nil {
			continue
		}
		u, err := url.Parse(result.URL)
		if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || strings.TrimSpace(result.Content+result.Text+result.Markdown) == "" {
			continue
		}
		out = append(out, agentSourcePayload{CallID: m.ToolID, Tool: m.ToolName, URL: result.URL, SHA256: fmt.Sprintf("%x", sha256.Sum256(m.ToolResult)), Payload: m.ToolResult})
	}
	return out, nil
}

type agentProvenanceObservation struct {
	ResolverProvider string   `json:"resolver_provider"`
	ResolverModel    string   `json:"resolver_model"`
	AdapterVerified  bool     `json:"adapter_verified"`
	Missing          []string `json:"missing"`
}

func agentObserveProvenance(home agentHomeModel, run store.BotRun) (agentProvenanceObservation, error) {
	p := agentProvenanceObservation{ResolverProvider: run.Provider, ResolverModel: run.Model,
		Missing: []string{"actual-adapter-type", "adapter-request-model", "adapter-request-response-correlation"}}
	if err := agentRunParity(home, run); err != nil {
		return p, err
	}
	// BotRun.Provider/Model are resolver inputs. BotRun.Usage is aggregate usage;
	// neither carries an adapter identity or authenticated provider request ID.
	return p, fmt.Errorf("production BotRun/transcript does not expose actual adapter request/response provenance")
}

type agentComparisonSource struct {
	ID    string `json:"id"`
	AsOf  string `json:"as_of"`
	Units int    `json:"units"`
}

type agentComparisonAnswer struct {
	SelectedSource string   `json:"selected_source"`
	AsOf           string   `json:"as_of"`
	Units          int      `json:"units"`
	Conflict       bool     `json:"conflict"`
	StaleSources   []string `json:"stale_sources"`
	Citations      []string `json:"citations"`
}

// This structured fixture is a test oracle, not a production topic classifier or
// an LLM assessor. Values vary with the invocation; all answer fields are checked.
func agentValidateComparison(raw string, sources []agentComparisonSource) error {
	if len(sources) < 2 {
		return fmt.Errorf("comparison needs multiple retained sources")
	}
	latest := sources[0]
	latestTime, err := time.Parse(time.RFC3339, latest.AsOf)
	if err != nil {
		return err
	}
	ids := map[string]bool{}
	for _, s := range sources {
		if s.ID == "" || ids[s.ID] {
			return fmt.Errorf("ambiguous source identity")
		}
		ids[s.ID] = true
		when, err := time.Parse(time.RFC3339, s.AsOf)
		if err != nil {
			return err
		}
		if when.After(latestTime) {
			latest = s
			latestTime = when
		}
	}
	// Reject duplicate/missing fields and null scalars before typed decoding;
	// otherwise a second value or Go's zero value can hide an unsupported claim.
	fields := json.NewDecoder(strings.NewReader(raw))
	opening, err := fields.Token()
	if err != nil || opening != json.Delim('{') {
		return fmt.Errorf("comparison must be one JSON object")
	}
	required := map[string]bool{"selected_source": true, "as_of": true, "units": true, "conflict": true, "stale_sources": true, "citations": true}
	seen := map[string]bool{}
	for fields.More() {
		key, err := fields.Token()
		if err != nil {
			return err
		}
		name, ok := key.(string)
		if !ok || !required[name] || seen[name] {
			return fmt.Errorf("duplicate or unknown comparison field")
		}
		seen[name] = true
		var value json.RawMessage
		if err := fields.Decode(&value); err != nil {
			return err
		}
		if string(value) == "null" {
			return fmt.Errorf("null comparison field")
		}
	}
	if len(seen) != len(required) {
		return fmt.Errorf("missing comparison fields")
	}
	var a agentComparisonAnswer
	dec := json.NewDecoder(strings.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&a); err != nil {
		return err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return fmt.Errorf("trailing comparison content")
	}
	stale := map[string]bool{}
	conflict := false
	for _, s := range sources {
		when, _ := time.Parse(time.RFC3339, s.AsOf)
		if s.ID != latest.ID && when.Equal(latestTime) {
			return fmt.Errorf("fixture has tied latest sources")
		}
		if when.Before(latestTime) {
			stale[s.ID] = true
		}
		conflict = conflict || s.Units != latest.Units
	}
	if a.SelectedSource != latest.ID || a.AsOf != latest.AsOf || a.Units != latest.Units || a.Conflict != conflict || !agentSameIDs(a.Citations, ids) || !agentSameIDs(a.StaleSources, stale) {
		return fmt.Errorf("comparison contradicts retained sources or omits conflict/staleness/citations")
	}
	return nil
}
func agentSameIDs(got []string, want map[string]bool) bool {
	if len(got) != len(want) {
		return false
	}
	seen := map[string]bool{}
	for _, id := range got {
		if !want[id] || seen[id] {
			return false
		}
		seen[id] = true
	}
	return true
}

func agentValidateRevision(before, after, old, replacement string) error {
	if strings.Count(before, old) != 1 || old == replacement || before == after || after != strings.Replace(before, old, replacement, 1) {
		return fmt.Errorf("published revision did not make exactly the requested change while preserving the existing document")
	}
	return nil
}

func agentRetainedArtifact(run store.BotRun, path, body string) (store.BotArtifact, error) {
	for _, a := range run.Report.Artifacts {
		if a.RelativePath == path && a.ID != uuid.Nil && a.HomeID != uuid.Nil && a.SHA256 == fmt.Sprintf("%x", sha256.Sum256([]byte(body))) {
			return a, nil
		}
	}
	return store.BotArtifact{}, fmt.Errorf("artifact %q lacks matching retained identity/digest", path)
}

// An independent task still shares the Home composer and admission boundary.
// A failed/uncertain send must not be converted into a second task submission.
type agentGeneralSendDependency struct{ pending string }

func (d *agentGeneralSendDependency) begin(key string) { d.pending = key }
func (d *agentGeneralSendDependency) settled()         { d.pending = "" }
func (d *agentGeneralSendDependency) blocker(state employeeComposerState) string {
	if d.pending != "" {
		return "blocked by unresolved preceding send: " + d.pending
	}
	if state.Input != "" {
		return "blocked by preserved unsent Home draft"
	}
	return ""
}

// All writes, including fixture creation, are requested in the native composer.
// Sources are explicitly supplied synthetic business records, never preseeded
// answers to the three live Stock Market News tasks.
func agentGeneralJourneys(page func() *rod.Page, api func(string, string, any) string, state map[string]any, base string, record func(string, string) string, evidence map[string]any, save func()) {
	journeys := map[string]any{}
	evidence["journeys"] = journeys
	botID, err := uuid.Parse(fmt.Sprint(state["bot_id"]))
	must(err)
	botPath := "/api/studio/bots/" + botID.String()
	var bot store.Bot
	must(json.Unmarshal([]byte(record("general-bot.json", api("GET", botPath+"?scope=personal", nil))), &bot))
	if bot.ID != botID || bot.HomeSessionID == uuid.Nil {
		panic("general journey Bot/Home identity mismatch")
	}
	home := base + "/#/chat/bots/personal/" + botID.String()
	initialComposer := employeeComposer(page())
	if initialComposer.Input != "" {
		for _, key := range []string{"document-revision", "conflicting-source-comparison"} {
			journeys[key+"-outcome"] = map[string]any{"status": "blocked-dependent", "blocker": "preserved pre-existing unsent Home draft", "composer": initialComposer}
		}
		save()
		return
	}
	if initialComposer.URL != home {
		page().MustNavigate(home)
	}
	page().MustElement(`[aria-label="Bot workspace"]`).MustVisible()
	capture := func(name, path string) string { return record(name, api("GET", path, nil)) }
	runs := func() []store.BotRun {
		var rows struct {
			Runs []store.BotRun `json:"runs"`
		}
		must(json.Unmarshal([]byte(capture("general-runs.json", botPath+"/runs?scope=personal")), &rows))
		return rows.Runs
	}
	var dependency agentGeneralSendDependency
	send := func(key, message string) store.BotRun {
		dependency.begin(key)
		receipt := map[string]any{"prompt": message, "status": "running"}
		journeys[key] = receipt
		save()
		target := employeeSend(page, home, key, message, func() store.BotSnapshot { return store.BotSnapshot{Bot: bot} }, runs, capture, func() { receipt["attempted"] = true; save() }, func(k string, v any) { receipt[k] = v; save() })
		boundedWait(key+" terminal result", 4*time.Minute, func() bool {
			for _, r := range runs() {
				if r.ID == target.ID {
					target = r
					return r.Status.Terminal()
				}
			}
			return false
		})
		receipt["run"] = target
		stream := capture(key+"-worker-events.txt", botPath+"/sessions/"+target.SessionID.String()+"/stream?scope=personal")
		payloads, sourceErr := agentSourcePayloads(stream)
		receipt["source_payloads"] = payloads
		if sourceErr != nil {
			receipt["source_capture_error"] = sourceErr.Error()
		}
		capture(key+"-history.json", "/api/studio/sessions/"+bot.HomeSessionID.String())
		save()
		must(agentRunResult(target, botID))
		must(agentRunParity(botProviderFromHome(capture, bot.HomeSessionID), target))
		boundedWait(key+" published Home result", time.Minute, func() bool {
			history := capture(key+"-published-history.json", "/api/studio/sessions/"+bot.HomeSessionID.String())
			bubbles := employeeReportBubbles(page())
			receipt["live_bubbles"] = bubbles
			save()
			return employeeReportExactlyOnce(history, bot.HomeSessionID.String(), target, bubbles) == nil
		})
		page().MustReload()
		page().MustElement(`[aria-label="Bot workspace"]`).MustVisible()
		restoredHistory := capture(key+"-restored-history.json", "/api/studio/sessions/"+bot.HomeSessionID.String())
		restoredBubbles := employeeReportBubbles(page())
		receipt["restored_bubbles"] = restoredBubbles
		must(employeeReportExactlyOnce(restoredHistory, bot.HomeSessionID.String(), target, restoredBubbles))
		receipt["status"] = "result-observed"
		dependency.settled()
		save()
		return target
	}
	artifact := func(key string, run store.BotRun, path string) (string, store.BotArtifact) {
		body := capture(key+"-artifact.txt", "/api/studio/artifacts/content?scope=personal&session="+run.SessionID.String()+"&path="+url.QueryEscape(path))
		a, err := agentRetainedArtifact(run, path, body)
		must(err)
		return body, a
	}
	attempt := func(key string, f func()) {
		defer func() {
			if failure := recover(); failure != nil {
				journeys[key+"-outcome"] = map[string]any{"status": "failed", "blocker": fmt.Sprint(failure)}
				save()
			}
		}()
		composer := employeeComposer(page())
		if blocker := dependency.blocker(composer); blocker != "" {
			journeys[key+"-outcome"] = map[string]any{"status": "blocked-dependent", "blocker": blocker, "composer": composer}
			save()
			return
		}
		f()
		journeys[key+"-outcome"] = map[string]any{"status": "verified"}
		save()
	}
	nonce := fmt.Sprint(state["agent_nonce"])
	if len(nonce) != 64 {
		panic("missing current invocation nonce")
	}
	stem := "agent-" + nonce[:12]
	attempt("document-revision", func() {
		path := "reports/" + stem + "-brief.md"
		old := "Review window: 14 days."
		replacement := "Review window: 21 days."
		initial := "# Operations brief\n\nRecord: " + nonce + "\n\n" + old + "\n\nBudget: unchanged.\n"
		seed := send("document-seed", "Save a Markdown artifact at "+path+" with exactly the following content, preserving its final newline. Publish the artifact here. Do not schedule work or change any other files.\n\n"+initial)
		before, prior := artifact("document-before", seed, path)
		if before != initial {
			panic("seed artifact content mismatch")
		}
		revision := send("document-revision", "Open the existing saved artifact "+path+". Replace only `"+old+"` with `"+replacement+"`, preserving every other byte. Save and publish the revised artifact here, not merely a suggested edit. Do not recreate it from this message or change other files.")
		after, next := artifact("document-after", revision, path)
		must(agentValidateRevision(before, after, old, replacement))
		if prior.HomeID != next.HomeID || seed.ID == revision.ID {
			panic("revision lost retained Home or reused run")
		}
		journeys["document-revision-proof"] = map[string]any{"before": prior, "after": next, "seed_run": seed.ID, "revision_run": revision.ID}
	})
	attempt("conflicting-source-comparison", func() {
		n := int(nonce[0]) + 100
		sources := []agentComparisonSource{{ID: stem + "-archive", AsOf: "2024-01-10T00:00:00Z", Units: n}, {ID: stem + "-current", AsOf: "2025-02-20T00:00:00Z", Units: n + 17}, {ID: stem + "-interim", AsOf: "2024-12-01T00:00:00Z", Units: n + 3}}
		raw, err := json.MarshalIndent(sources, "", "  ")
		must(err)
		path := "reports/" + stem + "-sources.json"
		seed := send("comparison-seed", "Save this supplied synthetic inventory source dossier exactly as JSON at "+path+" and publish the artifact. These are test records, not real current inventory. Do not analyze them yet.\n"+string(raw))
		retained, sourceArtifact := artifact("comparison-sources", seed, path)
		var actual []agentComparisonSource
		must(json.Unmarshal([]byte(retained), &actual))
		if !equalAgentSources(actual, sources) {
			panic("saved source dossier differs from supplied records")
		}
		output := "reports/" + stem + "-comparison.json"
		run := send("conflicting-source-comparison", "Read the existing saved source dossier "+path+". Compare every source, identify conflicting units and stale sources, and select the newest dated record, not an average. Publish your complete comparison as a JSON artifact at "+output+" with only these fields: selected_source (source ID), as_of (source timestamp), units (integer), conflict (boolean), stale_sources (all older source IDs), citations (every compared source ID). In Home explain that these are supplied synthetic records and not verified current inventory. Do not alter the source dossier.")
		answer, resultArtifact := artifact("comparison-answer", run, output)
		must(agentValidateComparison(answer, actual))
		unchanged, _ := artifact("comparison-sources-after", seed, path)
		if unchanged != retained {
			panic("comparison changed its sources")
		}
		journeys["comparison-proof"] = map[string]any{"source_artifact": sourceArtifact, "result_artifact": resultArtifact, "source_run": seed.ID, "comparison_run": run.ID, "sources": actual}
	})
}

func equalAgentSources(a, b []agentComparisonSource) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
func botProviderFromHome(capture func(string, string) string, home uuid.UUID) agentHomeModel {
	var model agentHomeModel
	must(json.Unmarshal([]byte(capture("general-model.json", "/api/studio/sessions/"+home.String()+"/model-status?scope=personal")), &model))
	return model
}
