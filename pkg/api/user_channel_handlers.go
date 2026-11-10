package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/SAP/astonish/pkg/channels"
	"github.com/SAP/astonish/pkg/mailer"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
)

// --- Link Code Store ---

var (
	linkCodeStoreMu sync.RWMutex
	linkCodeStore   LinkCodeBackend
)

// SetLinkCodeStore registers the link code store for API handlers.
func SetLinkCodeStore(s LinkCodeBackend) {
	linkCodeStoreMu.Lock()
	defer linkCodeStoreMu.Unlock()
	linkCodeStore = s
}

// GetLinkCodeStore returns the active link code store.
func GetLinkCodeStore() LinkCodeBackend {
	linkCodeStoreMu.RLock()
	defer linkCodeStoreMu.RUnlock()
	return linkCodeStore
}

// linkCodeStoreProvider is implemented by entstore.Store for DB-backed codes.
type linkCodeStoreProvider interface {
	LinkCodes() store.LinkCodeStore
}

// ensureLinkCodeStore returns the active link code backend, lazily wiring it
// from the platform DB when API pods have no local ChannelManager.
func ensureLinkCodeStore() LinkCodeBackend {
	if s := GetLinkCodeStore(); s != nil {
		return s
	}
	backend := getPlatformBackend()
	if backend == nil {
		return nil
	}
	if p, ok := backend.(linkCodeStoreProvider); ok {
		s := NewDBLinkCodeBackend(p.LinkCodes())
		SetLinkCodeStore(s)
		return s
	}
	return nil
}

// --- User Channel Management Endpoints ---
//
// These endpoints allow authenticated users to link and manage their
// external messaging channels (Telegram, Email). The linked channels
// are used for:
//   - Inbound message routing (Telegram → correct user/team context)
//   - Scheduler delivery (job results sent to the user's linked channels)
//   - Dynamic allowlist (replaces static config.yaml in platform mode)

// handleListUserChannels returns all channel links for the authenticated user.
// GET /api/user/channels
func handleListUserChannels(w http.ResponseWriter, r *http.Request) {
	user := GetPlatformUser(r)
	if user == nil {
		respondError(w, http.StatusUnauthorized, "authentication required")
		return
	}

	pg := getPlatformBackend()
	if pg == nil {
		respondError(w, http.StatusNotImplemented, "user channels require platform mode")
		return
	}

	channels, err := pg.UserChannels().ListByUser(r.Context(), user.ID)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "failed to list channels")
		return
	}
	if channels == nil {
		channels = []*store.UserChannel{}
	}

	respondJSON(w, http.StatusOK, map[string]any{"channels": channels})
}

// handleLinkUserChannel creates a new channel link.
// POST /api/user/channels
// Body: { "channel_type": "telegram", "external_id": "123456789", "display_name": "@user" }
func handleLinkUserChannel(w http.ResponseWriter, r *http.Request) {
	respondError(w, http.StatusBadRequest, "channel links must be verified with a link code")
}

