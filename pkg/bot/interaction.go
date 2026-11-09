package bot

import "context"

type interactionOwnerKey struct{}

// WithInteractionOwner is a worker-only capability. Tool adapters require it;
// an ordinary viewer connection cannot manufacture a checkpoint owner.
func WithInteractionOwner(ctx context.Context) context.Context {
	return context.WithValue(ctx, interactionOwnerKey{}, true)
}
func HasInteractionOwner(ctx context.Context) bool {
	v, _ := ctx.Value(interactionOwnerKey{}).(bool)
	return v
}
