package entstore

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	teament "github.com/SAP/astonish/ent/team"
	"github.com/SAP/astonish/ent/team/networkpolicy"
	"github.com/SAP/astonish/pkg/store"
)

// teamNetworkPolicyStore implements store.NetworkPolicyStore using the Ent team client.
type teamNetworkPolicyStore struct {
	client *teament.Client
}

var _ store.NetworkPolicyStore = (*teamNetworkPolicyStore)(nil)

func (s *teamNetworkPolicyStore) List(ctx context.Context) ([]store.NetworkPolicyRule, error) {
	client := s.client
	if tx, ok := ctx.Value(botPolicyTransactionKey{}).(botPolicyTransaction); ok && tx.outer == client {
		client = tx.current
	}
	rows, err := client.NetworkPolicy.Query().
		Order(networkpolicy.ByHost()).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("entstore: NetworkPolicyStore.List: %w", err)
	}
	result := make([]store.NetworkPolicyRule, len(rows))
	for i, row := range rows {
		result[i] = teamEntNetworkPolicyToStore(row)
	}
	return result, nil
}

func (s *teamNetworkPolicyStore) Get(ctx context.Context, id string) (*store.NetworkPolicyRule, error) {
	uid, err := uuid.Parse(id)
	if err != nil {
		return nil, fmt.Errorf("entstore: NetworkPolicyStore.Get: invalid id: %w", err)
	}
	row, err := s.client.NetworkPolicy.Get(ctx, uid)
	if err != nil {
		if teament.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("entstore: NetworkPolicyStore.Get: %w", err)
	}
	r := teamEntNetworkPolicyToStore(row)
	return &r, nil
}

func (s *teamNetworkPolicyStore) Save(ctx context.Context, rule *store.NetworkPolicyRule) error {
	createdBy := uuid.Nil
	if rule.CreatedBy != "" {
		if id, err := uuid.Parse(rule.CreatedBy); err == nil {
			createdBy = id
		}
	}

	action := networkpolicy.ActionAllow
	if rule.Action == store.NetworkPolicyDeny {
		action = networkpolicy.ActionDeny
	}

	// Upsert by host+port.
	n, err := s.client.NetworkPolicy.Update().
		Where(
			networkpolicy.HostEQ(rule.Host),
			networkpolicy.PortEQ(rule.Port),
		).
		SetAction(action).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("entstore: NetworkPolicyStore.Save: update: %w", err)
	}
	if n == 0 {
		_, err = s.client.NetworkPolicy.Create().
			SetHost(rule.Host).
			SetPort(rule.Port).
			SetAction(action).
			SetCreatedBy(createdBy).
			Save(ctx)
		if err != nil {
			return fmt.Errorf("entstore: NetworkPolicyStore.Save: create: %w", err)
		}
	}
	return nil
}

func (s *teamNetworkPolicyStore) Delete(ctx context.Context, id string) error {
	uid, err := uuid.Parse(id)
	if err != nil {
		return fmt.Errorf("entstore: NetworkPolicyStore.Delete: invalid id: %w", err)
	}
	err = s.client.NetworkPolicy.DeleteOneID(uid).Exec(ctx)
	if err != nil && !teament.IsNotFound(err) {
		return fmt.Errorf("entstore: NetworkPolicyStore.Delete: %w", err)
	}
	return nil
}

func teamEntNetworkPolicyToStore(row *teament.NetworkPolicy) store.NetworkPolicyRule {
	action := store.NetworkPolicyAllow
	if row.Action == networkpolicy.ActionDeny {
		action = store.NetworkPolicyDeny
	}
	return store.NetworkPolicyRule{
		ID:        row.ID.String(),
		Host:      row.Host,
		Port:      row.Port,
		Action:    action,
		CreatedBy: row.CreatedBy.String(),
		CreatedAt: row.CreatedAt,
		UpdatedAt: row.UpdatedAt,
	}
}
