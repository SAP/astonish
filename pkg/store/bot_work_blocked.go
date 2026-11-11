package store

import (
	"fmt"
	"reflect"
	"strings"
)

// Runtime blockers are observations, never model claims or permission grants.
// Only adapters that establish a dependency is outside authorized Bot tools may
// create one. An unavailable optional facility alone does not block an assignment.
type BotWorkBlockerKind string
type BotWorkResponseOutcome string

// Dependency purpose is reviewed against the original assignment, independently
// of both extraction scope and whether the candidate explanation is acceptable.
type BotWorkDependencyPurpose string

const (
	BotWorkDependencyConfigureDelivery BotWorkDependencyPurpose = "configure_delivery"
	BotWorkDependencySend              BotWorkDependencyPurpose = "send"
	BotWorkDependencySavePreference    BotWorkDependencyPurpose = "save_preference"
	BotWorkDependencyUnrelated         BotWorkDependencyPurpose = "unrelated"
)

const (
	BotWorkBlockerOwnerEmailTransport BotWorkBlockerKind     = "owner_email_transport_unavailable"
	BotWorkResponseSucceeded          BotWorkResponseOutcome = "work_succeeded"
	BotWorkResponseVerifiedBlocker    BotWorkResponseOutcome = "verified_blocker_explanation"
)

type BotWorkBlocker struct {
	ID             string             `json:"id"`
	Kind           BotWorkBlockerKind `json:"kind"`
	Reason         string             `json:"reason"`
	RequiredAction string             `json:"required_action"`
	Scope          string             `json:"scope"`
	ObservationRef string             `json:"observation_ref"`
}

// NewBotOwnerEmailTransportBlocker must only be called from a runtime-owned,
// scoped discovery read, never from candidate/tool response JSON.
func NewBotOwnerEmailTransportBlocker(observationRef string) BotWorkBlocker {
	return BotWorkBlocker{ID: "owner-email-transport", Kind: BotWorkBlockerOwnerEmailTransport,
		Reason:         "The native owner email delivery transport is unavailable.",
		RequiredAction: "An administrator must enable/configure the email channel; Bot tools cannot do this.",
		Scope:          "Native owner email delivery only; no other delivery settings or work are changed by this observation.", ObservationRef: observationRef}
}

// A separate model judgment about the exact candidate text. It cannot satisfy
// original criteria. SourceQuote binds dependency applicability to the original
// assignment, rather than to the model's extracted criterion or candidate prose.
type BotBlockedResponseAssessment struct {
	DependencyPurpose BotWorkDependencyPurpose `json:"dependency_purpose,omitempty"`
	DependencyVerdict BotCriterionVerdict      `json:"dependency_verdict,omitempty"`
	BlockerID         string                   `json:"blocker_id"`
	CriterionIDs      []string                 `json:"criterion_ids"`
	SourceID          string                   `json:"source_id"`
	SourceQuote       string                   `json:"source_quote"`
	Verdict           BotCriterionVerdict      `json:"verdict"`
	Method            BotAssessmentMethod      `json:"method"`
	Explanation       string                   `json:"explanation"`
}

// Safe persisted report projection. Text is the exact reviewed employee response,
// NOT reviewer findings and never an artifact/deliverable acceptance.
type BotWorkBlockedResponse struct {
	CandidateDigest string                       `json:"candidate_digest"`
	Text            string                       `json:"text"`
	Blocker         BotWorkBlocker               `json:"blocker"`
	Assessment      BotBlockedResponseAssessment `json:"assessment"`
}

func (w *BotWorkState) validateBlockers() error {
	if w.Candidate == nil {
		return nil
	}
	if len(w.Candidate.RuntimeBlockers) > 1 {
		return ErrBotInvalid
	}
	for _, b := range w.Candidate.RuntimeBlockers {
		if !reflect.DeepEqual(b, NewBotOwnerEmailTransportBlocker(b.ObservationRef)) {
			return ErrBotInvalid
		}
		found := false
		for i, observation := range w.Candidate.Observations {
			if fmt.Sprintf("observation:%d", i) == b.ObservationRef && observation.Kind == BotEvidenceObservation && observation.Source != "" && observation.ActorID != (BotID{}) && !observation.ObservedAt.IsZero() {
				found = true
			}
		}
		if !found {
			return ErrBotInvalid
		}
	}
	if w.Review == nil || w.Review.BlockedResponse == nil {
		return nil
	}
	a := w.Review.BlockedResponse
	if len(w.Candidate.RuntimeBlockers) != 1 || a.BlockerID != w.Candidate.RuntimeBlockers[0].ID || a.Method != BotAssessmentModel || strings.TrimSpace(a.Explanation) == "" || len(a.Explanation) > 2048 || len(a.SourceQuote) > 2048 || len(a.CriterionIDs) > BotWorkMaxCriteria {
		return ErrBotInvalid
	}
	switch a.Verdict {
	case BotCriterionSatisfied, BotCriterionFailed, BotCriterionUnverified:
	default:
		return ErrBotInvalid
	}
	// Missing fields retain the old current-turn-only contract for persisted
	// reviews. New reviews separate relevance from explanation quality.
	legacy := a.DependencyPurpose == "" && a.DependencyVerdict == ""
	if !legacy {
		switch a.DependencyPurpose {
		case BotWorkDependencyConfigureDelivery, BotWorkDependencySend, BotWorkDependencySavePreference, BotWorkDependencyUnrelated:
		default:
			return ErrBotInvalid
		}
		switch a.DependencyVerdict {
		case BotCriterionSatisfied, BotCriterionFailed, BotCriterionUnverified:
		default:
			return ErrBotInvalid
		}
		if a.DependencyVerdict != BotCriterionSatisfied {
			if a.Verdict == BotCriterionSatisfied {
				return ErrBotInvalid
			}
			return nil
		}
		if a.DependencyPurpose != BotWorkDependencyConfigureDelivery && a.DependencyPurpose != BotWorkDependencySend {
			return ErrBotInvalid
		}
	} else if a.Verdict != BotCriterionSatisfied {
		return nil
	}
	// An independent model judgment must quote the original assignment clause,
	// not the extracted criterion, historical goals, or candidate prose.
	quote := strings.ToLower(a.SourceQuote)
	if a.SourceID != "assignment" || a.SourceQuote == "" || !strings.Contains(w.Contract.Instructions, a.SourceQuote) || !(strings.Contains(quote, "email") || strings.Contains(quote, "e-mail")) || len(a.CriterionIDs) == 0 {
		return ErrBotInvalid
	}
	ids := map[string]bool{}
	for _, id := range a.CriterionIDs {
		if ids[id] {
			return ErrBotInvalid
		}
		ids[id] = true
		found := false
		for _, c := range w.Criteria {
			if c.ID == id && !c.Excluded && c.EffectiveVerificationKind() == BotWorkVerificationModel && (c.EffectiveFulfillmentScope() == BotWorkCriterionCurrentTurn || (!legacy && a.DependencyPurpose == BotWorkDependencyConfigureDelivery && c.EffectiveFulfillmentScope() == BotWorkCriterionFutureTaskDefinition)) {
				for _, source := range c.SourceIDs {
					if source == "assignment" {
						found = true
					}
				}
			}
		}
		if !found {
			return ErrBotInvalid
		}
	}
	for _, c := range w.Review.Criteria {
		if ids[c.CriterionID] && c.Verdict == BotCriterionSatisfied {
			return ErrBotInvalid
		}
	}
	return nil
}

