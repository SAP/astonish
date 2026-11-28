package api

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/SAP/astonish/pkg/store"
)

// These parsers recognize output fields, never infer requirements from prose.
var botCitationURL = regexp.MustCompile(`https?://[^\s<>"` + "`" + `\[\]|)]+`)
var botRetrievalLabel = regexp.MustCompile(`(?i)\b(retrieval(?:[ _-]+(?:time|timestamp))?|retrieved(?:[ _-]+at)?|fetched(?:[ _-]+at)?|fetch[ _-]+time)\b`)
var botRetrievalStamp = regexp.MustCompile(`\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}(?::\d{2}(?:\.\d+)?)?(?:Z|[ ]*UTC|[+-]\d{2}:\d{2})`)
var botRetrievalUTCStamp = regexp.MustCompile(`\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}(?::\d{2}(?:\.\d+)?)?(?:Z|[ ]*UTC|[+-]\d{2}:\d{2})?`)

type botRetrievalField struct {
	value string
	utc   bool
}

// A keyword in a sentence is not a metadata field. Require a colon or a
// Markdown cell boundary after the label (allowing formatting/qualifiers).
// In particular, never consume a neighboring measurement column as retrieval.
// Narrative claims remain subject to the independent model assessment.
func botRetrievalFields(line string) []botRetrievalField {
	var fields []botRetrievalField
	for _, label := range botRetrievalLabel.FindAllStringIndex(line, -1) {
		// Labels must begin a field, not occur inside a heading such as
		// "Measurement vs. fetch time:". Markdown cells and list/formatting
		// prefixes are field boundaries; prose before a label is not.
		prefix := line[:label[0]]
		if cell := strings.LastIndex(prefix, "|"); cell >= 0 {
			prefix = prefix[cell+1:]
		}
		if strings.Trim(prefix, " \t*`_>-+") != "" {
			continue
		}
		rest := strings.TrimLeft(line[label[1]:], " *`_")
		utc := false
		if strings.HasPrefix(rest, "(") {
			end := strings.Index(rest, ")")
			if end < 0 {
				continue
			}
			utc = strings.EqualFold(strings.TrimSpace(rest[1:end]), "UTC")
			rest = strings.TrimLeft(rest[end+1:], " *`_")
		}
		if len(rest) == 0 || (rest[0] != ':' && rest[0] != '|') {
			continue
		}
		value := rest[1:]
		if end := strings.Index(value, "|"); end >= 0 {
			value = value[:end]
		}
		fields = append(fields, botRetrievalField{value: value, utc: utc})
	}
	return fields
}

func botPublishedBodies(c *store.BotWorkCandidate) []string {
	bodies := []string{c.Text}
	for _, a := range c.Artifacts {
		bodies = append(bodies, c.Bodies[a.ID.String()])
	}
	return bodies
}
func botURLs(text string) []string {
	var out []string
	for _, raw := range botCitationURL.FindAllString(text, -1) {
		raw = strings.TrimRight(raw, ".,;")
		u, err := url.Parse(raw)
		if err == nil && u.Host != "" && !strings.Contains(raw, "...") && !strings.Contains(raw, "…") {
			out = append(out, raw)
		}
	}
	return out
}
func botFetchObservations(c *store.BotWorkCandidate) map[string][]int {
	out := map[string][]int{}
	for i, o := range c.Observations {
		if o.Kind != store.BotEvidenceObservation || o.ObservedAt.IsZero() {
			continue
		}
		u, err := url.Parse(o.Source)
		if err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != "" {
			out[o.Source] = append(out[o.Source], i)
		}
	}
	return out
}

// applyBotMechanicalChecks is called only AFTER the untrusted model response has
// passed identity, coverage, reference, and model-only method validation. It never
// upgrades a negative semantic judgment. Tool responses/private drafts cannot
// satisfy fields that must appear in the actual published deliverable.
func applyBotMechanicalChecks(w *store.BotWorkState, review *store.BotWorkReview) {
	byID := map[string]store.BotWorkCriterion{}
	for _, c := range w.Criteria {
		byID[c.ID] = c
	}
	for i := range review.Criteria {
		a := &review.Criteria[i]
		c := byID[a.CriterionID]
		if c.Excluded || c.EffectiveFulfillmentScope() != store.BotWorkCriterionCurrentTurn || c.EffectiveVerificationKind() == store.BotWorkVerificationModel || !botWorkMechanicalRequirementGrounded(w.Contract, c) {
			continue
		}
		verdict, refs, finding := botCheckMetadata(w.Candidate, c.EffectiveVerificationKind())
		if verdict == store.BotCriterionSatisfied && a.Verdict != store.BotCriterionSatisfied {
			continue
		}
		a.Verdict, a.Method, a.EvidenceRefs, a.Explanation = verdict, store.BotAssessmentMechanical, refs, finding
	}
}

