package store

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestPlatformChannelSettings_IgnoresLegacyA2AJSON(t *testing.T) {
	var settings PlatformSettings
	if err := json.Unmarshal([]byte(`{
		"channels": {
			"telegram": {"enabled": true},
			"a2a": {"enabled": true, "trusted_issuers": [{"issuer": "https://legacy.example"}]}
		}
	}`), &settings); err != nil {
		t.Fatalf("unmarshal settings: %v", err)
	}
	if settings.Channels == nil || settings.Channels.Telegram == nil || !settings.Channels.Telegram.Enabled {
		t.Fatalf("telegram settings = %#v, want preserved supported settings", settings.Channels)
	}

	encoded, err := json.Marshal(settings)
	if err != nil {
		t.Fatalf("marshal settings: %v", err)
	}
	if strings.Contains(string(encoded), "a2a") {
		t.Fatalf("saved settings retained inert legacy A2A configuration: %s", encoded)
	}
}
