package main

// This observer never writes product state. Owner requests go through employeeSend.
import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/SAP/astonish/pkg/store"
	"github.com/go-rod/rod"
	"github.com/google/uuid"
)

const qualityContract = "bot-quality-live-v1"
const qualityGoal = "Orlando report quality"
const qualitySample = "UNREVIEWED_SAMPLE_QUALITY: Orlando, 1999-01-01. Temperature 900 Celsius. No source."

var qualityRequired = []string{"persistent-requirements", "supplied-draft-corrected", "three-offline-assessed-results", "live-reload-final-only", "cadence-preserves-requirements", "owner-update-next-run"}

type qualityProbe struct {
	root, home string
	state      map[string]any
	receipts   map[string]map[string]any
	baseline   store.BotGoal
	ticks      []store.BotRun
}

func qualityGoalSource(w *store.BotWorkState) (store.BotGoal, error) {
	for _, g := range w.Contract.Goals {
		if g.Title == qualityGoal && g.ID != uuid.Nil && g.Revision > 0 && g.BotID == w.Contract.BotID {
			text := strings.ToLower(g.Description + " " + g.CompletionCriteria)
			for _, required := range []string{"celsius", "source", "utc", "markdown"} {
				if !strings.Contains(text, required) {
					return g, fmt.Errorf("saved quality requirement missing: %s", required)
				}
			}
			return g, nil
		}
	}
	return store.BotGoal{}, fmt.Errorf("saved quality goal missing")
}

func qualityWork(row employeeDiskRow) (*store.BotWorkState, store.BotRun, error) {
	_, fields, err := employeeStoredDocument(row.Data)
	if err != nil {
		return nil, store.BotRun{}, err
	}
	var w store.BotWorkState
	if err = json.Unmarshal(fields["work"], &w); err != nil {
		return nil, store.BotRun{}, fmt.Errorf("missing private work snapshot")
	}
	r := employeeRun(row)
	if err = w.Validate(); err != nil {
		return nil, r, err
	}
	q := r.Report.Quality
	if w.Contract.RunID != r.ID || w.Contract.BotID != r.BotID || w.Phase != store.BotWorkAccepted || r.Status != store.RunSucceeded || w.Candidate == nil || w.Review == nil || q == nil || q.SchemaVersion != store.BotWorkSchemaVersion || q.Outcome != store.BotWorkAccepted || q.RequirementsDigest != w.RequirementsDigest || q.CandidateDigest != w.Candidate.Digest || q.WorkRevision != w.Revision || !reflect.DeepEqual(q.Criteria, w.Review.Criteria) {
		return nil, r, fmt.Errorf("work/report identity or accepted assessment mismatch")
	}
	for _, a := range q.Criteria {
		if a.Verdict != store.BotCriterionSatisfied || len(a.EvidenceRefs) == 0 {
			return nil, r, fmt.Errorf("unverified criterion")
		}
	}
	if len(q.Criteria) == 0 {
		return nil, r, fmt.Errorf("empty assessment")
	}
	fresh := false
	for _, observation := range w.Candidate.Observations {
		if observation.Kind == store.BotEvidenceObservation && observation.Source != "" && r.StartedAt != nil && !observation.ObservedAt.Before(*r.StartedAt) {
			fresh = true
		}
	}
	if !fresh {
		return nil, r, fmt.Errorf("no fresh mechanical observation for this occurrence")
	}
	return &w, r, nil
}

func (p *qualityProbe) receipt(name string, source any, detail string) {
	raw, err := json.MarshalIndent(source, "", "  ")
	must(err)
	file := "quality-" + name + ".json"
	must(os.WriteFile(filepath.Join(p.root, file), raw, 0600))
	p.receipts[name] = map[string]any{"Candidate": p.state["binary_sha256"], "BrowserCandidate": p.state["browser_sha256"], "Nonce": p.state["quality_nonce"], "BotID": p.state["bot_id"], "Home": p.home, "SchemaVersion": 1, "Source": file, "SourceHash": fmt.Sprintf("%x", sha256.Sum256(raw)), "Detail": detail, "Status": "passed"}
}

