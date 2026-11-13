package main

// Bounded live candidate diagnostic. All mutations use the native Studio UI;
// GETs observe the production owner-scoped resolver and actual worker records.
// This does not equate resolver metadata with adapter or factual-grounding proof.
import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/SAP/astonish/pkg/store"
	"github.com/go-rod/rod"
	"github.com/google/uuid"
)

const agentLiveContract = "bot-agent-live-v1"
const agentProvider = "New AI Core Provider"
const agentModel = "anthropic--claude-4.6-sonnet"

var agentOutstanding = []string{
	"adapter-provenance",
	"source-grounding",
	"document-revision",
	"conflicting-source-comparison",
	"feedback-restart",
	"skill-three-scheduler-occurrences",
	"schedule-delete-manual-reuse",
	"studio-terminal-parity",
}

type agentHomeModel struct {
	Provider    string `json:"effectiveProvider"`
	Model       string `json:"effectiveModel"`
	Credentials bool   `json:"credentialsAvailable"`
}

func agentRunParity(home agentHomeModel, run store.BotRun) error {
	if home.Provider != agentProvider || home.Model != agentModel || !home.Credentials || run.Provider != agentProvider || run.Model != agentModel {
		return fmt.Errorf("Home/worker resolved identity differs from intended AI Core provider/model, or credentials unavailable")
	}
	return nil
}

func agentRunResult(run store.BotRun, bot uuid.UUID) error {
	if run.ID == uuid.Nil || run.BotID != bot || run.SessionID == uuid.Nil || run.Status != store.RunSucceeded || run.StartedAt == nil || run.FinishedAt == nil || run.FinishedAt.Before(*run.StartedAt) || strings.TrimSpace(run.Report.Summary) == "" {
		return fmt.Errorf("title run lacks successful, identified, nonempty final result")
	}
	w := run.Work
	if w == nil || w.Validate() != nil || w.BotID != bot || w.RunID != run.ID || w.Protocol != store.BotWorkProtocolAgentLedV1 || w.Phase != store.BotWorkFinished || (w.Disposition != store.AgentWorkAnswered && w.Disposition != store.AgentWorkComplete) || len(w.Remaining) != 0 {
		return fmt.Errorf("title run lacks complete agent-led work projection")
	}
	return nil
}

