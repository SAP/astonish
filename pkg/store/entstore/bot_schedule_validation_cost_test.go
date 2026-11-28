package entstore

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/store"
)

func TestBoundedScheduleParsesOncePerValidation(t *testing.T) {
	policy, err := mandateTestPolicy(context.Background(), store.BotMandateRef{})
	if err != nil {
		t.Fatal(err)
	}
	policy.Limits.MinScheduleIntervalSeconds = 60
	policy.Limits.MaxDailyRuns = 1440
	tr := store.TriggerSpec{Cron: "*/2 * * * *", Timezone: "UTC"}
	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	// A full horizon still visits every occurrence. Allocations must reflect
	// parsing one expression, not parsing hundreds of thousands of expressions.
	allocs := testing.AllocsPerRun(1, func() {
		if err := boundedSchedule(tr, now, policy.Limits); err != nil {
			t.Fatal(err)
		}
	})
	t.Logf("full-year two-minute schedule allocations: %.0f", allocs)
	if allocs > 1000 {
		t.Fatalf("schedule validation reparses per occurrence: %.0f allocations", allocs)
	}
}

func TestBoundedScheduleParsedCursorPreservesLimits(t *testing.T) {
	policy, err := mandateTestPolicy(context.Background(), store.BotMandateRef{})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, cron, zone string
		interval, daily  int64
		want             error
	}{
		{"two_minutes", "*/2 * * * *", "UTC", 120, 720, nil},
		{"daily_cap", "*/2 * * * *", "UTC", 60, 719, store.ErrBotForbidden},
		{"interval_cap", "*/2 * * * *", "UTC", 121, 1440, store.ErrBotForbidden},
		{"union_short_gap", "0,59 * * * *", "UTC", 120, 1440, store.ErrBotForbidden},
		{"spring_forward_interval", "0 9 * * *", "America/New_York", 86400, 2, store.ErrBotForbidden},
		{"fall_back_daily_cap", "* * * * *", "America/New_York", 60, 1440, nil},
		{"fall_back_repeated_hour", "0 1 * * *", "America/New_York", 60, 1, store.ErrBotForbidden},
		{"leap_day", "0 9 29 2 *", "UTC", 60, 1, nil},
		{"impossible_day", "0 9 31 2 *", "UTC", 60, 1, store.ErrBotInvalid},
		{"embedded_timezone", "CRON_TZ=UTC 0 9 * * *", "UTC", 60, 1, store.ErrBotInvalid},
		{"descriptor", "@daily", "UTC", 60, 1, store.ErrBotInvalid},
		{"bad_timezone", "0 9 * * *", "not/a/zone", 60, 1, store.ErrBotInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			limits := policy.Limits
			limits.MinScheduleIntervalSeconds, limits.MaxDailyRuns = tc.interval, tc.daily
			got := boundedSchedule(store.TriggerSpec{Cron: tc.cron, Timezone: tc.zone}, now, limits)
			if !errors.Is(got, tc.want) {
				t.Fatalf("bounded schedule = %v, want %v", got, tc.want)
			}
		})
	}
}
