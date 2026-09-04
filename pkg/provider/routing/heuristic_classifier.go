package routing

import "strings"

// HeuristicClassifier uses prompt analysis heuristics to score complexity.
// It is the default classifier for Auto model routing — pure Go, no external
// dependencies, no API calls.
type HeuristicClassifier struct{}

// Classifier weights (must sum to 1.0).
const (
	weightLength  = 0.25
	weightKeyword = 0.40
	weightTool    = 0.15
	weightDepth   = 0.20
)

// Keyword tiers.
var (
	highComplexityKeywords = []string{
		"refactor", "architect", "redesign", "security audit", "migration",
		"optimize", "performance", "concurrent", "distributed",
		"cross-cutting", "breaking change", "plan mode",
	}
	mediumComplexityKeywords = []string{
		"implement", "create", "build", "fix bug", "debug",
		"write tests", "code review", "analyze", "compare",
	}
	lowComplexityKeywords = []string{
		"list", "show", "what is", "how to", "explain",
		"read", "find", "search", "look up", "help", "hello", "hi",
	}
	// Tools that signal complex, planning-heavy tasks.
	complexTools = []string{"announce_plan", "codegraph_explore", "delegate_tasks"}
	// Tools that signal simple, read-only tasks.
	readOnlyTools = []string{"read_file", "grep_search", "find_files", "file_tree", "code_definition", "code_references"}
)

// Classify implements ComplexityClassifier.
func (h *HeuristicClassifier) Classify(prompt string, ctx ClassifierContext) ComplexityScore {
	ls := lengthScore(prompt)
	ks := keywordScore(prompt)
	ts := toolScore(ctx.ToolNames)
	ds := depthScore(ctx)

	score := weightLength*ls + weightKeyword*ks + weightTool*ts + weightDepth*ds
	if score < 0 {
		score = 0
	}
	if score > 1 {
		score = 1
	}
	return ComplexityScore(score)
}

func lengthScore(prompt string) float64 {
	n := float64(len(prompt))
	if n <= 200 {
		return 0.0
	}
	if n >= 4000 {
		return 1.0
	}
	return (n - 200) / 3800
}

func keywordScore(prompt string) float64 {
	lower := strings.ToLower(prompt)
	for _, kw := range highComplexityKeywords {
		if strings.Contains(lower, kw) {
			return 1.0
		}
	}
	for _, kw := range mediumComplexityKeywords {
		if strings.Contains(lower, kw) {
			return 0.6
		}
	}
	for _, kw := range lowComplexityKeywords {
		if strings.Contains(lower, kw) {
			return 0.2
		}
	}
	return 0.4
}

func toolScore(tools []string) float64 {
	if len(tools) == 0 {
		return 0.3
	}
	hasComplex := false
	hasReadOnly := false
	for _, t := range tools {
		for _, ct := range complexTools {
			if t == ct {
				hasComplex = true
				break
			}
		}
		for _, rt := range readOnlyTools {
			if t == rt {
				hasReadOnly = true
				break
			}
		}
	}
	switch {
	case hasComplex:
		return 0.8
	case hasReadOnly && !hasComplex:
		return 0.2
	default:
		return 0.5
	}
}

func depthScore(ctx ClassifierContext) float64 {
	if ctx.HasPlanMode {
		return 0.9
	}
	switch {
	case ctx.ConversationTurns == 0:
		return 0.1
	case ctx.ConversationTurns <= 3:
		return 0.3
	case ctx.ConversationTurns <= 10:
		return 0.6
	default:
		return 0.8
	}
}