func newAgentWorkflow(basePage *rod.Page, api func(string, string, any) string, state map[string]any, statePath, root, base string, evidence map[string]any, save func()) {
	inspectionTimeout, err := agentInspectionTimeout(os.LookupEnv)
	must(err) // Reject bad harness configuration before starting paid work.
	invocationStarted := time.Now().UTC()
	evidence["contract"] = agentLiveContract
	evidence["candidate"] = state["binary_sha256"]
	evidence["browser_candidate"] = state["browser_sha256"]
	evidence["nonce"] = state["agent_nonce"]
	evidence["unverified"] = agentOutstanding
	evidence["runtime_parity_verified"] = false
	// Never restore prior receipts as a pass, even when invoked outside the launcher.
	if state["workflow"] != agentLiveContract || state["agent_attempted"] != nil {
		panic("agent workflow requires fresh owned invocation")
	}
	var intended map[string]string
	raw, err := json.Marshal(state["agent_intended"])
	must(err)
	must(json.Unmarshal(raw, &intended))
	if !reflect.DeepEqual(intended, map[string]string{"provider": agentProvider, "provider_type": "sap_ai_core", "model": agentModel}) {
		panic("agent intended provider identity mismatch")
	}
	state["agent_attempted"] = true
	persist := func() {
		raw, err := json.MarshalIndent(state, "", "  ")
		must(err)
		must(os.WriteFile(statePath, raw, 0600))
	}
	persist()
	samples := []map[string]any{}
	evidence["samples"] = samples
	save()
	page := func() *rod.Page { return independentPageTimeout(basePage, 40*time.Second) }
	for n := 1; n <= 3; n++ {
		key := fmt.Sprintf("stock-%d", n)
		sample := map[string]any{"prompt": "Stock Market News", "sample": key, "status": "running", "sources": map[string]string{}}
		samples = append(samples, sample)
		evidence["samples"] = samples
		evidence["stage"] = key
		save()
		// A failed sample remains evidence and does not suppress later fresh samples.
		func() {
			defer func() {
				if failure := recover(); failure != nil {
					sample["status"] = "failed"
					sample["blocker"] = fmt.Sprint(failure)
					save()
				}
			}()
			capture := func(name, path string) string {
				text := api("GET", path, nil)
				file := "agent-" + key + "-" + name
				must(os.WriteFile(filepath.Join(root, file), []byte(text), 0600))
				sample["sources"].(map[string]string)[file] = fmt.Sprintf("%x", sha256.Sum256([]byte(text)))
				save()
				return text
			}
			page().MustNavigate(base + "/#/chat")
			page().MustElementR("button", "^New Bot$").MustClick()
			page().MustElement(`[aria-label="Bot name"]`).MustInput("Stock Market News")
			sample["creation_attempted"] = true
			save()
			page().MustElementR("button", "^Start conversation$").MustClick()
			page().MustElement(`[aria-label="Bot workspace"]`).MustVisible()
			homeURL := page().MustInfo().URL
			prefix := base + "/#/chat/bots/personal/"
			if !strings.HasPrefix(homeURL, prefix) {
				panic("not candidate personal Home")
			}
			botID, err := uuid.Parse(strings.TrimPrefix(homeURL, prefix))
			must(err)
			sample["bot_id"] = botID.String()
			sample["home"] = homeURL
			state["bot_id"] = botID.String()
			persist()
			save()
			botPath := "/api/studio/bots/" + botID.String()
			var bot store.Bot
			must(json.Unmarshal([]byte(capture("bot.json", botPath+"?scope=personal")), &bot))
			if bot.ID != botID || bot.HomeSessionID == uuid.Nil {
				panic("Bot/Home identity mismatch")
			}
			modelPath := "/api/studio/sessions/" + bot.HomeSessionID.String() + "/model-status?scope=personal"
			var home agentHomeModel
			must(json.Unmarshal([]byte(capture("model-before.json", modelPath)), &home))
			if home.Provider != agentProvider || home.Model != agentModel || !home.Credentials {
				panic("Home resolver parity unavailable")
			}
			var run store.BotRun
			boundedWait("fresh agent title result", 12*time.Minute, func() bool {
				var rows struct {
					Runs []store.BotRun `json:"runs"`
				}
				must(json.Unmarshal([]byte(capture("runs.json", botPath+"/runs?scope=personal")), &rows))
				if len(rows.Runs) > 1 {
					panic("duplicate title run")
				}
				if len(rows.Runs) == 0 {
					return false
				}
				run = rows.Runs[0]
				return run.Status.Terminal()
			})
			sample["run_id"] = run.ID.String()
			save()
			// Retain all outcomes before asserting success, including failed/partial runs.
			historyPath := "/api/studio/sessions/" + bot.HomeSessionID.String()
			before := capture("history-before.json", historyPath)
			stream := capture("worker-events.txt", botPath+"/sessions/"+run.SessionID.String()+"/stream?scope=personal")
			// Per-sample fields are resolver observations only. The final independent
			// adapter gate reads actual object/call records from owned daemon logs.
			sample["resolver_observation"] = map[string]string{"provider": run.Provider, "model": run.Model}
			payloads, payloadErr := agentSourcePayloads(stream)
			sample["source_payloads"] = payloads
			if payloadErr != nil {
				sample["source_capture_error"] = payloadErr.Error()
			}
			sample["grounding_verified"] = false
			sample["grounding_blocker"] = "Captured source payloads are evidence for assessment, not proof that every material answer claim is supported"
			save()
			must(agentRunParity(home, run))
			must(agentRunResult(run, botID))
			if !hasRealWebEvidence(stream) {
				panic("no correlated successful producer-selected retrieval in worker transcript")
			}
			messages, err := chatModelMessages(before)
			must(err)
			answer := chatModelAnswerIndex(messages)
			if answer < 0 || answer >= len(messages) {
				panic("published Home answer missing")
			}
			selector := fmt.Sprintf(`[aria-label="Bot workspace"] [data-message-index="%d"]`, answer)
			visible := page().MustElement(selector).MustText()
			if strings.TrimSpace(visible) == "" {
				panic("empty visible Home answer")
			}
			page().MustReload()
			if page().MustElement(selector).MustText() != visible {
				panic("reloaded answer differs from live answer")
			}
			after := capture("history-after.json", historyPath)
			restored, err := chatModelMessages(after)
			must(err)
			if !reflect.DeepEqual(messages, restored) {
				panic("reconnect changed ordered persisted messages")
			}
			must(json.Unmarshal([]byte(capture("model-after.json", modelPath)), &home))
			must(agentRunParity(home, run))
			var restoredRuns struct {
				Runs []store.BotRun `json:"runs"`
			}
			must(json.Unmarshal([]byte(capture("runs-after.json", botPath+"/runs?scope=personal")), &restoredRuns))
			if len(restoredRuns.Runs) != 1 || !reflect.DeepEqual(restoredRuns.Runs[0].Work, run.Work) || !reflect.DeepEqual(restoredRuns.Runs[0].Report, run.Report) {
				panic("reconnect duplicated work or changed report/progress")
			}
			sample["status"] = agentDiagnosticObserved
			sample["observed"] = []string{"single-fresh-agent-result", "home-worker-resolver-parity", "producer-selected-retrieval", "live-reloaded-answer-and-projection"}
			// No PASS receipt: tools returning data cannot certify factual correctness.
			save()
		}()
	}
	func() {
		defer func() {
			if failure := recover(); failure != nil {
				evidence["general_journey_blocker"] = fmt.Sprint(failure)
				save()
			}
		}()
		files := map[string]string{}
		evidence["general_sources"] = files
		agentGeneralJourneys(page, api, state, base, func(name, text string) string {
			file := "agent-" + name
			must(os.WriteFile(filepath.Join(root, file), []byte(text), 0600))
			files[file] = fmt.Sprintf("%x", sha256.Sum256([]byte(text)))
			save()
			return text
		}, evidence, save)
	}()
	func() {
		defer func() {
			if failure := recover(); failure != nil {
				evidence["learning_blocker"] = fmt.Sprint(failure)
				save()
			}
		}()
		evidence["stage"] = "agent-learning"
		save()
		agentLearningLifecycle(basePage, api, state, statePath, root, base, evidence, persist, save)
	}()
	func() {
		defer func() {
			if failure := recover(); failure != nil {
				evidence["studio_blocker"] = fmt.Sprint(failure)
				save()
			}
		}()
		receipt, err := agentObserveStudio(root, base, state["bot_id"].(string), state, func(pass int) agentStudioPass {
			page().MustNavigate(base + "/#/chat/bots/personal/" + state["bot_id"].(string))
			page().MustReload()
			page().MustElement(`[aria-label="Bot workspace"]`).MustVisible()
			var exchanges []agentTerminalExchange
			must(json.Unmarshal([]byte(page().MustEval(agentStudioExchangeJS, state["bot_id"]).Str()), &exchanges))
			page().MustElementR("button", "^Details$").MustClick()
			page().MustElementR("summary", "^Skills & history$").MustClick()
			page().MustElement(`[aria-label="Bot skills"]`).MustVisible()
			var rendered agentStudioRendered
			boundedWait("Studio rendered skill dataset", 20*time.Second, func() bool {
				result := page().MustEval(agentStudioSkillsJS).Str()
				if result == "" {
					return false
				}
				must(json.Unmarshal([]byte(result), &rendered.Skills))
				return true
			})
			rendered.SkillsText = page().MustElement(`[aria-label="Bot skills"]`).MustText()
			page().MustElementR("button", "^Close$").MustClick()
			boundedWait("Studio canonical final bubble and work", 25*time.Second, func() bool {
				result := page().MustEval(agentStudioRenderedJS).Str()
				if result == "" {
					return false
				}
				skills := rendered.Skills
				must(json.Unmarshal([]byte(result), &rendered))
				rendered.Skills = skills
				return rendered.Visible && rendered.Text != ""
			})
			return agentStudioPass{Exchanges: exchanges, Rendered: rendered}
		})
		must(err)
		evidence["studio_manifest"] = receipt
		save()
	}()
	manifest, err := json.Marshal(map[string]any{"contract": agentLiveContract, "nonce": state["agent_nonce"], "candidate": state["binary_sha256"], "browser_candidate": state["browser_sha256"], "sources": evidence["general_sources"], "journeys": evidence["journeys"]})
	must(err)
	must(os.WriteFile(filepath.Join(root, "agent-general-manifest.json"), manifest, 0600))
	evidence["general_manifest"] = map[string]string{"Source": "agent-general-manifest.json", "SourceHash": fmt.Sprintf("%x", sha256.Sum256(manifest))}
	adapterReceipt, adapterErr := agentCaptureAdapters(root, state, invocationStarted)
	if adapterErr != nil {
		evidence["adapter_blocker"] = adapterErr.Error()
	} else {
		evidence["adapter_manifest"] = adapterReceipt
	}
	inspectionErr := agentGroundingInspection(root, samples, evidence, save, inspectionTimeout, agentInspectionClock{
		now: time.Now, sleep: time.Sleep, check: agentAssessmentReady,
	})
	// Re-read retained records through the same structural evidence validator used
	// by the release launcher. Semantic judgments belong to the named evaluator;
	// producer status flags are never evidence of completion.
	raw, err = json.Marshal(map[string]any{"state": state, "evidence": evidence})
	must(err)
	command := exec.Command("python3", "-B", "tests/e2eboot/agent_live_contract.py")
	command.Stdin = bytes.NewReader(raw)
	validationErr := agentEvaluateAfterInspection(evidence, inspectionErr, command.Output)
	if validationErr != nil {
		evidence["status"] = "blocked"
		evidence["unverified"] = append([]string{"stock-1", "stock-2", "stock-3"}, agentOutstanding...)
		evidence["passed"] = []string{}
		delete(evidence, "receipts")
		evidence["blocker"] = "Structural evidence validator failed: " + validationErr.Error()
	}
	evidence["stage"] = "agent-proof-obligations"
	save()
}

