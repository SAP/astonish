package main

// These checks supplement, never replace, the stored quality assessment.
import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/SAP/astonish/pkg/store"
)

var qualityNumber = regexp.MustCompile(`^(-?[0-9]+(?:\.[0-9]+)?)\s*(?:°\s*C|Celsius|C)$`)
var qualityURL = regexp.MustCompile("https?://[^\\s|<>\\x60\\)]+")
var qualityUTC = regexp.MustCompile(`\d{4}-\d{2}-\d{2}[ T]~?\d{2}:\d{2}(?::\d{2}(?:\.\d+)?)?(?:Z| UTC)`)

func qualityTable(text string) map[string]string {
	fields := map[string]string{}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "|") {
			// Citation metadata may be explicitly labelled outside the table.
			// Do not scan arbitrary prose for a convenient date or URL.
			plain := strings.ReplaceAll(line, "*", "")
			label, value, ok := strings.Cut(plain, ":")
			key := strings.ToLower(strings.TrimSpace(label))
			switch key {
			case "source", "source url", "retrieved", "retrieved at", "retrieval time", "retrieval time (utc)", "retrieved (utc)", "fetched", "fetched at", "fetched (utc)", "fetch time":
				if ok {
					if _, exists := fields[key]; exists {
						fields[key] = "duplicate field"
					} else {
						fields[key] = strings.TrimSpace(value)
					}
				}
			}
			continue
		}
		cells := strings.Split(strings.Trim(line, "|"), "|")
		if len(cells) != 2 {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(strings.ReplaceAll(cells[0], "*", "")))
		value := strings.TrimSpace(strings.ReplaceAll(cells[1], "*", ""))
		if _, exists := fields[key]; exists {
			fields[key] = "duplicate field"
		} else {
			fields[key] = value
		}
	}
	return fields
}

func qualityTemperature(text string) (float64, error) {
	fields := qualityTable(text)
	value := ""
	seen := false
	for _, key := range []string{"current temperature", "temperature", "temperature (now)", "temperature (current)", "temperature now", "current temp", "air temperature"} {
		if v, ok := fields[key]; ok {
			if seen {
				return 0, fmt.Errorf("ambiguous current temperature fields")
			}
			value, seen = v, true
		}
	}
	match := qualityNumber.FindStringSubmatch(value)
	if len(match) != 2 {
		return 0, fmt.Errorf("missing unambiguous current Celsius temperature table field")
	}
	n, err := strconv.ParseFloat(match[1], 64)
	if err != nil || n < -90 || n > 60 {
		return 0, fmt.Errorf("invalid delivered temperature")
	}
	return n, nil
}

type qualityTemperatureKind int

const (
	qualityCurrent qualityTemperatureKind = iota
	qualityForecastHigh
	qualityForecastLow
)

type qualityTemperatureComponent struct {
	kind    qualityTemperatureKind
	celsius float64
}

var qualityCelsiusComponent = regexp.MustCompile(`(-?[0-9]+(?:\.[0-9]+)?)\s*(?:°\s*C|Celsius)\b`)
var qualityHighLabel = regexp.MustCompile(`\bhigh\b`)
var qualityLowLabel = regexp.MustCompile(`\blow\b`)

func qualityTemperatures(text string) ([]qualityTemperatureComponent, error) {
	var result []qualityTemperatureComponent
	seen := map[qualityTemperatureKind]bool{}
	for key, value := range qualityTable(text) {
		kind := qualityCurrent
		current := false
		switch key {
		case "current temperature", "temperature", "temperature (now)", "temperature (current)", "temperature now", "current temp", "air temperature":
			current = true
		}
		if !current {
			// Period headings plus high/low semantics explicitly distinguish a
			// forecast from an observation. Never infer this from arbitrary prose.
			if key != "today" && key != "tonight" && !strings.Contains(key, "forecast") {
				continue
			}
			label := key + " " + strings.ToLower(value)
			high, low := qualityHighLabel.MatchString(label), qualityLowLabel.MatchString(label)
			if high == low || (key == "today" && !high) || (key == "tonight" && !low) {
				if qualityCelsiusComponent.MatchString(value) {
					return nil, fmt.Errorf("ambiguous forecast period label")
				}
				continue
			}
			kind = qualityForecastHigh
			if low {
				kind = qualityForecastLow
			}
		}
		matches := qualityCelsiusComponent.FindAllStringSubmatch(value, -1)
		if current { // Preserve strict current-value parsing and its former negatives.
			matches = nil
			if m := qualityNumber.FindStringSubmatch(value); len(m) == 2 {
				matches = append(matches, m)
			}
		}
		if len(matches) != 1 || seen[kind] {
			return nil, fmt.Errorf("missing or ambiguous Celsius temperature field")
		}
		n, err := strconv.ParseFloat(matches[0][1], 64)
		if err != nil || n < -90 || n > 60 {
			return nil, fmt.Errorf("invalid delivered temperature")
		}
		seen[kind] = true
		result = append(result, qualityTemperatureComponent{kind: kind, celsius: n})
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("missing labelled Celsius temperature table field")
	}
	return result, nil
}

