package bot

import "google.golang.org/genai"

// ModelUsageTokens converts cumulative provider metadata to billable input and
// output counts, matching BudgetLLM's accounting. ADK thoughts are separate from
// candidates; total tokens can include output omitted from those fields. Do not
// add the total again, infer usage from visible text, or sum repeated snapshots.
// Invalid negative fields cannot reduce usage, and widening before addition
// keeps even maximum int32 counters from overflowing.
func ModelUsageTokens(m *genai.GenerateContentResponseUsageMetadata) (input, output int64) {
	if m == nil {
		return 0, 0
	}
	input = max(int64(m.PromptTokenCount), 0) + max(int64(m.ToolUsePromptTokenCount), 0)
	output = max(int64(m.CandidatesTokenCount), 0) + max(int64(m.ThoughtsTokenCount), 0)
	output = max(output, int64(m.TotalTokenCount)-input)
	return input, output
}
