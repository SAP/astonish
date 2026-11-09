package config

import (
	"encoding/json"
	"testing"
)

func TestBotMandateDefaultsFinite(t *testing.T) {
	c := DefaultBotMandateConfig()
	if c.MinScheduleIntervalSeconds != 60 || c.MaxSchedules != 8 || c.MaxDailyRuns != 1440 || c.MaxConcurrentRuns != 1 || c.MaxRuntimeSeconds != 300 || c.MaxToolCalls != 100 || c.MaxTokens != 100000 {
		t.Fatalf("defaults: %+v", c)
	}
	if c.Validate() != nil {
		t.Fatal("defaults invalid")
	}
	b, e := json.Marshal(c)
	if e != nil {
		t.Fatal(e)
	}
	var d BotMandateConfig
	if json.Unmarshal(b, &d) != nil || d != c {
		t.Fatal("roundtrip")
	}
	c.MaxTokens = 0
	if c.Validate() == nil {
		t.Fatal("unbounded")
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
