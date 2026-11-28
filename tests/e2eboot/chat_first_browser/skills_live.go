package main

// All mutations are natural owner messages. HTTP reads are the real authenticated
// Bot skills endpoints; disk reads use the existing owned read-only observer.
import (
	"encoding/json"
	"fmt"
	"github.com/SAP/astonish/pkg/store"
	"github.com/go-rod/rod"
	"github.com/google/uuid"
	"os"
	"reflect"
	"regexp"
	"strings"
	"time"
)

// agentSkillsJourneyHooks keeps process ownership in the launcher. Schedule must
// send the owner request through Studio and return the applied version/trigger.
// Restart must close the browser, restart only the owned candidate and return its
// observer timestamp. Read remains usable without login; Reconnect authenticates
// only after three offline occurrences. Save persists state/evidence after each
// mutation; callers must never reuse state with agent_learning_attempted set.
type agentSkillsJourneyHooks struct {
	API       func(string, string, any) string
	Send      func(string, string) store.BotRun
	Read      func(string, string) employeeDisk
	Runs      func() []store.BotRun
	Page      func() *rod.Page
	Change    func(string, string, store.BotControlOperation, bool) store.BotOperationView
	Schedule  func(uuid.UUID) (store.BotVersion, uuid.UUID)
	Restart   func() time.Time
	Reconnect func()
	Save      func()
}

// runAgentSkillsJourney runs only learning/scheduler acceptance on a fresh owned
// Bot. It cannot certify provider parity, factual grounding, or terminal parity.
// Partial receipts survive a failure but never satisfy finishAgent.
func runAgentSkillsJourney(root, home string, state, evidence map[string]any, h agentSkillsJourneyHooks) {
	if h.API == nil || h.Send == nil || h.Read == nil || h.Runs == nil || h.Page == nil || h.Change == nil || h.Schedule == nil || h.Restart == nil || h.Reconnect == nil || h.Save == nil {
		panic("agent skills requires complete owned lifecycle hooks")
	}
	send := func(key, message string) store.BotRun {
		h.Save() // persist attempted marker before the first mutation
		r := h.Send(key, message)
		h.Save()
		return r
	}
	p := startAgentSkills(root, home, state, h.API, send, h.Read, h.Runs)
	evidence["learning"] = map[string]any{"contract": agentSkillsContract, "nonce": state["agent_nonce"], "status": "running", "receipts": p.receipts}
	defer h.Save()
	h.Save()
	version, trigger := h.Schedule(p.skill)
	p.schedule(version, trigger)
	h.Save()
	restarted := h.Restart()
	if restarted.IsZero() || restarted.Before(p.startedAt) {
		panic("missing owned restart timestamp")
	}
	var offline employeeDisk
	boundedWait("agent skill three post-restart occurrences", 30*time.Minute, func() bool {
		offline = h.Read("agent-skills-offline", "read")
		return p.offline(offline, trigger, restarted)
	})
	h.Reconnect()
	p.restored()
	ids := []string{}
	for _, r := range p.ticks {
		ids = append(ids, r.ID.String())
	}
	p.receipt("owned-restart-three-scheduled", map[string]any{"restart": restarted, "trigger": trigger, "disk": offline}, "Three distinct post-restart scheduler occurrences with independently captured retrieval streams", map[string]any{"RunIDs": ids})
	h.Save()
	p.removeAndInspect(h.Page, send, h.Change)
	evidence["learning"] = p.finishAgent()
}

func skillsJourneyTimeout() time.Duration {
	if os.Getenv("BOT_SKILLS_ACCEPTANCE") == "1" {
		return 60 * time.Minute
	}
	return 30 * time.Minute
}

type skillsProbe struct {
	root, home, botPath string
	state               map[string]any
	receipts            map[string]map[string]any
	api                 func(string, string, any) string
	read                func(string, string) employeeDisk
	runs                func() []store.BotRun
	skill               uuid.UUID
	baseline, updated   store.BotSkillRevision
	manual              store.BotRun
	ticks               []store.BotRun
	agent               bool
	startedAt           time.Time
	captures            map[uuid.UUID]string
}

