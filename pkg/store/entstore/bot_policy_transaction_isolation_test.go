package entstore

import (
	"context"
	"github.com/SAP/astonish/pkg/store"
	"testing"
	"time"
)

func TestBotPolicyTransactionDoesNotCrossClients(t *testing.T) {
	first, _, _, _, _ := runtimeFixture(t, store.BotScopeTeam)
	second, _, _, _, _ := runtimeFixture(t, store.BotScopeTeam)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	other := &teamNetworkPolicyStore{client: second.db.(teamBotDB).client}
	if err := other.Save(ctx, &store.NetworkPolicyRule{Host: "other-team.test", Action: store.NetworkPolicyAllow}); err != nil {
		t.Fatal(err)
	}
	if err := first.db.Transaction(ctx, func(tx botDraftDB) error {
		rules, err := other.List(botPolicyContext(ctx, tx))
		if err != nil {
			return err
		}
		if len(rules) != 1 || rules[0].Host != "other-team.test" {
			t.Fatal("policy lookup crossed team clients")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
