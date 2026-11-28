package sandbox

import "testing"

func TestBotHomeArtifactPaths(t *testing.T) {
	for _, p := range []string{"", ".", "..", "../escape", "/root/file", "a/../file", "a//b", "a/./b", "a\\b", "a\x00b", "a\nb"} {
		if ValidateBotHomeArtifactPath(p) == nil {
			t.Errorf("accepted %q", p)
		}
	}
	for _, p := range []string{"results/report.txt", "file with spaces", "-flag"} {
		if err := ValidateBotHomeArtifactPath(p); err != nil {
			t.Errorf("rejected %q: %v", p, err)
		}
	}
}
