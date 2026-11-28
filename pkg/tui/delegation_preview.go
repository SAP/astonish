package tui

import "github.com/charmbracelet/x/ansi"

// Preserve existing SGR styling and avoid lipgloss reprocessing whole documents.
func wrapDelegationStyled(s string, width int) string { return ansi.Hardwrap(s, max(1, width), true) }

// delegationPreviewValue bounds diagnostic work before JSON serialization or
// wrapping. Stored tool results remain untouched; this is only an opt-in preview.
func delegationPreviewValue(v any, depth int) any {
	if depth > 3 {
		return "…"
	}
	switch x := v.(type) {
	case string:
		if len(x) > 2048 {
			return x[:2048] + "\n… (preview clipped)"
		}
		return x
	case map[string]any:
		out := make(map[string]any)
		n := 0
		for k, value := range x {
			if n == 16 {
				out["…"] = "preview clipped"
				break
			}
			out[k] = delegationPreviewValue(value, depth+1)
			n++
		}
		return out
	case []any:
		out := make([]any, 0, 17)
		for i, value := range x {
			if i == 16 {
				out = append(out, "… (preview clipped)")
				break
			}
			out = append(out, delegationPreviewValue(value, depth+1))
		}
		return out
	case nil, bool, float64, float32, int, int64, int32, uint64:
		return x
	default:
		return "(structured output)"
	}
}
