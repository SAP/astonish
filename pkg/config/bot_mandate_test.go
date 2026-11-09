package config

import (
	"encoding/json"
	"fmt"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestBotMandateDefaultsFinite(t *testing.T) {
	c := DefaultBotMandateConfig()
	want := BotMandateConfig{
		MinScheduleIntervalSeconds: 60,
		MaxSchedules:               8,
		MaxDailyRuns:               1440,
		MaxConcurrentRuns:          1,
		MaxRuntimeSeconds:          300,
		MaxToolCalls:               100,
		MaxTokens:                  1_000_000,
		MaxTotalToolCalls:          144000,
		MaxTotalTokens:             1_440_000_000,
	}
	if c != want {
		t.Fatalf("defaults: got %+v, want %+v", c, want)
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("defaults invalid: %v", err)
	}
	b, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	var d BotMandateConfig
	if err := json.Unmarshal(b, &d); err != nil || d != c {
		t.Fatalf("roundtrip: got %+v, err %v", d, err)
	}
}

func TestBotMandateEffectiveDefaults(t *testing.T) {
	for name, cfg := range map[string]*AppConfig{"nil receiver": nil, "absent config": {}} {
		t.Run(name, func(t *testing.T) {
			got, err := cfg.EffectiveBotMandateConfig()
			if err != nil || got != DefaultBotMandateConfig() || got.MaxTokens != 1_000_000 || got.MaxTotalTokens != 1_440_000_000 {
				t.Fatalf("effective defaults: %+v, err %v", got, err)
			}
		})
	}
}

// Complete configuration replaces defaults; values equal to historical defaults
// are just as explicit as lower or higher operator ceilings.
func TestBotMandateConfigOverrides(t *testing.T) {
	for _, limits := range []struct {
		name   string
		tokens int64
		total  int64
	}{
		{"lower", 50000, 72000000},
		{"historical defaults", 100000, 144000000},
		{"higher", 2000000, 2880000000},
	} {
		t.Run(limits.name, func(t *testing.T) {
			want := BotMandateConfig{
				MinScheduleIntervalSeconds: 120, MaxSchedules: 4,
				MaxDailyRuns: 720, MaxConcurrentRuns: 2,
				MaxRuntimeSeconds: 600, MaxToolCalls: 50,
				MaxTokens: limits.tokens, MaxTotalToolCalls: 72000,
				MaxTotalTokens: limits.total,
			}
			for _, format := range []struct {
				name      string
				input     string
				marshal   func(any) ([]byte, error)
				unmarshal func([]byte, any) error
			}{
				{"yaml", fmt.Sprintf(`bot_mandates:
  min_schedule_interval_seconds: 120
  max_schedules: 4
  max_daily_runs: 720
  max_concurrent_runs: 2
  max_runtime_seconds: 600
  max_tool_calls: 50
  max_tokens: %d
  max_total_tool_calls: 72000
  max_total_tokens: %d
`, limits.tokens, limits.total), yaml.Marshal, yaml.Unmarshal},
				{"json", fmt.Sprintf(`{"bot_mandates":{"min_schedule_interval_seconds":120,"max_schedules":4,"max_daily_runs":720,"max_concurrent_runs":2,"max_runtime_seconds":600,"max_tool_calls":50,"max_tokens":%d,"max_total_tool_calls":72000,"max_total_tokens":%d}}`, limits.tokens, limits.total), json.Marshal, json.Unmarshal},
			} {
				t.Run(format.name, func(t *testing.T) {
					var cfg AppConfig
					if err := format.unmarshal([]byte(format.input), &cfg); err != nil {
						t.Fatal(err)
					}
					got, err := cfg.EffectiveBotMandateConfig()
					if err != nil || got != want {
						t.Fatalf("override: got %+v, want %+v, err %v", got, want, err)
					}
					data, err := format.marshal(cfg)
					if err != nil {
						t.Fatal(err)
					}
					var roundtrip AppConfig
					if err := format.unmarshal(data, &roundtrip); err != nil {
						t.Fatal(err)
					}
					got, err = roundtrip.EffectiveBotMandateConfig()
					if err != nil || got != want {
						t.Fatalf("roundtrip: got %+v, want %+v, err %v", got, want, err)
					}
				})
			}
		})
	}
}

func TestBotMandateConfigInvalidOverrides(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*BotMandateConfig)
	}{
		{"zero tokens", func(c *BotMandateConfig) { c.MaxTokens = 0 }},
		{"zero aggregate tokens", func(c *BotMandateConfig) { c.MaxTotalTokens = 0 }},
		{"negative tokens", func(c *BotMandateConfig) { c.MaxTokens = -1 }},
		{"negative aggregate tokens", func(c *BotMandateConfig) { c.MaxTotalTokens = -1 }},
		{"run exceeds aggregate", func(c *BotMandateConfig) { c.MaxTokens = c.MaxTotalTokens + 1 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			want := DefaultBotMandateConfig()
			tc.change(&want)
			got, err := (&AppConfig{BotMandates: &want}).EffectiveBotMandateConfig()
			if err == nil || got != want {
				t.Fatalf("invalid explicit limits must not select defaults: got %+v, err %v", got, err)
			}
		})
	}
	for _, input := range []string{
		"bot_mandates: {}",
		"bot_mandates:\n  max_tokens: 1000000\n  max_total_tokens: 1440000000\n",
	} {
		var cfg AppConfig
		if err := yaml.Unmarshal([]byte(input), &cfg); err != nil {
			t.Fatal(err)
		}
		if _, err := cfg.EffectiveBotMandateConfig(); err == nil {
			t.Fatalf("incomplete config accepted: %s", input)
		}
	}
}

func TestBotDelegationOperatorCeiling(t *testing.T) {
	c := DefaultBotMandateConfig()
	r := c
	r.MaxTokens++
	if r.ValidateWithin(c) == nil {
		t.Fatal("raised operator ceiling")
	}
	r = c
	r.MinScheduleIntervalSeconds--
	if r.ValidateWithin(c) == nil {
		t.Fatal("lowered minimum")
	}
}
