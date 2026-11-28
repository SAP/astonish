package client

// BotResponseError keeps HTTP assignment/access responses distinct from network
// uncertainty while preserving existing error text for callers.
type BotResponseError struct {
	StatusCode int
	Cause      error
}

func (e *BotResponseError) Error() string { return e.Cause.Error() }
func (e *BotResponseError) Unwrap() error { return e.Cause }
