package events

import (
	"fmt"
	"reflect"
	"testing"
)

// This fixture starts with a scheduling tool, not bare report messages: the
// preceding activity must never absorb later autonomous completed responses.
// Bot Watch replay feeds the same Events through Apply (not LoadHistory).
// Typed publications emitted by the mapper retain their durable row identity.
func TestBotScheduledReportsKeepDistinctCompletedBodies(t *testing.T) {
	for _, linear := range []bool{false, true} {
		for _, identical := range []bool{false, true} {
			t.Run(fmt.Sprintf("linear=%t/identical=%t", linear, identical), func(t *testing.T) {
				live := NewTranscript()
				live.LinearThread = linear
				journal := []Event{
					NewUser("Schedule recurring weather reports"),
					NewToolCall("schedule_job", "schedule-1", map[string]any{"name": "weather"}),
					NewToolResult("schedule_job", "schedule-1", map[string]any{"success": true}),
					NewText("Weather schedule created."),
					NewDone(),
				}
				for _, ev := range journal {
					live.Apply(ev)
				}
				want := []string{"Weather schedule created."}
				for occurrence := 1; occurrence <= 3; occurrence++ {
					body := fmt.Sprintf("Occurrence %d: Orlando is sunny.", occurrence)
					if identical {
						body = "Orlando is sunny."
					}
					want = append(want, body)
					batch := []Event{NewBotReport(fmt.Sprintf("bot-report:00000000-0000-0000-0000-%012d", occurrence), fmt.Sprintf("report-%d", occurrence), body)}
					journal = append(journal, batch...)
					for _, ev := range batch {
						live.Apply(ev)
					}
					assertCompletedBotBodies(t, live, want, fmt.Sprintf("live occurrence %d", occurrence))

					restored := NewTranscript()
					restored.LinearThread = linear
					for _, ev := range journal {
						restored.Apply(ev)
					}
					assertCompletedBotBodies(t, restored, want, fmt.Sprintf("replayed occurrence %d", occurrence))
					if !reflect.DeepEqual(live.Items, restored.Items) {
						t.Errorf("live/replay differ after occurrence %d", occurrence)
					}
				}
				// Even later tool activity must not turn completed reports back
				// into an interstitial sticky answer that can be overwritten.
				live.Apply(NewToolCall("read_file", "later-1", nil))
				live.Apply(NewToolResult("read_file", "later-1", "ok"))
				live.Apply(NewText("Later activity finished."))
				live.Apply(NewDone())
				var after []string
				for _, item := range live.Items {
					if item.Kind == ItemAgent {
						after = append(after, item.Content)
					}
				}
				wantAfter := append(append([]string{}, want...), "Later activity finished.")
				if !reflect.DeepEqual(after, wantAfter) {
					t.Errorf("later tools changed completed reports: got %#v, want %#v", after, wantAfter)
				}
			})
		}
	}
}

// Committed useful partial and known-denied results must survive both layouts
// verbatim. The consumer must not replace them with a generic failure warning.
func TestBotEmployeeCanonicalReportLiveReplay(t *testing.T) {
	for _, linear := range []bool{false, true} {
		for _, body := range []string{
			"Apple Inc. launched a new device. Source: https://fixture.test/product.",
			"Apple Inc. launched a new device. Market coverage is unavailable; this is a partial result.",
			"Search was denied by the saved scope before dispatch. No source was retrieved.",
			"The write outcome is unconfirmed. Do not repeat the action until its result is reconciled.",
		} {
			t.Run(fmt.Sprintf("linear=%t/%s", linear, body), func(t *testing.T) {
				journal := []Event{
					NewUser("Apple Inc. launches and market developments"),
					NewText("I'll investigate launches and market developments."),
					NewToolCall("perplexity_web_search", "search-employee", map[string]any{"query": "Apple Inc."}),
					NewToolResult("perplexity_web_search", "search-employee", "source response"),
					NewBotReport("bot-report:11111111-1111-4111-8111-111111111111", "employee-final", body),
					NewDone(),
				}
				live, restored := NewTranscript(), NewTranscript()
				live.LinearThread, restored.LinearThread = linear, linear
				for _, ev := range journal {
					live.Apply(ev)
				}
				for _, ev := range journal {
					restored.Apply(ev)
				}
				if !reflect.DeepEqual(live.Items, restored.Items) {
					t.Fatal("employee live/replay chronology differs")
				}
				for _, tr := range []*Transcript{live, restored} {
					count := 0
					for _, item := range tr.Items {
						if item.Kind == ItemAgent && item.Content == body {
							count++
							if item.Provisional {
								t.Fatal("committed employee body became provisional")
							}
						}
					}
					if count != 1 || tr.Streaming {
						t.Fatalf("employee body count=%d streaming=%t", count, tr.Streaming)
					}
				}
			})
		}
	}
}

func assertCompletedBotBodies(t *testing.T, tr *Transcript, want []string, stage string) {
	t.Helper()
	var got []string
	activities := 0
	for _, item := range tr.Items {
		switch item.Kind {
		case ItemAgent:
			got = append(got, item.Content)
			if item.Provisional {
				t.Errorf("%s: completed body became provisional: %q", stage, item.Content)
			}
		case ItemActivity:
			activities++
			if item.Expanded || len(item.Steps) != 1 || item.Steps[0].Name != "schedule_job" {
				t.Errorf("%s: preceding activity changed: %+v", stage, item)
			}
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("%s: independent completed bodies = %#v, want %#v", stage, got, want)
	}
	if activities != 1 {
		t.Errorf("%s: activity folds = %d, want 1", stage, activities)
	}
	if tr.Streaming {
		t.Errorf("%s: completed reports left transcript streaming", stage)
	}
}