// handleUpdateUserChannel updates a channel link's mutable fields.
// PATCH /api/user/channels/{id}
func handleUpdateUserChannel(w http.ResponseWriter, r *http.Request) {
	user := GetPlatformUser(r)
	if user == nil {
		respondError(w, http.StatusUnauthorized, "authentication required")
		return
	}

	pg := getPlatformBackend()
	if pg == nil {
		respondError(w, http.StatusNotImplemented, "user channels require platform mode")
		return
	}

	id := mux.Vars(r)["id"]
	ch, err := pg.UserChannels().GetByID(r.Context(), id)
	if err != nil {
		respondError(w, http.StatusNotFound, "channel link not found")
		return
	}

	// Ensure user owns this link
	if ch.UserID != user.ID {
		respondError(w, http.StatusForbidden, "not your channel link")
		return
	}

	var req struct {
		DisplayName *string `json:"display_name"`
		Enabled     *bool   `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid request body")
		return
	}

	if req.DisplayName != nil {
		ch.DisplayName = *req.DisplayName
	}
	if req.Enabled != nil {
		ch.Enabled = *req.Enabled
	}

	if err := pg.UserChannels().Update(r.Context(), ch); err != nil {
		respondError(w, http.StatusInternalServerError, "failed to update channel")
		return
	}

	respondJSON(w, http.StatusOK, map[string]any{"channel": ch})
}

// handleUnlinkUserChannel removes a channel link.
// DELETE /api/user/channels/{id}
func handleUnlinkUserChannel(w http.ResponseWriter, r *http.Request) {
	user := GetPlatformUser(r)
	if user == nil {
		respondError(w, http.StatusUnauthorized, "authentication required")
		return
	}

	pg := getPlatformBackend()
	if pg == nil {
		respondError(w, http.StatusNotImplemented, "user channels require platform mode")
		return
	}

	id := mux.Vars(r)["id"]
	ch, err := pg.UserChannels().GetByID(r.Context(), id)
	if err != nil {
		respondError(w, http.StatusNotFound, "channel link not found")
		return
	}

	// Ensure user owns this link
	if ch.UserID != user.ID {
		respondError(w, http.StatusForbidden, "not your channel link")
		return
	}

	if err := pg.UserChannels().Unlink(r.Context(), id); err != nil {
		respondError(w, http.StatusInternalServerError, "failed to unlink channel")
		return
	}

	respondJSON(w, http.StatusOK, map[string]string{"status": "unlinked"})
}

// handleVerifyUserChannel rejects direct verification attempts.
// POST /api/user/channels/{id}/verify
// Channel ownership must be proven through the channel-specific link-code flow.
func handleVerifyUserChannel(w http.ResponseWriter, r *http.Request) {
	if GetPlatformUser(r) == nil {
		respondError(w, http.StatusUnauthorized, "authentication required")
		return
	}

	respondError(w, http.StatusGone, "direct channel verification is disabled; unlink and use the channel link-code flow")
}

// handleGenerateLinkCode creates a one-time link code for channel linking.
// POST /api/user/channels/link-code
// Body: { "channel_type": "telegram" }               → returns code + bot_username
// Body: { "channel_type": "email", "email": "..." }  → sends code via email
func handleGenerateLinkCode(w http.ResponseWriter, r *http.Request) {
	user := GetPlatformUser(r)
	if user == nil {
		respondError(w, http.StatusUnauthorized, "authentication required")
		return
	}

	store := ensureLinkCodeStore()
	if store == nil {
		respondError(w, http.StatusNotImplemented, "link code service not available")
		return
	}

	var req struct {
		ChannelType      string                `json:"channel_type"`
		ExpectedIdentity channels.LinkIdentity `json:"expected_identity"`
		Email            string                `json:"email"` // required for email channel
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.ChannelType == "" {
		req.ChannelType = "telegram"
	}

	binding := ""
	if req.ChannelType == string(channels.LinkTelegram) || req.ChannelType == string(channels.LinkSlack) {
		var err error
		binding, err = channels.LinkBinding(channels.LinkChannel(req.ChannelType), req.ExpectedIdentity)
		if err != nil {
			respondError(w, http.StatusBadRequest, err.Error())
			return
		}
	}

	switch req.ChannelType {
	case "telegram":
		// Generate the code
		code, err := store.Generate(r.Context(), user.ID, user.Email, binding)
		if err != nil {
			respondError(w, http.StatusInternalServerError, "failed to generate link code")
			return
		}

		// Get bot username from channel manager
		botUsername := ""
		if cm := GetChannelManager(); cm != nil {
			botUsername = cm.GetTelegramBotUsername()
		}

		respondJSON(w, http.StatusOK, map[string]any{
			"code":         code,
			"bot_username": botUsername,
			"expires_in":   300,
		})

	case "email":
		// Omitted recipient means the authenticated account's live email, never
		// an address supplied by a Bot or cached login profile.
		emailAddr, err := channelLinkEmail(r.Context(), getPlatformBackend(), user.ID, req.Email)
		if err != nil {
			respondError(w, http.StatusBadRequest, "valid account email is required; update your profile or provide an email")
			return
		}

		// Check email channel is configured
		if !mailer.IsConfigured() {
			respondError(w, http.StatusServiceUnavailable, "email channel is not configured")
			return
		}

		// Generate the code
		code, err := store.Generate(r.Context(), user.ID, user.Email, "email:"+emailAddr)
		if err != nil {
			respondError(w, http.StatusInternalServerError, "failed to generate link code")
			return
		}

		// Send verification email
		devEnv := false
		if pb := getPlatformBackend(); pb != nil {
			devEnv = isDevEnvironment(r.Context(), pb.PlatformSettings())
		}
		if err := mailer.Send(r.Context(), mailer.VerificationCode{
			Recipient:      emailAddr,
			Code:           code,
			DevEnvironment: devEnv,
		}); err != nil {
			respondError(w, http.StatusInternalServerError, "failed to send verification email: "+err.Error())
			return
		}

		respondJSON(w, http.StatusOK, map[string]any{
			"code_sent":  true,
			"email":      emailAddr,
			"expires_in": 300,
		})

	case "slack":
		// Generate the code
		code, err := store.Generate(r.Context(), user.ID, user.Email, binding)
		if err != nil {
			respondError(w, http.StatusInternalServerError, "failed to generate link code")
			return
		}

		// Get bot user ID from channel manager
		botUserID := ""
		if cm := GetChannelManager(); cm != nil {
			botUserID = cm.GetSlackBotUserID()
		}

		respondJSON(w, http.StatusOK, map[string]any{
			"code":        code,
			"bot_user_id": botUserID,
			"expires_in":  300,
		})

	default:
		respondError(w, http.StatusBadRequest, "unsupported channel type: "+req.ChannelType)
	}
}

// handleVerifyEmailCode validates a code the user received via email and
// creates a verified user_channel record linking their email address.
// POST /api/user/channels/verify-email-code
// Body: { "code": "ABC123" }
func handleVerifyEmailCode(w http.ResponseWriter, r *http.Request) {
	user := GetPlatformUser(r)
	if user == nil {
		respondError(w, http.StatusUnauthorized, "authentication required")
		return
	}

	linkStore := ensureLinkCodeStore()
	if linkStore == nil {
		respondError(w, http.StatusNotImplemented, "link code service not available")
		return
	}

	var req struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	code := strings.TrimSpace(req.Code)
	if code == "" {
		respondError(w, http.StatusBadRequest, "code is required")
		return
	}

	if !channelLinkAttempts.Allow(channels.LinkEmail, user.ID) {
		respondError(w, http.StatusTooManyRequests, "too many verification attempts; try again later")
		return
	}

	// Validate ownership and intended channel before consuming the one-time code.
	pending := linkStore.Peek(r.Context(), code)
	if pending == nil {
		respondError(w, http.StatusBadRequest, "invalid or expired code")
		return
	}
	if pending.UserID != user.ID || !strings.HasPrefix(pending.Channel, "email:") {
		// Do not disclose whether a guessed code exists or which user/channel owns it.
		respondError(w, http.StatusBadRequest, "invalid or expired code")
		return
	}

	pending = linkStore.Consume(r.Context(), code)
	if pending == nil {
		respondError(w, http.StatusBadRequest, "invalid or expired code")
		return
	}

	// The Channel field for email codes has the format "email:<address>"
	emailAddr := strings.TrimPrefix(pending.Channel, "email:")

	// Check if this email is already linked to another user
	pgStore := getPlatformBackend()
	if pgStore == nil {
		respondError(w, http.StatusInternalServerError, "platform store not available")
		return
	}
	ucStore := pgStore.UserChannels()

	existing, err := ucStore.GetByExternalID(r.Context(), "email", emailAddr)
	if err != nil {
		respondError(w, http.StatusInternalServerError, "database error")
		return
	}
	if existing != nil && existing.UserID != user.ID {
		respondError(w, http.StatusConflict, "this email is already linked to a different user")
		return
	}
	if existing != nil && existing.UserID == user.ID {
		// Verification and enablement must commit together. Never fall back to
		// separate writes: a failure would leave a partially verified link.
		proofStore, ok := ucStore.(store.UserChannelProofStore)
		if !ok {
			respondError(w, http.StatusInternalServerError, "failed to verify email")
			return
		}
		existing, err = proofStore.VerifyAndEnable(r.Context(), existing.ID, user.ID)
		if err != nil {
			respondError(w, http.StatusInternalServerError, "failed to verify email")
			return
		}
		// Refresh email allowlist
		refreshEmailAllowlist(r.Context(), pgStore)
		respondJSON(w, http.StatusOK, map[string]any{
			"message": "email re-verified",
			"channel": existing,
		})
		return
	}

	// Create new verified user_channel
	ch := &store.UserChannel{
		ID:          uuid.New().String(),
		UserID:      user.ID,
		ChannelType: "email",
		ExternalID:  emailAddr,
		DisplayName: emailAddr,
		Enabled:     true,
		Verified:    true,
		VerifiedAt:  timePtr(time.Now()),
		CreatedAt:   time.Now(),
	}
	if err := ucStore.Link(r.Context(), ch); err != nil {
		respondError(w, http.StatusInternalServerError, "failed to link email")
		return
	}

	// Refresh email allowlist so the channel starts accepting from this address
	refreshEmailAllowlist(r.Context(), pgStore)

	respondJSON(w, http.StatusOK, map[string]any{
		"message": "email linked and verified",
		"channel": ch,
	})
}

// refreshEmailAllowlist rebuilds the email channel's allowlist from the
// user_channels table. This ensures newly-linked email addresses are
// immediately accepted by the email channel.
func refreshEmailAllowlist(ctx context.Context, pgStore interface{ UserChannels() store.UserChannelStore }) {
	cm := GetChannelManager()
	if cm == nil {
		return
	}

	// Get all verified+enabled email channels from DB
	ucStore := pgStore.UserChannels()
	channels, err := ucStore.ListByChannelType(ctx, "email")
	if err != nil {
		return
	}

	// Build the new allowlist
	var addresses []string
	for _, ch := range channels {
		if ch.Enabled && ch.Verified {
			addresses = append(addresses, ch.ExternalID)
		}
	}

	// Update the channel manager's allowlists
	cm.UpdateAllowlists(map[string][]string{
		"email": addresses,
	})
}

func timePtr(t time.Time) *time.Time {
	return &t
}

// handleGetChannelInfo returns information about configured channels.
// GET /api/channels/info
// Response: { "telegram": { ... }, "email": { ... } }
func handleGetChannelInfo(w http.ResponseWriter, r *http.Request) {
	info := map[string]any{}

	// --- Telegram ---
	botUsername := ""
	tgConnected := false
	if cm := GetChannelManager(); cm != nil {
		botUsername = cm.GetTelegramBotUsername()
		tgConnected = botUsername != ""
	}

	tgEnabled := false
	tgError := ""
	emailEnabled := false
	emailError := ""
	emailAddress := ""
	slackEnabled := false
	slackError := ""

	if cfgStatuses := getChannelConfigStatuses(); cfgStatuses != nil {
		// Channels are running in this process — use in-memory status.
		if tg, ok := cfgStatuses["telegram"]; ok {
			tgEnabled = tg.Enabled
			tgError = tg.Error
		}
		if em, ok := cfgStatuses["email"]; ok {
			emailEnabled = em.Enabled
			emailError = em.Error
		}
		if sl, ok := cfgStatuses["slack"]; ok {
			slackEnabled = sl.Enabled
			slackError = sl.Error
		}
	} else if backend := getPlatformBackend(); backend != nil {
		// API-mode fallback: channels run on a separate worker pod, so
		// channelConfigStatuses is nil in this process. Read the enabled
		// state directly from the platform DB (same source the admin UI uses).
		settings, _ := backend.PlatformSettings().Get(r.Context())
		if settings != nil && settings.Channels != nil {
			if settings.Channels.Telegram != nil && settings.Channels.Telegram.Enabled {
				tgEnabled = true
			}
			if settings.Channels.Email != nil && settings.Channels.Email.Enabled {
				emailEnabled = true
				if settings.Channels.Email.Address != "" {
					emailAddress = settings.Channels.Email.Address
				}
			}
			if settings.Channels.Slack != nil && settings.Channels.Slack.Enabled {
				slackEnabled = true
			}
		}
	}

	// If connected, it's definitely enabled
	if tgConnected {
		tgEnabled = true
	}

	info["telegram"] = map[string]any{
		"bot_username": botUsername,
		"configured":   tgConnected || tgEnabled, // admin has set up the channel (may still be connecting)
		"connected":    tgConnected,              // bot is actually authenticated right now
		"enabled":      tgEnabled,                // channel is enabled in config
		"error":        tgError,
	}

	// --- Email ---
	emailConnected := false
	if cm := GetChannelManager(); cm != nil {
		if addr := cm.GetEmailAddress(); addr != "" {
			emailConnected = true
			emailAddress = addr
		}
	}
	if emailConnected {
		emailEnabled = true
	}

	info["email"] = map[string]any{
		"configured": emailConnected || emailEnabled, // admin has set up the channel (may still be connecting)
		"connected":  emailConnected,                 // IMAP is actually connected right now
		"enabled":    emailEnabled,                   // channel is enabled in config
		"error":      emailError,
		"address":    emailAddress,
	}

	// --- Slack ---
	slackBotUserID := ""
	slackConnected := false
	if cm := GetChannelManager(); cm != nil {
		slackBotUserID = cm.GetSlackBotUserID()
		slackConnected = slackBotUserID != ""
	}

	if slackConnected {
		slackEnabled = true
	}

	info["slack"] = map[string]any{
		"bot_user_id": slackBotUserID,
		"configured":  slackConnected || slackEnabled, // admin has set up the channel (may still be connecting)
		"connected":   slackConnected,                 // bot is actually connected right now
		"enabled":     slackEnabled,                   // channel is enabled in config
		"error":       slackError,
	}

	respondJSON(w, http.StatusOK, info)
}
