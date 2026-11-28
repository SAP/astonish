package main

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestAgentAdapterQuotedLogEnvelope(t *testing.T) {
	record := agentAdapterRecord{At: time.Now().UTC(), Phase: "resolved", AdapterName: "model\"quoted", CallID: "00000000-0000-0000-0000-000000000000"}
	raw, _ := json.Marshal(record)
	var logs bytes.Buffer
	slog.New(slog.NewTextHandler(&logs, nil)).Info(agentAdapterMarker + string(raw))
	for _, line := range []string{logs.String(), "2026/01/01 " + agentAdapterMarker + string(raw)} {
		got, err := agentReadAdapterRecords(strings.NewReader(line), time.Time{})
		if err != nil || len(got) != 1 || got[0] != record {
			t.Fatalf("quoted observation did not round trip: %v", err)
		}
	}
	for _, payload := range []string{`{"prompt":"secret"}`, `{`, `{} {}`} {
		line := "time=now level=INFO msg=" + strconv.Quote(agentAdapterMarker+payload)
		if _, err := agentReadAdapterRecords(strings.NewReader(line), time.Time{}); err == nil || strings.Contains(err.Error(), "secret") {
			t.Fatal("unsafe quoted schema handling")
		}
	}
}

func TestAgentAdapterRetainedRotations(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "logs"), 0700); err != nil {
		t.Fatal(err)
	}
	state := map[string]any{"root": root, "workflow": agentLiveContract, "agent_attempted": true}
	for i, name := range []string{"daemon.log.3", "daemon.log.2", "daemon.log.1", "daemon.log"} {
		record := agentAdapterRecord{At: time.Now().UTC(), Phase: []string{"resolved", "generation-start", "generation-end", "resolved"}[i]}
		raw, _ := json.Marshal(record)
		if err := os.WriteFile(filepath.Join(root, "logs", name), []byte(agentAdapterMarker+string(raw)+"\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	ref, err := agentCaptureAdapters(root, state, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(root, ref["Source"]))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Records []agentAdapterRecord `json:"records"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Records) != 4 || doc.Records[1].Phase != "generation-start" || doc.Records[2].Phase != "generation-end" {
		t.Fatal("rotated invocation lost/reordered")
	}
	// Empty/missing older logs are normal; an unsafe retained log is not.
	path := filepath.Join(root, "logs", "daemon.log.2")
	if err := os.WriteFile(path, []byte("ordinary SECRET log\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := agentCaptureAdapters(root, state, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := agentCaptureAdapters(root, state, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "logs", "daemon.log"), path); err != nil {
		t.Fatal(err)
	}
	if _, err := agentCaptureAdapters(root, state, time.Time{}); err == nil {
		t.Fatal("accepted rotated symlink")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := agentCaptureAdapters(root, state, time.Time{}); err == nil {
		t.Fatal("accepted rotated directory")
	}
}

func TestAgentAdapterOwnedLogCapture(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "logs"), 0700); err != nil {
		t.Fatal(err)
	}
	start := time.Now().UTC().Add(-time.Second)
	record := agentAdapterRecord{At: time.Now().UTC(), Boundary: "native-worker-adapter", Phase: "resolved", AdapterType: "*sap.Provider"}
	raw, _ := json.Marshal(record)
	log := "ordinary SECRET log must not be retained\n" + agentAdapterMarker + string(raw) + "\n"
	path := filepath.Join(root, "logs", "daemon.log")
	if err := os.WriteFile(path, []byte(log), 0600); err != nil {
		t.Fatal(err)
	}
	state := map[string]any{"root": root, "workflow": agentLiveContract, "agent_attempted": true, "agent_nonce": "nonce", "binary_sha256": "candidate", "browser_sha256": "browser"}
	receipt, err := agentCaptureAdapters(root, state, start)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := os.ReadFile(filepath.Join(root, receipt["Source"]))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(saved), "SECRET") || !strings.Contains(string(saved), "*sap.Provider") {
		t.Fatal("wrong capture")
	}
	info, _ := os.Stat(filepath.Join(root, receipt["Source"]))
	if info.Mode().Perm() != 0600 {
		t.Fatal("not private")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, receipt["Source"]), path); err != nil {
		t.Fatal(err)
	}
	if _, err := agentCaptureAdapters(root, state, start); err == nil {
		t.Fatal("accepted symlink")
	}
}
func TestAgentAdapterLogReaderRejectsPayloadAndIncompleteRecords(t *testing.T) {
	for _, raw := range []string{`{"prompt":"secret"}`, `{`, `{} {}`} {
		if _, err := agentReadAdapterRecords(strings.NewReader(agentAdapterMarker+raw), time.Time{}); err == nil {
			t.Fatal("invalid record accepted")
		}
	}
	old, _ := json.Marshal(agentAdapterRecord{At: time.Now().Add(-time.Hour)})
	if _, err := agentReadAdapterRecords(strings.NewReader(agentAdapterMarker+string(old)), time.Now()); err == nil {
		t.Fatal("stale record accepted")
	}
}
