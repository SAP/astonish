package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

func taskTestCandidate() map[string]any {
	return map[string]any{"workflow": taskLiveContract, "task_config_verified": true, "task_nonce": "fresh-nonce", "binary_sha256": strings.Repeat("a", 64), "browser_sha256": strings.Repeat("b", 64), "task_intended": map[string]string{"provider": taskProvider, "provider_type": "sap_ai_core", "model": taskModel}, "task_search": taskSearchRoute{Engine: "perplexity", Provider: "selected-search", Model: "sonar-pro", Tool: "perplexity_web_search", Fingerprint: "selected-config-digest"}}
}

func TestTaskFreshCandidateRequiresExplicitConfigAndIdentity(t *testing.T) {
	state := taskTestCandidate()
	if _, err := taskFreshCandidate(state, ""); err == nil {
		t.Fatal("paid work admitted without explicit private config")
	}
	if _, err := taskFreshCandidate(state, "/explicit/private.json"); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"task_config_verified", "task_nonce", "binary_sha256", "browser_sha256", "task_intended", "task_search"} {
		bad := taskTestCandidate()
		delete(bad, field)
		if _, err := taskFreshCandidate(bad, "/explicit/private.json"); err == nil {
			t.Fatalf("missing %s admitted", field)
		}
	}
	state["task_attempted"] = true
	if _, err := taskFreshCandidate(state, "/explicit/private.json"); err == nil {
		t.Fatal("state reuse admitted")
	}
	state = taskTestCandidate()
	state["workflow"] = agentLiveContract
	if _, err := taskFreshCandidate(state, "/explicit/private.json"); err == nil {
		t.Fatal("historical workflow repurposed")
	}
}

func TestTaskModeDoesNotSelectHistoricalModes(t *testing.T) {
	for _, mode := range []string{"BOT_AGENT_ACCEPTANCE", "BOT_EMPLOYEE_ACCEPTANCE", "BOT_QUALITY_ACCEPTANCE", "CHAT_MODEL_ACCEPTANCE"} {
		if taskModeEnabled(func(key string) string {
			if key == mode {
				return "1"
			}
			return ""
		}) {
			t.Fatalf("historical %s selected Task mode", mode)
		}
	}
	if !taskModeEnabled(func(key string) string {
		if key == "BOT_TASK_ACCEPTANCE" {
			return "1"
		}
		return ""
	}) {
		t.Fatal("Task mode missing")
	}
}

func taskTestStream(messages ...chatModelMessage) string {
	raw, _ := json.Marshal(map[string]any{"messages": messages})
	return string(raw)
}
func taskTestCall(id, name string) chatModelMessage {
	return chatModelMessage{Type: "tool_call", ToolID: id, ToolName: name}
}
func taskTestResult(id, name, result string) chatModelMessage {
	return chatModelMessage{Type: "tool_result", ToolID: id, ToolName: name, ToolResult: json.RawMessage(result)}
}

func TestTaskSearchFirstExactConfiguredOrdering(t *testing.T) {
	route := taskSearchRoute{Engine: "perplexity", Provider: "selected-search", Model: "sonar-pro"}
	search := taskTestCall("s", "perplexity_web_search")
	result := taskTestResult("s", "perplexity_web_search", `{"provider":"selected-search","model":"sonar-pro","query":"Stock Market News","citations":["https://example.test/source"]}`)
	detail := taskTestCall("d", "web_fetch")
	if err := taskSearchFirst(taskTestStream(search, result, detail), route); err != nil {
		t.Fatal(err)
	}
	for name, messages := range map[string][]chatModelMessage{
		"detail-first":        {detail, search, result},
		"search-not-finished": {search, detail, result},
		"wrong-provider":      {search, taskTestResult("s", "perplexity_web_search", `{"provider":"other","query":"Stock Market News","citations":["https://example.test"]}`), detail},
		"failed-search":       {search, taskTestResult("s", "perplexity_web_search", `{"error":"429"}`), detail},
		"snippet-only":        {search, result},
		"orphan":              {result, detail},
		"duplicate":           {search, result, result, detail},
	} {
		t.Run(name, func(t *testing.T) {
			if taskSearchFirst(taskTestStream(messages...), route) == nil {
				t.Fatal("invalid search order accepted")
			}
		})
	}
	tavily := taskSearchRoute{Engine: "tavily", Tool: "mcp:selected/tavily_search"}
	if err := taskSearchFirst(taskTestStream(search, taskTestResult("s", "perplexity_web_search", `{"provider":"tavily","query":"Stock Market News","search_results":[{"url":"https://example.test"}]}`), detail), tavily); err != nil {
		t.Fatal(err)
	}
}

