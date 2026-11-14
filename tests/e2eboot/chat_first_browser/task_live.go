package main

// Fresh Task candidate evidence collection. Product mutations use native Studio;
// source transcripts and workspace observations are retained, never counted as
// semantic acceptance merely because a worker reported success.
import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/SAP/astonish/pkg/store"
	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
	"github.com/google/uuid"
)

const taskLiveContract = "bot-task-live-v1"
const taskProvider = "sap_ai_core_sci"
const taskModel = "anthropic--claude-4.6-sonnet"

// These are independent obligations. A completed title call does not discharge
// recurrence, source adequacy, backup/recovery, or surface parity.
var taskRequired = []string{
	"stock-1", "stock-2", "stock-3", "compatibility", "workspace", "configured-search-first",
	"adapter-provenance", "source-grounding", "execution", "continuity", "owner-correction",
	"python-helper-sqlite-state", "task-three-scheduler-occurrences", "schedule-delete-manual-reuse",
	"restart", "postgres-backup-restore", "isolation-permissions-cancellation-no-replay", "studio-terminal-parity",
}

type taskSearchRoute struct {
	Engine      string `json:"engine"`
	Provider    string `json:"provider"`
	Model       string `json:"model"`
	Tool        string `json:"tool"`
	Fingerprint string `json:"fingerprint"`
}

func taskFreshCandidate(state map[string]any, configPath string) (taskSearchRoute, error) {
	var route taskSearchRoute
	if strings.TrimSpace(configPath) == "" {
		return route, fmt.Errorf("LOCAL_BOT_TASK_CONFIG explicit private configuration required; no paid work started")
	}
	if state["workflow"] != taskLiveContract || state["task_attempted"] != nil || state["task_config_verified"] != true {
		return route, fmt.Errorf("Task workflow requires fresh verified owned configuration")
	}
	for _, field := range []string{"task_nonce", "binary_sha256", "browser_sha256"} {
		value, ok := state[field].(string)
		if !ok || strings.TrimSpace(value) == "" {
			return route, fmt.Errorf("missing Task candidate binding: %s", field)
		}
	}
	raw, err := json.Marshal(state["task_intended"])
	if err != nil {
		return route, err
	}
	var intended map[string]string
	if json.Unmarshal(raw, &intended) != nil || !reflect.DeepEqual(intended, map[string]string{"provider": taskProvider, "provider_type": "sap_ai_core", "model": taskModel}) {
		return route, fmt.Errorf("Task intended provider identity mismatch")
	}
	raw, err = json.Marshal(state["task_search"])
	if err != nil {
		return route, err
	}
	if json.Unmarshal(raw, &route) != nil || (route.Engine != "perplexity" && route.Engine != "tavily") || route.Tool == "" || route.Fingerprint == "" || (route.Engine == "perplexity" && (route.Provider == "" || route.Model == "")) {
		return route, fmt.Errorf("Task effective configured search route unavailable")
	}
	return route, nil
}

func taskRunParity(home agentHomeModel, run store.BotRun) error {
	if home.Provider != taskProvider || home.Model != taskModel || !home.Credentials || run.Provider != taskProvider || run.Model != taskModel {
		return fmt.Errorf("Task Home/worker identity mismatch or credentials unavailable")
	}
	return nil
}