func (p *qualityProbe) inspectTarget(d employeeDisk, target store.BotRun) *store.BotWorkState {
	for _, row := range d.Runs {
		if row.ID == target.ID.String() {
			w, _, err := qualityWork(row)
			must(err)
			return w
		}
	}
	panic("quality target missing")
}

func startQuality(root, home string, state map[string]any, send func(string, string) store.BotRun, read func(string, string) employeeDisk, runs func() []store.BotRun) *qualityProbe {
	if os.Getenv("BOT_QUALITY_ACCEPTANCE") != "1" {
		return nil
	}
	p := &qualityProbe{root: root, home: home, state: state, receipts: map[string]map[string]any{}}
	wait := func(target store.BotRun) {
		boundedWait("quality owner turn", 6*time.Minute, func() bool {
			for _, r := range runs() {
				if r.ID == target.ID && r.Status.Terminal() {
					if r.Status != store.RunSucceeded {
						panic("quality owner turn failed")
					}
					return true
				}
			}
			return false
		})
	}
	target := send("quality-goal", "Keep an ongoing goal named Orlando report quality. Each recurring Orlando weather report must retrieve a current source on that occurrence, give the source URL and retrieval time in UTC, temperature in Celsius, and a concise Markdown table. Do not reuse stale readings; state uncertainty honestly. These are ongoing completion criteria, not a one-time task. Save these standards without changing the cadence.")
	wait(target)
	target = send("quality-sample", "Inspect and correct this supplied draft against my saved Orlando report quality standards, obtaining current source data. Deliver the corrected report here; do not ask me to approve a sample. Draft: "+qualitySample)
	wait(target)
	disk := read("quality-sample", "read")
	w := p.inspectTarget(disk, target)
	g, err := qualityGoalSource(w)
	must(err)
	p.baseline = g
	if !strings.Contains(w.Contract.Instructions, qualitySample) {
		panic("supplied draft missing from contract")
	}
	for _, row := range disk.Runs {
		if row.ID == target.ID.String() {
			must(qualityCorrectedReport(w, employeeRun(row)))
		}
	}
	p.receipt("persistent-requirements", w.Contract, "Original assignment and revisioned source goal persisted")
	p.receipt("supplied-draft-corrected", disk, "Supplied defective sample inspected and corrected with captured source observations and accepted assessment; model repair count is not forced")
	return p
}

func (p *qualityProbe) offline(d employeeDisk, bot, trigger uuid.UUID, since time.Time) bool {
	if p == nil {
		return true
	}
	ticks := []store.BotRun{}
	for _, row := range d.Runs {
		r := employeeRun(row)
		if !employeePostRestartRun(r, bot, trigger, since) {
			continue
		}
		if !r.Status.Terminal() {
			continue
		}
		w, r, err := qualityWork(row)
		must(err)
		g, err := qualityGoalSource(w)
		must(err)
		if !reflect.DeepEqual(g, p.baseline) {
			panic("scheduled work lost saved criteria")
		}
		if len(w.Candidate.Observations) == 0 {
			panic("scheduled report lacks fresh observations")
		}
		ticks = append(ticks, r)
	}
	if len(ticks) < 3 {
		return false
	}
	p.ticks = ticks[:3]
	p.receipt("three-offline-assessed-results", d, "Three independent fresh scheduled assessed results after browser closure and owned restart, before login")
	ids := []string{}
	for _, r := range p.ticks {
		ids = append(ids, r.ID.String())
	}
	p.receipts["three-offline-assessed-results"]["RunIDs"] = ids
	return true
}

func qualityFinalOnly(history string, runs []store.BotRun) error {
	var h struct {
		Messages []struct {
			Type, Content string
			InvocationID  string `json:"invocationId"`
		}
	}
	if err := json.Unmarshal([]byte(history), &h); err != nil {
		return err
	}
	for _, m := range h.Messages {
		if m.Type != "agent" {
			continue
		}
		if strings.Contains(m.Content, qualitySample) && !qualityExplainedSample(m.Content) {
			return fmt.Errorf("defective supplied draft published as assistant answer")
		}
		for _, r := range runs {
			if m.InvocationID == r.ID.String() || strings.HasPrefix(m.InvocationID, r.ID.String()+":") {
				return fmt.Errorf("assessed run draft leaked to Home")
			}
		}
	}
	return nil
}

