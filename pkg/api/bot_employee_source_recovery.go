package api

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"

	"github.com/SAP/astonish/pkg/store"
)

// Recovery is a bounded navigation aid from exact run-local captures, not an
// evidence assessment, transport attestation, or authorization grant.
type botEmployeeRecoveryLead struct {
	URL           string `json:"url"`
	Context       string `json:"context"`
	CaptureRef    string `json:"capture_ref"`
	CaptureDigest string `json:"capture_sha256"`
	Path          string `json:"path"`
}
type botEmployeeRecoverySource struct {
	Access  store.BotSourceAccessFact `json:"access"`
	Excerpt string                    `json:"captured_excerpt,omitempty"`
}
type botEmployeeRecoveryContext struct {
	UntriedLeads    []botEmployeeRecoveryLead   `json:"untried_discovered_urls"`
	FailedSources   []botEmployeeRecoverySource `json:"failed_sources"`
	ReturnedSources []botEmployeeRecoverySource `json:"returned_sources_usefulness_unassessed"`
	Truncated       bool                        `json:"truncated,omitempty"`
}

var botEmployeeRecoveryURL = regexp.MustCompile(`https?://[^\s<>"\x60\\]+`)

func botEmployeeRecoveryURLKey(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return ""
	}
	u.Fragment = ""
	return u.String()
}

func botEmployeeSourceRecovery(w *store.BotWorkState) (botEmployeeRecoveryContext, error) {
	out := botEmployeeRecoveryContext{UntriedLeads: []botEmployeeRecoveryLead{}, FailedSources: []botEmployeeRecoverySource{}, ReturnedSources: []botEmployeeRecoverySource{}}
	if w == nil || w.Candidate == nil || w.AgentWork == nil {
		return out, nil
	}
	// Only server-captured evidence with its original run/ref/digest may supply
	// clues. Arbitrary candidate notes and remote JSON metadata are excluded.
	bodies := map[string]string{}
	bound := map[string]store.BotWorkEvidenceReference{}
	for _, ref := range w.AgentWork.Evidence {
		key := "tool-response:" + ref.CallID
		body, ok := w.Candidate.Bodies[key]
		if !ok || ref.CallID == "" || ref.Ref != "capture:"+ref.CallID || ref.RunID != w.Contract.RunID || botAgentDigest([]byte(body)) != ref.Digest {
			continue
		}
		bodies[key] = body
		bound[ref.CallID] = ref
	}
	in := botWorkContentInput{Sources: map[string]string{}, Origins: map[string]botContentSourceOrigin{}, ExecutionEvidence: map[string]string{}}
	if err := botContentSourceViews(&in, bodies); err != nil {
		return out, err
	}
	// Configured search captures use answer/citations/search_results rather than
	// fetched content. Decode only that known discovery shape; never recursively
	// mine metadata or turn these URLs into decisive content evidence.
	searchViews := botContentViews{in: &in, count: len(in.Sources)}
	for _, text := range in.Sources {
		searchViews.bytes += len(text)
	}
	keys := make([]string, 0, len(bodies))
	for key := range bodies {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		var envelope map[string]any
		if json.Unmarshal([]byte(bodies[key]), &envelope) != nil {
			continue
		}
		if _, ok := envelope["query"].(string); !ok {
			continue
		}
		if _, ok := envelope["provider"].(string); !ok {
			continue
		}
		searchViews.captureRef, searchViews.captureDigest = key, botAgentDigest([]byte(bodies[key]))
		if text, ok := envelope["answer"].(string); ok {
			if err := searchViews.addText("#json:/answer", text); err != nil {
				return out, err
			}
		}
		if citations, ok := envelope["citations"].([]any); ok {
			for i, value := range citations {
				if text, ok := value.(string); ok {
					if err := searchViews.addText(fmt.Sprintf("#json:/citations/%d", i), text); err != nil {
						return out, err
					}
				}
			}
		}
		if hits, ok := envelope["search_results"].([]any); ok {
			for i, value := range hits {
				hit, ok := value.(map[string]any)
				if !ok {
					continue
				}
				for _, field := range []string{"url", "title"} {
					if text, ok := hit[field].(string); ok {
						if err := searchViews.addText(fmt.Sprintf("#json:/search_results/%d/%s", i, field), text); err != nil {
							return out, err
						}
					}
				}
			}
		}
	}
	refs := make([]string, 0, len(in.Sources))
	for ref := range in.Sources {
		refs = append(refs, ref)
	}
	sort.Strings(refs)
	excerpts := map[string]string{}
	for _, ref := range refs {
		origin := in.Origins[ref]
		if origin.Path != origin.BasePath {
			continue
		} // Do not duplicate presentation aliases.
		prior := excerpts[origin.CaptureRef]
		text := in.Sources[ref]
		if len(prior) < 1024 {
			if len(text) > 1024-len(prior) {
				text = text[:1024-len(prior)]
			}
			excerpts[origin.CaptureRef] = prior + text
		}
	}
	attempted := map[string]bool{}
	facts := append([]store.BotSourceAccessFact(nil), w.AgentWork.SourceAccess...)
	sort.SliceStable(facts, func(i, j int) bool { return facts[i].ObservedAt.Before(facts[j].ObservedAt) })
	for _, fact := range facts {
		ref, ok := bound[fact.CallID]
		if !ok || fact.EvidenceRef != ref.Ref || fact.BodyDigest != ref.Digest {
			continue
		}
		if fact.Status != store.BotSourceAccessFailed && fact.Status != store.BotSourceAccessReturned {
			continue
		}
		for _, raw := range []string{fact.OriginalURL, fact.FinalURL} {
			if key := botEmployeeRecoveryURLKey(raw); key != "" {
				attempted[key] = true
			}
		}
		item := botEmployeeRecoverySource{Access: fact, Excerpt: excerpts["tool-response:"+fact.CallID]}
		if len(out.FailedSources)+len(out.ReturnedSources) >= 64 {
			out.Truncated = true
			continue
		}
		if fact.Status == store.BotSourceAccessFailed {
			out.FailedSources = append(out.FailedSources, item)
		} else {
			out.ReturnedSources = append(out.ReturnedSources, item)
		}
	}
	seen := map[string]bool{}
	for _, ref := range refs {
		origin := in.Origins[ref]
		if origin.Path != origin.BasePath {
			continue
		}
		text := in.Sources[ref]
		for _, loc := range botEmployeeRecoveryURL.FindAllStringIndex(text, -1) {
			raw := strings.TrimRight(text[loc[0]:loc[1]], ").,;!?]}")
			key := botEmployeeRecoveryURLKey(raw)
			if key == "" || len(raw) > 2048 || attempted[key] || seen[key] {
				continue
			}
			seen[key] = true
			if len(out.UntriedLeads) >= 32 {
				out.Truncated = true
				continue
			}
			start, end := loc[0]-160, loc[1]+160
			if start < 0 {
				start = 0
			}
			if end > len(text) {
				end = len(text)
			}
			out.UntriedLeads = append(out.UntriedLeads, botEmployeeRecoveryLead{URL: raw, Context: text[start:end], CaptureRef: origin.CaptureRef, CaptureDigest: origin.CaptureDigest, Path: origin.BasePath})
		}
	}
	return out, nil
}
