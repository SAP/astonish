package main

import (
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"testing"
	"time"
)

func TestInvokeAgentLearningLifecycle(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "verified", true: "failed"}[fail], func(t *testing.T) {
			state := map[string]any{"agent_nonce": "current"}
			evidence := map[string]any{"unverified": agentOutstanding, "runtime_parity_verified": false}
			var sequence []string
			hooks := agentSkillsJourneyHooks{
				Save: func() { sequence = append(sequence, "save") },
				Schedule: func(uuid.UUID) (store.BotVersion, uuid.UUID) {
					sequence = append(sequence, "schedule")
					return store.BotVersion{}, uuid.New()
				},
				Restart:   func() time.Time { sequence = append(sequence, "restart"); return time.Now() },
				Reconnect: func() { sequence = append(sequence, "reconnect") },
			}
			invokeAgentLearning("root", "home", state, evidence, hooks, func(root, home string, s, e map[string]any, h agentSkillsJourneyHooks) {
				if root != "root" || home != "home" || s["agent_nonce"] != "current" {
					t.Fatal("lost invocation identity")
				}
				h.Schedule(uuid.New())
				h.Restart()
				e["learning"] = map[string]any{"status": "running", "receipts": "partial", "contract": agentSkillsContract, "nonce": s["agent_nonce"]}
				if fail {
					panic("actual offline failure")
				}
				h.Reconnect()
				e["learning"].(map[string]any)["status"] = "verified"
			})
			if sequence[0] != "schedule" || sequence[1] != "restart" || sequence[len(sequence)-1] != "save" {
				t.Fatal(sequence)
			}
			if evidence["runtime_parity_verified"] != false {
				t.Fatal("learning certified adapter")
			}
			if fail {
				if evidence["learning_blocker"] != "actual offline failure" || evidence["journeys"] != nil {
					t.Fatal(evidence)
				}
				if evidence["learning"].(map[string]any)["receipts"] != "partial" {
					t.Fatal("lost partial evidence")
				}
			} else {
				journeys := evidence["journeys"].(map[string]any)
				if len(journeys) != 3 || journeys["source-grounding-outcome"] != nil || journeys["studio-terminal-parity-outcome"] != nil {
					t.Fatal(journeys)
				}
			}
		})
	}
}
