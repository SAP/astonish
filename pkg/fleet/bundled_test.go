package fleet

import (
	"strings"
	"testing"
)

func TestIsBundledKey(t *testing.T) {
	t.Parallel()
	if !IsBundledKey("software-dev") {
		t.Fatal("expected software-dev to be a bundled key")
	}
	if IsBundledKey("my-custom-fleet") {
		t.Fatal("expected my-custom-fleet not to be bundled")
	}
}

func TestBundledKeys(t *testing.T) {
	t.Parallel()
	keys := BundledKeys()
	if _, ok := keys["software-dev"]; !ok {
		t.Fatal("BundledKeys missing software-dev")
	}
}

func TestSoftwareDevelopmentSetupProfileUsesRawContentForFileCredentials(t *testing.T) {
	t.Parallel()

	profile, ok := GetBundledSetupProfile("software-development")
	if !ok {
		t.Fatal("missing software-development setup profile")
	}

	var credentialsPrompt string
	for _, step := range profile.Steps {
		if step.ID == "credentials" {
			credentialsPrompt = step.Prompt
			break
		}
	}
	if credentialsPrompt == "" {
		t.Fatal("software-development setup profile missing credentials step prompt")
	}

	for _, want := range []string{
		"raw_content with field\n  `content` for YAML/JSON/dotenv/text file contents",
		"Save full file content as raw_content type",
		"field: content",
	} {
		if !strings.Contains(credentialsPrompt, want) {
			t.Fatalf("credentials prompt missing %q\n\nPrompt:\n%s", want, credentialsPrompt)
		}
	}

	for _, old := range []string{
		"api_key with field\n  `value` for YAML/JSON config file contents",
		"Save full file content as api_key type",
		"field: value",
	} {
		if strings.Contains(credentialsPrompt, old) {
			t.Fatalf("credentials prompt still contains old api_key file guidance %q\n\nPrompt:\n%s", old, credentialsPrompt)
		}
	}
}
