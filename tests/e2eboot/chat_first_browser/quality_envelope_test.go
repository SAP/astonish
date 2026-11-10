package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestQualityResponseEnvelopes(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(map[string]any)
		wrap   bool
		valid  bool
	}{
		{"flat", func(m map[string]any) {}, false, true},
		{"ADK http_request", func(m map[string]any) { m["status"] = "200 OK" }, true, true},
		{"web_fetch content", func(m map[string]any) {
			m["content"] = m["body"]
			delete(m, "body")
			m["url"] = m["final_url"]
			delete(m, "final_url")
			m["status"] = 200
			delete(m, "status_code")
		}, true, true},
		{"error status", func(m map[string]any) { m["status_code"] = 500 }, true, false},
		{"error flag", func(m map[string]any) { m["error"] = "failed" }, true, false},
		{"missing status", func(m map[string]any) { delete(m, "status_code") }, true, false},
		{"wrong requested source", func(m map[string]any) { m["url"] = "https://example.com" }, true, false},
		{"missing source", func(m map[string]any) { delete(m, "final_url") }, true, false},
		{"ambiguous body", func(m map[string]any) { m["content"] = "different" }, true, false},
		{"deep wrapper", func(m map[string]any) {
			n := map[string]any{}
			for k, v := range m {
				n[k] = v
				delete(m, k)
			}
			m["result"] = n
		}, true, false},
		{"wrong units", func(m map[string]any) { m["body"] = strings.ReplaceAll(m["body"].(string), "°C", "°F") }, true, false},
		{"wrong UTC offset", func(m map[string]any) {
			m["body"] = strings.ReplaceAll(m["body"].(string), `"utc_offset_seconds":0`, `"utc_offset_seconds":3600`)
		}, true, false},
		{"future reading", func(m map[string]any) { m["body"] = strings.ReplaceAll(m["body"].(string), "13:45", "14:00") }, true, false},
		{"wrong value", func(m map[string]any) { m["body"] = strings.ReplaceAll(m["body"].(string), "22.5", "23.5") }, true, false},
		{"missing temperature", func(m map[string]any) {
			m["body"] = strings.ReplaceAll(m["body"].(string), `"temperature_2m":22.5,`, "")
		}, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w, r := qualityWeatherFixture()
			var m map[string]any
			if err := json.Unmarshal([]byte(w.Candidate.Bodies["tool-response:weather"]), &m); err != nil {
				t.Fatal(err)
			}
			tc.change(m)
			var payload any = m
			if tc.wrap {
				payload = map[string]any{"result": m}
			}
			raw, _ := json.Marshal(payload)
			w.Candidate.Bodies["tool-response:weather"] = string(raw)
			if err := qualityCorrectedReport(w, r); (err == nil) != tc.valid {
				t.Fatalf("valid=%v err=%v", tc.valid, err)
			}
		})
	}
	for _, delta := range []time.Duration{-time.Minute, 2*time.Minute + time.Nanosecond} {
		w, r := qualityWeatherFixture()
		w.Candidate.Observations[0].ObservedAt = r.StartedAt.Add(delta)
		if err := qualityCorrectedReport(w, r); err == nil {
			t.Fatal("out of retrieval/run bounds accepted")
		}
	}
}

func TestQualityExplainedSection(t *testing.T) {
	explanation := "The supplied draft (`" + qualitySample + "`) failed every quality standard."
	for _, tc := range []struct {
		name, text string
		valid      bool
	}{
		{"explicit section", "## Draft defects found\n" + explanation, true},
		{"no section", explanation, false},
		{"unrelated section", "## Weather\n" + explanation, false},
		{"section reset", "## Draft defects found\n## Weather\n" + explanation, false},
		{"heading alone", "## Draft defects found\n" + qualitySample, false},
		{"negated failure", "## Draft defects found\n" + strings.ReplaceAll(explanation, "failed every", "did not fail every"), false},
		{"table sample", "## Draft defects found\n| Note | " + explanation + " |", false},
		{"later raw publication", "## Draft defects found\n" + explanation + "\n" + qualitySample, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := qualityExplainedSample(tc.text + "\n" + qualityTestReport); got != tc.valid {
				t.Fatalf("got %v want %v", got, tc.valid)
			}
		})
	}
}
