package main

import (
	"errors"
	"reflect"
	"testing"

	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

func TestAgentLearningTerminalSendOrdering(t *testing.T) {
	for _, mode := range []string{"success", "observer-before-send", "observer-after-send", "duplicate-callback"} {
		t.Run(mode, func(t *testing.T) {
			var sequence []string
			sends := 0
			replayed := false
			evidence := map[string]any{}
			admitted := store.BotRun{ID: uuid.New(), BotID: uuid.New(), Status: store.RunRunning}
			settled := admitted
			settled.Status = store.RunSucceeded
			receipt := map[string]string{"Source": "agent-terminal.json", "SourceHash": "retained"}
			send := agentLearningTerminalSend(func(key, message string) store.BotRun {
				if key != "skills-after-removal" || message != "existing owner request" || !replayed {
					t.Fatal("send before replay barrier or altered request")
				}
				sends++
				sequence = append(sequence, "send")
				return admitted
			}, func(run store.BotRun) store.BotRun {
				if run.ID != admitted.ID {
					t.Fatal("wait changed run identity")
				}
				sequence = append(sequence, "settle")
				return settled
			}, func(start func() error) (map[string]string, error) {
				sequence = append(sequence, "observe")
				if mode == "observer-before-send" {
					return nil, errors.New("unavailable")
				}
				replayed = true
				sequence = append(sequence, "replay_complete")
				if err := start(); err != nil {
					t.Fatal(err)
				}
				if mode == "duplicate-callback" && start() == nil {
					t.Fatal("duplicate callback accepted")
				}
				sequence = append(sequence, "capture")
				if mode == "observer-after-send" {
					return receipt, errors.New("capture failed")
				}
				return receipt, nil
			}, evidence, func() { sequence = append(sequence, "persist") }, func() { sequence = append(sequence, "save") })
			var result store.BotRun
			var failure any
			func() {
				defer func() { failure = recover() }()
				result = send("skills-after-removal", "existing owner request")
			}()
			if mode == "observer-before-send" {
				if failure == nil || sends != 0 || evidence["terminal_blocker"] == nil {
					t.Fatalf("unsafe fallback: %v %d %v", failure, sends, evidence)
				}
				if !reflect.DeepEqual(sequence, []string{"observe", "persist", "save"}) {
					t.Fatal(sequence)
				}
				return
			}
			if failure != nil || sends != 1 || !reflect.DeepEqual(result, settled) {
				t.Fatalf("lost/duplicated real run: %v %d %+v", failure, sends, result)
			}
			if !reflect.DeepEqual(sequence, []string{"observe", "replay_complete", "send", "settle", "capture", "persist", "save"}) {
				t.Fatal(sequence)
			}
			if !reflect.DeepEqual(evidence["terminal_manifest"], receipt) {
				t.Fatal("receipt not retained")
			}
			if mode == "observer-after-send" && evidence["terminal_blocker"] != "capture failed" {
				t.Fatal(evidence)
			}
			var duplicate any
			func() {
				defer func() { duplicate = recover() }()
				send("skills-after-removal", "existing owner request")
			}()
			if duplicate == nil || sends != 1 {
				t.Fatal("repeated wrapper call replayed work")
			}
		})
	}
}

func TestAgentLearningTerminalSendLeavesOtherActionsUnchanged(t *testing.T) {
	run := store.BotRun{ID: uuid.New()}
	sends := 0
	send := agentLearningTerminalSend(func(key, message string) store.BotRun {
		sends++
		if key != "skills-feedback" || message != "feedback" {
			t.Fatal("changed request")
		}
		return run
	}, nil, func(func() error) (map[string]string, error) { t.Fatal("observed earlier action"); return nil, nil }, nil, nil, nil)
	if got := send("skills-feedback", "feedback"); got.ID != run.ID || sends != 1 {
		t.Fatal("changed normal send")
	}
}
