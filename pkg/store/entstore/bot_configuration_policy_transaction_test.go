package entstore

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

// newTeamClient uses MaxOpenConns(1), matching production SQLite. A missing
// transaction route must fail on this deadline rather than hang the suite.
func TestBotConfigurationPolicyTransaction(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			repo, base, _, _, _, _ := botRunFixture(t, scope)
			ctx, cancel := context.WithTimeout(base, 3*time.Second)
			defer cancel()
			owner, _ := execution.PrincipalFromContext(ctx)
			v := store.DefaultBotVersion()
			v.Charter = "Reviewed policy"
			b, err := repo.Create(ctx, store.BotCreateRequest{Name: "Policy", OwnerID: uuid.MustParse(owner.Subject), Version: v, IdempotencyKey: uuid.NewString()})
			if err != nil {
				t.Fatal(err)
			}
			b, err = repo.Pause(ctx, store.BotLifecycleRequest{BotID: b.ID, ActorID: b.OwnerID, ExpectedRevision: b.Revision, Reason: "Review sensitive policy"})
			if err != nil {
				t.Fatal(err)
			}
			client := newTeamClient(t)
			if d, ok := repo.db.(teamBotDB); ok {
				client = d.client
			}
			networks := &teamNetworkPolicyStore{client: client}
			rule := &store.NetworkPolicyRule{Host: "policy.test", Action: store.NetworkPolicyAllow}
			if err := networks.Save(ctx, rule); err != nil {
				t.Fatal(err)
			}
			calls := 0
			policy := func(c context.Context, ref store.BotMandateRef) (store.BotMandatePolicy, error) {
				calls++
				rules, err := networks.List(c)
				if err != nil {
					return store.BotMandatePolicy{}, err
				}
				if len(rules) != 1 || rules[0].Action != store.NetworkPolicyAllow {
					return store.BotMandatePolicy{}, store.ErrBotForbidden
				}
				return store.BotMandatePolicy{RuntimePolicy: execution.NewBotProgressRuntimePolicy()}, nil
			}
			ctx = store.WithBotMandateAdmission(ctx, policy)
			q := store.BotConfigurationRequest{BotID: b.ID, ExpectedRevision: b.Revision, ExpectedVersion: b.Version, Reviewed: true, Reason: "Reviewed runtime", Autonomy: v.Autonomy, Budget: v.Budget, Delivery: v.Delivery}
			q.Budget.RuntimePolicy = execution.NewBotProgressRuntimePolicy()
			configured, err := repo.ConfigureReviewed(ctx, q)
			if err != nil {
				t.Fatalf("single-connection configuration: %v", err)
			}
			if calls != 1 || configured.Version != b.Version+1 {
				t.Fatalf("policy not freshly resolved: calls=%d bot=%+v", calls, configured)
			}
			// Policy changes after successful resolution must be read again, not cached.
			rule.Action = store.NetworkPolicyDeny
			if err := networks.Save(ctx, rule); err != nil {
				t.Fatal(err)
			}
			q.ExpectedRevision, q.ExpectedVersion = configured.Revision, configured.Version
			if _, err := repo.ConfigureReviewed(ctx, q); !errors.Is(err, store.ErrBotForbidden) {
				t.Fatalf("revoked policy accepted: %v", err)
			}
			after, err := repo.Get(ctx, b.ID)
			if err != nil || after.Revision != configured.Revision || after.Version != configured.Version || calls != 2 {
				t.Fatalf("rejected configuration changed state: %+v calls=%d err=%v", after, calls, err)
			}
		})
	}
}

func TestBotPolicyTeamMCPTransaction(t *testing.T) {
	client := newTeamClient(t)
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	outer := &teamMCPServerStore{client: client}
	tx, err := client.Tx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	inside := &teamMCPServerStore{client: tx.Client()}
	if err := inside.Save(ctx, &store.MCPServer{Name: "current-policy", Command: "test"}); err != nil {
		t.Fatal(err)
	}
	routed := botPolicyContext(ctx, teamBotDB{client: tx.Client(), policyOuter: client})
	rows, err := outer.List(routed)
	if err != nil || len(rows) != 1 || rows[0].Name != "current-policy" {
		t.Fatalf("transaction-local MCP policy: %+v %v", rows, err)
	}
	foreign := &teamMCPServerStore{client: newTeamClient(t)}
	if err := foreign.Save(ctx, &store.MCPServer{Name: "foreign-policy", Command: "test"}); err != nil {
		t.Fatal(err)
	}
	rows, err = foreign.List(routed)
	if err != nil || len(rows) != 1 || rows[0].Name != "foreign-policy" {
		t.Fatalf("MCP policy crossed clients: %+v %v", rows, err)
	}
}
