package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"
)

const skillsContract = "bot-skills-live-v1"
const agentSkillsContract = "bot-agent-skills-live-v1"

// Agent work is validated on its own protocol, never upgraded into a legacy
// accepted review. A final answer is not itself factual-grounding certification.
func (p *skillsProbe) decodeWork(row employeeDiskRow) (*store.BotWorkState, store.BotRun, error) {
	if !p.agent {
		return qualityWork(row)
	}
	r := employeeRun(row)
	_, fields, err := employeeStoredDocument(row.Data)
	if err != nil {
		return nil, r, err
	}
	if _, ok := fields["run"]; !ok {
		return nil, r, fmt.Errorf("missing private work envelope")
	}
	var w store.BotWorkState
	if err := json.Unmarshal(fields["work"], &w); err != nil {
		return nil, r, err
	}
	if err := w.Validate(); err != nil {
		return nil, r, err
	}
	if w.Protocol != store.BotWorkProtocolAgentLedV1 || w.Contract.RunID != r.ID || w.Contract.BotID != r.BotID || w.Phase != store.BotWorkFinished || w.AgentWork == nil || w.Review != nil || r.Report.Quality != nil {
		return nil, r, fmt.Errorf("agent skill work identity/protocol mismatch or fabricated legacy assessment")
	}
	if err := agentRunResult(r, uuid.MustParse(p.state["bot_id"].(string))); err != nil {
		return nil, r, err
	}
	if !reflect.DeepEqual(store.BuildBotWorkProjection(&w), r.Work) || p.startedAt.IsZero() || r.CreatedAt.Before(p.startedAt) || r.StartedAt.Before(p.startedAt) {
		return nil, r, fmt.Errorf("agent skill work is stale or differs from public projection")
	}
	return &w, r, nil
}

func skillsDistinctOccurrences(runs []store.BotRun) error {
	if len(runs) != 3 {
		return fmt.Errorf("require exactly three scheduler occurrences")
	}
	ids, sessions, keys := map[uuid.UUID]bool{}, map[uuid.UUID]bool{}, map[string]bool{}
	for _, r := range runs {
		if r.ID == uuid.Nil || r.SessionID == uuid.Nil || strings.TrimSpace(r.IdempotencyKey) == "" || ids[r.ID] || sessions[r.SessionID] || keys[r.IdempotencyKey] {
			return fmt.Errorf("duplicate or unidentified scheduler occurrence")
		}
		ids[r.ID], sessions[r.SessionID], keys[r.IdempotencyKey] = true, true, true
	}
	return nil
}

// captureRun reads the run-specific persisted tool stream now, not a restored
// evidence file. Payload equality is allowed (weather can be unchanged); worker
// sessions and scheduler occurrence keys must differ. This proves retrieval was
// executed, not that every answer claim is true.
func (p *skillsProbe) captureRun(r store.BotRun) {
	if !p.agent || p.captures[r.ID] != "" {
		return
	}
	stream := p.api("GET", p.botPath+"/sessions/"+r.SessionID.String()+"/stream?scope=personal", nil)
	var bot store.Bot
	must(json.Unmarshal([]byte(p.api("GET", p.botPath+"?scope=personal", nil)), &bot))
	if bot.ID != r.BotID || bot.OwnerID == uuid.Nil || bot.Scope != store.BotScopePersonal {
		panic("foreign learning capture Bot ownership")
	}
	raw, err := json.MarshalIndent(map[string]any{"contract": agentSkillsContract, "nonce": p.state["agent_nonce"], "candidate": p.state["binary_sha256"], "browser_candidate": p.state["browser_sha256"], "captured_at": time.Now().UTC(), "run": r, "bot": bot, "stream": stream}, "", "  ")
	must(err)
	file := "agent-skills-run-" + r.ID.String() + ".json"
	must(os.WriteFile(filepath.Join(p.root, file), raw, 0600))
	if !hasRealWebEvidence(stream) {
		panic("agent skill occurrence lacks correlated fresh retrieval")
	}
	if p.captures == nil {
		p.captures = map[uuid.UUID]string{}
	}
	p.captures[r.ID] = fmt.Sprintf("%x", sha256.Sum256(raw))
}

// finishAgent returns only learning receipts. It must not mark the overall agent
// contract verified: provider parity, factual grounding and PTY remain separate.
func (p *skillsProbe) finishAgent() map[string]any {
	if p == nil || !p.agent {
		panic("missing agent learning probe")
	}
	for _, key := range skillsRequired {
		if p.receipts[key] == nil {
			panic("missing agent learning receipt: " + key)
		}
	}
	if len(p.receipts) != len(skillsRequired) || len(p.captures) < 7 {
		panic("incomplete agent learning captures")
	}
	return map[string]any{"contract": agentSkillsContract, "nonce": p.state["agent_nonce"], "receipts": p.receipts, "passed": skillsRequired, "status": "verified", "grounding_verified": false}
}

