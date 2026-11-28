package api

import (
	"context"
	"strings"

	"github.com/SAP/astonish/pkg/store"
)

// channelLinkEmail only selects a destination for a verification challenge. It
// does not create or verify a link; proof still requires the one-time code.
func channelLinkEmail(ctx context.Context, backend store.PlatformBackend, userID, supplied string) (string, error) {
	address := supplied
	if strings.TrimSpace(address) == "" {
		if backend == nil || userID == "" {
			return "", store.ErrBotForbidden
		}
		user, err := backend.Users().GetByID(ctx, userID)
		if err != nil {
			return "", err
		}
		if user == nil || user.ID != userID || user.Status != "active" {
			return "", store.ErrBotForbidden
		}
		address = user.Email
	}
	address = store.CanonicalBotOwnerEmail(address)
	if address == "" {
		return "", store.ErrBotInvalid
	}
	return address, nil
}
