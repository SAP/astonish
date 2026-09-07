package sandbox

import "strings"

// SanitizeInstanceName replaces characters that are invalid in sandbox
// instance names with hyphens and collapses consecutive hyphens.
func SanitizeInstanceName(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	b.Grow(len(s))
	prevHyphen := true
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			prevHyphen = false
		} else if !prevHyphen {
			b.WriteByte('-')
			prevHyphen = true
		}
	}
	return strings.TrimRight(b.String(), "-")
}

func sanitizeInstanceName(s string) string { return SanitizeInstanceName(s) }

func safeShortID(id string, maxLen int) string {
	if len(id) <= maxLen {
		return id
	}
	return id[:maxLen]
}
