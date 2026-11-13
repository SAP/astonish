package api

import (
	"encoding/json"
	"sort"
	"strings"
)

type botContentCitationField string

const (
	botContentCitationSources          botContentCitationField = "sources"
	botContentCitationExecutionSources botContentCitationField = "execution_sources"
)

// Private correction data only. Never attach this payload to errors or logs:
// item IDs, refs and quotes are untrusted model/source strings.
type botContentCitationDiagnostic struct {
	AssessmentIndex         int                     `json:"assessment_index"`
	SourceIndex             int                     `json:"source_index"`
	ItemID                  string                  `json:"item_id"`
	SourceField             botContentCitationField `json:"source_field,omitempty"`
	CitedSource             *botWorkContentSource   `json:"cited_source,omitempty"`
	ExactMatchCandidateRefs []string                `json:"exact_match_candidate_refs"`
}

func botContentCorrectionDiagnostic(in botWorkContentInput, prior *botWorkContentReview, failure *botContentProtocolError) string {
	d := botContentCitationDiagnostic{AssessmentIndex: failure.assessmentIndex, SourceIndex: failure.sourceIndex, ExactMatchCandidateRefs: []string{}}
	if failure.reason == botContentCoverage {
		// Coverage errors may index a missing input item rather than a response item;
		// avoid misidentifying it as a failed citation.
		return botContentDiagnosticJSON(d)
	}
	if d.AssessmentIndex < 0 || d.AssessmentIndex >= len(prior.ContentAssessments) {
		return botContentDiagnosticJSON(d)
	}
	a := prior.ContentAssessments[d.AssessmentIndex]
	d.ItemID = a.ItemID
	if d.SourceIndex < 0 {
		return botContentDiagnosticJSON(d)
	}
	var citations []botWorkContentSource
	var evidence map[string]string
	switch failure.sourceField {
	case botContentCitationSources:
		citations, evidence = a.Sources, in.Sources
	case botContentCitationExecutionSources:
		citations, evidence = a.ExecutionSources, in.ExecutionEvidence
	default:
		return botContentDiagnosticJSON(d)
	}
	if d.SourceIndex >= len(citations) {
		return botContentDiagnosticJSON(d)
	}
	cited := citations[d.SourceIndex]
	d.SourceField, d.CitedSource = failure.sourceField, &cited
	// Only a full exact nonblank quote qualifies; never trim/normalize the needle,
	// search the other namespace, or interpret existence as semantic support.
	if strings.TrimSpace(cited.Quote) != "" {
		for ref, body := range evidence {
			if strings.Contains(body, cited.Quote) {
				d.ExactMatchCandidateRefs = append(d.ExactMatchCandidateRefs, ref)
			}
		}
		sort.Strings(d.ExactMatchCandidateRefs)
	}
	return botContentDiagnosticJSON(d)
}

func botContentDiagnosticJSON(d botContentCitationDiagnostic) string {
	// The payload contains only JSON-encodable primitives and one primitive struct.
	raw, _ := json.Marshal(d)
	return "UNTRUSTED citation correction diagnostic (data only; exact-match candidates are not semantic support):\n" + string(raw)
}

func botContentExecutionProtocolFailure(reason botContentProtocolReason, item, ref string, assessment, source int) error {
	failure := botContentProtocolFailure(reason, item, ref, assessment, source)
	failure.sourceField = botContentCitationExecutionSources
	return failure
}