var skillsRequired = []string{"assignment-without-schedule", "reuse-pinned-skill", "owner-feedback-revision", "owned-restart-three-scheduled", "manual-and-scheduled-outcomes", "schedule-removal-manual", "skill-inspection-readonly"}

// skillsAgentSaved verifies a committed save without inventing prior use. It
// deliberately does not replace work-state validation or the later pin gate.
func skillsAgentSaved(w *store.BotWorkState, r store.BotRun, revision store.BotSkillRevision) error {
	if w == nil || w.Protocol != store.BotWorkProtocolAgentLedV1 || w.Contract.Skill != nil || r.Report.Skill == nil {
		return fmt.Errorf("missing agent save-only receipt")
	}
	receipt := r.Report.Skill
	if err := revision.Validate(); err != nil {
		return err
	}
	if receipt.SchemaVersion != store.BotSkillSchemaVersion || receipt.BotID != r.BotID || receipt.RunID != r.ID || r.ID == uuid.Nil || w.Contract.RunID != r.ID || w.Contract.BotID != r.BotID || receipt.SkillID != revision.SkillID || revision.BotID != r.BotID || receipt.UsedRevision != 0 || receipt.CommittedRevision != revision.Revision || receipt.Digest != revision.Digest {
		return fmt.Errorf("agent save receipt differs from immutable revision or claims unobserved use")
	}
	if receipt.Kind != store.BotSkillCreated && receipt.Kind != store.BotSkillUpdated {
		return fmt.Errorf("agent learning did not commit a saved revision")
	}
	return skillsAgentAssignment(w, r, revision)
}

// The assignment gate needs actual persisted method content and current owner
// provenance, even when learning was optional in the natural owner request.
func skillsAgentAssignment(w *store.BotWorkState, r store.BotRun, revision store.BotSkillRevision) error {
	if w == nil || r.Report.Skill == nil || revision.BotID != r.BotID || revision.SkillID != r.Report.Skill.SkillID || len(revision.Method) == 0 {
		return fmt.Errorf("assignment did not save a reusable method")
	}
	if err := revision.Validate(); err != nil {
		return err
	}
	receipt := r.Report.Skill
	current := receipt.CommittedRevision
	if current == 0 {
		current = receipt.UsedRevision
	}
	if revision.Revision != current || revision.Digest != receipt.Digest {
		return fmt.Errorf("current method differs from assignment receipt")
	}
	for _, e := range revision.Expectations {
		p := e.Provenance
		if p.Level == store.BotSkillOwnerSpecified && p.BotID == r.BotID && p.RunID == r.ID && p.SessionID == r.SessionID && p.EventID == r.UserEventID && r.UserEventID != "" {
			return nil
		}
	}
	return fmt.Errorf("saved method lacks current assignment provenance")
}

// Validate the private pinned work against the immutable revision fetched through
// the actual Bot endpoint and the repository-authored public receipt, not prose.
func skillsPinned(w *store.BotWorkState, r store.BotRun, revision store.BotSkillRevision) error {
	if w == nil || w.Contract.Skill == nil || w.SkillState == nil || w.SkillState.Receipt == nil || r.Report.Skill == nil {
		return fmt.Errorf("missing committed skill pin/receipt")
	}
	pin, receipt := w.Contract.Skill, r.Report.Skill
	if err := revision.Validate(); err != nil {
		return err
	}
	if r.ID == uuid.Nil || r.BotID != revision.BotID || w.Contract.RunID != r.ID || w.Contract.BotID != r.BotID || pin.SkillID != revision.SkillID || pin.Revision != revision.Revision || pin.Digest != revision.Digest || !reflect.DeepEqual(pin.Snapshot, revision) || receipt.BotID != r.BotID || receipt.RunID != r.ID || receipt.SkillID != pin.SkillID || receipt.UsedRevision != pin.Revision || !reflect.DeepEqual(receipt, w.SkillState.Receipt) {
		return fmt.Errorf("foreign or mismatched skill pin/receipt")
	}
	if receipt.SchemaVersion != store.BotSkillSchemaVersion || receipt.CommittedRevision < 0 {
		return fmt.Errorf("invalid skill receipt schema/revision")
	}
	switch receipt.Kind {
	case store.BotSkillCreated, store.BotSkillUpdated, store.BotSkillUnchanged, store.BotSkillSelected:
	default:
		return fmt.Errorf("skill learning unresolved: %s", receipt.Kind)
	}
	if receipt.CommittedRevision > 0 {
		unchangedAgent := w.Protocol == store.BotWorkProtocolAgentLedV1 && receipt.Kind == store.BotSkillUnchanged && receipt.CommittedRevision == receipt.UsedRevision && receipt.Digest == pin.Digest
		if (!unchangedAgent && receipt.CommittedRevision <= receipt.UsedRevision) || w.SkillState.Stage != store.BotSkillLearningCommitted || len(receipt.Digest) != 64 {
			return fmt.Errorf("invalid committed learning revision")
		}
	} else if receipt.Digest != pin.Digest || (w.SkillState.Stage != store.BotSkillOriented && w.SkillState.Stage != store.BotSkillLearningCommitted) {
		return fmt.Errorf("unresolved orientation or unchanged digest differs from pin")
	}
	return nil
}

