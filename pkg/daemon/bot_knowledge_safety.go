package daemon

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/credentials"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/launcher"
	"github.com/SAP/astonish/pkg/store"
)

// This policy accepts only inert scoped Markdown. It grants neither execution nor
// research capabilities and is deliberately separate from answer publication.
func botKnowledgeSafety(c context.Context, i store.BotKnowledgeExtractionInput, d store.BotKnowledgeExtractionDecision) error {
	if err := c.Err(); err != nil {
		return err
	}
	if i.Validate() != nil || d.Validate() != nil {
		return store.ErrBotInvalid
	}
	r := credentials.RedactorFromContext(c)
	if r == nil {
		return store.ErrBotUnsupported
	}
	raw, err := json.Marshal(d)
	if err != nil {
		return err
	}
	if r.Redact(string(raw)) != string(raw) {
		return store.ErrBotForbidden
	}
	for _, p := range d.Proposals {
		text := strings.ToLower(p.Content)
		for _, forbidden := range []string{"```", "~~~", "<script", "<iframe", "javascript:", "data:", "{{credential:", "<<<", "-----begin ", "authorization:", "bearer ", "client_secret", "api_key=", "password="} {
			if strings.Contains(text, forbidden) {
				return store.ErrBotForbidden
			}
		}
	}
	return nil
}

// Redaction uses only this owner's permitted credential stores and the effective
// provider configuration layers. It does not copy secrets into retained notes.
func productionKnowledgeRedactor(backend store.PlatformBackend, cfg *config.AppConfig) func(context.Context, execution.Principal, store.BotScope) (*credentials.Redactor, error) {
	return func(c context.Context, p execution.Principal, scope store.BotScope) (*credentials.Redactor, error) {
		org, err := backend.ForOrg(p.OrgSlug)
		if err != nil {
			return nil, err
		}
		svc, err := launcher.BotChatServicesForOwner(org, p, scope)
		if err != nil {
			return nil, err
		}
		r := credentials.NewRedactor()
		for _, cs := range []store.CredentialStore{svc.Credentials, svc.PersonalCredentials} {
			if cs == nil {
				continue
			}
			if err := cs.Reload(c); err != nil {
				return nil, err
			}
			for name := range cs.List(c) {
				cred := cs.Get(c, name)
				if cred == nil {
					return nil, store.ErrBotUnsupported
				}
				data, err := json.Marshal(cred)
				if err != nil {
					return nil, err
				}
				var fields map[string]any
				if err := json.Unmarshal(data, &fields); err != nil {
					return nil, err
				}
				for key, value := range fields {
					if s, ok := value.(string); ok && key != "type" && key != "header" {
						r.AddSecret(name, s)
					}
				}
			}
			for _, key := range cs.ListSecrets(c) {
				r.AddSecret(key, cs.GetSecret(c, key))
			}
		}
		add := func(v any) {
			data, _ := json.Marshal(v)
			var fields any
			_ = json.Unmarshal(data, &fields)
			var walk func(any)
			walk = func(v any) {
				switch x := v.(type) {
				case string:
					r.AddSecret("provider", x)
				case map[string]any:
					for key, y := range x {
						lower := strings.ToLower(key)
						if strings.Contains(lower, "secret") || strings.Contains(lower, "token") || strings.Contains(lower, "password") || strings.Contains(lower, "api_key") || lower == "client_id" {
							walk(y)
						} else {
							if _, nested := y.(map[string]any); nested {
								walk(y)
							}
						}
					}
				case []any:
					for _, y := range x {
						walk(y)
					}
				}
			}
			walk(fields)
		}
		if cfg != nil {
			add(cfg)
		}
		ps, err := backend.PlatformSettings().Get(c)
		if err != nil {
			return nil, err
		}
		if ps != nil {
			add(ps.Providers)
		}
		os, err := backend.OrgSettings(p.OrgSlug).Get(c)
		if err != nil {
			return nil, err
		}
		if os != nil {
			add(os.Providers)
		}
		ts, err := svc.Settings.Get(c)
		if err != nil {
			return nil, err
		}
		if ts != nil {
			add(ts.Providers)
		}
		return r, c.Err()
	}
}

var _ bot.KnowledgeExtractionSafety = botKnowledgeSafety