func qualityCorrectedReport(w *store.BotWorkState, r store.BotRun) error {
	if w.Candidate == nil || r.StartedAt == nil {
		return fmt.Errorf("missing candidate or run start")
	}
	c := w.Candidate
	actual, err := qualityTemperatures(c.Text)
	if err != nil {
		return err
	}
	if !strings.Contains(strings.ToLower(c.Text), "orlando") {
		return fmt.Errorf("missing Orlando location")
	}
	fields := qualityTable(c.Text)
	// Only explicit retrieval labels qualify. Never scan unrelated fields (in
	// particular Source observation timestamp) for a plausible-looking time.
	var source, retrieved string
	var sourceSeen, retrievedSeen bool
	for key, value := range fields {
		label := strings.TrimSpace(strings.TrimSuffix(key, "(utc)"))
		switch label {
		case "source", "source url":
			if sourceSeen {
				return fmt.Errorf("ambiguous source fields")
			}
			source, sourceSeen = value, true
		case "retrieval time", "retrieved", "retrieved at", "fetched", "fetched at", "fetch time", "retrieved / fetched", "retrieved/fetched":
			if retrievedSeen {
				return fmt.Errorf("ambiguous retrieval time fields")
			}
			retrieved, retrievedSeen = value, true
		}
	}
	sources := qualityURL.FindAllString(source, -1)
	// A field may distinguish observation time from retrieval time; use only the
	// explicitly fetched portion, never substitute the weather reading timestamp.
	if i := strings.Index(strings.ToLower(retrieved), "fetched"); i >= 0 {
		retrieved = retrieved[i+len("fetched"):]
	}
	times := qualityUTC.FindAllString(retrieved, -1)
	if len(times) != 1 {
		return fmt.Errorf("missing unambiguous UTC retrieval time")
	}
	stamp := strings.ReplaceAll(strings.ReplaceAll(times[0], "~", ""), " UTC", "Z")
	stamp = strings.Replace(stamp, " ", "T", 1)
	if len(stamp) == 17 {
		stamp = strings.TrimSuffix(stamp, "Z") + ":00Z"
	}
	fetched, err := time.Parse(time.RFC3339Nano, stamp)
	if err != nil {
		return fmt.Errorf("invalid UTC retrieval time: %w", err)
	}
	for _, o := range c.Observations {
		if o.Kind != store.BotEvidenceObservation || o.ObservedAt.Before(*r.StartedAt) || (r.FinishedAt != nil && o.ObservedAt.After(*r.FinishedAt)) {
			continue
		}
		matched := false
		for _, source := range sources {
			if source == o.Source && !strings.Contains(source, "...") && !strings.Contains(source, "…") {
				matched = true
			}
		}
		if !matched || o.ObservedAt.Sub(fetched) < 0 || o.ObservedAt.Sub(fetched) >= time.Minute {
			continue
		}
		for key, raw := range c.Bodies {
			if !strings.HasPrefix(key, "tool-response:") {
				continue
			}
			response, decodeErr := employeeDecodeWebResponse(json.RawMessage(raw))
			sourceURL := response.FinalURL
			if sourceURL == "" {
				sourceURL = response.URL
			}
			if decodeErr != nil || response.StatusCode != 200 || sourceURL != o.Source || (response.URL != "" && response.URL != o.Source) {
				continue
			}
			if qualityNWSForecast(response.Body, actual, o.ObservedAt, fields["source issued"]) {
				return nil
			}
			if len(actual) != 1 || actual[0].kind != qualityCurrent {
				continue
			}
			var weather struct {
				UTCOffset int     `json:"utc_offset_seconds"`
				Latitude  float64 `json:"latitude"`
				Longitude float64 `json:"longitude"`
				Units     struct {
					Temperature string `json:"temperature_2m"`
				} `json:"current_units"`
				Current struct {
					Temperature *float64 `json:"temperature_2m"`
					Time        string   `json:"time"`
				} `json:"current"`
			}
			if json.Unmarshal([]byte(response.Body), &weather) != nil || weather.Current.Temperature == nil || weather.Units.Temperature != "°C" || weather.UTCOffset != 0 || math.Abs(weather.Latitude-28.54) > 0.2 || math.Abs(weather.Longitude+81.38) > 0.2 {
				continue
			}
			reading, err := time.Parse("2006-01-02T15:04", weather.Current.Time)
			if err != nil {
				reading, err = time.Parse("2006-01-02T15:04:05", weather.Current.Time)
			}
			if err != nil || reading.After(o.ObservedAt) || o.ObservedAt.Sub(reading) > time.Hour {
				continue
			}
			if math.Abs(actual[0].celsius-*weather.Current.Temperature) < 0.051 {
				return nil
			}
		}
	}
	return fmt.Errorf("corrected report lacks matching fresh weather body, complete source URL, or UTC retrieval time")
}

