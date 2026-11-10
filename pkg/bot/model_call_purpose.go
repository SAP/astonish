package bot

import "context"

// ModelCallPurpose identifies internal prompt composition, not execution authority.
// It must never select a different provider, budget, scope or authorization policy.
type ModelCallPurpose uint8

const (
	ModelCallPurposeProducer ModelCallPurpose = iota
	ModelCallPurposeAssessment
)

type modelCallPurposeKey struct{}

func WithModelCallPurpose(ctx context.Context, purpose ModelCallPurpose) context.Context {
	return context.WithValue(ctx, modelCallPurposeKey{}, purpose)
}

func ModelCallPurposeFromContext(ctx context.Context) ModelCallPurpose {
	purpose, _ := ctx.Value(modelCallPurposeKey{}).(ModelCallPurpose)
	return purpose
}
