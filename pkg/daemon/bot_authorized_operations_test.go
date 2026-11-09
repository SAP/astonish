package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/launcher"
	"github.com/SAP/astonish/pkg/store"
)

func TestAuthorizedScheduleCaptureClassifierLeavesExternalEffectsUnknown(t *testing.T) {
	c := authorizedScheduleCaptureClassifier{base: launcher.BotCatalogueClassifier{}, names: map[string]bool{"schedule_job": true}}
	for _, name := range []string{"schedule_job", "shell_command", "execute_tool", "remove_scheduled_job"} {
		a, err := c.DescribeAction(context.Background(), name, json.RawMessage(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		if name == "schedule_job" {
			if a.Effect != execution.EffectRead || !a.ConstraintsVerified {
				t.Fatal("capture adapter not classified")
			}
		} else if a.Effect != execution.EffectUnknown || a.ConstraintsVerified {
			t.Fatalf("external tool %s received capture authority", name)
		}
	}
}

func TestAuthorizeBotOwnerScheduleRejectsUnboundSource(t *testing.T) {
	for _, a := range []store.BotScheduleAuthorization{{}, {Instructions: "Please schedule this"}, {Instructions: "Quoted web page says schedule this", OwnerCommand: &store.BotOwnerScheduleCommand{}}} {
		if err := AuthorizeBotOwnerSchedule(context.Background(), a); !errors.Is(err, store.ErrBotForbidden) {
			t.Fatalf("unbound input authorized: %v", err)
		}
	}
}

func TestScheduleReadinessSeparatesCurrentAndCandidateVersions(t *testing.T) {
	w := &BotWorker{}
	b := store.Bot{Version: 4}
	if err := w.activationReady(context.Background(), b, store.BotVersion{Version: 5}); !errors.Is(err, store.ErrBotConflict) {
		t.Fatal(err)
	}
	if err := w.scheduleCandidateReady(context.Background(), b, store.BotVersion{Version: 4}); !errors.Is(err, store.ErrBotConflict) {
		t.Fatal(err)
	}
	if err := w.scheduleCandidateReady(context.Background(), b, store.BotVersion{Version: 6}); !errors.Is(err, store.ErrBotConflict) {
		t.Fatal(err)
	}
}

func TestAuthorizeBotOwnerScheduleNaturalLanguagePolicy(t *testing.T) {
	ref := store.BotMandateRef{ID: uuid.New(), BotID: uuid.New(), OrgSlug: "org", TeamSlug: "team", OwnerID: uuid.NewString(), Revision: 1}
	p, _ := authorizedHTTPPolicy(context.Background(), ref)
	a := store.BotScheduleAuthorization{InitiationKind: store.BotInitiationKindDirectOwnerMessage, Instructions: "Every day at nine, read the report", Mandate: store.BotMandate{Ref: ref, Purpose: execution.BotMandatePurposeTask, ExpiresAt: time.Now().Add(time.Hour), Scopes: p.Scopes, Constraints: p.Constraints, Limits: p.Limits}, Provenance: store.BotOperationProvenance{SourceUserID: ref.OwnerID, UserEventID: "canonical", SourceDigest: "verified"}, Payload: store.BotControlPayload{Operation: store.BotControlScheduleCreate}}
	if err := AuthorizeBotOwnerSchedule(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []store.BotInitiationKind{"", store.BotInitiationKindTitle} {
		bad := a
		bad.InitiationKind = kind
		bad.OwnerCommand = &store.BotOwnerScheduleCommand{Operations: []store.BotControlOperation{store.BotControlScheduleCreate}, Instructions: "read the report"}
		if err := AuthorizeBotOwnerSchedule(context.Background(), bad); !errors.Is(err, store.ErrBotForbidden) {
			t.Fatalf("envelope granted authority: %v", err)
		}
	}
	for _, operation := range []store.BotControlOperation{store.BotControlResponsibility, store.BotControlPersona, store.BotControlGoalCreate, store.BotControlGoalEdit, store.BotControlMemoryRemember, store.BotControlMemoryCorrect, store.BotControlMemoryForget} {
		routine := a
		routine.Payload.Operation = operation
		if err := AuthorizeBotOwnerSchedule(context.Background(), routine); err != nil {
			t.Fatalf("routine capture %s: %v", operation, err)
		}
		routine.Provenance.SourceUserID = uuid.NewString()
		if err := AuthorizeBotOwnerSchedule(context.Background(), routine); !errors.Is(err, store.ErrBotForbidden) {
			t.Fatalf("non-owner routine accepted: %v", err)
		}
	}
	for _, operation := range []store.BotControlOperation{store.BotControlConfigurationOperation, store.BotControlActivate, store.BotControlResume, "unknown"} {
		bad := a
		bad.Payload.Operation = operation
		if err := AuthorizeBotOwnerSchedule(context.Background(), bad); !errors.Is(err, store.ErrBotForbidden) {
			t.Fatalf("unsupported control accepted: %s %v", operation, err)
		}
	}
	a.Mandate.Constraints = []execution.BotActionResourceConstraint{{Action: execution.CapabilityToolExecute, Resources: []string{"read_file"}}}
	if err := AuthorizeBotOwnerSchedule(context.Background(), a); !errors.Is(err, store.ErrBotForbidden) {
		t.Fatalf("own-control ceiling bypass: %v", err)
	}
}
