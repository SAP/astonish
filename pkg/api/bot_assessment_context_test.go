package api

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

func TestBotAssessmentContextCapture(t *testing.T) {
	for _, scenario := range []string{"valid", "blocked", "invalid"} {
		t.Run(scenario, func(t *testing.T) {
			candidate := &store.BotWorkCandidate{Text: "Queued; not applied", Bodies: map[string]string{"pending-control:0": `{"status":"pending"}`}}
			before := candidate.ContentDigest()
			resources := &BotChatResources{AssessmentContext: func(context.Context) (json.RawMessage, store.BotEvidence, error) {
				if scenario == "blocked" {
					return nil, store.BotEvidence{}, store.ErrBotForbidden
				}
				raw := json.RawMessage(`{"data_class":"untrusted_observed_metadata","effective_enabled":false}`)
				if scenario == "invalid" {
					raw = json.RawMessage(`not JSON`)
				}
				return raw, store.BotEvidence{Kind: store.BotEvidenceObservation, ActorID: uuid.New(), Source: "bot-assessment-state://scoped", ObservedAt: time.Now()}, nil
			}}
			err := captureBotAssessmentContext(context.Background(), resources, candidate)
			if scenario != "valid" {
				if err == nil || len(candidate.Observations) != 0 {
					t.Fatal("invalid evidence captured")
				}
				if scenario == "blocked" && !errors.Is(err, store.ErrBotForbidden) {
					t.Fatal(err)
				}
				return
			}
			if candidate.ContentDigest() == before || len(candidate.Observations) != 1 || candidate.Bodies[botAssessmentContextKey] == "" {
				t.Fatal("snapshot not digest-pinned")
			}
			if candidate.Text != "Queued; not applied" || candidate.Bodies["pending-control:0"] != `{"status":"pending"}` {
				t.Fatal("pending work changed")
			}
		})
	}
}
