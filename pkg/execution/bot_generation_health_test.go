package execution

import (
	"encoding/json"
	"testing"
)

func TestBotGenerationHealthValidation(t *testing.T) {
	for name, change := range map[string]func(*BotRuntimePolicy){
		"conflicting elapsed":     func(p *BotRuntimePolicy) { p.ModelCallTimeoutSeconds = 120 },
		"missing legacy timeout":  func(p *BotRuntimePolicy) { p.GenerationHealth = nil },
		"zero idle":               func(p *BotRuntimePolicy) { p.GenerationHealth.IdleTimeoutSeconds = 0 },
		"negative check":          func(p *BotRuntimePolicy) { p.GenerationHealth.CheckIntervalSeconds = -1 },
		"overflow idle":           func(p *BotRuntimePolicy) { p.GenerationHealth.IdleTimeoutSeconds = 1<<63 - 1 },
		"overflow check":          func(p *BotRuntimePolicy) { p.GenerationHealth.CheckIntervalSeconds = 1<<63 - 1 },
		"checks slower than idle": func(p *BotRuntimePolicy) { p.GenerationHealth.CheckIntervalSeconds = 121 },
	} {
		t.Run(name, func(t *testing.T) {
			p := NewBotProgressRuntimePolicy()
			change(p)
			if p.Validate() == nil {
				t.Fatal("invalid policy accepted")
			}
		})
	}
	var old BotRuntimePolicy
	if err := json.Unmarshal([]byte(`{"schema_version":1,"mode":"progress_v1","evaluation_interval_seconds":300,"model_call_timeout_seconds":120,"tool_call_timeout_seconds":120}`), &old); err != nil || old.Validate() != nil || old.GenerationHealth != nil || old.ModelCallTimeoutSeconds != 120 {
		t.Fatalf("saved progress policy upgraded: %+v %v", old, err)
	}
	var health BotRuntimePolicy
	if err := json.Unmarshal([]byte(`{"schema_version":1,"mode":"progress_v1","generation_health":{"idle_timeout_seconds":120,"check_interval_seconds":10,"ignore_cancellation":true}}`), &health); err == nil {
		t.Fatal("unknown health option accepted")
	}
	raw, err := json.Marshal(NewBotProgressRuntimePolicy())
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &health); err != nil || health.Validate() != nil || health.GenerationHealth == nil || health.ModelCallTimeoutSeconds != 0 {
		t.Fatalf("health roundtrip: %s %v", raw, err)
	}
}

func TestBotGenerationHealthCeilingsAndClone(t *testing.T) {
	health := NewBotProgressRuntimePolicy()
	old := health.Clone()
	old.GenerationHealth = nil
	old.ModelCallTimeoutSeconds = 120
	if health.ValidateWithin(old) == nil {
		t.Fatal("removed trusted elapsed ceiling")
	}
	if err := old.ValidateWithin(health); err != nil {
		t.Fatal("historical finite policy not allowed within health ceiling", err)
	}
	old.ModelCallTimeoutSeconds = 121
	if old.ValidateWithin(health) == nil {
		t.Fatal("historical timeout exceeds health idle ceiling")
	}
	for _, change := range []func(*BotRuntimePolicy){
		func(p *BotRuntimePolicy) { p.GenerationHealth.IdleTimeoutSeconds++ },
		func(p *BotRuntimePolicy) { p.GenerationHealth.CheckIntervalSeconds++ },
	} {
		p := health.Clone()
		change(p)
		if p.ValidateWithin(health) == nil {
			t.Fatal("health ceiling widened")
		}
	}
	narrowed := health.Clone()
	narrowed.GenerationHealth.IdleTimeoutSeconds = 60
	narrowed.GenerationHealth.CheckIntervalSeconds = 5
	if err := narrowed.ValidateWithin(health); err != nil {
		t.Fatal(err)
	}
	if health.GenerationHealth.IdleTimeoutSeconds != 120 || health.GenerationHealth.CheckIntervalSeconds != 10 {
		t.Fatal("clone aliases health policy")
	}
	m := testMandate()
	m.RuntimePolicy = health
	copy := m.Clone()
	copy.RuntimePolicy.GenerationHealth.IdleTimeoutSeconds = 1
	if m.RuntimePolicy.GenerationHealth.IdleTimeoutSeconds != 120 {
		t.Fatal("mandate clone aliases health")
	}
}
