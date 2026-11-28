package execution

import "testing"

func TestBotMandateExecutionCapsAreHistoricalMetadata(t *testing.T) {
	ceiling := BotMandateLimits{MinScheduleIntervalSeconds: 60, MaxSchedules: 1, MaxDailyRuns: 2, MaxConcurrentRuns: 1}
	if err := ceiling.Validate(); err != nil {
		t.Fatal(err)
	}
	requested := ceiling
	requested.MaxRuntimeSeconds, requested.MaxTokens, requested.MaxTotalTokens = 900, 100, 1
	requested.MaxToolCalls, requested.MaxTotalToolCalls = 100, 1
	if err := requested.ValidateWithin(ceiling); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*BotMandateLimits){
		func(l *BotMandateLimits) { l.MaxTokens = -1 },
		func(l *BotMandateLimits) { l.MaxTotalTokens = -1 },
		func(l *BotMandateLimits) { l.MaxToolCalls = -1 },
		func(l *BotMandateLimits) { l.MaxTotalToolCalls = -1 },
		func(l *BotMandateLimits) { l.MaxRuntimeSeconds = -1 },
		func(l *BotMandateLimits) { l.MaxConcurrentRuns = 0 },
		func(l *BotMandateLimits) { l.MaxDailyRuns = 0 },
		func(l *BotMandateLimits) { l.MaxSpendMicros = 1 },
	} {
		bad := ceiling
		mutate(&bad)
		if bad.ValidateWithin(ceiling) == nil {
			t.Fatalf("invalid authority accepted: %+v", bad)
		}
	}
}
