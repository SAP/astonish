package fleet

import (
	"strings"
	"testing"
)

func TestLoadBundledSetupProfiles(t *testing.T) {
	profiles, err := LoadBundledSetupProfiles()
	if err != nil {
		t.Fatalf("LoadBundledSetupProfiles: %v", err)
	}
	if _, ok := profiles["software-development"]; !ok {
		t.Fatal("missing software-development profile")
	}
	if _, ok := profiles["generic"]; !ok {
		t.Fatal("missing generic profile")
	}
}

func TestSetupEngine_BuildPlanArgs_SoftwareDevelopment(t *testing.T) {
	profile, ok := GetBundledSetupProfile("software-development")
	if !ok {
		t.Fatal("software-development profile not found")
	}
	engine := NewSetupEngine(nil)
	collected := SetupCollected{
		"channel": {
			"type": "chat",
		},
		"project_source": {
			"type": "git_repo",
			"repo": "acme/billing-api",
		},
		"provisioning": {
			"template":                "billing-api",
			"container_workspace_dir": "/root/billing-api",
		},
		"artifacts": {
			"code_repo": "acme/billing-api",
			"docs_repo": "acme/billing-api",
		},
		"identity": {
			"key":         "billing-api",
			"name":        "Billing API",
			"description": "Software dev for billing-api",
		},
	}

	build, err := engine.BuildPlanArgs(profile, "software-dev", collected)
	if err != nil {
		t.Fatalf("BuildPlanArgs: %v", err)
	}
	if build.Key != "billing-api" {
		t.Fatalf("key = %q", build.Key)
	}
	if build.ChannelType != "chat" {
		t.Fatalf("channel = %q", build.ChannelType)
	}
	if build.ProjectSource == nil || build.ProjectSource.Repo != "acme/billing-api" {
		t.Fatalf("project source = %+v", build.ProjectSource)
	}
	if build.Template != "billing-api" {
		t.Fatalf("template = %q", build.Template)
	}
	if build.Artifacts["code"].Type != "git_repo" {
		t.Fatalf("code artifact = %+v", build.Artifacts["code"])
	}
}

func TestSetupEngine_BuildPlanArgs_CredentialInjection(t *testing.T) {
	profile, ok := GetBundledSetupProfile("software-development")
	if !ok {
		t.Fatal("software-development profile not found")
	}
	engine := NewSetupEngine(nil)
	collected := SetupCollected{
		"channel":        {"type": "chat"},
		"project_source": {"type": "git_repo", "repo": "acme/app"},
		"provisioning":   {"template": "app", "container_workspace_dir": "/root/app"},
		"artifacts":      {"code_repo": "acme/app", "docs_repo": "acme/app"},
		"identity":       {"key": "app", "name": "App", "description": "test"},
		"credentials": {
			"github":  "github-pat",
			"trading": "juicytrade-providers",
			"credential_injection": map[string]any{
				"files": []any{
					map[string]any{
						"credential": "trading",
						"path":       "/root/app/config/credentials.yaml",
						"field":      "value",
						"format":     "yaml",
					},
				},
			},
		},
	}
	build, err := engine.BuildPlanArgs(profile, "software-dev", collected)
	if err != nil {
		t.Fatalf("BuildPlanArgs: %v", err)
	}
	if build.Credentials["trading"] != "juicytrade-providers" {
		t.Fatalf("credentials = %+v", build.Credentials)
	}
	if build.CredentialInjection == nil || len(build.CredentialInjection.Files) != 1 {
		t.Fatalf("credential_injection = %+v", build.CredentialInjection)
	}
	if build.CredentialInjection.Files[0].Path != "/root/app/config/credentials.yaml" {
		t.Fatalf("file path = %q", build.CredentialInjection.Files[0].Path)
	}
}

func TestSetupEngine_ValidateStep_RequiredChannelRepo(t *testing.T) {
	profile, ok := GetBundledSetupProfile("software-development")
	if !ok {
		t.Fatal("profile not found")
	}
	engine := NewSetupEngine(nil)
	collected := SetupCollected{
		"channel": {"type": "github_issues"},
	}
	if err := engine.ValidateStep(profile, "channel", collected); err == nil {
		t.Fatal("expected repo required for github_issues")
	}
}

func TestSetupEngine_ComposeWizardPrompt_StepScoped(t *testing.T) {
	profile, ok := GetBundledSetupProfile("generic")
	if !ok {
		t.Fatal("profile not found")
	}
	engine := NewSetupEngine(nil)
	prompt := engine.ComposeWizardPrompt(profile, "my-template", nil)
	if prompt == "" {
		t.Fatal("expected non-empty prompt")
	}
	// Step-scoped: should mention current step (identity is first in generic)
	if !strings.Contains(prompt, "Plan identity") && !strings.Contains(prompt, "identity") {
		t.Fatalf("prompt should focus on first step, got: %s", prompt[:min(200, len(prompt))])
	}
	// Should not include all step titles at once (old monolithic behavior)
	if strings.Count(prompt, "## Current step:") > 1 {
		t.Fatal("prompt should only include one current step section")
	}
}

func TestSetupEngine_CurrentStep_SoftwareDevelopment(t *testing.T) {
	profile, ok := GetBundledSetupProfile("software-development")
	if !ok {
		t.Fatal("profile not found")
	}
	engine := NewSetupEngine(nil)
	step, stepID := engine.CurrentStep(profile, nil)
	if stepID != "overview" {
		t.Fatalf("first step = %q (%q), want overview", stepID, step.Title)
	}
	prompt := engine.ComposeWizardPrompt(profile, "software-dev", nil)
	if strings.Contains(prompt, "Plan identity") {
		t.Fatal("initial prompt should not focus on identity step")
	}
	if !strings.Contains(prompt, "Overview") && !strings.Contains(prompt, "Communication channel") {
		t.Fatalf("prompt should focus on early steps, got head: %s", prompt[:min(250, len(prompt))])
	}
	if !strings.Contains(prompt, `"_ack": true`) {
		t.Fatalf("overview prompt should document info-step acknowledgement payload, got head: %s", prompt[:min(500, len(prompt))])
	}
}

func TestSetupEngine_ValidateStep_InfoAckErrorNamesField(t *testing.T) {
	profile, ok := GetBundledSetupProfile("software-development")
	if !ok {
		t.Fatal("profile not found")
	}
	engine := NewSetupEngine(nil)
	err := engine.ValidateStep(profile, "overview", SetupCollected{"overview": {"acknowledged": true}})
	if err == nil {
		t.Fatal("expected validation error")
	}
	if !strings.Contains(err.Error(), `"_ack": true`) {
		t.Fatalf("expected error to mention _ack payload, got %q", err.Error())
	}
}

func TestSetupEngine_ComposeWizardPrompt(t *testing.T) {
	profile, ok := GetBundledSetupProfile("generic")
	if !ok {
		t.Fatal("profile not found")
	}
	engine := NewSetupEngine(nil)
	prompt := engine.ComposeWizardPrompt(profile, "my-template", nil)
	if prompt == "" {
		t.Fatal("expected non-empty prompt")
	}
}

func TestProfileForTemplate(t *testing.T) {
	cfg := &FleetConfig{SetupProfileKey: "software-development"}
	if got := ProfileForTemplate(cfg); got != "software-development" {
		t.Fatalf("got %q", got)
	}
}
