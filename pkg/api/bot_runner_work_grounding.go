package api

import (
	"regexp"

	"github.com/SAP/astonish/pkg/store"
)

// Field vocabulary is a necessary condition for selecting a mechanical parser,
// not proof that a field is requested. Applicability, negation, temporal scope,
// and semantics remain the independent assessor's responsibility. In particular,
// freshness, sources, and factual honesty alone do not request metadata fields.
var botWorkURLRequirement = regexp.MustCompile(`(?i)\b(urls?|links?|hyperlinks?|web addresses?)\b`)
var botWorkRetrievalRequirement = regexp.MustCompile(`(?i)\b(retrieval[\s_-]+(?:times?|timestamps?|dates?)|(?:fetch|fetched|retrieved|accessed|access)[\s_-]+(?:at|on|times?|timestamps?|dates?)|(?:times?|timestamps?|dates?)[\s_-]+(?:of[\s_-]+)?(?:retrieval|fetch|access)|when[\s_-]+(?:(?:you|it|they|was|were)[\s_-]+)*(?:fetched|retrieved|accessed))\b`)

// botWorkMechanicalRequirementGrounded inspects only original requirement text
// belonging to this criterion's pinned source IDs. Never use the extracted Text,
// filenames, IDs, tool observations, goal evidence, or unrelated sources to
// manufacture a field requirement. No criterion or original source is removed.
func botWorkMechanicalRequirementGrounded(contract store.BotWorkContract, criterion store.BotWorkCriterion) bool {
	var field *regexp.Regexp
	switch criterion.EffectiveVerificationKind() {
	case store.BotWorkVerificationSourceURL:
		field = botWorkURLRequirement
	case store.BotWorkVerificationRetrievalTime:
		field = botWorkRetrievalRequirement
	default:
		return false
	}
	for _, id := range criterion.SourceIDs {
		switch id {
		case "assignment":
			if field.MatchString(contract.Instructions) {
				return true
			}
			if contract.Skill != nil {
				for _, expectation := range contract.Skill.Snapshot.Expectations {
					if expectation.Provenance.Level == store.BotSkillOwnerSpecified && field.MatchString(expectation.Text) {
						return true
					}
				}
			}
		case "charter":
			if field.MatchString(contract.Charter) {
				return true
			}
		default:
			for _, goal := range contract.Goals {
				if id == "goal:"+goal.ID.String() && (field.MatchString(goal.Title) || field.MatchString(goal.Description) || field.MatchString(goal.CompletionCriteria)) {
					return true
				}
			}
			for _, attachment := range contract.Attachments {
				if id == attachment.SourceID && attachment.Blocker == "" && field.MatchString(attachment.Text) {
					return true
				}
			}
		}
	}
	return false
}
