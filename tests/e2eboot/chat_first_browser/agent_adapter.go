package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const agentAdapterMarker = "[bot-model-observation-v1] "

// Closed payload-free mirror. Unknown fields fail rather than copying payloads
// from future log formats into evidence.
type agentAdapterRecord struct {
	Boundary     string    `json:"observation_boundary"`
	WireVerified bool      `json:"wire_verified"`
	At           time.Time `json:"at"`
	PID          int       `json:"pid"`
	WorkerID     string    `json:"worker_id"`
	AdapterID    string    `json:"adapter_id"`
	BotID        string    `json:"bot_id"`
	RunID        string    `json:"run_id"`
	SessionID    string    `json:"session_id"`
	OwnerID      string    `json:"owner_id"`
	Scope        string    `json:"scope"`
	Provider     string    `json:"provider"`
	Model        string    `json:"model"`
	AdapterType  string    `json:"adapter_type"`
	AdapterName  string    `json:"adapter_name"`
	Phase        string    `json:"phase"`
	CallID       string    `json:"call_id,omitempty"`
	Ordinal      uint64    `json:"ordinal,omitempty"`
	Stream       bool      `json:"stream"`
	RequestModel string    `json:"request_model"`
	Responses    int       `json:"responses"`
	Outcome      string    `json:"outcome,omitempty"`
}

func agentReadAdapterRecords(reader io.Reader, since time.Time) ([]agentAdapterRecord, error) {
	records := []agentAdapterRecord{}
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 65536), 4<<20)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.Contains(line, agentAdapterMarker) {
			continue
		}
		// log.Printf may be routed through slog's TextHandler. Decode its
		// quoted message, not the escaped JSON suffix or unrelated attributes.
		if _, quoted, ok := strings.Cut(line, ` msg="`); ok {
			var message string
			if err := json.NewDecoder(strings.NewReader(`"` + quoted)).Decode(&message); err != nil {
				return nil, fmt.Errorf("invalid adapter log envelope")
			}
			line = message
		}
		_, raw, found := strings.Cut(line, agentAdapterMarker)
		if !found {
			continue
		}
		var record agentAdapterRecord
		decoder := json.NewDecoder(strings.NewReader(raw))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&record); err != nil {
			return nil, fmt.Errorf("invalid adapter observation schema")
		}
		if decoder.Decode(new(any)) != io.EOF {
			return nil, fmt.Errorf("trailing adapter observation data")
		}
		if record.At.Before(since) {
			continue
		}
		records = append(records, record)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("adapter log read incomplete")
	}
	if len(records) == 0 {
		return nil, fmt.Errorf("no native adapter observations in invocation")
	}
	return records, nil
}

func agentCaptureAdapters(root string, state map[string]any, since time.Time) (map[string]string, error) {
	if state["root"] != root || state["workflow"] != agentLiveContract || state["agent_attempted"] != true {
		return nil, fmt.Errorf("adapter candidate ownership mismatch")
	}
	// The launcher owns this private directory and verifies the candidate process.
	// No API, Docker exec, normal-daemon access or alternate log path is used.
	for _, path := range []string{root, filepath.Join(root, "logs"), filepath.Join(root, "logs", "daemon.log")} {
		info, err := os.Lstat(path)
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("missing/symlink owned adapter log")
		}
	}
	readers := []io.Reader{}
	paths := []string{}
	infos := []os.FileInfo{}
	for _, name := range []string{"daemon.log.3", "daemon.log.2", "daemon.log.1", "daemon.log"} {
		path := filepath.Join(root, "logs", name)
		info, err := os.Lstat(path)
		paths = append(paths, path)
		infos = append(infos, info)
		if os.IsNotExist(err) && name != "daemon.log" {
			continue
		}
		if err != nil || !info.Mode().IsRegular() {
			return nil, fmt.Errorf("missing/nonregular owned adapter log")
		}
		file, err := os.Open(path)
		if err != nil {
			return nil, fmt.Errorf("cannot open owned adapter log")
		}
		defer file.Close()
		opened, err := file.Stat()
		if err != nil || !os.SameFile(info, opened) {
			return nil, fmt.Errorf("owned adapter log changed during capture")
		}
		readers = append(readers, io.NewSectionReader(file, 0, info.Size()), strings.NewReader("\n"))
	}
	records, err := agentReadAdapterRecords(io.MultiReader(readers...), since)
	if err != nil {
		return nil, err
	}
	// A rotation during the snapshot could omit or duplicate observations. Fail
	// closed rather than silently publishing evidence assembled from two sets.
	for i, path := range paths {
		now, err := os.Lstat(path)
		if infos[i] == nil && os.IsNotExist(err) {
			continue
		}
		if err != nil || infos[i] == nil || !now.Mode().IsRegular() || !os.SameFile(infos[i], now) || now.Size() < infos[i].Size() {
			return nil, fmt.Errorf("owned adapter logs rotated during capture")
		}
	}
	raw, err := json.Marshal(map[string]any{"contract": "bot-native-adapter-v1", "nonce": state["agent_nonce"], "candidate": state["binary_sha256"], "browser_candidate": state["browser_sha256"], "invocation_started_at": since, "captured_at": time.Now().UTC(), "log_path": "logs/daemon.log", "records": records})
	if err != nil {
		return nil, err
	}
	name := "agent-native-adapters.json"
	if err := os.WriteFile(filepath.Join(root, name), raw, 0600); err != nil {
		return nil, err
	}
	return map[string]string{"Source": name, "SourceHash": fmt.Sprintf("%x", sha256.Sum256(raw))}, nil
}
