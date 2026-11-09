package store

import (
	"context"
	"net/url"
	"strings"
)

// BotHookPoll is a bounded deterministic GET/change detector. The endpoint and
// exact allowed host are reviewed configuration, never event-provided routing.
type BotHookPoll struct {
	URL         string `json:"url"`
	AllowedHost string `json:"allowed_host"`
}

func (p *BotHookPoll) Validate() error {
	if p == nil {
		return ErrBotInvalid
	}
	u, err := url.Parse(p.URL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Fragment != "" || u.RawQuery != "" || u.Port() != "" && u.Port() != "443" || strings.ToLower(u.Hostname()) != p.AllowedHost || strings.ContainsAny(p.AllowedHost, "* /\\") {
		return ErrBotInvalid
	}
	return nil
}

// BotHookPollStore only handles persisted scheduler occurrences. handled=false
// means the trigger is not an outbound poll and normal scheduled admission applies.
type BotHookPollStore interface {
	PollScheduledHook(context.Context, BotScheduledOccurrence) (handled bool, admission *BotScheduledAdmission, err error)
}
