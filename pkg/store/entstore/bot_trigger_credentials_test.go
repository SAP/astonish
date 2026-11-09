package entstore

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

type mutableTriggerCredentials struct {
	store.CredentialStore
	secret    string
	beforeGet func()
}

func (c *mutableTriggerCredentials) Get(context.Context, string) *store.Credential {
	if c.beforeGet != nil {
		f := c.beforeGet
		c.beforeGet = nil
		f()
	}
	if c.secret == "" {
		return nil
	}
	return &store.Credential{Type: store.CredAPIKey, Value: c.secret}
}

func TestBotTriggerCredentialBindingAndRevocation(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		for _, kind := range []store.TriggerKind{store.TriggerHook, store.TriggerWebhook} {
			t.Run(string(scope)+"/"+string(kind), func(t *testing.T) {
				repo, ctx, _, seed := scheduledFixture(t, scope, store.TriggerCron, 20)
				creds := &mutableTriggerCredentials{secret: triggerFixtureSecret}
				ctx = store.WithServices(ctx, &store.Services{PersonalCredentials: creds, Credentials: creds})
				tid := uuid.New()
				def := store.TriggerSpec{Kind: kind, Instructions: "Reviewed input", CredentialRef: "signing"}
				change := func(action store.BotTriggerAction, proof *store.BotExternalEvent, want error) *store.Bot {
					t.Helper()
					b, err := repo.Get(ctx, seed.BotID)
					if err != nil {
						t.Fatal(err)
					}
					out, err := repo.ChangeTrigger(ctx, store.BotTriggerChange{BotID: b.ID, TriggerID: tid, ExpectedRevision: b.Revision, Action: action, Reason: "reviewed", Definition: def, Proof: proof})
					if !errors.Is(err, want) {
						t.Fatalf("%s: got %v want %v", action, err, want)
					}
					return out
				}
				b := change(store.BotTriggerSave, nil, nil)
				q := store.BotExternalEvent{BotID: b.ID, TriggerID: tid, Version: b.Version, EventID: uuid.NewString(), Timestamp: time.Now().Unix()}
				q.Signature = store.BotTriggerSignature(creds.secret, q, "run")
				change(store.BotTriggerDryRun, &q, store.ErrBotForbidden)
				q.Signature = store.BotTriggerSignature(creds.secret, q, "dry-run")
				change(store.BotTriggerDryRun, &q, nil)
				creds.secret = "rotated-fixture-only-secret-at-least-32-bytes"
				change(store.BotTriggerEnable, nil, store.ErrBotConflict)
				change(store.BotTriggerDisable, nil, nil)
				b = change(store.BotTriggerSave, nil, nil)
				q.Version = b.Version
				q.Signature = store.BotTriggerSignature(creds.secret, q, "dry-run")
				change(store.BotTriggerDryRun, &q, nil)
				b = change(store.BotTriggerEnable, nil, nil)
				q.Version = b.Version
				q.Signature = store.BotTriggerSignature(creds.secret, q, "run")
				creds.secret = ""
				if _, err := repo.AdmitExternalRun(ctx, q); !errors.Is(err, store.ErrBotForbidden) {
					t.Fatalf("revoked credential: %v", err)
				}
				creds.secret = "rotated-fixture-only-secret-at-least-32-bytes"
				// Mutation during credential lookup must be rejected against the transaction's
				// exact current revision/version, rather than accepting preflight authority.
				creds.beforeGet = func() { def.Instructions = "Changed during lookup"; change(store.BotTriggerSave, nil, nil) }
				if _, err := repo.AdmitExternalRun(ctx, q); !errors.Is(err, store.ErrBotConflict) {
					t.Fatalf("admission race: %v", err)
				}
				b, _ = repo.Get(ctx, seed.BotID)
				q.Version = b.Version
				q.Signature = store.BotTriggerSignature(creds.secret, q, "dry-run")
				creds.beforeGet = func() { def.CredentialRef = "replacement"; change(store.BotTriggerSave, nil, nil) }
				change(store.BotTriggerDryRun, &q, store.ErrBotConflict)
				b, _ = repo.Get(ctx, seed.BotID)
				q.Version = b.Version
				q.Signature = store.BotTriggerSignature(creds.secret, q, "dry-run")
				change(store.BotTriggerDryRun, &q, nil)
				b = change(store.BotTriggerEnable, nil, nil)
				q.Version = b.Version
				q.Signature = store.BotTriggerSignature(creds.secret, q, "run")
				if _, err := repo.AdmitExternalRun(ctx, q); err != nil {
					t.Fatal(err)
				}
				change(store.BotTriggerDisable, nil, nil)
				change(store.BotTriggerEnable, nil, store.ErrBotConflict)
				change(store.BotTriggerRemove, nil, nil)
				if _, err := repo.AdmitExternalRun(ctx, q); !errors.Is(err, store.ErrBotNotFound) {
					t.Fatalf("removed trigger: %v", err)
				}
				current, _ := repo.Get(ctx, seed.BotID)
				for version := int64(1); version <= current.Version; version++ {
					v, err := repo.GetVersion(ctx, seed.BotID, version)
					if err != nil {
						t.Fatal(err)
					}
					data, err := json.Marshal(v)
					if err != nil {
						t.Fatal(err)
					}
					if strings.Contains(string(data), triggerFixtureSecret) || strings.Contains(string(data), creds.secret) {
						t.Fatal("signing secret persisted in version")
					}
				}
				runs, err := repo.ListRuns(ctx, seed.BotID, store.BotPage{})
				if err != nil || len(runs) != 1 {
					t.Fatalf("rejected requests leaked runs: %+v %v", runs, err)
				}
			})
		}
	}
}