type agentStudioRendered struct {
	Invocation string            `json:"invocation_id"`
	Text       string            `json:"text"`
	Visible    bool              `json:"visible"`
	RunStates  json.RawMessage   `json:"run_states"`
	Skills     json.RawMessage   `json:"skills"`
	WorkText   map[string]string `json:"work_text"`
	SkillsText string            `json:"skills_text"`
}
type agentStudioPass struct {
	Exchanges []agentTerminalExchange `json:"exchanges"`
	Rendered  agentStudioRendered     `json:"rendered"`
}

// Observe is a fixture seam, not an API-to-DOM adapter. Production supplies a
// fresh browser navigation, raw read-only responses, and actual component/DOM
// observation on both calls. No terminal records are inputs to this observer.
func agentObserveStudio(root, base, bot string, state map[string]any, observe func(int) agentStudioPass) (map[string]string, error) {
	if state["url"] != base || state["bot_id"] != bot {
		return nil, fmt.Errorf("Studio candidate binding mismatch")
	}
	passes := []agentStudioPass{observe(0), observe(1)}
	manifest := map[string]any{"contract": "bot-agent-studio-v1", "nonce": state["agent_nonce"], "candidate": state["binary_sha256"], "browser_candidate": state["browser_sha256"], "bot_id": bot, "url": base, "passes": passes}
	raw, err := json.Marshal(manifest)
	if err != nil {
		return nil, err
	}
	name := "agent-studio.json"
	if err = os.WriteFile(filepath.Join(root, name), raw, 0600); err != nil {
		return nil, err
	}
	return map[string]string{"Source": name, "SourceHash": fmt.Sprintf("%x", sha256.Sum256(raw))}, nil
}