// Inspect actual ordered tool calls/results. Search summaries alone do not prove
// grounding; a result must precede any detail fetch and match the selected route.
func taskSearchFirst(stream string, route taskSearchRoute) error {
	messages, err := chatModelMessages(stream)
	if err != nil {
		return err
	}
	calls := map[string]string{}
	results := map[string]bool{}
	searched := false
	details := 0
	for _, m := range messages {
		if m.Type == "tool_call" {
			if m.ToolID == "" || calls[m.ToolID] != "" {
				return fmt.Errorf("missing/duplicate tool call identity")
			}
			calls[m.ToolID] = m.ToolName
			switch m.ToolName {
			case "web_fetch", "http_request", "browser_navigate":
				if !searched {
					return fmt.Errorf("detail fetch %s preceded successful configured search", m.ToolName)
				}
				details++
			}
		}
		if m.Type != "tool_result" {
			continue
		}
		if m.ToolID == "" || calls[m.ToolID] != m.ToolName || results[m.ToolID] {
			return fmt.Errorf("orphan/mismatched/duplicate tool result")
		}
		results[m.ToolID] = true
		// Production's scoped wrapper is fixed; an MCP route is dispatched inside it.
		if m.ToolName != "perplexity_web_search" {
			continue
		}
		var response struct {
			Provider  string            `json:"provider"`
			Model     string            `json:"model"`
			Query     string            `json:"query"`
			Error     any               `json:"error"`
			Citations []string          `json:"citations"`
			Hits      []json.RawMessage `json:"search_results"`
		}
		if json.Unmarshal(m.ToolResult, &response) != nil || response.Error != nil || response.Query == "" || len(response.Citations)+len(response.Hits) == 0 {
			return fmt.Errorf("configured search failed or returned no discovery evidence")
		}
		expected := route.Provider
		if route.Engine == "tavily" {
			expected = "tavily"
		}
		if response.Provider != expected || (route.Engine == "perplexity" && response.Model != route.Model) {
			return fmt.Errorf("configured search result provider/model differs from selected route")
		}
		searched = true
	}
	if !searched || details == 0 {
		return fmt.Errorf("missing successful configured search then decisive detail retrieval")
	}
	return nil
}

type taskPhysicalFile struct {
	TaskID uuid.UUID `json:"task_id"`
	Path   string    `json:"path"`
	Bytes  []byte    `json:"bytes"`
}
type taskWorkspaceObservation struct {
	Source    string                  `json:"observation_source"`
	BotID     uuid.UUID               `json:"bot_id"`
	Tasks     []store.BotTask         `json:"tasks"`
	Revisions []store.BotTaskRevision `json:"revisions"`
	Files     []taskPhysicalFile      `json:"physical_files"`
}

// The independent launcher observer must read the owned writer's registry and
// actual durable volume bytes, not return the immutable bundle as physical files.
func taskValidateWorkspace(raw []byte, botID uuid.UUID) error {
	var observation taskWorkspaceObservation
	if taskDecodeExact(raw, &observation) != nil || observation.Source != "owned-writer-container-and-home-volume" || observation.BotID != botID || botID == uuid.Nil || len(observation.Tasks) == 0 {
		return fmt.Errorf("missing owned physical Task workspace observation")
	}
	revisions := map[uuid.UUID]store.BotTaskRevision{}
	for _, r := range observation.Revisions {
		if r.BotID != botID || r.Validate(store.BotTaskReady) != nil || revisions[r.Ref.TaskID].ID != uuid.Nil {
			return fmt.Errorf("invalid/duplicate committed Task revision")
		}
		revisions[r.Ref.TaskID] = r
	}
	files := map[string][]byte{}
	for _, f := range observation.Files {
		if f.TaskID == uuid.Nil || store.ValidateBotTaskAssetPath(f.Path) != nil || len(f.Bytes) > int(store.BotTaskMaxAssetBytes) {
			return fmt.Errorf("invalid physical Task asset")
		}
		key := f.TaskID.String() + "/" + f.Path
		if _, exists := files[key]; exists {
			return fmt.Errorf("duplicate physical Task asset")
		}
		files[key] = f.Bytes
	}
	seen := map[uuid.UUID]bool{}
	for _, task := range observation.Tasks {
		r, exists := revisions[task.ID]
		if seen[task.ID] || task.BotID != botID || task.Status != store.BotTaskReady || task.Head == nil || !exists || *task.Head != r.Ref || r.Manifest.Assignment != "Stock Market News" || len(r.Bundle) < 2 {
			return fmt.Errorf("Task head/assignment/supporting assets missing or mismatched")
		}
		seen[task.ID] = true
		if strings.TrimSpace(string(r.Bundle["TASK.md"])) == "" {
			return fmt.Errorf("authoritative TASK.md missing")
		}
		for path, archived := range r.Bundle {
			physical, exists := files[task.ID.String()+"/"+path]
			if !exists || !bytes.Equal(archived, physical) {
				return fmt.Errorf("physical Task bytes differ from committed %s", path)
			}
		}
	}
	return nil
}