func TestBotTriggerResolvedCredentialExactBinding(t *testing.T) {
	repo, ctx, _, seed := scheduledFixture(t, store.BotScopePersonal, store.TriggerCron, 5)
	ctx = store.WithServices(ctx, &store.Services{PersonalCredentials: triggerCredentials{}})
	b, _ := repo.Get(ctx, seed.BotID)
	tid := uuid.New()
	b, err := repo.ChangeTrigger(ctx, store.BotTriggerChange{BotID: b.ID, TriggerID: tid, ExpectedRevision: b.Revision, Action: store.BotTriggerSave, Reason: "reviewed", Definition: store.TriggerSpec{Kind: store.TriggerHook, Instructions: "Reviewed", CredentialRef: "signing"}})
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := repo.resolveTriggerCredential(ctx, b.ID, tid)
	if err != nil {
		t.Fatal(err)
	}
	rec, _ := repo.db.GetBot(ctx, b.ID)
	v, _ := repo.GetVersion(ctx, b.ID, b.Version)
	tr := v.Triggers[len(v.Triggers)-1]
	if err := repo.validateTriggerCredential(resolved, rec, tr); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"bot", "owner", "version", "revision", "scope", "credential", "instructions", "enabled", "proof"} {
		t.Run(field, func(t *testing.T) {
			changed, trigger := *rec, tr
			switch field {
			case "bot":
				changed.Bot.ID = uuid.New()
			case "owner":
				changed.Bot.OwnerID = uuid.New()
			case "version":
				changed.Bot.Version++
			case "revision":
				changed.Bot.Revision++
			case "scope":
				changed.Bot.Scope = store.BotScopeTeam
			case "credential":
				trigger.CredentialRef = "other"
			case "instructions":
				trigger.Instructions = "other"
			case "enabled":
				trigger.Enabled = true
			case "proof":
				trigger.HookStatus = store.HookDryRunPassed
			}
			if err := repo.validateTriggerCredential(resolved, &changed, trigger); !errors.Is(err, store.ErrBotConflict) {
				t.Fatalf("changed %s accepted: %v", field, err)
			}
		})
	}
}
