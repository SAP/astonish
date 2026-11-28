package config

import (
	"encoding/json"
	"gopkg.in/yaml.v3"
	"testing"
)

func TestBotRoutinePolicyConfig(t *testing.T) {
	for _, raw := range []string{`{}`, `{"bot_routine_policy":{"require_approval":["memory_forget"]}}`} {
		var c AppConfig
		if err := json.Unmarshal([]byte(raw), &c); err != nil {
			t.Fatal(err)
		}
		if err := c.BotRoutinePolicy.Validate(); err != nil {
			t.Fatal(err)
		}
	}
	var c AppConfig
	if err := yaml.Unmarshal([]byte("bot_routine_policy:\n  require_approval: [memory_forget]\n"), &c); err != nil {
		t.Fatal(err)
	}
	if len(c.BotRoutinePolicy.RequireApproval) != 1 || c.BotRoutinePolicy.RequireApproval[0] != BotRoutineMemoryForget {
		t.Fatal("operator policy not decoded")
	}
	for _, controls := range [][]BotRoutineControl{{"*"}, {"shell_command"}, {"memory_remember"}, {BotRoutineMemoryForget, BotRoutineMemoryForget}} {
		if (BotRoutinePolicyConfig{RequireApproval: controls}).Validate() == nil {
			t.Fatalf("accepted unsupported restriction: %v", controls)
		}
	}
}
