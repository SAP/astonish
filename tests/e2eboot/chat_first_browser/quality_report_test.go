package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/store"
)

const qualityTestURL = "https://api.open-meteo.com/v1/forecast?latitude=28.5383&longitude=-81.3792&current=temperature_2m&timezone=UTC"
const qualityTestReport = "The draft's quoted ‘900 Celsius’ is impossible.\n## Orlando corrected report\n| Field | Value |\n|---|---|\n| Current temperature | 22.5 °C |\n| Source | " + qualityTestURL + " |\n| Retrieval time (UTC) | 2026-11-10 13:51 UTC |"

func qualityWeatherFixture() (*store.BotWorkState, store.BotRun) {
	now := time.Date(2026, 11, 10, 13, 51, 25, 0, time.UTC)
	start, finish := now.Add(-time.Minute), now.Add(time.Minute)
	body := `{"latitude":28.55,"longitude":-81.38,"utc_offset_seconds":0,"current_units":{"temperature_2m":"°C"},"current":{"temperature_2m":22.5,"time":"2026-11-10T13:45"}}`
	raw, _ := json.Marshal(map[string]any{"final_url": qualityTestURL, "status_code": 200, "body": body})
	return &store.BotWorkState{Candidate: &store.BotWorkCandidate{Text: qualityTestReport, Bodies: map[string]string{"tool-response:weather": string(raw)}, Observations: []store.BotEvidence{{Kind: store.BotEvidenceObservation, Source: qualityTestURL, ObservedAt: now}}}}, store.BotRun{StartedAt: &start, FinishedAt: &finish}
}

func TestQualityCorrectedEvidence(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*store.BotWorkState, *store.BotRun)
		valid  bool
	}{
		{"temperature now label", func(w *store.BotWorkState, r *store.BotRun) {
			w.Candidate.Text = strings.ReplaceAll(w.Candidate.Text, "Current temperature", "Temperature (now)")
		}, true},
		{"competing temperature labels", func(w *store.BotWorkState, r *store.BotRun) { w.Candidate.Text += "\n| Temperature (now) | 22.5 °C |" }, false},
		{"explained bad value", func(w *store.BotWorkState, r *store.BotRun) {}, true},
		{"wrong actual value", func(w *store.BotWorkState, r *store.BotRun) {
			w.Candidate.Text = strings.ReplaceAll(w.Candidate.Text, "22.5 °C", "900 Celsius")
		}, false},
		{"plausible wrong actual value", func(w *store.BotWorkState, r *store.BotRun) {
			w.Candidate.Text = strings.ReplaceAll(w.Candidate.Text, "22.5 °C", "23.5 °C")
		}, false},
		{"abbreviated source", func(w *store.BotWorkState, r *store.BotRun) {
			w.Candidate.Text = strings.ReplaceAll(w.Candidate.Text, qualityTestURL, "https://api.open-meteo.com/v1/forecast?latitude=28.5383&...")
		}, false},
		{"markdown source link", func(w *store.BotWorkState, r *store.BotRun) {
			w.Candidate.Text = strings.ReplaceAll(w.Candidate.Text, qualityTestURL, "[Open-Meteo]("+qualityTestURL+")")
		}, true},
		{"observation not retrieval", func(w *store.BotWorkState, r *store.BotRun) {
			w.Candidate.Text = strings.ReplaceAll(w.Candidate.Text, "13:51 UTC", "13:45 UTC")
		}, false},
		{"rough correct fetched minute", func(w *store.BotWorkState, r *store.BotRun) {
			w.Candidate.Text = strings.ReplaceAll(w.Candidate.Text, "2026-11-10 13:51 UTC", "2026-11-10 13:45 UTC (observation); fetched 2026-11-10 ~13:51 UTC")
		}, true},
		{"missing body", func(w *store.BotWorkState, r *store.BotRun) { w.Candidate.Bodies = nil }, false},
		{"wrong body source", func(w *store.BotWorkState, r *store.BotRun) {
			w.Candidate.Bodies["tool-response:weather"] = strings.ReplaceAll(w.Candidate.Bodies["tool-response:weather"], "api.open-meteo.com", "example.com")
		}, false},
		{"old body reading", func(w *store.BotWorkState, r *store.BotRun) {
			w.Candidate.Bodies["tool-response:weather"] = strings.ReplaceAll(w.Candidate.Bodies["tool-response:weather"], "13:45", "10:45")
		}, false},
		{"observation from prior occurrence", func(w *store.BotWorkState, r *store.BotRun) {
			w.Candidate.Observations[0].ObservedAt = r.StartedAt.Add(-time.Second)
		}, false},
		{"raw sample", func(w *store.BotWorkState, r *store.BotRun) { w.Candidate.Text = qualitySample }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w, r := qualityWeatherFixture()
			tc.change(w, &r)
			err := qualityCorrectedReport(w, r)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v err=%v", tc.valid, err)
			}
		})
	}
}

