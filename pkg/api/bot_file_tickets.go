package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
)

// A ticket is an exact-object, short-lived browser credential, not a general
// session. PlatformAuthMiddleware rechecks live directory access before using it.
// Only the random HttpOnly cookie is secret; the returned URL is not a grant.
const botFileTicketCookie = "astonish_bot_download"

type botFileTicket struct {
	user, org, team, target string
	expires                 time.Time
}

var botFileTickets = struct {
	sync.Mutex
	items map[[32]byte]botFileTicket
}{items: make(map[[32]byte]botFileTicket)}

func botFileTicketTarget(r *http.Request) string { return r.URL.Path + "?" + r.URL.Query().Encode() }
func BotFileDownloadTicketHandler(w http.ResponseWriter, r *http.Request) {
	if origin := r.Header.Get("Origin"); origin != "" {
		u, e := url.Parse(origin)
		scheme := "http"
		if isSecureRequest(r) {
			scheme = "https"
		}
		if e != nil || u.Host != r.Host || u.Scheme != scheme {
			http.Error(w, "Origin forbidden", 403)
			return
		}
	}
	if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		http.Error(w, "Origin forbidden", 403)
		return
	}
	o, ok := botResolveFile(w, r)
	if !ok {
		return
	}
	if o.entry.Kind != store.BotFileRegular {
		botFileError(w, store.ErrBotInvalid)
		return
	}
	p, ok := execution.PrincipalFromContext(r.Context())
	if !ok || p.Validate() != nil || p.Kind != execution.PrincipalKindUser {
		http.Error(w, "Authentication required", 401)
		return
	}
	target := *r.URL
	target.Path = strings.TrimSuffix(target.Path, "/download-ticket") + "/download"
	target.RawPath = ""
	// Only supported object selectors are returned, never caller-supplied tokens.
	q := url.Values{}
	for _, name := range []string{"path", "scope"} {
		if value := r.URL.Query().Get(name); value != "" {
			q.Set(name, value)
		}
	}
	q.Set("team", p.TeamSlug)
	target.RawQuery = q.Encode()
	token := make([]byte, 32)
	if _, err := rand.Read(token); err != nil {
		http.Error(w, "Download unavailable", 503)
		return
	}
	value := hex.EncodeToString(token)
	key := sha256.Sum256([]byte(value))
	expires := time.Now().Add(90 * time.Second)
	if evidence, ok := execution.AuthenticationEvidenceFromContext(r.Context()); ok && evidence.ExpiresAt.Before(expires) {
		expires = evidence.ExpiresAt
	}
	if !expires.After(time.Now()) {
		http.Error(w, "Authentication expired", 401)
		return
	}
	botFileTickets.Lock()
	for k, v := range botFileTickets.items {
		if !time.Now().Before(v.expires) {
			delete(botFileTickets.items, k)
		}
	}
	if len(botFileTickets.items) >= 4096 {
		botFileTickets.Unlock()
		http.Error(w, "Too many download tickets", 429)
		return
	}
	botFileTickets.items[key] = botFileTicket{user: p.Subject, org: p.OrgSlug, team: p.TeamSlug, target: target.Path + "?" + q.Encode(), expires: expires}
	botFileTickets.Unlock()
	http.SetCookie(w, &http.Cookie{Name: botFileTicketCookie, Value: value, Path: target.Path, HttpOnly: true, Secure: isSecureRequest(r), SameSite: http.SameSiteStrictMode, MaxAge: 90, Expires: expires})
	botWriteJSON(w, 200, map[string]any{"url": target.RequestURI(), "expires_at": expires})
}

func botFileRequestTicket(r *http.Request) (botFileTicket, bool) {
	if (r.Method != http.MethodGet && r.Method != http.MethodHead) || r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		return botFileTicket{}, false
	}
	cookie, err := r.Cookie(botFileTicketCookie)
	if err != nil || len(cookie.Value) != 64 {
		return botFileTicket{}, false
	}
	key := sha256.Sum256([]byte(cookie.Value))
	botFileTickets.Lock()
	ticket, ok := botFileTickets.items[key]
	if ok && !time.Now().Before(ticket.expires) {
		delete(botFileTickets.items, key)
		ok = false
	}
	botFileTickets.Unlock()
	return ticket, ok && ticket.target == botFileTicketTarget(r)
}
func botFileTicketAllowed(r *http.Request) bool {
	ticket, ok := botFileRequestTicket(r)
	p, present := execution.PrincipalFromContext(r.Context())
	return ok && present && p.Validate() == nil && ticket.user == p.Subject && ticket.org == p.OrgSlug && ticket.team == p.TeamSlug
}

// No cached roles, owner/admin exception, or authentication evidence is minted.
// This context can only reach the exact GET/HEAD target already checked above;
// downstream tenant routing and the Bot repository still authorize the object.
func (pa *PlatformAuth) botFileTicketContext(r *http.Request, ticket botFileTicket) (context.Context, error) {
	if pa.pgStore == nil {
		return nil, store.ErrBotForbidden
	}
	ctx := r.Context()
	u, err := pa.pgStore.Users().GetByID(ctx, ticket.user)
	if err != nil || u == nil || u.ID != ticket.user || u.Status != "active" {
		return nil, store.ErrBotForbidden
	}
	org, err := pa.pgStore.Organizations().GetBySlug(ctx, ticket.org)
	if err != nil || org == nil || org.Slug != ticket.org || org.Status != "active" {
		return nil, store.ErrBotForbidden
	}
	role, err := pa.pgStore.Organizations().GetMemberRole(ctx, u.ID, org.ID)
	if err != nil || role == "" {
		return nil, store.ErrBotForbidden
	}
	data, err := pa.pgStore.ForOrg(ticket.org)
	if err != nil || data == nil || data.Teams() == nil {
		return nil, store.ErrBotForbidden
	}
	member, err := data.Teams().IsTeamMember(ctx, u.ID, ticket.team)
	if err != nil || !member || !time.Now().Before(ticket.expires) {
		return nil, store.ErrBotForbidden
	}
	return buildAuthenticatedContext(ctx, &PlatformClaims{UserID: u.ID, OrgSlug: ticket.org, DefaultTeamSlug: ticket.team, Role: role}, ticket.team)
}