func (p *skillsProbe) get(suffix string, dest any) {
	raw := p.api("GET", p.botPath+"/skills"+suffix+"?scope=personal", nil)
	must(json.Unmarshal([]byte(raw), dest))
}
func (p *skillsProbe) head() store.BotSkillRevision {
	var r store.BotSkillRevision
	p.get("/"+p.skill.String()+"/head", &r)
	must(r.Validate())
	if r.BotID.String() != p.state["bot_id"] || r.SkillID != p.skill {
		panic("foreign skill head")
	}
	return r
}
func (p *skillsProbe) wait(target store.BotRun) store.BotRun {
	var result store.BotRun
	boundedWait("skills owner result", 8*time.Minute, func() bool {
		for _, r := range p.runs() {
			if r.ID == target.ID && r.Status.Terminal() {
				if r.Status != store.RunSucceeded {
					panic("skills owner turn failed")
				}
				result = r
				return true
			}
		}
		return false
	})
	return result
}
func (p *skillsProbe) work(r store.BotRun) *store.BotWorkState {
	found := false
	for _, public := range p.runs() {
		if public.ID == r.ID && public.BotID == r.BotID && reflect.DeepEqual(public.Report, r.Report) {
			found = true
			break
		}
	}
	if !found {
		panic("skill outcome missing from authenticated Bot run endpoint")
	}
	for _, row := range p.read("skills-work", "read").Runs {
		if row.ID == r.ID.String() {
			w, actual, err := p.decodeWork(row)
			must(err)
			if actual.BotID.String() != p.state["bot_id"] || actual.ID != r.ID || !reflect.DeepEqual(actual.Report, r.Report) {
				panic("skill run differs from authenticated run endpoint")
			}
			if p.agent && w.Contract.Skill == nil {
				// Saving during the first assignment is not prior method use.
				// Only a repository-issued receipt plus immutable readback can
				// establish the saved method; a prose claim is insufficient.
				if actual.Report.Skill == nil {
					panic("missing committed agent learning receipt")
				}
				var committed store.BotSkillRevision
				p.get(fmt.Sprintf("/%s/revisions/%d", actual.Report.Skill.SkillID, actual.Report.Skill.CommittedRevision), &committed)
				must(skillsAgentSaved(w, actual, committed))
				p.captureRun(actual)
				return w
			}
			if w.Contract.Skill == nil {
				panic("missing actual skill invocation")
			}
			var revision store.BotSkillRevision
			p.get(fmt.Sprintf("/%s/revisions/%d", w.Contract.Skill.SkillID, w.Contract.Skill.Revision), &revision)
			must(skillsPinned(w, actual, revision))
			if actual.Report.Skill.CommittedRevision > 0 {
				var committed store.BotSkillRevision
				p.get(fmt.Sprintf("/%s/revisions/%d", actual.Report.Skill.SkillID, actual.Report.Skill.CommittedRevision), &committed)
				must(committed.Validate())
				if committed.BotID != actual.BotID || committed.SkillID != revision.SkillID || committed.Digest != actual.Report.Skill.Digest {
					panic("committed skill receipt does not match endpoint revision")
				}
			}
			p.captureRun(actual)
			return w
		}
	}
	panic("missing skill work")
}
func skillsNoSchedule(d employeeDisk) error {
	if len(d.Jobs) != 0 {
		return fmt.Errorf("assignment/removal retained scheduler jobs")
	}
	for _, row := range d.Runs {
		if employeeRun(row).Trigger == store.TriggerCron {
			return fmt.Errorf("assignment scheduled work without consent")
		}
	}
	return nil
}
func startSkills(root, home string, state map[string]any, api func(string, string, any) string, send func(string, string) store.BotRun, read func(string, string) employeeDisk, runs func() []store.BotRun) *skillsProbe {
	if os.Getenv("BOT_SKILLS_ACCEPTANCE") != "1" {
		return nil
	}
	if state["workflow"] != skillsContract || state["skills_nonce"] == nil {
		panic("skills requires fresh launcher contract")
	}
	return startSkillsProbe(root, home, state, api, send, read, runs, false)
}