func TestQualityCorrectedRetrievedFetchedReplay(t *testing.T) {
	// Deterministic replay of run f689d68b-a0c8-4417-ae92-3613a85e25e1:
	// retrieved at 14:05:26Z, API measurement at 14:00Z. Labels are verbatim.
	const source = "https://api.open-meteo.com/v1/forecast?latitude=28.5383&longitude=-81.3792&current=temperature_2m,relative_humidity_2m,apparent_temperature,precipitation,weather_code,wind_speed_10m&daily=weather_code,temperature_2m_max,temperature_2m_min,precipitation_probability_max,precipitation_sum&temperature_unit=celsius&wind_speed_unit=kmh&precipitation_unit=mm&timezone=UTC&forecast_days=1"
	const fetchRow = "| **Retrieved / fetched (UTC)** | 2026-11-10 14:05 UTC (observed fetch time) |"
	const report = "## Weather in Orlando, FL — Today\n| Field | Value |\n|---|---|\n| Current temperature | 23.4 °C |\n| **Source URL** | " + source + " |\n" + fetchRow + "\n| **Source observation timestamp (UTC)** | 2026-11-10 14:00 UTC (API `current.time`, 15-min interval reading) |"
	for _, tc := range []struct {
		name, text string
		valid      bool
	}{
		{"labelled prose citation", strings.ReplaceAll(strings.ReplaceAll(report, "| **Source URL** | "+source+" |", "**Source:** `"+source+"`"), fetchRow, "**Retrieved:** 2026-11-10 14:05 UTC (HTTP 200)"), true},
		{"actual corrected labels", report, true},
		{"retrieved synonym", strings.ReplaceAll(report, "Retrieved / fetched (UTC)", "Retrieved (UTC)"), true},
		{"fetched synonym", strings.ReplaceAll(report, "Retrieved / fetched (UTC)", "Fetched (UTC)"), true},
		{"observation only", strings.ReplaceAll(report, fetchRow, ""), false},
		{"observation label even at fetch time", strings.ReplaceAll(report, "Retrieved / fetched (UTC)", "Observation time (UTC)"), false},
		{"wrong fetch time", strings.ReplaceAll(report, "14:05 UTC", "14:00 UTC"), false},
		{"ambiguous timestamps", strings.ReplaceAll(report, "14:05 UTC (observed fetch time)", "14:05 UTC or 2026-11-10 14:06 UTC"), false},
		{"competing retrieval fields", report + "\n| Retrieval time (UTC) | 2026-11-10 14:05 UTC |", false},
		{"empty competing retrieval field", report + "\n| Retrieval time (UTC) | |", false},
		{"competing source fields", report + "\n| Source | " + source + " |", false},
		{"future fetch time", strings.ReplaceAll(report, "14:05 UTC", "14:06 UTC"), false},
		{"duplicate retrieval field", report + "\n" + fetchRow, false},
		{"missing explicit UTC", strings.ReplaceAll(report, "14:05 UTC", "14:05"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w, r := qualityWeatherFixture()
			w.Candidate.Text = tc.text
			now := time.Date(2026, 11, 10, 14, 5, 26, 540353547, time.UTC)
			start, finish := now.Add(-15*time.Second), now.Add(72*time.Second)
			r.StartedAt, r.FinishedAt = &start, &finish
			w.Candidate.Observations[0].Source = source
			w.Candidate.Observations[0].ObservedAt = now
			body := `{"latitude":28.55,"longitude":-81.38,"utc_offset_seconds":0,"current_units":{"temperature_2m":"°C"},"current":{"temperature_2m":23.4,"time":"2026-11-10T14:00"}}`
			raw, err := json.Marshal(map[string]any{"final_url": source, "status_code": 200, "body": body})
			if err != nil {
				t.Fatal(err)
			}
			w.Candidate.Bodies["tool-response:weather"] = string(raw)
			if err := qualityCorrectedReport(w, r); (err == nil) != tc.valid {
				t.Fatalf("valid=%v err=%v", tc.valid, err)
			}
		})
	}
}

func TestQualityFinalOnlyQuotedDefects(t *testing.T) {
	for _, tc := range []struct {
		text  string
		valid bool
	}{
		{qualitySample, false},
		{qualitySample + "\n" + qualityTestReport, false},
		{"Defective draft: \"" + qualitySample + "\"\n" + qualityTestReport, true},
		{"Defective draft: \"" + qualitySample + "\"\n" + strings.ReplaceAll(qualityTestReport, "22.5 °C", "900 Celsius"), false},
	} {
		raw, _ := json.Marshal(map[string]any{"messages": []any{map[string]any{"type": "agent", "content": tc.text}}})
		if err := qualityFinalOnly(string(raw), nil); (err == nil) != tc.valid {
			t.Fatalf("valid=%v err=%v text=%s", tc.valid, err, tc.text)
		}
	}
}
