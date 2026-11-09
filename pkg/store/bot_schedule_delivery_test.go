package store

import (
	"errors"
	"testing"
)

func TestBotScheduleProposalCannotSmuggleExternalDelivery(t *testing.T) {
	p := BotControlPayload{Operation: BotControlScheduleCreate, Reason: "Boston weather every minute", Schedule: &BotControlSchedule{ExpectedRevision: 2, Trigger: TriggerSpec{Kind: TriggerCron, Cron: "* * * * *", Timezone: "UTC", Instructions: "Boston weather"}}}
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	p.Schedule.Trigger.ChannelBinding = &BotChannelBinding{Channel: "email", ChatID: "outside@example.test"}
	if err := p.Validate(); !errors.Is(err, ErrBotForbidden) {
		t.Fatalf("schedule admitted transport configuration: %v", err)
	}
	p = BotControlPayload{Operation: BotControlConfigurationOperation, Reason: "Explicit external delivery review", Configuration: &BotControlConfiguration{Delivery: DeliveryPolicy{Bindings: []BotChannelBinding{{Channel: "email", ChatID: "outside@example.test"}}}}}
	if err := p.Validate(); err != nil {
		t.Fatalf("separate external configuration blocked: %v", err)
	}
}