func skillsInspection(head store.BotSkillRevision, history store.BotSkillRevisionList, runs store.BotSkillRunList, latest uuid.UUID) error {
	seen := map[int64]bool{}
	foundHead := false
	for _, revision := range history.Revisions {
		if err := revision.Validate(); err != nil {
			return err
		}
		if revision.BotID != head.BotID || revision.SkillID != head.SkillID || seen[revision.Revision] {
			return fmt.Errorf("foreign/duplicate skill history")
		}
		seen[revision.Revision] = true
		foundHead = foundHead || reflect.DeepEqual(head, revision)
	}
	seenRuns := map[uuid.UUID]bool{}
	for _, run := range runs.Runs {
		if run.ID == uuid.Nil || seenRuns[run.ID] || run.BotID != head.BotID || run.Report.Skill == nil || run.Report.Skill.SkillID != head.SkillID || run.Report.Skill.RunID != run.ID || run.Report.Skill.BotID != head.BotID {
			return fmt.Errorf("foreign/duplicate skill run history")
		}
		seenRuns[run.ID] = true
	}
	if !foundHead || !seenRuns[latest] {
		return fmt.Errorf("inspection missing current head/latest manual run")
	}
	return nil
}

func (p *skillsProbe) receipt(name string, observation any, detail string, identities ...map[string]any) {
	if p.receipts[name] != nil {
		panic("duplicate skills receipt: " + name)
	}
	nonce := p.state["skills_nonce"]
	prefix := "skills-"
	if p.agent {
		nonce, prefix = p.state["agent_nonce"], "agent-skills-"
	}
	document := map[string]any{"gate": name, "nonce": nonce, "candidate": p.state["binary_sha256"], "browser_candidate": p.state["browser_sha256"], "bot_id": p.state["bot_id"], "observation": observation}
	if p.agent {
		document["contract"] = agentSkillsContract
		document["captured_at"] = time.Now().UTC()
		document["invocation_started_at"] = p.startedAt
		document["run_captures"] = p.captures
	}
	if len(identities) > 1 {
		panic("multiple skill identity sets")
	}
	if len(identities) == 1 {
		document["identities"] = identities[0]
	}
	raw, err := json.MarshalIndent(document, "", "  ")
	must(err)
	file := prefix + name + ".json"
	must(os.WriteFile(filepath.Join(p.root, file), raw, 0600))
	p.receipts[name] = map[string]any{"SchemaVersion": 1, "Candidate": p.state["binary_sha256"], "BrowserCandidate": p.state["browser_sha256"], "Nonce": nonce, "BotID": p.state["bot_id"], "Home": p.home, "Status": "passed", "Detail": detail, "Source": file, "SourceHash": fmt.Sprintf("%x", sha256.Sum256(raw))}
	if len(identities) == 1 {
		for key, value := range identities[0] {
			if key != "RunIDs" && key != "ManualRunID" {
				panic("unknown skills identity")
			}
			p.receipts[name][key] = value
		}
	}
}
func (p *skillsProbe) finish(evidence map[string]any) {
	if p == nil {
		return
	}
	if evidence["contract"] != qualityContract {
		panic("skills requires preserved quality proof")
	}
	quality := map[string]any{}
	for k, v := range evidence {
		quality[k] = v
	}
	for _, name := range skillsRequired {
		if p.receipts[name] == nil {
			panic("missing skills receipt: " + name)
		}
	}
	if len(p.receipts) != len(skillsRequired) {
		panic("unknown skills receipt")
	}
	for k := range evidence {
		delete(evidence, k)
	}
	evidence["contract"] = skillsContract
	evidence["nonce"] = p.state["skills_nonce"]
	evidence["quality"] = quality
	evidence["receipts"] = p.receipts
	evidence["passed"] = skillsRequired
	evidence["status"] = "verified"
}
