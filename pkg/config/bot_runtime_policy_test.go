package config

import (
	"github.com/SAP/astonish/pkg/execution"
	"testing"
)

func TestBotRuntimePolicyDefaultsAndExplicitOperatorCaps(t *testing.T) {
	for _, cfg := range []*AppConfig{nil, {}} {
		p, err := cfg.EffectiveBotRuntimePolicy()
		if err != nil || p.EffectiveMode() != execution.BotRuntimeModeProgressV1 || p.MaxActiveSeconds != nil || p.ModelCallTimeoutSeconds != 0 || p.GenerationHealth == nil || p.GenerationHealth.IdleTimeoutSeconds != 120 || p.GenerationHealth.CheckIntervalSeconds != 10 {
			t.Fatalf("defaults: %+v %v", p, err)
		}
	}
	legacy := &AppConfig{BotRuntimePolicy: &execution.BotRuntimePolicy{SchemaVersion: execution.BotRuntimePolicySchemaVersion, Mode: execution.BotRuntimeModeLegacy}}
	p, err := legacy.EffectiveBotRuntimePolicy()
	if err != nil || p.EffectiveMode() != execution.BotRuntimeModeLegacy {
		t.Fatalf("explicit legacy changed: %+v %v", p, err)
	}
	limits := DefaultBotMandateConfig()
	cfg := &AppConfig{BotMandates: &limits}
	p, err = cfg.EffectiveBotRuntimePolicy()
	if err != nil || p != nil {
		t.Fatalf("historical operator config upgraded: %+v %v", p, err)
	}
	cfg.BotRuntimePolicy = execution.NewBotProgressRuntimePolicy()
	if _, err := cfg.EffectiveBotRuntimePolicy(); err == nil {
		t.Fatal("conflicting explicit finite cap ignored")
	}
	cap := int64(200)
	cfg.BotRuntimePolicy.MaxActiveSeconds = &cap
	p, err = cfg.EffectiveBotRuntimePolicy()
	if err != nil || *p.MaxActiveSeconds != cap {
		t.Fatal(err)
	}
	*p.MaxActiveSeconds = 1
	p.GenerationHealth.IdleTimeoutSeconds = 1
	if cfg.BotRuntimePolicy.GenerationHealth.IdleTimeoutSeconds != 120 {
		t.Fatal("health policy aliases config")
	}
	if *cfg.BotRuntimePolicy.MaxActiveSeconds != 200 {
		t.Fatal("policy aliases config")
	}
	cfg.BotRuntimePolicy.Mode = "unknown"
	if _, err := cfg.EffectiveBotRuntimePolicy(); err == nil {
		t.Fatal("unknown mode accepted")
	}
}