func botCheckMetadata(c *store.BotWorkCandidate, kind store.BotWorkVerificationKind) (store.BotCriterionVerdict, []string, string) {
	refs := []string{"candidate:" + c.Digest}
	observed := botFetchObservations(c)
	if len(observed) == 0 {
		return store.BotCriterionUnverified, refs, "Mechanical metadata check: no recorded HTTP source/fetch observation; do not invent source evidence."
	}
	bodies := botPublishedBodies(c)
	if kind == store.BotWorkVerificationSourceURL {
		for _, body := range bodies {
			for _, u := range botURLs(body) {
				if ids := observed[u]; len(ids) > 0 {
					return store.BotCriterionSatisfied, append(refs, fmt.Sprintf("observation:%d", ids[0])), "Mechanical source URL check: complete URL, including query, matches recorded observation."
				}
			}
		}
		return store.BotCriterionFailed, refs, "Mechanical source URL check: include a complete recorded source URL with its exact query; endpoint-only, abbreviated, or private tool-response citations do not satisfy this field."
	}
	checked := 0
	for _, body := range bodies {
		allURLs := botURLs(body)
		// A document with a single distinct cited source is unambiguous even when
		// the source follows its retrieval field. Otherwise require a local source.
		unique := map[string]bool{}
		for _, u := range allURLs {
			unique[u] = true
		}
		current := ""
		if len(unique) == 1 {
			for u := range unique {
				current = u
			}
		}
		for _, line := range strings.Split(body, "\n") {
			urls := botURLs(line)
			if len(urls) == 1 {
				current = urls[0]
			} else if len(urls) > 1 {
				current = ""
			}
			for _, field := range botRetrievalFields(line) {
				stamps := botRetrievalStamp.FindAllString(field.value, -1)
				if field.utc {
					stamps = botRetrievalUTCStamp.FindAllString(field.value, -1)
					for i, stamp := range stamps {
						if !botRetrievalStamp.MatchString(stamp) {
							stamps[i] = stamp + "Z"
						}
					}
				}
				if len(stamps) == 0 {
					return store.BotCriterionFailed, refs, "Mechanical retrieval-time check: a labeled retrieval field has no supported explicit timestamp; use YYYY-MM-DDTHH:MMZ."
				}
				ids := observed[current]
				if len(ids) == 0 {
					return store.BotCriterionUnverified, refs, "Mechanical retrieval-time check: associate each retrieval field with a complete recorded source URL (on the same line or preceding source field)."
				}
				for _, stamp := range stamps {
					stamp = strings.ReplaceAll(stamp, " UTC", "Z")
					stamp = strings.ReplaceAll(stamp, "UTC", "Z")
					stamp = strings.Replace(stamp, " ", "T", 1)
					var parsed time.Time
					for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04Z07:00"} {
						var err error
						parsed, err = time.Parse(layout, stamp)
						if err == nil {
							break
						}
					}
					matched := -1
					for _, id := range ids {
						if !parsed.IsZero() && parsed.UTC().Truncate(time.Minute).Equal(c.Observations[id].ObservedAt.UTC().Truncate(time.Minute)) {
							matched = id
							break
						}
					}
					if matched < 0 {
						return store.BotCriterionFailed, append(refs, fmt.Sprintf("observation:%d", ids[len(ids)-1])), fmt.Sprintf("Mechanical retrieval-time check: reported %s does not match the associated recorded fetch minute; use %s (UTC), not a source measurement timestamp.", stamp, c.Observations[ids[len(ids)-1]].ObservedAt.UTC().Format("2006-01-02T15:04Z"))
					}
					refs = append(refs, fmt.Sprintf("observation:%d", matched))
					checked++
				}
			}
		}
	}
	if checked == 0 {
		return store.BotCriterionFailed, refs, "Mechanical retrieval-time check: include an explicit Retrieval time: YYYY-MM-DDTHH:MMZ field associated with the complete source URL; a source measurement timestamp is not a retrieval field."
	}
	return store.BotCriterionSatisfied, refs, "Mechanical retrieval-time check: every parsed retrieval timestamp matches its associated recorded fetch minute."
}
