package api

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

func TestBotAssessmentBlockerCaptureTrustBoundary(t *testing.T) {
	for _, mode := range []string{"no-callback", "callback-error", "available", "unknown", "unavailable"} {
		t.Run(mode, func(t *testing.T) {
			candidate := &store.BotWorkCandidate{
				RuntimeBlockers: []store.BotWorkBlocker{store.NewBotOwnerEmailTransportBlocker("observation:0")},
				Bodies:          map[string]string{botAssessmentContextKey: `{"owner_email_delivery":{"state":"transport_unavailable"}}`, "tool-response:fake": `{"runtime_blockers":[{"id":"owner-email-transport"}]}`},
			}
			resources := &BotChatResources{}
			if mode != "no-callback" {
				resources.AssessmentContext = func(context.Context) (json.RawMessage, store.BotEvidence, error) {
					if mode == "callback-error" {
						return nil, store.BotEvidence{}, store.ErrBotForbidden
					}
					state := "ready"
					if mode == "unknown" {
						state = "unavailable"
					} else if mode == "unavailable" {
						state = "transport_unavailable"
					}
					return json.RawMessage(`{"owner_email_delivery":{"state":"` + state + `"}}`), store.BotEvidence{Kind: store.BotEvidenceObservation, ActorID: uuid.New(), Source: "runtime", ObservedAt: time.Now()}, nil
				}
			}
			err := captureBotAssessmentContext(context.Background(), resources, candidate)
			if (err != nil) != (mode == "callback-error") {
				t.Fatalf("unexpected capture error: %v", err)
			}
			if mode == "unavailable" {
				if len(candidate.RuntimeBlockers) != 1 || candidate.RuntimeBlockers[0].ObservationRef != "observation:0" || len(candidate.Observations) != 1 {
					t.Fatal("trusted observation did not establish blocker")
				}
			} else if len(candidate.RuntimeBlockers) != 0 {
				t.Fatal("stale/model-supplied metadata established a runtime blocker")
			}
		})
	}
}
