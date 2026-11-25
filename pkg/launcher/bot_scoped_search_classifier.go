package launcher

import (
	"context"
	"encoding/json"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
)

// botScopedSearchClassifier adds target binding without granting read authority.
// Generic scheduled search still requires the existing exact owner approval.
// Each attempt binds the freshly resolved scoped target and checks it against
// its construction snapshot; fingerprints include provider, model, endpoint and
// credentials. The exact resume guard rejects changed fingerprints before
// approval consumption.
// Secrets themselves never enter approval records or public model context.
type botScopedSearchClassifier struct {
	services *store.Services
	config   *config.AppConfig
	fallback agent.BotActionClassifier
}

func (c botScopedSearchClassifier) DescribeAction(ctx context.Context, name string, args json.RawMessage) (execution.ActionDescriptor, error) {
	if c.fallback == nil {
		return execution.ActionDescriptor{}, store.ErrBotForbidden
	}
	qualified := "configured_search/perplexity_web_search"
	if name != qualified {
		action, err := c.fallback.DescribeAction(ctx, name, args)
		if err != nil || name != "perplexity_web_search" {
			return action, err
		}
	}
	action := execution.ActionDescriptor{Tool: name, Arguments: args, Effect: execution.EffectUnknown}
	if c.services == nil || c.config == nil {
		return execution.ActionDescriptor{}, store.ErrBotUnsupported
	}
	// The ordinary search body re-resolves scoped settings at dispatch. Bind that
	// current target, not only the construction snapshot, and fail closed instead
	// of inheriting Chat's tolerant fallback on missing/failed scoped settings.
	current, err := botKnowledgeSearchConfig(ctx, c.services)
	if err != nil {
		return execution.ActionDescriptor{}, err
	}
	snapshot, err := botConfiguredSearchRoute(ctx, c.services, current)
	if err != nil {
		return execution.ActionDescriptor{}, err
	}
	prepared, err := botConfiguredSearchRoute(ctx, c.services, c.config)
	if err != nil {
		return execution.ActionDescriptor{}, err
	}
	if snapshot.Engine != execution.BotSearchPerplexity || prepared.Engine != snapshot.Engine || prepared.Fingerprint != snapshot.Fingerprint {
		return execution.ActionDescriptor{}, store.ErrBotConflict
	}
	action.TargetFingerprint = snapshot.Fingerprint
	// The exact scoped provider/model/endpoint/credential fingerprint is now
	// freshly bound and the configured search implementation is read-only. This
	// classification lets completion consume the bridge response as source
	// evidence; policy and saved search grants still authorize dispatch.
	action.Effect = execution.EffectRead
	action.ConstraintsVerified = true
	return action, nil
}
