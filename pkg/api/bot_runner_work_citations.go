package api

import (
	"github.com/SAP/astonish/pkg/store"
	"strings"
)

const botWorkCitationGuidance = `
EVIDENCE CITATION KEYS: Copy evidence_refs exactly from top-level available_evidence_refs. These are symbolic candidate:<digest>, artifact:<id>:<sha256>, or observation:<zero-based index> keys, NOT requirement source_ids (assignment/charter/goal), URLs, tool-call IDs, or candidate.bodies keys. The candidate key binds the captured text and bodies; an observation key identifies that indexed captured observation. Cite a key only when its actual captured content supports the finding. A URL or body key can be discussed in explanation, never used as evidence_refs. When captured evidence cannot establish a criterion, return unverified with empty evidence_refs; never manufacture support.`

const botWorkUnknownCitationFinding = "Unverified: the assessment cited evidence outside the current candidate inventory. No cited support was accepted for this criterion. Inspect existing captured evidence; do not replay completed effects or pending controls."

// botWorkUnknownCitationCriteria rejects support, not just bad strings. No model
// retry and no substitution of an arbitrary candidate reference is performed.
// Invalid verdict/explanation/method remain untouched so ordinary validation
// still fails closed rather than having those errors hidden by the downgrade.
func botWorkUnknownCitationCriteria(w *store.BotWorkState, review *store.BotWorkReview) []int {
	valid := map[string]bool{}
	for _, ref := range w.Candidate.AvailableEvidenceRefs() {
		valid[ref] = true
	}
	var affected []int
	for i, c := range review.Criteria {
		if c.Method != store.BotAssessmentModel || strings.TrimSpace(c.Explanation) == "" {
			continue
		}
		switch c.Verdict {
		case store.BotCriterionSatisfied, store.BotCriterionFailed, store.BotCriterionUnverified:
		default:
			continue
		}
		for _, ref := range c.EvidenceRefs {
			if !valid[ref] {
				affected = append(affected, i)
				break
			}
		}
	}
	botWorkRejectUnknownCitations(review, affected)
	return affected
}

// Run again after mechanical checks: a citation rejection must not be promoted
// into a supported verdict by an unrelated mechanical metadata check.
func botWorkHasRejectedCitations(review *store.BotWorkReview) bool {
	for _, c := range review.Criteria {
		if c.Verdict == store.BotCriterionUnverified && c.Explanation == botWorkUnknownCitationFinding {
			return true
		}
	}
	return false
}

func botWorkRejectUnknownCitations(review *store.BotWorkReview, affected []int) {
	for _, i := range affected {
		review.Criteria[i].Verdict = store.BotCriterionUnverified
		review.Criteria[i].Method = store.BotAssessmentModel
		review.Criteria[i].EvidenceRefs = []string{}
		review.Criteria[i].Explanation = botWorkUnknownCitationFinding
	}
}