func taskDecodeExact(raw []byte, dst any) error {
	if _, err := employeeDiskObject(string(raw)); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return err
	}
	if decoder.Decode(new(any)) != io.EOF {
		return fmt.Errorf("trailing Task evidence data")
	}
	return nil
}

func taskApplyResults(evidence map[string]any, raw []byte) error {
	var results map[string]struct {
		Status string `json:"status"`
		Reason string `json:"reason"`
	}
	if taskDecodeExact(raw, &results) != nil || len(results) != len(taskRequired) {
		return fmt.Errorf("Task validator missing or unknown exact cases")
	}
	passed, outstanding := []string{}, []string{}
	for _, name := range taskRequired {
		result, ok := results[name]
		if !ok || (result.Status != "passed" && result.Status != "unverified" && result.Status != "failed") || (result.Status != "passed" && result.Reason == "") {
			return fmt.Errorf("invalid Task evidence case %s", name)
		}
		if result.Status == "passed" {
			passed = append(passed, name)
		} else {
			outstanding = append(outstanding, name)
		}
	}
	evidence["receipts"], evidence["passed"], evidence["unverified"] = results, passed, outstanding
	evidence["status"] = "blocked"
	evidence["blocker"] = "Outstanding exact Task cases: " + strings.Join(outstanding, ", ")
	if len(outstanding) == 0 {
		evidence["status"] = "verified"
		delete(evidence, "blocker")
	}
	return nil
}

// Observe a native POST receipt without replacing fetch, intercepting a route,
// mutating a handler, or issuing another creation request.
func taskCreationReceipt(page *rod.Page) (<-chan []byte, func()) {
	ctx, cancel := context.WithCancel(page.GetContext())
	page = page.Context(ctx)
	must((&proto.NetworkEnable{}).Call(page))
	receipts := make(chan []byte, 1)
	pending := map[proto.NetworkRequestID]bool{}
	stop := page.EachEvent(func(e *proto.NetworkResponseReceived) {
		if e.Response.Status == 201 && strings.HasSuffix(e.Response.URL, "/api/studio/bots") {
			pending[e.RequestID] = true
		}
	}, func(e *proto.NetworkLoadingFinished) {
		if !pending[e.RequestID] {
			return
		}
		delete(pending, e.RequestID)
		response, err := (&proto.NetworkGetResponseBody{RequestID: e.RequestID}).Call(page)
		if err == nil && !response.Base64Encoded {
			select {
			case receipts <- []byte(response.Body):
			default:
			}
		}
	})
	go stop()
	return receipts, cancel
}

