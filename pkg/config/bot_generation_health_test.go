package config

import (
	"testing"

	"github.com/SAP/astonish/pkg/execution"
	"gopkg.in/yaml.v3"
)

func TestBotGenerationHealthConfigYAML(t *testing.T) {
	var cfg AppConfig
	if err := yaml.Unmarshal([]byte(`bot_runtime_policy:
  schema_version: 1
  mode: progress_v1
  evaluation_interval_seconds: 300
  tool_call_timeout_seconds: 120
  generation_health:
    idle_timeout_seconds: 120
    check_interval_seconds: 10
`), &cfg); err != nil {
		t.Fatal(err)
	}
	p, err := cfg.EffectiveBotRuntimePolicy()
	if err != nil || p.GenerationHealth == nil || p.ModelCallTimeoutSeconds != 0 {
		t.Fatalf("health config: %+v %v", p, err)
	}
	cfg.BotRuntimePolicy.ModelCallTimeoutSeconds = 120
	if _, err := cfg.EffectiveBotRuntimePolicy(); err == nil {
		t.Fatal("conflicting model policies accepted")
	}
	cfg.BotRuntimePolicy.GenerationHealth = nil
	saved, err := cfg.EffectiveBotRuntimePolicy()
	if err != nil || saved.GenerationHealth != nil || saved.ModelCallTimeoutSeconds != 120 {
		t.Fatalf("saved progress config upgraded: %+v %v", saved, err)
	}
	cfg.BotRuntimePolicy = execution.NewBotProgressRuntimePolicy()
	cfg.BotRuntimePolicy.GenerationHealth.CheckIntervalSeconds = 0
	if _, err := cfg.EffectiveBotRuntimePolicy(); err == nil {
		t.Fatal("missing health config defaulted")
	}
}
