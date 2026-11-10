package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/store"
)

// Reduced deterministic replay of run 5e5c8a93-5465-463f-aaa2-f3b27b373389
// from .tmp/astonish-chat-first-47e5ocb9/employee-runs.json. The delivered
// rows, observed fetch and source issuance/conversions are retained verbatim.
const qualityNWSURL = "https://api.weather.gov/gridpoints/MLB/26,68/forecast"
const qualityNWSReport = `## Corrected Orlando, FL Weather Report
| Field | Value |
|---|---|
| **Today** | Partly sunny; high near **28.3 °C** (83 °F) |
| **Tonight** | Mostly cloudy, patchy fog after 4 AM; low near **20.0 °C** (68 °F) |
| **Source URL** | ` + "`" + qualityNWSURL + "`" + ` |
| **Source issued** | 2026-11-10T15:14:50Z |
| **Retrieved (UTC)** | 2026-11-10T16:54Z |
These are NWS forecast values, not observed measurements.`
const qualityNWSBody = `{"properties":{"updateTime":"2026-11-10T15:14:50Z","generatedAt":"2026-11-10T15:22:14Z","periods":[{"name":"Today","startTime":"2026-11-10T11:00:00-05:00","endTime":"2026-11-10T18:00:00-05:00","isDaytime":true,"temperature":83,"temperatureUnit":"F"},{"name":"Tonight","startTime":"2026-11-10T18:00:00-05:00","endTime":"2026-11-11T06:00:00-05:00","isDaytime":false,"temperature":68,"temperatureUnit":"F"}]}}`

func TestQualityNWSForecastReplay(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*store.BotWorkState, *store.BotRun)
		valid  bool
	}{
		{"captured report", func(w *store.BotWorkState, r *store.BotRun) {}, true},
		{"forecast headings", func(w *store.BotWorkState, r *store.BotRun) {
			w.Candidate.Text = strings.ReplaceAll(strings.ReplaceAll(w.Candidate.Text, "**Today**", "**Forecast high**"), "**Tonight**", "**Forecast low**")
		}, true},
		{"web fetch envelope", func(w *store.BotWorkState, r *store.BotRun) {
			raw, _ := json.Marshal(map[string]any{"result": map[string]any{"final_url": qualityNWSURL, "status": 200, "content": qualityNWSBody}})
			w.Candidate.Bodies["tool-response:weather"] = string(raw)
		}, true},
		{"wrong high", func(w *store.BotWorkState, r *store.BotRun) {
			w.Candidate.Text = strings.ReplaceAll(w.Candidate.Text, "28.3", "28.4")
		}, false},
		{"wrong low", func(w *store.BotWorkState, r *store.BotRun) {
			w.Candidate.Text = strings.ReplaceAll(w.Candidate.Text, "20.0", "21.0")
		}, false},
		{"impossible", func(w *store.BotWorkState, r *store.BotRun) {
			w.Candidate.Text = strings.ReplaceAll(w.Candidate.Text, "28.3", "900")
		}, false},
		{"unlabelled", func(w *store.BotWorkState, r *store.BotRun) {
			w.Candidate.Text = strings.ReplaceAll(strings.ReplaceAll(w.Candidate.Text, "**Today**", "Reading A"), "**Tonight**", "Reading B")
		}, false},
		{"not current", func(w *store.BotWorkState, r *store.BotRun) {
			w.Candidate.Text += "\n| Current temperature | 28.3 °C |"
		}, false},
		{"wrong URL", func(w *store.BotWorkState, r *store.BotRun) {
			w.Candidate.Text = strings.ReplaceAll(w.Candidate.Text, "26,68", "27,68")
		}, false},
		{"abbreviated URL", func(w *store.BotWorkState, r *store.BotRun) {
			w.Candidate.Text = strings.ReplaceAll(w.Candidate.Text, "26,68/forecast", "...")
		}, false},
		{"wrong fetch", func(w *store.BotWorkState, r *store.BotRun) {
			w.Candidate.Text = strings.ReplaceAll(w.Candidate.Text, "16:54Z", "16:53Z")
		}, false},
		{"wrong reported issue", func(w *store.BotWorkState, r *store.BotRun) {
			w.Candidate.Text = strings.ReplaceAll(w.Candidate.Text, "15:14:50Z", "14:14:50Z")
		}, false},
		{"prior run", func(w *store.BotWorkState, r *store.BotRun) {
			w.Candidate.Observations[0].ObservedAt = r.StartedAt.Add(-time.Second)
		}, false},
		{"missing body", func(w *store.BotWorkState, r *store.BotRun) { w.Candidate.Bodies = nil }, false},
		{"mismatched envelope URL", func(w *store.BotWorkState, r *store.BotRun) {
			w.Candidate.Bodies["tool-response:weather"] = strings.ReplaceAll(w.Candidate.Bodies["tool-response:weather"], "26,68", "27,68")
		}, false},
		{"old issue", func(w *store.BotWorkState, r *store.BotRun) {
			w.Candidate.Bodies["tool-response:weather"] = strings.ReplaceAll(w.Candidate.Bodies["tool-response:weather"], "2026-11-10T15:14:50Z", "2026-11-09T15:14:50Z")
		}, false},
		{"future issue", func(w *store.BotWorkState, r *store.BotRun) {
			w.Candidate.Bodies["tool-response:weather"] = strings.ReplaceAll(w.Candidate.Bodies["tool-response:weather"], "15:14:50Z", "17:14:50Z")
		}, false},
		{"old period", func(w *store.BotWorkState, r *store.BotRun) {
			w.Candidate.Bodies["tool-response:weather"] = strings.ReplaceAll(w.Candidate.Bodies["tool-response:weather"], "2026-11-10T11:00", "2026-11-09T11:00")
		}, false},
		{"future period", func(w *store.BotWorkState, r *store.BotRun) {
			w.Candidate.Bodies["tool-response:weather"] = strings.ReplaceAll(w.Candidate.Bodies["tool-response:weather"], "2026-11-10T11:00", "2026-11-11T11:00")
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w, r := qualityWeatherFixture()
			now := time.Date(2026, 11, 10, 16, 54, 32, 194117286, time.UTC)
			start, finish := now.Add(-15*time.Second), now.Add(32*time.Second)
			r.StartedAt, r.FinishedAt = &start, &finish
			w.Candidate.Text = qualityNWSReport
			w.Candidate.Observations[0].Source = qualityNWSURL
			w.Candidate.Observations[0].ObservedAt = now
			raw, _ := json.Marshal(map[string]any{"url": qualityNWSURL, "status_code": 200, "body": qualityNWSBody})
			w.Candidate.Bodies["tool-response:weather"] = string(raw)
			tc.change(w, &r)
			if err := qualityCorrectedReport(w, r); (err == nil) != tc.valid {
				t.Fatalf("valid=%v err=%v", tc.valid, err)
			}
		})
	}
}
