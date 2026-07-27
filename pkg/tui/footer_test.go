package tui

import "testing"

func TestModelFooterTextShowsProviderAndConcreteModel(t *testing.T) {
	got := modelFooterText("sap-ai-core", "anthropic--claude-3.7-sonnet")
	want := "sap-ai-core / anthropic--claude-3.7-sonnet"
	if got != want {
		t.Fatalf("modelFooterText() = %q, want %q", got, want)
	}
}

func TestModelFooterTextDoesNotShowDefaultAsModel(t *testing.T) {
	got := modelFooterText("sap-ai-core", "default")
	want := "sap-ai-core / model resolving…"
	if got != want {
		t.Fatalf("modelFooterText() = %q, want %q", got, want)
	}
}

func TestModelFooterTextWaitsForProviderAndModel(t *testing.T) {
	got := modelFooterText("", "")
	want := "Provider/model loading…"
	if got != want {
		t.Fatalf("modelFooterText() = %q, want %q", got, want)
	}
}
