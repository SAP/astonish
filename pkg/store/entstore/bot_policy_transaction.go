package entstore

import (
	"context"
	teament "github.com/SAP/astonish/ent/team"
)

// Private transaction routing is installed only at trusted mandate resolver calls.
// Matching the outer client prevents a policy read for another team from being
// accidentally routed through this team's transaction. No policy is cached.
type botPolicyTransactionKey struct{}
type botPolicyTransaction struct{ outer, current *teament.Client }

func botPolicyContext(ctx context.Context, db botDraftDB) context.Context {
	if d, ok := db.(teamBotDB); ok && d.policyOuter != nil {
		return context.WithValue(ctx, botPolicyTransactionKey{}, botPolicyTransaction{d.policyOuter, d.client})
	}
	return ctx
}