func taskTestWorkspace(t *testing.T) taskWorkspaceObservation {
	t.Helper()
	botID, taskID := uuid.New(), uuid.New()
	bundle := map[string][]byte{"TASK.md": []byte("# Stock Market News\nUse decisive sources.\n"), "sources.md": []byte("Retained source details\n")}
	assets := []store.BotTaskAsset{}
	for path, data := range bundle {
		kind := store.BotTaskAssetText
		if path == "TASK.md" {
			kind = store.BotTaskAssetDefinition
		}
		assets = append(assets, store.BotTaskAsset{Path: path, Kind: kind, Digest: fmt.Sprintf("%x", sha256.Sum256(data)), SizeBytes: int64(len(data))})
	}
	revision := store.BotTaskRevision{SchemaVersion: store.BotTaskSchemaVersion, ID: uuid.New(), BotID: botID, Ref: store.BotTaskRef{TaskID: taskID, Revision: 1}, Manifest: store.BotTaskManifest{SchemaVersion: store.BotTaskSchemaVersion, BotID: botID, TaskID: taskID, Title: "Stock Market News", Assignment: "Stock Market News", Assets: assets}, Bundle: bundle, CreatedAt: time.Now()}
	revision.Ref.Digest = revision.ContentDigest()
	if err := revision.Validate(store.BotTaskReady); err != nil {
		t.Fatal(err)
	}
	task := store.BotTask{ID: taskID, BotID: botID, Title: "Stock Market News", Status: store.BotTaskReady, Revision: 1, Head: revision.Ref.Clone(), HomePath: "tasks/stock-market"}
	files := []taskPhysicalFile{}
	for path, data := range bundle {
		files = append(files, taskPhysicalFile{TaskID: taskID, Path: path, Bytes: append([]byte(nil), data...)})
	}
	return taskWorkspaceObservation{Source: "owned-writer-container-and-home-volume", BotID: botID, Tasks: []store.BotTask{task}, Revisions: []store.BotTaskRevision{revision}, Files: files}
}

func TestTaskWorkspaceRequiresPhysicalBytesAndCommittedHead(t *testing.T) {
	good := taskTestWorkspace(t)
	encode := func(v any) []byte {
		raw, e := json.Marshal(v)
		if e != nil {
			t.Fatal(e)
		}
		return raw
	}
	if err := taskValidateWorkspace(encode(good), good.BotID); err != nil {
		t.Fatal(err)
	}
	for _, caseName := range []string{"no-physical", "bytes-drift", "wrong-owner", "head-drift", "duplicate-file", "path-escape", "archive-only"} {
		t.Run(caseName, func(t *testing.T) {
			bad := taskTestWorkspace(t)
			switch caseName {
			case "no-physical":
				bad.Files = nil
			case "bytes-drift":
				bad.Files[0].Bytes = []byte("model prose is not evidence")
			case "wrong-owner":
				bad.Tasks[0].BotID = uuid.New()
			case "head-drift":
				bad.Tasks[0].Head.Revision++
			case "duplicate-file":
				bad.Files = append(bad.Files, bad.Files[0])
			case "path-escape":
				bad.Files[0].Path = "../TASK.md"
			case "archive-only":
				bad.Source = "immutable-bundle"
			}
			if taskValidateWorkspace(encode(bad), bad.BotID) == nil {
				t.Fatal("invalid physical evidence accepted")
			}
		})
	}
}