// ConfirmedRequiredBlocker exposes only runtime-owned facts after a valid,
// candidate-bound relevance review. Rejected explanation prose is never returned.
// Legacy combined reviews cannot establish this independent fallback authority.
func (w *BotWorkState) ConfirmedRequiredBlocker() *BotWorkBlocker {
	if w == nil || w.Validate() != nil || w.Candidate == nil || w.Review == nil || w.Review.BlockedResponse == nil {
		return nil
	}
	a := w.Review.BlockedResponse
	if a.DependencyVerdict != BotCriterionSatisfied || (a.DependencyPurpose != BotWorkDependencyConfigureDelivery && a.DependencyPurpose != BotWorkDependencySend) || len(w.Candidate.RuntimeBlockers) != 1 {
		return nil
	}
	blocker := w.Candidate.RuntimeBlockers[0]
	return &blocker
}

// HasVerifiedBlockedResponse is intentionally stricter than a useful-sounding
// explanation: all remaining failed work must be mapped to the real dependency.
// Correctable missing content/format/evidence must continue normal repair.
func (w *BotWorkState) HasVerifiedBlockedResponse() bool {
	if w == nil || w.Candidate == nil || w.Review == nil || w.Review.BlockedResponse == nil || w.Candidate.Digest != w.Candidate.ContentDigest() || w.validateReview() != nil || w.validateBlockers() != nil {
		return false
	}
	a := w.Review.BlockedResponse
	if a.Verdict != BotCriterionSatisfied {
		return false
	}
	ids := map[string]bool{}
	for _, id := range a.CriterionIDs {
		ids[id] = true
	}
	failed := false
	for _, c := range w.Review.Criteria {
		if c.Verdict != BotCriterionSatisfied {
			failed = true
			if !ids[c.CriterionID] {
				return false
			}
		}
	}
	return failed
}

func (w *BotWorkState) VerifiedBlockedResponse() *BotWorkBlockedResponse {
	if w == nil || w.Phase != BotWorkBlocked || w.ResponseOutcome != BotWorkResponseVerifiedBlocker || !w.HasVerifiedBlockedResponse() {
		return nil
	}
	assessment := *w.Review.BlockedResponse
	assessment.CriterionIDs = append([]string(nil), assessment.CriterionIDs...)
	return &BotWorkBlockedResponse{CandidateDigest: w.Candidate.Digest, Text: w.Candidate.Text, Blocker: w.Candidate.RuntimeBlockers[0], Assessment: assessment}
}

// ValidateBotWorkCompletion adds explanation binding without relaxing any of the
// repository's accepted-candidate, lease, revision, artifact or authority gates.
func ValidateBotWorkCompletion(w *BotWorkState, q BotRunCommit) error {
	var supplied *BotWorkBlockedResponse
	var outcome BotWorkResponseOutcome
	if q.Report.Quality != nil {
		supplied = q.Report.Quality.BlockedResponse
		outcome = q.Report.Quality.ResponseOutcome
	}
	expected := w.VerifiedBlockedResponse()
	if !reflect.DeepEqual(supplied, expected) {
		return ErrBotInvalid
	}
	if expected != nil {
		if q.Status != RunPartial || outcome != BotWorkResponseVerifiedBlocker || q.Report.Summary != expected.Text || len(q.Report.Artifacts) != 0 {
			return ErrBotInvalid
		}
	} else if outcome == BotWorkResponseVerifiedBlocker {
		return ErrBotInvalid
	}
	if outcome != "" && outcome != BotWorkResponseVerifiedBlocker && (outcome != BotWorkResponseSucceeded || w == nil || w.Phase != BotWorkAccepted) {
		return ErrBotInvalid
	}
	return nil
}
