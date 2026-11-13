package launcher

import (
	"encoding/json"
	"sort"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/store"
)

type continuityClaimType string

const (
	continuityFactual         continuityClaimType = "factual"
	continuityExecution       continuityClaimType = "execution"
	continuityNonfactual      continuityClaimType = "nonfactual"
	resumeReceiptLimitation                       = "I cannot confirm schedule application from this conversation."
	continuityProposalAnswer                      = "Proposed, not applied. Please review and confirm after this conversation."
	continuityProposalReceipt                     = "Saved control proposal for review; not applied"
	continuityWeatherPassage                      = `"current_condition":[{"temp_C":"24","weatherDesc":[{"value":"Clear"}]}]`
)

type continuityContentInput struct {
	Items []struct {
		ID   string `json:"id"`
		Text string `json:"text"`
	} `json:"content_items"`
	Sources           map[string]string `json:"content_sources"`
	ExecutionEvidence map[string]string `json:"execution_evidence"`
}

type continuityCitation struct {
	Ref   string `json:"ref"`
	Quote string `json:"quote"`
}

type continuityContentAssessment struct {
	ItemID           string                    `json:"item_id"`
	ClaimType        continuityClaimType       `json:"claim_type"`
	Relevance        store.BotCriterionVerdict `json:"relevance"`
	Support          store.BotCriterionVerdict `json:"support"`
	Explanation      string                    `json:"explanation"`
	Sources          []continuityCitation      `json:"sources"`
	ExecutionSources []continuityCitation      `json:"execution_sources"`
}

// Only the explicitly scripted claims below have known semantics. Unknown prose
// stays unverified; receipts never substitute for external factual evidence.
func continuityContentAssessments(input continuityContentInput) []continuityContentAssessment {
	cite := func(sources map[string]string, passage string, content bool) []continuityCitation {
		refs := make([]string, 0, len(sources))
		for ref := range sources {
			refs = append(refs, ref)
		}
		sort.Strings(refs)
		for _, ref := range refs {
			if (!content || strings.HasPrefix(ref, "source_")) && strings.Contains(sources[ref], passage) {
				return []continuityCitation{{Ref: ref, Quote: passage}}
			}
		}
		return []continuityCitation{}
	}
	assessments := make([]continuityContentAssessment, 0, len(input.Items))
	for _, item := range input.Items {
		a := continuityContentAssessment{ItemID: item.ID, ClaimType: continuityFactual, Relevance: store.BotCriterionSatisfied, Support: store.BotCriterionUnverified, Explanation: "No known captured evidence supports this controlled candidate.", Sources: []continuityCitation{}, ExecutionSources: []continuityCitation{}}
		switch item.Text {
		case homeWeatherAnswer:
			a.Sources = cite(input.Sources, continuityWeatherPassage, true)
			a.Explanation = "The controlled weather response body gives 24 C and Clear for the requested Orlando fetch; neither HTTP status nor a tool receipt supports weather facts."
		case continuityProposalAnswer:
			a.ClaimType = continuityExecution
			a.ExecutionSources = cite(input.ExecutionEvidence, continuityProposalReceipt, false)
			a.Explanation = "The captured control receipt confirms a proposal saved for review, not application; the remaining sentence requests owner review."
		case resumeReceiptLimitation:
			a.ClaimType = continuityNonfactual
			a.Support = store.BotCriterionSatisfied
			a.Explanation = "An honest limitation about absent in-conversation confirmation, not a claim that an out-of-band control was captured or applied."
		}
		if len(a.Sources) != 0 || len(a.ExecutionSources) != 0 {
			a.Support = store.BotCriterionSatisfied
		}
		assessments = append(assessments, a)
	}
	return assessments
}

func TestContinuityContentAssessments(t *testing.T) {
	for _, tc := range []struct {
		name, text        string
		sources, receipts map[string]string
		kind              continuityClaimType
		verdict           store.BotCriterionVerdict
	}{
		{"weather body", homeWeatherAnswer, map[string]string{"source_7": "{" + continuityWeatherPassage + "}"}, nil, continuityFactual, store.BotCriterionSatisfied},
		{"weather no body", homeWeatherAnswer, nil, nil, continuityFactual, store.BotCriterionUnverified},
		{"weather receipt cannot corroborate", homeWeatherAnswer, nil, map[string]string{"observation:0": continuityWeatherPassage}, continuityFactual, store.BotCriterionUnverified},
		{"legacy source ref rejected", homeWeatherAnswer, map[string]string{"observation:0": continuityWeatherPassage}, nil, continuityFactual, store.BotCriterionUnverified},
		{"wrong weather", homeWeatherAnswer, map[string]string{"source_1": `{"temp_C":"30"}`}, nil, continuityFactual, store.BotCriterionUnverified},
		{"proposal receipt", continuityProposalAnswer, nil, map[string]string{"observation:2": continuityProposalReceipt}, continuityExecution, store.BotCriterionSatisfied},
		{"proposal no receipt", continuityProposalAnswer, nil, nil, continuityExecution, store.BotCriterionUnverified},
		{"body cannot attest proposal", continuityProposalAnswer, map[string]string{"source_1": continuityProposalReceipt}, nil, continuityExecution, store.BotCriterionUnverified},
		{"pending cannot attest applied", "The proposal was applied.", nil, map[string]string{"observation:0": continuityProposalReceipt}, continuityFactual, store.BotCriterionUnverified},
		{"honest limitation", resumeReceiptLimitation, nil, nil, continuityNonfactual, store.BotCriterionSatisfied},
		{"unknown factual assertion", "A company grew revenue by 50%.", nil, nil, continuityFactual, store.BotCriterionUnverified},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := json.Marshal(map[string]any{"content_items": []map[string]string{{"id": "body:0:line:0", "text": tc.text}}, "content_sources": tc.sources, "execution_evidence": tc.receipts})
			if err != nil {
				t.Fatal(err)
			}
			var input continuityContentInput
			if err := json.Unmarshal(raw, &input); err != nil {
				t.Fatal(err)
			}
			got := continuityContentAssessments(input)
			if len(got) != 1 || got[0].ItemID != "body:0:line:0" || got[0].ClaimType != tc.kind || got[0].Support != tc.verdict {
				t.Fatalf("assessment=%+v", got)
			}
			for _, citation := range got[0].Sources {
				if !strings.Contains(tc.sources[citation.Ref], citation.Quote) || citation.Quote == "" {
					t.Fatalf("uncaptured body citation: %+v", citation)
				}
			}
			for _, citation := range got[0].ExecutionSources {
				if !strings.Contains(tc.receipts[citation.Ref], citation.Quote) || citation.Quote == "" {
					t.Fatalf("uncaptured receipt citation: %+v", citation)
				}
			}
		})
	}
}