func (p *qualityProbe) restored(page *rod.Page, history string, homeID string) {
	if p == nil {
		return
	}
	must(qualityFinalOnly(history, p.ticks))
	live := employeeReportBubbles(page)
	for _, r := range p.ticks {
		must(employeeReportExactlyOnce(history, homeID, r, live))
	}
	page.MustReload()
	page.MustElement(`[aria-label="Bot workspace"]`).MustVisible()
	restored := employeeReportBubbles(page)
	for _, r := range p.ticks {
		must(employeeReportExactlyOnce(history, homeID, r, restored))
	}
	p.receipt("live-reload-final-only", map[string]any{"history": history, "live": live, "reload": restored}, "Each of three canonical reports appears once live and after reload; candidate drafts remain diagnostics")
}

func (p *qualityProbe) cadence(d employeeDisk) {
	if p == nil {
		return
	}
	// Read persisted source records, not run-specific requirements digests (which include run IDs).
	for _, raw := range d.Goals {
		var row struct{ Data string }
		must(json.Unmarshal(raw, &row))
		body, fields, err := employeeStoredDocument(row.Data)
		must(err)
		if value, ok := fields["goal"]; ok {
			body = string(value)
		}
		var g store.BotGoal
		must(json.Unmarshal([]byte(body), &g))
		if g.ID == p.baseline.ID {
			if !reflect.DeepEqual(g, p.baseline) {
				panic("cadence changed quality goal")
			}
			p.receipt("cadence-preserves-requirements", d, "Cadence-only change preserved exact saved goal identity, revision and content")
			return
		}
	}
	panic("cadence quality source absent")
}

func (p *qualityProbe) ownerUpdate(send func(string, string) store.BotRun, read func(string, string) employeeDisk, runs func() []store.BotRun) {
	target := send("quality-owner-update", "Update the ongoing Orlando report quality goal: retain all existing criteria and additionally include wind speed in km/h in every report. Save this requirement for future scheduled occurrences; do not change the cadence.")
	boundedWait("quality requirement update", 6*time.Minute, func() bool {
		for _, r := range runs() {
			if r.ID == target.ID && r.Status.Terminal() {
				if r.Status != store.RunSucceeded {
					panic("quality update failed")
				}
				return true
			}
		}
		return false
	})
	since := time.Now()
	boundedWait("quality updated scheduled input", 8*time.Minute, func() bool {
		disk := read("quality-updated-tick", "read")
		for _, row := range disk.Runs {
			r := employeeRun(row)
			if r.Trigger != store.TriggerCron || !r.CreatedAt.After(since) || !r.Status.Terminal() {
				continue
			}
			w, _, err := qualityWork(row)
			must(err)
			g, err := qualityGoalSource(w)
			must(err)
			if g.ID != p.baseline.ID || g.Revision <= p.baseline.Revision || !strings.Contains(strings.ToLower(g.Description+g.CompletionCriteria), "wind") || !strings.Contains(strings.ToLower(w.Candidate.Text), "wind") {
				panic("later scheduled run ignored updated criterion")
			}
			p.receipt("owner-update-next-run", disk, "Owner criterion update persisted at a newer goal revision and governed a fresh scheduled report without owner restatement")
			return true
		}
		return false
	})
}

func (p *qualityProbe) finish(evidence map[string]any) {
	if p == nil {
		return
	}
	employee := map[string]any{}
	for k, v := range evidence {
		employee[k] = v
	}
	for _, name := range qualityRequired {
		if p.receipts[name] == nil {
			panic("missing quality receipt: " + name)
		}
	}
	for k := range evidence {
		delete(evidence, k)
	}
	evidence["contract"] = qualityContract
	evidence["nonce"] = p.state["quality_nonce"]
	evidence["employee"] = employee
	evidence["receipts"] = p.receipts
	evidence["passed"] = qualityRequired
	evidence["status"] = "verified"
}