// Browser-origin independent raw reads. SSE is cancelled only after replay_complete;
// response bodies remain unmodified and retain every transcript sequence.
const agentStudioExchangeJS = `async (bot) => {
 const base='/api/studio/bots/'+bot, rows=[];
 const controller=new AbortController(), timer=setTimeout(()=>controller.abort(),20000);
 const get=async path=>{const r=await fetch(path,{credentials:'include',signal:controller.signal}); const body=await r.text(); rows.push({path,status:r.status,body}); if(!r.ok) throw Error('Studio read HTTP '+r.status); return JSON.parse(body)};
 try {
  const snapshot=await get(base+'/snapshot?scope=personal');
  await get(base+'/skills?scope=personal');
  const path=base+'/sessions/'+snapshot.bot.home_session_id+'/events?scope=personal';
  const r=await fetch(path,{credentials:'include',signal:controller.signal});
  if(!r.ok) throw Error('Studio replay HTTP '+r.status);
  const reader=r.body.getReader(), decoder=new TextDecoder(); let pending='';
  for(;;){const part=await reader.read(); if(part.done) throw Error('Studio replay ended before replay_complete'); pending+=decoder.decode(part.value,{stream:true});
   let split; while((split=pending.indexOf('\n\n'))>=0){const body=pending.slice(0,split);pending=pending.slice(split+2);if(body.includes('data:')) rows.push({path,status:r.status,body});if(body.includes('event: replay_complete')){await reader.cancel(); return JSON.stringify(rows)}}
  }
 } finally {clearTimeout(timer);controller.abort()}
}`