// startAgentSkills is the agent harness entry point. State must belong to this
// fresh launcher invocation, with bot_id, agent_nonce and both candidate hashes.
// The caller owns schedule creation, owned restart/reconnect and removal; see
// runAgentSkillsJourney for the complete sequence. No legacy quality pass is used.
func startAgentSkills(root, home string, state map[string]any, api func(string, string, any) string, send func(string, string) store.BotRun, read func(string, string) employeeDisk, runs func() []store.BotRun) *skillsProbe {
	if state["workflow"] != agentLiveContract || state["agent_learning_attempted"] != nil {
		panic("agent skills requires fresh launcher invocation")
	}
	for _, key := range []string{"agent_nonce", "binary_sha256", "browser_sha256", "bot_id"} {
		if value, ok := state[key].(string); !ok || strings.TrimSpace(value) == "" {
			panic("agent skills missing identity: " + key)
		}
	}
	state["agent_learning_attempted"] = true
	return startSkillsProbe(root, home, state, api, send, read, runs, true)
}

func startSkillsProbe(root, home string, state map[string]any, api func(string, string, any) string, send func(string, string) store.BotRun, read func(string, string) employeeDisk, runs func() []store.BotRun, agent bool) *skillsProbe {
	p := &skillsProbe{root: root, home: home, state: state, botPath: "/api/studio/bots/" + state["bot_id"].(string), api: api, read: read, runs: runs, receipts: map[string]map[string]any{}, agent: agent, startedAt: time.Now().UTC(), captures: map[uuid.UUID]string{}}
	assignment := "Take ongoing responsibility for my Orlando weather reports. Save and use a reusable Bot skill for this responsibility: retrieve fresh current weather, source URL and UTC retrieval time, Celsius and concise Markdown table, following my saved Orlando report quality standards. Produce a current report now. Do not create or enable any schedule."
	if agent {
		assignment = "Take ongoing responsibility for my Orlando weather reports: retrieve fresh current weather, source URL and UTC retrieval time, Celsius and a concise Markdown table. Produce a current report now and retain a reusable method for future reports if useful. Do not create or enable any schedule."
	}
	r := p.wait(send("skills-assign", assignment))
	w := p.work(r)
	if agent && w.Contract.Skill == nil {
		p.skill = r.Report.Skill.SkillID // work verified the committed receipt/readback
	} else {
		p.skill = w.Contract.Skill.SkillID
	}
	p.baseline = p.head()
	if agent {
		must(skillsAgentAssignment(w, r, p.baseline))
	}
	d := read("skills-no-schedule", "read")
	must(skillsNoSchedule(d))
	p.receipt("assignment-without-schedule", map[string]any{"head": p.baseline, "work": w, "disk": d}, "Owner responsibility committed as Bot skill without scheduler jobs")
	p.manual = p.wait(send("skills-reuse", "Produce my Orlando weather report again now using the saved responsibility and method. No schedule."))
	reuse := p.work(p.manual)
	if reuse.Contract.Skill == nil || reuse.Contract.Skill.SkillID != p.skill || reuse.Contract.Skill.Revision < p.baseline.Revision {
		panic("manual report did not reuse the assigned skill")
	}
	p.receipt("reuse-pinned-skill", map[string]any{"run": p.manual, "work": reuse}, "New owner run reused immutable saved Bot skill revision")
	before := p.head()
	feedback := p.wait(send("skills-feedback", "Improve my saved Orlando weather report skill: retain all previous requirements and additionally always include relative humidity as a percentage. This is owner feedback for future reports, not just this turn. Save the revised skill and deliver a fresh report now; still do not schedule anything."))
	p.work(feedback)
	p.updated = p.head()
	must(skillsOwnerRevision(before, p.updated, feedback))
	must(skillsNoSchedule(read("skills-feedback-no-schedule", "read")))
	var old store.BotSkillRevision
	p.get(fmt.Sprintf("/%s/revisions/%d", p.skill, before.Revision), &old)
	if !reflect.DeepEqual(old, before) {
		panic("owner feedback overwrote immutable history")
	}
	p.receipt("owner-feedback-revision", map[string]any{"before": before, "after": p.updated, "source_run": feedback, "old_readback": old}, "Owner feedback committed newer revision with source provenance; old revision unchanged")
	return p
}
func skillsOwnerRevision(before, after store.BotSkillRevision, source store.BotRun) error {
	if before.SkillID != after.SkillID || before.BotID != after.BotID || source.BotID != after.BotID || after.Revision <= before.Revision || after.Digest == before.Digest {
		return fmt.Errorf("owner feedback did not advance same skill")
	}
	found := false
	for _, e := range after.Expectations {
		if strings.Contains(strings.ToLower(e.Text), "humidity") && e.Provenance.Level == store.BotSkillOwnerSpecified && e.Provenance.RunID == source.ID && e.Provenance.EventID == source.UserEventID && source.UserEventID != "" {
			found = true
		}
	}
	if !found {
		return fmt.Errorf("humidity expectation lacks exact owner source")
	}
	for _, e := range before.Expectations {
		retained := false
		for _, next := range after.Expectations {
			if next.ID == e.ID && next.Text == e.Text {
				retained = true
			}
		}
		if !retained {
			return fmt.Errorf("owner feedback lost prior expectation")
		}
	}
	return nil
}
func (p *skillsProbe) schedule(version store.BotVersion, trigger uuid.UUID) {
	if version.BotID.String() != p.state["bot_id"] || trigger == uuid.Nil {
		panic("foreign skill schedule")
	}
	for _, t := range version.Triggers {
		if t.ID == trigger && t.SkillID != nil && *t.SkillID == p.skill && t.Invocation != "" && t.Enabled {
			return
		}
	}
	panic("schedule is not a thin invocation of the assigned skill")
}