func newTaskWorkflow(basePage *rod.Page, api func(string, string, any) string, state map[string]any, statePath, root, base string, evidence map[string]any, save func()) {
	evidence["contract"] = taskLiveContract
	evidence["nonce"] = state["task_nonce"]
	evidence["candidate"] = state["binary_sha256"]
	evidence["browser_candidate"] = state["browser_sha256"]
	evidence["passed"] = []string{}
	evidence["unverified"] = append([]string(nil), taskRequired...)
	evidence["status"] = "blocked"
	save()
	route, err := taskFreshCandidate(state, os.Getenv("LOCAL_BOT_TASK_CONFIG"))
	must(err)
	state["task_attempted"] = true
	persist := func() {
		raw, e := json.MarshalIndent(state, "", "  ")
		must(e)
		must(os.WriteFile(statePath, raw, 0600))
	}
	persist()
	started := time.Now().UTC()
	evidence["invocation_started_at"] = started
	evidence["configured_search"] = route
	samples := []map[string]any{}
	page := func() *rod.Page { return independentPageTimeout(basePage, 40*time.Second) }
	for n := 1; n <= 3; n++ {
		key := fmt.Sprintf("stock-%d", n)
		sample := map[string]any{"sample": key, "prompt": "Stock Market News", "status": "running", "sources": map[string]string{}, "grounding_verified": false}
		samples = append(samples, sample)
		evidence["samples"] = samples
		evidence["stage"] = key
		save()
		func() {
			defer func() {
				if failure := recover(); failure != nil {
					sample["status"] = "failed"
					sample["blocker"] = fmt.Sprint(failure)
					save()
				}
			}()
			retain := func(name string, raw []byte) {
				file := "task-" + key + "-" + name
				must(os.WriteFile(filepath.Join(root, file), raw, 0600))
				sample["sources"].(map[string]string)[file] = fmt.Sprintf("%x", sha256.Sum256(raw))
				save()
			}
			capture := func(name, path string) string { text := api("GET", path, nil); retain(name, []byte(text)); return text }
			page().MustNavigate(base + "/#/chat")
			page().MustElementR("button", "^New Bot$").MustClick()
			page().MustElement(`[aria-label="Bot name"]`).MustInput("Stock Market News")
			observerPage := basePage.Context(basePage.GetContext())
			receipts, stop := taskCreationReceipt(observerPage)
			defer stop()
			sample["creation_attempted"] = true
			save()
			page().MustElementR("button", "^Start conversation$").MustClick()
			var admission struct {
				store.Bot
				InitialTurn *store.BotMessageReceipt `json:"initial_turn"`
			}
			select {
			case raw := <-receipts:
				retain("admission.json", raw)
				must(json.Unmarshal(raw, &admission))
			case <-time.After(40 * time.Second):
				panic("native title creation receipt missing; never resubmit uncertain creation")
			}
			if admission.ID == uuid.Nil || admission.InitialTurn == nil || !admission.InitialTurn.Run.IsInteractiveChat() || admission.InitialTurn.Run.BotID != admission.ID {
				panic("unidentified title admission")
			}
			botID, runID := admission.ID, admission.InitialTurn.Run.ID
			sample["bot_id"], sample["run_id"] = botID.String(), runID.String()
			state["bot_id"] = botID.String()
			persist()
			save()
			page().MustElement(`[aria-label="Bot workspace"]`).MustVisible()
			homeURL := base + "/#/chat/bots/personal/" + botID.String()
			if page().MustInfo().URL != homeURL {
				panic("native admission and visible Home differ")
			}
			botPath := "/api/studio/bots/" + botID.String()
			var b store.Bot
			must(json.Unmarshal([]byte(capture("bot.json", botPath+"?scope=personal")), &b))
			if b.ID != botID || b.HomeSessionID != admission.HomeSessionID {
				panic("Bot/Home identity changed")
			}
			var home agentHomeModel
			modelPath := "/api/studio/sessions/" + b.HomeSessionID.String() + "/model-status?scope=personal"
			must(json.Unmarshal([]byte(capture("model-before.json", modelPath)), &home))
			var run store.BotRun
			boundedWait("fresh Task title result", 12*time.Minute, func() bool {
				var rows struct {
					Runs []store.BotRun `json:"runs"`
				}
				must(json.Unmarshal([]byte(capture("runs.json", botPath+"/runs?scope=personal")), &rows))
				if len(rows.Runs) != 1 || rows.Runs[0].ID != runID {
					panic("missing, duplicate or changed admitted title run")
				}
				run = rows.Runs[0]
				return run.Status.Terminal()
			})
			must(json.Unmarshal([]byte(capture("run.json", botPath+"/runs/"+runID.String()+"?scope=personal")), &run))
			if run.ID != runID || run.BotID != botID {
				panic("terminal run receipt identity changed")
			}
			historyPath := "/api/studio/sessions/" + b.HomeSessionID.String()
			before := capture("history-before.json", historyPath)
			stream := capture("worker-events.txt", botPath+"/sessions/"+run.SessionID.String()+"/stream?scope=personal")
			sample["usage"] = run.Usage
			sample["resolver_observation"] = map[string]string{"provider": run.Provider, "model": run.Model}
			payloads, payloadErr := agentSourcePayloads(stream)
			sample["source_payloads"] = payloads
			if payloadErr != nil {
				sample["source_capture_error"] = payloadErr.Error()
			}
			// Independent physical observer has no model, credentials, API, or host fallback.
			observationState := make(map[string]any, len(state))
			for k, v := range state {
				observationState[k] = v
			}
			observationState["bot_id"] = botID.String()
			cmdCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			cmd := exec.CommandContext(cmdCtx, "python3", "-B", "-c", "import json,sys; sys.path.insert(0,'tests/e2eboot'); import local_bot_smoke_launcher as l; print(json.dumps(l.task_readonly(json.load(sys.stdin))))")
			cmd.Stdin = bytes.NewReader(mustTaskJSON(observationState))
			workspace, workspaceErr := cmd.Output()
			if len(workspace) > 0 {
				retain("workspace.json", workspace)
			}
			if workspaceErr != nil {
				sample["workspace_blocker"] = "owned physical Task observer unavailable"
			} else if e := taskValidateWorkspace(workspace, botID); e != nil {
				sample["workspace_blocker"] = e.Error()
			} else {
				sample["workspace_observed"] = true
			}
			save()
			must(taskRunParity(home, run))
			must(agentRunResult(run, botID))
			must(taskSearchFirst(stream, route))
			sample["search_first_observed"] = true
			page().MustReload()
			after := capture("history-after.json", historyPath)
			messages, e := chatModelMessages(before)
			must(e)
			restored, e := chatModelMessages(after)
			must(e)
			if !reflect.DeepEqual(messages, restored) {
				panic("restored ordered Home messages changed")
			}
			sample["status"] = "diagnostic-observed"
			sample["grounding_blocker"] = "Retained sources and result require accountable assignment adequacy/claim assessment; worker status is not factual proof"
			save()
		}()
	}
	if receipt, err := taskCaptureAdapters(root, state, started); err != nil {
		evidence["adapter_blocker"] = err.Error()
	} else {
		evidence["adapter_manifest"] = receipt
	}
	// The validator owns exact independent lifecycle/semantic cases. Missing code,
	// failed command, absent evidence or unknown case cannot manufacture PASS.
	evidence["stage"] = "task-proof-obligations"
	input := mustTaskJSON(map[string]any{"state": state, "evidence": evidence})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, "python3", "-B", "tests/e2eboot/task_live_contract.py")
	command.Stdin = bytes.NewReader(input)
	raw, validationErr := command.Output()
	if validationErr == nil {
		validationErr = taskApplyResults(evidence, raw)
	}
	if validationErr != nil {
		evidence["status"] = "blocked"
		evidence["passed"] = []string{}
		evidence["unverified"] = append([]string(nil), taskRequired...)
		delete(evidence, "receipts")
		evidence["blocker"] = "Task evidence validator unavailable/invalid; outstanding exact cases: " + strings.Join(taskRequired, ", ")
	}
	save()
	if evidence["status"] != "verified" {
		panic(evidence["blocker"])
	}
}

