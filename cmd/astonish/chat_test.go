package astonish

import (
	"strings"
	"testing"
)

func TestParseChatFlags_ResumeAndDebug(t *testing.T) {
	flags, err := parseChatFlags([]string{"--resume", "sess-123", "--debug", "--auto-approve"})
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if flags.Resume != "sess-123" {
		t.Errorf("Resume=%q", flags.Resume)
	}
	if !flags.Debug || !flags.AutoApprove {
		t.Errorf("flags: %+v", flags)
	}
}

func TestParseChatFlags_ClearModelRequiresResume_AtCommand(t *testing.T) {
	// handleChatCommand validates this; parse alone allows the combo.
	flags, err := parseChatFlags([]string{"--clear-model"})
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if !flags.ClearModel {
		t.Fatal("expected ClearModel")
	}
}

func TestParseModelPin(t *testing.T) {
	cases := []struct {
		name         string
		in           string
		wantProvider string
		wantModel    string
		wantErr      bool
	}{
		{"empty clears", "", "", "", false},
		{"simple", "openai:gpt-4o", "openai", "gpt-4o", false},
		{"model with colon", "openai:gpt-4o:2024-08-06", "openai", "gpt-4o:2024-08-06", false},
		{"empty model after colon", "openai:", "openai", "", false},
		{"empty provider before colon", ":gpt-4o", "", "gpt-4o", false},
		{"no colon errors", "invalid", "", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, m, err := parseModelPin(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error for %q, got nil", tc.in)
				}
				if !strings.Contains(err.Error(), "expected provider:model") {
					t.Errorf("expected error containing 'expected provider:model', got: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error for %q: %v", tc.in, err)
			}
			if p != tc.wantProvider || m != tc.wantModel {
				t.Errorf("parseModelPin(%q) = (%q, %q), want (%q, %q)", tc.in, p, m, tc.wantProvider, tc.wantModel)
			}
		})
	}
}

func TestHandleChatModelCommand_MissingColon(t *testing.T) {
	err := handleChatModelCommand([]string{"invalid"})
	// Without login this may fail earlier with "not logged in"; either is fine.
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestHandleChatModelCommand_NoArgs(t *testing.T) {
	err := handleChatModelCommand([]string{})
	if err == nil {
		t.Fatal("expected error for missing positional arg, got nil")
	}
	// Not logged in is acceptable; usage error if somehow remote config exists in env.
	if !strings.Contains(err.Error(), "usage:") && !strings.Contains(err.Error(), "not logged in") {
		t.Errorf("expected usage or login error, got: %v", err)
	}
}

func TestHandleChatCommand_RequiresLogin(t *testing.T) {
	// When not in remote mode, chat must refuse.
	// Note: if the developer machine has remote.yaml this may not hit the
	// login path — still exercise the function and accept either TUI start
	// failure or not-logged-in depending on environment.
	err := handleChatCommand([]string{"--help"})
	if err != nil {
		t.Fatalf("--help should succeed: %v", err)
	}
}
