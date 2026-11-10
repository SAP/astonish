package entstore

import (
	"context"
	"errors"
	"testing"
	"time"

	botdomain "github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

type externalAdmissionInputStore struct {
	store.BotStore
	input store.BotRunInput
}

func (s externalAdmissionInputStore) LoadRunInput(context.Context, store.BotID, store.RunID) (*store.BotRunInput, error) {
	input := s.input
	return &input, nil
}

func TestBotSupervisorExecutesValidatedExternalAdmissions(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopeTeam, store.BotScopePersonal} {
		for _, kind := range []store.TriggerKind{store.TriggerHook, store.TriggerWebhook, store.TriggerChannel} {
			t.Run(string(scope)+"/"+string(kind), func(t *testing.T) {
				repo, scoped, ctx, seed, home := runtimeFixture(t, scope)
				ctx = store.WithServices(ctx, &store.Services{Credentials: triggerCredentials{}, PersonalCredentials: triggerCredentials{}})
				tid := uuid.New()
				binding := store.BotChannelBinding{ID: uuid.New(), Channel: "slack", ChatID: "exact"}
				def := store.TriggerSpec{Kind: kind, Instructions: "Saved authenticated input", CredentialRef: "key"}
				if kind == store.TriggerChannel {
					def.CredentialRef = ""
					def.ChannelBinding = &binding
				}
				change := func(action store.BotTriggerAction, proof *store.BotExternalEvent) *store.Bot {
					b, err := repo.Get(ctx, seed.BotID)
					if err != nil {
						t.Fatal(err)
					}
					b, err = repo.ChangeTrigger(ctx, store.BotTriggerChange{BotID: b.ID, TriggerID: tid, ExpectedRevision: b.Revision, Action: action, Reason: "reviewed", Definition: def, Proof: proof})
					if err != nil {
						t.Fatal(err)
					}
					return b
				}
				b := change(store.BotTriggerSave, nil)
				event := store.BotExternalEvent{BotID: b.ID, TriggerID: tid, Version: b.Version, EventID: uuid.NewString(), Timestamp: time.Now().Unix(), Payload: []byte("untrusted")}
				if kind != store.TriggerChannel {
					event.Signature = store.BotTriggerSignature(triggerFixtureSecret, event, "dry-run")
					change(store.BotTriggerDryRun, &event)
				}
				b = change(store.BotTriggerEnable, nil)
				event.Version = b.Version
				admitCtx := ctx
				if kind == store.TriggerChannel {
					p, _ := execution.PrincipalFromContext(ctx)
					p.Authentication = execution.AuthMethodChannel
					p.Surface = execution.SurfaceChannel
					admitCtx, _ = execution.WithPrincipal(ctx, p)
					event.Binding = &binding
				} else {
					event.Signature = store.BotTriggerSignature(triggerFixtureSecret, event, "run")
				}
				run, err := repo.AdmitExternalRun(admitCtx, event)
				if err != nil {
					t.Fatal(err)
				}
				input, err := repo.LoadRunInput(ctx, b.ID, run.ID)
				if err != nil || !input.ExternalAdmissionValidated {
					t.Fatalf("missing persisted admission: %+v %v", input, err)
				}
				adapter := &runtimeTestAdapter{start: func(c context.Context, q botdomain.RunRequest, _ botdomain.EventSink) (botdomain.RunResult, error) {
					if q.Instructions != def.Instructions || q.Run.Trigger != kind {
						t.Fatal("dispatch input replaced saved admission")
					}
					return botdomain.RunResult{Status: store.RunSucceeded, Report: acceptedWorkReport(t, repo, c, q.Run.Lease, "Executed validated admission")}, nil
				}}
				// Neither an external kind alone nor a validated marker without the
				// persisted trigger identity is enough to execute.
				for _, missingMarker := range []bool{true, false} {
					invalid := *input
					if missingMarker {
						invalid.ExternalAdmissionValidated = false
					} else {
						invalid.Run.TriggerID = uuid.Nil
					}
					denied, err := botdomain.NewSupervisor(runtimeConfig(externalAdmissionInputStore{BotStore: scoped, input: invalid}, home, adapter))
					if err != nil {
						t.Fatal(err)
					}
					if _, err := denied.Execute(ctx, b.ID, run.ID); !errors.Is(err, store.ErrBotForbidden) {
						t.Fatalf("unvalidated external input executed: %v", err)
					}
				}
				if adapter.calls.Load() != 0 {
					t.Fatal("denied admission reached runtime")
				}
				supervisor, err := botdomain.NewSupervisor(runtimeConfig(scoped, home, adapter))
				if err != nil {
					t.Fatal(err)
				}
				if _, err = supervisor.Execute(ctx, b.ID, run.ID); err != nil {
					t.Fatal(err)
				}
				if adapter.calls.Load() != 1 {
					t.Fatal("external admission not executed")
				}
				if _, err = supervisor.Execute(ctx, b.ID, run.ID); !errors.Is(err, store.ErrBotConflict) {
					t.Fatal("duplicate executed", err)
				}
			})
		}
	}
}
