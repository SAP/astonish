package launcher

import (
	"reflect"
	"testing"

	"github.com/SAP/astonish/pkg/tui/events"
)

func TestBotDeliveryTimestampWireCompatibility(t *testing.T) {
	plain := canonicalBotFrame(t, "11111111-1111-1111-1111-111111111111", "saved/0", "text", `{"text":"Saved report","invocationId":"bot-report:22222222-2222-2222-2222-222222222222"}`, 1)
	timed := canonicalBotFrame(t, "11111111-1111-1111-1111-111111111111", "saved/0", "text", `{"text":"Saved report","invocationId":"bot-report:22222222-2222-2222-2222-222222222222","timestamp":"2026-11-27T17:00:00.123Z"}`, 1)
	var baseline, mapper botTranscriptMapper
	want, err := baseline.mapFrame(plain)
	if err != nil {
		t.Fatal(err)
	}
	got, err := mapper.mapFrame(timed)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("additive delivery time changed terminal event: %+v != %+v (%v)", got, want, err)
	}
	if len(got) != 1 || got[0].Kind != events.KindBotReport {
		t.Fatalf("report identity lost: %+v", got)
	}
	duplicate, err := mapper.mapFrame(timed)
	if err != nil || len(duplicate) != 0 {
		t.Fatalf("reconnect duplicated report: %+v %v", duplicate, err)
	}
	later := canonicalBotFrame(t, "11111111-1111-1111-1111-111111111111", "saved/1", "text", `{"text":"Saved report","invocationId":"bot-report:33333333-3333-3333-3333-333333333333","timestamp":"2026-11-27T18:00:00Z"}`, 2)
	got, err = mapper.mapFrame(later)
	if err != nil || len(got) != 1 || got[0].InvocationID != "bot-report:33333333-3333-3333-3333-333333333333" {
		t.Fatalf("distinct report merged: %+v %v", got, err)
	}
}