// Read only the mounted React component dataset: unlike an API copy, these are
// the values actually passed to/rendered by Studio. Fail closed if React internals
// change. The final text itself always comes from the visible DOM bubble.
const agentStudioSkillsJS = `() => {
 const node=document.querySelector('[aria-label="Bot skills"]'); if(!node || !node.checkVisibility()) return '';
 const key=Object.keys(node).find(k=>k.startsWith('__reactFiber$')); if(!key) return '';
 let f=node[key];
 while(f){for(let h=f.memoizedState;h;h=h.next){const v=h.memoizedState;if(v && v.data && Array.isArray(v.data.skills) && v.loading===false && !v.error){return JSON.stringify(v.data.skills)}} f=f.return}
 return '';
}`
const agentStudioRenderedJS = `() => {
 const root=document.querySelector('[aria-label="Bot workspace"]');if(!root) return '';
 const area=root.querySelector('[data-testid="message-area"]');if(!area) return '';
 const key=Object.keys(area).find(k=>k.startsWith('__reactFiber$'));if(!key) return '';
 let f=area[key], messages, states;
 while(f){for(let h=f.memoizedState;h;h=h.next){const v=h.memoizedState;
  if(Array.isArray(v) && v.some(m=>m && m.type==='agent' && typeof m.invocationId==='string')) messages=v;
  if(v && !Array.isArray(v) && typeof v==='object'){const a=Object.values(v);if(a.length && a.every(s=>s && s.schema_version===1 && s.run_id && s.state_sequence)) states=a}
 }if(messages && states) break; f=f.return}
 if(!messages || !states) return '';
 let index=-1;messages.forEach((m,i)=>{if(m.type==='agent' && (m.invocationId||'').startsWith('bot-report:')) index=i});
 if(index<0) return '';
 const holder=root.querySelector('[data-message-index="'+index+'"]'), bubble=holder && holder.querySelector('[data-testid="agent-message-bubble"]');
 if(!bubble) return '';
 bubble.scrollIntoView({block:'nearest'});
 // Expand actual work controls and retain readable DOM work separately from
 // the mounted projection dataset. Missing/hidden work is not evidence.
 const workText={};
 for(const state of states){
  if(!state.work) continue;
  const section=root.querySelector('[aria-label="Work for run '+state.run_id+'"]');
  const detail=section && section.querySelector('details[aria-label="Agent work"]');
  if(!detail) return '';detail.open=true;
  if(!detail.checkVisibility() || !detail.innerText.trim()) return '';
  workText[state.run_id]=detail.innerText;
 }
 return JSON.stringify({invocation_id:messages[index].invocationId,text:bubble.innerText,visible:bubble.checkVisibility(),run_states:states,work_text:workText});
}`

func agentApplyEvidenceResults(evidence map[string]any, raw []byte) error {
	var results map[string]struct {
		Status string `json:"status"`
		Reason string `json:"reason,omitempty"`
	}
	if err := json.Unmarshal(raw, &results); err != nil {
		return err
	}
	required := append([]string{"stock-1", "stock-2", "stock-3"}, agentOutstanding...)
	if len(results) != len(required) {
		return fmt.Errorf("missing or unknown semantic gates")
	}
	passed, remaining := []string{}, []string{}
	for _, gate := range required {
		result, ok := results[gate]
		if !ok || (result.Status != "passed" && result.Status != "unverified") || (result.Status == "unverified" && result.Reason == "") {
			return fmt.Errorf("invalid semantic gate: %s", gate)
		}
		if result.Status == "passed" {
			passed = append(passed, gate)
		} else {
			remaining = append(remaining, gate)
		}
	}
	evidence["receipts"] = results
	evidence["passed"] = passed
	evidence["unverified"] = remaining
	evidence["status"] = "blocked"
	evidence["blocker"] = "Unverified obligations: " + strings.Join(remaining, ", ")
	if len(remaining) == 0 {
		evidence["status"] = "verified"
		delete(evidence, "blocker")
	}
	return nil
}