func taskCaptureAdapters(root string, state map[string]any, since time.Time) (map[string]string, error) {
	if state["root"] != root || state["workflow"] != taskLiveContract || state["task_attempted"] != true {
		return nil, fmt.Errorf("Task adapter ownership mismatch")
	}
	for _, path := range []string{root, filepath.Join(root, "logs")} {
		info, err := os.Lstat(path)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("Task adapter owned log directory unavailable")
		}
	}
	path := filepath.Join(root, "logs", "daemon.log")
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("Task adapter owned log unavailable")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("Task adapter log cannot be opened")
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, fmt.Errorf("Task adapter log changed")
	}
	records, err := agentReadAdapterRecords(io.NewSectionReader(file, 0, info.Size()), since)
	if err != nil {
		return nil, err
	}
	after, err := os.Lstat(path)
	if err != nil || !os.SameFile(info, after) || after.Size() < info.Size() {
		return nil, fmt.Errorf("Task adapter log rotated during capture")
	}
	// Absence due to earlier rotation is an honest blocker, never resolver parity.
	raw, err := json.Marshal(map[string]any{"contract": "bot-native-adapter-v1", "nonce": state["task_nonce"], "candidate": state["binary_sha256"], "browser_candidate": state["browser_sha256"], "invocation_started_at": since, "captured_at": time.Now().UTC(), "log_path": "logs/daemon.log", "records": records})
	if err != nil {
		return nil, err
	}
	name := "task-native-adapters.json"
	if err := os.WriteFile(filepath.Join(root, name), raw, 0600); err != nil {
		return nil, err
	}
	return map[string]string{"Source": name, "SourceHash": fmt.Sprintf("%x", sha256.Sum256(raw))}, nil
}

func taskModeEnabled(getenv func(string) string) bool { return getenv("BOT_TASK_ACCEPTANCE") == "1" }

func mustTaskJSON(value any) []byte { raw, err := json.Marshal(value); must(err); return raw }
