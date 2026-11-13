package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

func TestAgentCaptureProducerStrictContract(t *testing.T) {
	// The Python contract owns the synthetic record fixture. Feed it through the
	// real Go manifest writers and raw-byte encoder, then validate those files.
	// This is a schema integration test, explicitly not a live parity receipt.
	seed := exec.Command("python3", "-B", "-c", `import json,sys
sys.path.insert(0,'..')
from agent_terminal_contract_test import TerminalContractTests
f=TerminalContractTests();f.setUp()
print(json.dumps(dict(state=f.state, terminal=f.terminal['passes'][0], studio=f.studio['passes'][0])))
f.doCleanups()`)
	raw, err := seed.CombinedOutput()
	if err != nil {
		t.Fatalf("contract fixture: %v %s", err, raw)
	}
	var fixture struct {
		State    map[string]any    `json:"state"`
		Terminal agentTerminalPass `json:"terminal"`
		Studio   agentStudioPass   `json:"studio"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	fixture.State["root"] = root
	binary := filepath.Join(root, "fixture-cli")
	if err := os.WriteFile(binary, []byte("synthetic CLI fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	hash, err := agentBindCLIHash(binary, fixture.State)
	if err != nil {
		t.Fatal(err)
	}
	for name, frame := range fixture.Terminal.Frames {
		wire, err := base64.StdEncoding.DecodeString(frame.Data)
		if err != nil {
			t.Fatal(err)
		}
		p := &agentTerminalPTY{}
		p.output.Write(wire)
		fixture.Terminal.Frames[name] = p.rawFrame(frame.Cols)
	}
	base, bot := fixture.State["url"].(string), fixture.State["bot_id"].(string)
	terminal, err := agentWriteTerminalManifest(root, base, bot, hash, fixture.State, []agentTerminalPass{fixture.Terminal, fixture.Terminal}, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	studio, err := agentObserveStudio(root, base, bot, fixture.State, func(int) agentStudioPass { return fixture.Studio })
	if err != nil {
		t.Fatal(err)
	}
	actual := map[string]any{"terminal_manifest": terminal, "studio_manifest": studio}
	validate := func() ([]byte, error) {
		input, err := json.Marshal(map[string]any{"state": fixture.State, "actual": actual})
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command("python3", "-B", "-c", `import json,sys
sys.path.insert(0,'..')
from agent_terminal_contract import terminal_parity
v=json.load(sys.stdin);terminal_parity(v['state'],v['actual'])`)
		cmd.Stdin = bytes.NewReader(input)
		return cmd.CombinedOutput()
	}
	if out, err := validate(); err != nil {
		t.Fatalf("producer/validator schema mismatch: %v %s", err, out)
	}
	// Both observers agreeing on records must not turn different DOM text into
	// a pass. Rehash via the production writer, so the semantic gate catches it.
	fixture.Studio.Rendered.Text = "not the canonical DOM answer"
	studio, err = agentObserveStudio(root, base, bot, fixture.State, func(int) agentStudioPass { return fixture.Studio })
	if err != nil {
		t.Fatal(err)
	}
	actual["studio_manifest"] = studio
	if out, err := validate(); err == nil || !strings.Contains(string(out), "Studio final text differs") {
		t.Fatalf("DOM mismatch not rejected: %v %s", err, out)
	}
}

func TestAgentStudioManifestUsesIndependentPasses(t *testing.T) {
	root := t.TempDir()
	state := map[string]any{"url": "http://127.0.0.1:12345", "bot_id": "owned", "agent_nonce": "nonce", "binary_sha256": "candidate", "browser_sha256": "browser"}
	calls := 0
	receipt, err := agentObserveStudio(root, state["url"].(string), "owned", state, func(pass int) agentStudioPass {
		if pass != calls {
			t.Fatal("not independent ordered observations")
		}
		calls++
		return agentStudioPass{Exchanges: []agentTerminalExchange{{Path: "raw-path", Status: 200, Body: fmt.Sprintf("raw-%d", pass)}}, Rendered: agentStudioRendered{Text: fmt.Sprintf("DOM-%d", pass), Invocation: "bot-report:fixture", Visible: true, RunStates: json.RawMessage(`[]`), Skills: json.RawMessage(`[]`)}}
	})
	if err != nil || calls != 2 {
		t.Fatalf("capture failed: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(root, receipt["Source"]))
	if err != nil || receipt["SourceHash"] != fmt.Sprintf("%x", sha256.Sum256(raw)) {
		t.Fatal("manifest digest mismatch")
	}
	var doc struct {
		Contract string            `json:"contract"`
		Passes   []agentStudioPass `json:"passes"`
	}
	if json.Unmarshal(raw, &doc) != nil || doc.Contract != "bot-agent-studio-v1" || len(doc.Passes) != 2 || doc.Passes[0].Rendered.Text != "DOM-0" || doc.Passes[1].Exchanges[0].Body != "raw-1" {
		t.Fatal("capture transformed independent evidence")
	}
	if _, err = agentObserveStudio(root, "http://127.0.0.1:9393", "owned", state, func(int) agentStudioPass { t.Fatal("foreign capture executed"); return agentStudioPass{} }); err == nil {
		t.Fatal("foreign candidate accepted")
	}
}

func TestAgentStudioRawExchangeScript(t *testing.T) {
	script := `const assert=require('node:assert/strict');
 const bodies=[{bot:{home_session_id:'home'}},{skills:[]}], paths=[];
 const transcript='id: home:1\nevent: transcript\ndata: {"content":{"parts":[{"text":"raw answer"}]}}\n\n';
 const replay='event: replay_complete\ndata: {}\n\n';
 const chunks=[transcript.slice(0,19),transcript.slice(19)+replay];let cancelled=false;
 global.fetch=async (path,opts)=>{paths.push(path);assert.equal(opts.credentials,'include');assert.equal(opts.method,undefined);const n=paths.length;
 if(n<3)return {ok:true,status:200,text:async()=>JSON.stringify(bodies[n-1])};
 return {ok:true,status:200,body:{getReader:()=>({read:async()=>chunks.length?{done:false,value:new TextEncoder().encode(chunks.shift())}:{done:true},cancel:async()=>{cancelled=true}})}}};
 (async()=>{const rows=JSON.parse(await (` + agentStudioExchangeJS + `)('owned'));assert.equal(rows.length,4);assert.equal(rows[2].body,transcript.trimEnd());assert.equal(rows[3].body,replay.trimEnd());assert.equal(rows[2].path,'/api/studio/bots/owned/sessions/home/events?scope=personal');assert.equal(cancelled,true);})().catch(e=>{console.error(e);process.exit(1)});
 `
	if out, err := exec.Command("node", "-e", script).CombinedOutput(); err != nil {
		t.Fatalf("raw browser exchange script: %v %s", err, out)
	}
}

func TestAgentStudioScriptsReadDOMNotAPIFabrication(t *testing.T) {
	// Execute the actual observation functions against minimal mounted-component
	// fixtures; no daemon, provider, credentials or normal browser profile.
	script := `const assert=require('node:assert/strict');
 const skills=[{id:'skill',title:'DOM skill'}];
 const skillNode={checkVisibility:()=>true,__reactFiber$fixture:{memoizedState:{memoizedState:{data:{skills},loading:false},next:null},return:null}};
 const bubble={innerText:'visible DOM answer',checkVisibility:()=>true,scrollIntoView:()=>{}};
 const detail={open:false,checkVisibility:()=>true,innerText:'Completed by agent: DOM work'};
 const messages=[{type:'agent',invocationId:'bot-report:fixture',content:'NOT THE DOM TEXT'}];
 const states={fixture:{schema_version:1,run_id:'fixture',state_sequence:1,work:{summary:'NOT THE DOM WORK'}}};
 const area={__reactFiber$fixture:{memoizedState:{memoizedState:messages,next:{memoizedState:states,next:null}},return:null}};
 const root={querySelector:s=>s.includes('message-area')?area:s.includes('data-message-index')?{querySelector:()=>bubble}:s.includes('Work for run')?{querySelector:()=>detail}:null};
 global.document={querySelector:s=>s.includes('Bot skills')?skillNode:root};
 const observedSkills=(` + agentStudioSkillsJS + `)();
 assert.deepEqual(JSON.parse(observedSkills),skills);
 const observed=JSON.parse((` + agentStudioRenderedJS + `)());
 assert.equal(observed.text,'visible DOM answer');assert.equal(observed.invocation_id,'bot-report:fixture');assert.equal(observed.visible,true);assert.deepEqual(observed.run_states,Object.values(states));assert.equal(detail.open,true);
 assert.equal(observed.work_text.fixture,'Completed by agent: DOM work');
 detail.checkVisibility=()=>false;assert.equal((` + agentStudioRenderedJS + `)(),'');detail.checkVisibility=()=>true;
 bubble.checkVisibility=()=>false;assert.equal(JSON.parse((` + agentStudioRenderedJS + `)()).visible,false);
 delete area.__reactFiber$fixture;assert.equal((` + agentStudioRenderedJS + `)(),'');
 `
	if out, err := exec.Command("node", "-e", script).CombinedOutput(); err != nil {
		t.Fatalf("DOM capture scripts: %v %s", err, out)
	}
}

func TestAgentLiveSemanticSummaryDoesNotTrustJourneyFlags(t *testing.T) {
	evidence := map[string]any{"journeys": map[string]any{"adapter-provenance-outcome": map[string]any{"status": "verified"}}}
	results := map[string]map[string]string{}
	for _, name := range append([]string{"stock-1", "stock-2", "stock-3"}, agentOutstanding...) {
		results[name] = map[string]string{"status": "unverified", "reason": "missing semantic proof"}
	}
	results["document-revision"] = map[string]string{"status": "passed"}
	raw, err := json.Marshal(results)
	if err != nil {
		t.Fatal(err)
	}
	if err := agentApplyEvidenceResults(evidence, raw); err != nil {
		t.Fatal(err)
	}
	if evidence["status"] != "blocked" || len(evidence["passed"].([]string)) != 1 || evidence["passed"].([]string)[0] != "document-revision" {
		t.Fatalf("incorrect semantic summary: %v", evidence)
	}
	for _, invalid := range []string{`{}`, `{"invented":{"status":"passed"}}`, `not json`} {
		if agentApplyEvidenceResults(evidence, []byte(invalid)) == nil {
			t.Fatal("accepted malformed evaluator results")
		}
	}
	results["document-revision"]["status"] = "verified"
	raw, _ = json.Marshal(results)
	if agentApplyEvidenceResults(evidence, raw) == nil {
		t.Fatal("accepted producer status instead of evaluator result")
	}
}

func TestAgentLiveResolvedParityRejectsAliasAndMissingCredentials(t *testing.T) {
	home := agentHomeModel{Provider: agentProvider, Model: agentModel, Credentials: true}
	run := store.BotRun{Provider: agentProvider, Model: agentModel}
	if err := agentRunParity(home, run); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*agentHomeModel, *store.BotRun){
		func(h *agentHomeModel, r *store.BotRun) { h.Provider = "alias" },
		func(h *agentHomeModel, r *store.BotRun) { h.Model = "other" },
		func(h *agentHomeModel, r *store.BotRun) { h.Credentials = false },
		func(h *agentHomeModel, r *store.BotRun) { r.Provider = "alias" },
		func(h *agentHomeModel, r *store.BotRun) { r.Model = "other" },
	} {
		h, r := home, run
		mutate(&h, &r)
		if agentRunParity(h, r) == nil {
			t.Fatal("accepted mismatched runtime identity")
		}
	}
}

func TestAgentLiveResultRequiresAgentProjectionNotLegacyReview(t *testing.T) {
	now := time.Now()
	run := store.BotRun{ID: uuid.New(), BotID: uuid.New(), SessionID: uuid.New(), Status: store.RunSucceeded, StartedAt: &now, FinishedAt: &now, Report: store.BotReport{Summary: "Actual answer"}}
	run.Work = &store.BotWorkProjection{SchemaVersion: store.BotAgentWorkSchemaVersion, Protocol: store.BotWorkProtocolAgentLedV1, BotID: run.BotID, RunID: run.ID, WorkRevision: 1, Phase: store.BotWorkFinished, Disposition: store.AgentWorkAnswered, CandidateDigest: strings.Repeat("a", 64)}
	if err := agentRunResult(run, run.BotID); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*store.BotRun){
		func(r *store.BotRun) { r.Work = nil },
		func(r *store.BotRun) { r.Status = store.RunPartial },
		func(r *store.BotRun) { r.Report.Summary = " " },
		func(r *store.BotRun) { r.Work.RunID = uuid.New() },
		func(r *store.BotRun) { r.Work.Phase = store.BotWorkAccepted },
		func(r *store.BotRun) { r.Work.Disposition = store.AgentWorkPartial },
	} {
		r := run
		w := *run.Work
		r.Work = &w
		mutate(&r)
		if agentRunResult(r, run.BotID) == nil {
			t.Fatal("accepted incomplete or legacy run")
		}
	}
}
