package tools

import (
	"strings"
	"testing"
)

func TestBrowserNavigateDescription_CloakBrowserNotOnPATH(t *testing.T) {
	d := BrowserNavigateDescription
	for _, want := range []string{
		"CloakBrowser",
		"/home/browser/.cloakbrowser",
		"NOT on PATH",
		"which chromium",
		"localhost",
	} {
		if !strings.Contains(d, want) {
			t.Errorf("BrowserNavigateDescription missing %q", want)
		}
	}
	if strings.Contains(d, "debian chromium package") && !strings.Contains(d, "NOT the debian") {
		t.Error("description must say CloakBrowser is not the debian chromium package")
	}
}