// Forecast freshness is issuance freshness, not the tighter observation-reading
// window. NWS routinely issues a day's periods several hours before retrieval.
func qualityNWSForecast(body string, actual []qualityTemperatureComponent, observed time.Time, reportedIssue string) bool {
	var weather struct {
		Properties struct {
			UpdateTime  string `json:"updateTime"`
			GeneratedAt string `json:"generatedAt"`
			Periods     []struct {
				Start       string   `json:"startTime"`
				End         string   `json:"endTime"`
				Day         *bool    `json:"isDaytime"`
				Temperature *float64 `json:"temperature"`
				Unit        string   `json:"temperatureUnit"`
			} `json:"periods"`
		} `json:"properties"`
	}
	if json.Unmarshal([]byte(body), &weather) != nil {
		return false
	}
	issued, err := time.Parse(time.RFC3339, weather.Properties.UpdateTime)
	if err != nil || issued.After(observed) || observed.Sub(issued) > 12*time.Hour {
		return false
	}
	if reportedIssue != "" {
		t, err := time.Parse(time.RFC3339, strings.Trim(reportedIssue, "` "))
		if err != nil || !t.Equal(issued) {
			return false
		}
	}
	if weather.Properties.GeneratedAt != "" {
		t, err := time.Parse(time.RFC3339, weather.Properties.GeneratedAt)
		if err != nil || t.Before(issued) || t.After(observed) {
			return false
		}
	}
	for _, component := range actual {
		if component.kind == qualityCurrent {
			return false
		}
		matched := 0
		for _, period := range weather.Properties.Periods {
			start, e1 := time.Parse(time.RFC3339, period.Start)
			end, e2 := time.Parse(time.RFC3339, period.End)
			if e1 != nil || e2 != nil || !end.After(start) || !end.After(observed) || end.Sub(start) > 24*time.Hour || period.Day == nil || period.Temperature == nil {
				continue
			}
			// Tonight may begin later today and end tomorrow. Today's high
			// must cover the fetch itself; neither can use yesterday's period.
			if start.Format("2006-01-02") != observed.In(start.Location()).Format("2006-01-02") {
				continue
			}
			if *period.Day != (component.kind == qualityForecastHigh) {
				continue
			}
			if component.kind == qualityForecastHigh && start.After(observed) {
				continue
			}
			celsius := *period.Temperature
			switch period.Unit {
			case "F":
				celsius = (celsius - 32) * 5 / 9
			case "C":
			default:
				continue
			}
			if math.Abs(component.celsius-math.Round(celsius*10)/10) < 0.001 {
				matched++
			}
		}
		if matched != 1 {
			return false
		}
	}
	return len(actual) > 0
}

var qualityRejectedSuppliedDraft = regexp.MustCompile("(?i)^(?:[-+] )?(?:the )?supplied draft \\([`\"“‘]?" + regexp.QuoteMeta(qualitySample) + "[`\"”’]?\\) failed every quality standard(?:[.:]|$)")

func qualityExplainedSample(text string) bool {
	if _, err := qualityTemperatures(text); err != nil {
		return false
	}
	// Require each sample occurrence to be explicitly identified as a defect,
	// outside the actual table. Merely appending a good table to a raw draft is not enough.
	defectSection := false
	for _, line := range strings.Split(text, "\n") {
		plain := strings.TrimSpace(strings.ReplaceAll(line, "*", ""))
		if strings.HasPrefix(plain, "#") {
			defectSection = strings.EqualFold(strings.TrimSpace(strings.TrimLeft(plain, "#")), "Draft defects found")
		} else if strings.EqualFold(plain, "Draft defects found") {
			defectSection = true
		}
		if !strings.Contains(line, qualitySample) {
			continue
		}
		lower := strings.ToLower(line)
		contextual := defectSection && qualityRejectedSuppliedDraft.MatchString(plain) && strings.Count(line, qualitySample) == 1
		if strings.HasPrefix(strings.TrimSpace(line), "|") || !(contextual || strings.Contains(lower, "defective draft") || strings.Contains(lower, "incorrect draft") || strings.Contains(lower, "draft defect")) {
			return false
		}
	}
	return true
}
