package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Opt-in replay: only the explicitly supplied owned artifact, never live state.
func TestQualitySavedArtifact(t *testing.T) {
	root := os.Getenv("QUALITY_SAVED_ARTIFACT")
	if root == "" {
		t.Skip("offline saved-artifact opt-in")
	}
	raw, err := os.ReadFile(filepath.Join(root, "employee-quality-sample.json"))
	if err != nil {
		t.Fatal(err)
	}
	var disk employeeDisk
	if err := json.Unmarshal(raw, &disk); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, row := range disk.Runs {
		if row.ID != "19abb1ef-fe8b-4f35-ac36-1988f99f32ff" {
			continue
		}
		found = true
		w, r, err := qualityWork(row)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("run=%s observations=%d bodies=%d", r.ID, len(w.Candidate.Observations), len(w.Candidate.Bodies))
		if n, err := qualityTemperature(w.Candidate.Text); err != nil || n != 27.1 {
			t.Fatal("captured table temperature mismatch")
		}
		weatherBodies := 0
		for key, body := range w.Candidate.Bodies {
			if !strings.HasPrefix(key, "tool-response:") {
				continue
			}
			response, err := employeeDecodeWebResponse(json.RawMessage(body))
			if err != nil {
				t.Fatal(err)
			}
			weatherBodies++
			var weather struct {
				Current struct {
					Temperature float64 `json:"temperature_2m"`
					Time        string  `json:"time"`
				} `json:"current"`
			}
			if json.Unmarshal([]byte(response.Body), &weather) != nil || weather.Current.Temperature != 27.1 || weather.Current.Time != "2026-11-10T16:00" {
				t.Fatal("captured body measurement mismatch")
			}
			t.Logf("status=%d temperature=%.1f source_time=%s", response.StatusCode, weather.Current.Temperature, weather.Current.Time)
		}
		if weatherBodies != 1 {
			t.Fatal("expected single captured weather response")
		}
		if err := qualityCorrectedReport(w, r); err != nil {
			t.Error(err)
		}
		if !qualityExplainedSample(w.Candidate.Text) {
			t.Error("sample explanation rejected")
		}
	}
	if !found {
		t.Fatal("expected captured run missing")
	}
}