func TestTaskResultsFailClosedExactCases(t *testing.T) {
	results := map[string]map[string]string{}
	for _, name := range taskRequired {
		results[name] = map[string]string{"status": "unverified", "reason": "not exercised"}
	}
	encode := func() []byte { raw, _ := json.Marshal(results); return raw }
	evidence := map[string]any{}
	if err := taskApplyResults(evidence, encode()); err != nil {
		t.Fatal(err)
	}
	if evidence["status"] != "blocked" || !reflect.DeepEqual(evidence["unverified"], taskRequired) {
		t.Fatal("outstanding cases lost")
	}
	delete(results, "stock-2")
	if taskApplyResults(evidence, encode()) == nil {
		t.Fatal("missing sample accepted")
	}
	results["stock-2"] = map[string]string{"status": "passed"}
	results["extra"] = map[string]string{"status": "passed"}
	if taskApplyResults(evidence, encode()) == nil {
		t.Fatal("unknown case accepted")
	}
	delete(results, "extra")
	for _, name := range taskRequired {
		results[name] = map[string]string{"status": "passed"}
	}
	if err := taskApplyResults(evidence, encode()); err != nil || evidence["status"] != "verified" {
		t.Fatal(err, evidence)
	}
	results["source-grounding"] = map[string]string{"status": "failed", "reason": "unsupported claim"}
	if err := taskApplyResults(evidence, encode()); err != nil || evidence["status"] != "blocked" {
		t.Fatal("semantic failure treated as pass", err)
	}
}

func TestTaskExactEvidenceRejectsAliases(t *testing.T) {
	for _, raw := range []string{`{"status":"passed","Status":"failed"}`, `{"status":"passed","status":"failed"}`, `{"unknown":true}`, `{"status":"passed"} {}`} {
		var target struct {
			Status string `json:"status"`
		}
		if taskDecodeExact([]byte(raw), &target) == nil {
			t.Fatalf("ambiguous evidence admitted: %s", raw)
		}
	}
}

func TestTaskAdapterCaptureOwnedPrivateAndFresh(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "logs"), 0700); err != nil {
		t.Fatal(err)
	}
	started := time.Now().UTC()
	record := agentAdapterRecord{At: started.Add(time.Second), Provider: taskProvider, Model: taskModel, Phase: "resolved"}
	raw, _ := json.Marshal(record)
	path := filepath.Join(root, "logs", "daemon.log")
	if err := os.WriteFile(path, append([]byte(agentAdapterMarker), raw...), 0600); err != nil {
		t.Fatal(err)
	}
	state := taskTestCandidate()
	state["root"] = root
	state["task_attempted"] = true
	receipt, err := taskCaptureAdapters(root, state, started)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(root, receipt["Source"]))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("adapter evidence is not private", err)
	}
	if _, err := taskCaptureAdapters(root, state, started.Add(time.Minute)); err == nil {
		t.Fatal("prior adapter evidence counted as fresh")
	}
	state["root"] = "not-owned"
	if _, err := taskCaptureAdapters(root, state, started); err == nil {
		t.Fatal("unowned adapter log admitted")
	}
}

func TestTaskRunParityExactOwnedProvider(t *testing.T) {
	home := agentHomeModel{Provider: taskProvider, Model: taskModel, Credentials: true}
	run := store.BotRun{Provider: taskProvider, Model: taskModel}
	if taskRunParity(home, run) != nil {
		t.Fatal("exact Task provider rejected")
	}
	run.Provider = agentProvider
	if taskRunParity(home, run) == nil {
		t.Fatal("historical provider accepted for Task")
	}
}
