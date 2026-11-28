package store

import (
	"context"
	"errors"
)

// ErrPlatformInitialized means another registration won the bootstrap race.
var ErrPlatformInitialized = errors.New("platform already initialized")

// FirstUserBootstrap publishes the first identity and organization only after
// all required tenant schemas and General-team membership have been prepared.
// A failed call must not make setup-status initialized or leave a login user.
// Implementations may retain unpublished, idempotently reusable schema files.
type FirstUserBootstrap interface {
	BootstrapFirstUser(context.Context, *User, *Organization) error
}
