package astonish

import (
	"testing"
)

func TestParseCodeFlags_Defaults(t *testing.T) {
	flags, err := parseCodeFlags(nil)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if flags.Provider != "" || flags.Model != "" || flags.Dir != "" || flags.Resume != "" {
		t.Errorf("expected empty defaults, got %+v", flags)
	}
	if flags.AutoApprove || flags.Debug {
		t.Errorf("expected false bool defaults, got %+v", flags)
	}
}

func TestParseCodeFlags_ModelPin(t *testing.T) {
	flags, err := parseCodeFlags([]string{"-m", "openai:gpt-4o"})
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if flags.Provider != "openai" || flags.Model != "gpt-4o" {
		t.Errorf("expected provider/model split, got provider=%q model=%q", flags.Provider, flags.Model)
	}
}

func TestParseCodeFlags_BareModel(t *testing.T) {
	// A bare model name (no colon) is not a valid pin; it becomes Model only.
	flags, err := parseCodeFlags([]string{"--model", "gpt-4o"})
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if flags.Provider != "" {
		t.Errorf("expected empty provider for bare model, got %q", flags.Provider)
	}
	if flags.Model != "gpt-4o" {
		t.Errorf("expected Model=gpt-4o, got %q", flags.Model)
	}
}

func TestParseCodeFlags_ModelWithMultipleColons(t *testing.T) {
	flags, err := parseCodeFlags([]string{"-m", "openai:gpt-4o:2024-08-06"})
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if flags.Provider != "openai" || flags.Model != "gpt-4o:2024-08-06" {
		t.Errorf("expected split on first colon, got provider=%q model=%q", flags.Provider, flags.Model)
	}
}

func TestParseCodeFlags_YoloAliasesAutoApprove(t *testing.T) {
	flags, err := parseCodeFlags([]string{"--yolo"})
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if !flags.AutoApprove {
		t.Error("expected --yolo to enable AutoApprove")
	}
}

func TestParseCodeFlags_DirAndResume(t *testing.T) {
	flags, err := parseCodeFlags([]string{"-C", "/tmp/proj", "-r", "sess-9", "--debug"})
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	if flags.Dir != "/tmp/proj" {
		t.Errorf("Dir=%q", flags.Dir)
	}
	if flags.Resume != "sess-9" {
		t.Errorf("Resume=%q", flags.Resume)
	}
	if !flags.Debug {
		t.Error("expected Debug")
	}
}

func TestHandleCodeCommand_Help(t *testing.T) {
	if err := handleCodeCommand([]string{"--help"}); err != nil {
		t.Fatalf("--help should succeed: %v", err)
	}
	if err := handleCodeCommand([]string{"-h"}); err != nil {
		t.Fatalf("-h should succeed: %v", err)
	}
}