func (p *skillsProbe) offline(d employeeDisk, trigger uuid.UUID, since time.Time) bool {
	if p == nil {
		return true
	}
	ticks := []store.BotRun{}
	seen := map[uuid.UUID]bool{}
	for _, row := range d.Runs {
		r := employeeRun(row)
		if !employeePostRestartRun(r, uuid.MustParse(p.state["bot_id"].(string)), trigger, since) || !r.Status.Terminal() {
			continue
		}
		w, r, err := p.decodeWork(row)
		must(err)
		if w.Contract.Skill == nil || w.Contract.Skill.SkillID != p.skill || w.Contract.Skill.Revision < p.updated.Revision {
			panic("scheduled run lost updated skill")
		}
		must(skillsPinned(w, r, w.Contract.Skill.Snapshot))
		if seen[r.ID] {
			panic("duplicate scheduled skill run")
		}
		seen[r.ID] = true
		ticks = append(ticks, r)
	}
	if len(ticks) < 3 {
		return false
	}
	p.ticks = ticks[:3]
	ids := []string{}
	for _, r := range p.ticks {
		ids = append(ids, r.ID.String())
	}
	if p.agent {
		must(skillsDistinctOccurrences(p.ticks))
		// Offline observations prove restart, not retrieval. Finish this receipt
		// only after reconnect fetches each occurrence's actual worker transcript.
		return true
	}
	p.receipt("owned-restart-three-scheduled", map[string]any{"restart": since, "trigger": trigger, "disk": d}, "Three independent committed skill outcomes after owned restart with browser closed before login", map[string]any{"RunIDs": ids})
	return true
}
func (p *skillsProbe) restored() {
	if p == nil {
		return
	}
	observations := []any{p.work(p.manual)}
	ids := []string{}
	for _, r := range p.ticks {
		observations = append(observations, p.work(r))
		ids = append(ids, r.ID.String())
	}
	p.receipt("manual-and-scheduled-outcomes", observations, "Manual and all three scheduled pins independently match immutable revisions read from actual Bot endpoints", map[string]any{"ManualRunID": p.manual.ID.String(), "RunIDs": ids})
}
func (p *skillsProbe) removeAndInspect(currentPage func() *rod.Page, send func(string, string) store.BotRun, change func(string, string, store.BotControlOperation, bool) store.BotOperationView) {
	if p == nil {
		return
	}
	change("skills-remove-schedule", "Remove the weather schedule entirely, not just pause it. Keep my saved Orlando weather report skill and all learned standards available for manual requests.", store.BotControlScheduleRemove, false)
	// Removal must not erase knowledge. Wait for drained work before observing GET purity.
	boundedWait("skills removed schedule drained", 6*time.Minute, func() bool {
		return employeeExecutionIdle(p.read("skills-remove-idle", "read"), p.state["bot_id"].(string))
	})
	before := p.head()
	r := p.wait(send("skills-after-removal", "Produce my Orlando weather report now using the saved skill. Do not recreate any schedule."))
	w := p.work(r)
	if w.Contract.Skill == nil || w.Contract.Skill.SkillID != p.skill || w.Contract.Skill.Revision < before.Revision {
		panic("schedule removal lost manual skill")
	}
	d := p.read("skills-removed", "read")
	if len(d.Jobs) != 0 {
		panic("schedule removal left jobs")
	}
	p.receipt("schedule-removal-manual", map[string]any{"run": r, "work": w, "head": p.head(), "disk": d}, "Deleting schedule retained skill and a new manual outcome without jobs", map[string]any{"ManualRunID": r.ID.String()})
	boundedWait("skills inspection idle", 6*time.Minute, func() bool {
		return employeeExecutionIdle(p.read("skills-inspect-idle", "read"), p.state["bot_id"].(string))
	})
	head := p.head()
	prior := p.runs()
	var list store.BotSkillList
	p.get("", &list)
	var history store.BotSkillRevisionList
	p.get("/"+p.skill.String()+"/revisions", &history)
	var runs store.BotSkillRunList
	p.get("/"+p.skill.String()+"/runs", &runs)
	if len(history.Revisions) < 2 || len(runs.Runs) < 5 {
		panic("skill inspection lacks revision/run history")
	}
	must(skillsInspection(head, history, runs, r.ID))
	page := currentPage() // Fresh browser deadline after potentially long model turns.
	page.MustElementR("button", "^Details$").MustClick()
	page.MustElementR("summary", "^Skills & history$").MustClick()
	title := ""
	for _, s := range list.Skills {
		if s.ID == p.skill {
			title = s.Title
		}
	}
	if title == "" {
		panic("skill absent from listing")
	}
	page.MustElementR(`[aria-label="Bot skills"] button`, "^"+regexp.QuoteMeta(title)+"$").MustClick()
	page.MustElement(`[aria-label="Skill history"]`).MustVisible()
	page.MustElementR(`[role="dialog"] button`, "^Close$").MustClick()
	page.MustReload()
	page.MustElement(`[aria-label="Bot workspace"]`).MustVisible()
	if !reflect.DeepEqual(head, p.head()) || !reflect.DeepEqual(prior, p.runs()) {
		panic("read-only skill inspection changed state")
	}
	p.receipt("skill-inspection-readonly", map[string]any{"list": list, "history": history, "runs": runs, "head": head}, "Skills UI and actual list/head/history/run endpoints inspected; reload did not mutate head or runs")
}
