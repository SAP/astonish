package launcher

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/chatruntime"
	"github.com/SAP/astonish/pkg/common"
	"github.com/SAP/astonish/pkg/credentials"
	"github.com/SAP/astonish/pkg/email"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/tools"
	"google.golang.org/adk/tool"
)

// BotEmailTools keeps all eight Chat schemas. No account is looked up, credential
// read, or network connection opened while preparing the catalogue. Missing
// configuration is an explicit invocation prerequisite, not a global fallback.
func BotEmailTools(r bot.RunRequest, scope *BotChatScope, backend sandbox.Backend, client sandbox.ToolNodeClient) ([]tool.Tool, error) {
	if scope == nil || scope.Services == nil || backend == nil || client == nil {
		return nil, store.ErrBotUnsupported
	}
	if r.Bot.Scope != store.BotScopePersonal && r.Bot.Scope != store.BotScopeTeam {
		return nil, store.ErrBotInvalid
	}
	schemas, err := tools.GetEmailToolsForClient(nil)
	if err != nil {
		return nil, err
	}
	result := make([]tool.Tool, 0, len(schemas))
	sources := &botEmailReplySources{values: map[string]email.ReplySource{}}
	for _, schema := range schemas {
		result = append(result, &botEmailTool{botRunnableTool: schema.(botRunnableTool), request: r, services: scope.Services, backend: backend, client: client, sources: sources})
	}
	return result, nil
}

type botEmailTool struct {
	botRunnableTool
	request  bot.RunRequest
	services *store.Services
	backend  sandbox.Backend
	client   sandbox.ToolNodeClient
	sources  *botEmailReplySources
}

// Sources are shared only by this owner's run catalogue, never by child inputs.
// After restart only a matching durable approval checkpoint can restore a snapshot;
// otherwise another explicitly authorized read is required.
type botEmailReplySources struct {
	mu     sync.Mutex
	values map[string]email.ReplySource
}

func (t *botEmailTool) replySnapshot(ctx context.Context, a email.Account, raw []byte) (email.ReplySnapshot, error) {
	var args tools.EmailReplyArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return email.ReplySnapshot{}, err
	}
	if t.sources == nil {
		return email.ReplySnapshot{}, store.ErrBotForbidden
	}
	t.sources.mu.Lock()
	source, ok := t.sources.values[a.Fingerprint()+":"+args.ID]
	t.sources.mu.Unlock()
	if !ok {
		// Approval records are owner-scoped durable evidence, not model history.
		if checkpoint, err := bot.DecodeApprovalCheckpoint(t.request.Run.Checkpoint, t.request.Run); err == nil {
			if approvals := bot.ApprovalStoreFromContext(ctx); approvals != nil {
				rows, err := approvals.ListApprovals(ctx, t.request.Bot.ID, store.BotPage{Limit: 100})
				if err != nil {
					return email.ReplySnapshot{}, err
				}
				for _, row := range rows {
					if !checkpoint.Matches(row) || row.Action.Tool != "email_reply" {
						continue
					}
					var approvedArgs tools.EmailReplyArgs
					var snapshot email.ReplySnapshot
					if json.Unmarshal(row.Action.Arguments, &approvedArgs) != nil || approvedArgs != args || json.Unmarshal(row.Action.TargetSnapshot, &snapshot) != nil {
						continue
					}
					if snapshot.Account.Fingerprint() != a.Fingerprint() || snapshot.Fingerprint() != row.Action.TargetFingerprint {
						return email.ReplySnapshot{}, store.ErrBotConflict
					}
					return snapshot, nil
				}
			}
		}
		return email.ReplySnapshot{}, fmt.Errorf("%w: email_reply requires a separately authorized email_read of the original message in this run", store.ErrBotForbidden)
	}
	return email.NewReplySnapshot(a, source, args.Body, args.HTML, args.ReplyAll)
}

type botEmailAuthorizedKey struct{}
type botEmailAuthorization struct {
	target      *botEmailTool
	fingerprint string
	snapshot    *email.ReplySnapshot
	check       func(context.Context) error
}

func (t *botEmailTool) account(ctx context.Context) (email.Account, error) {
	if ctx == nil {
		return email.Account{}, store.ErrBotForbidden
	}
	if err := ctx.Err(); err != nil {
		return email.Account{}, err
	}
	id, ok := execution.BotRunIdentityFromContext(ctx)
	p, authenticated := execution.PrincipalFromContext(ctx)
	r := t.request
	if !ok || !authenticated || p.Validate() != nil || p.Subject != r.Bot.OwnerID.String() || id.ActorID != p.Subject || id.BotID != r.Bot.ID || id.RunID != r.Run.ID || id.Version != r.Version.Version || id.OrgSlug != r.Principal.OrgSlug || id.TeamSlug != r.Principal.TeamSlug || p.OrgSlug != id.OrgSlug || p.TeamSlug != id.TeamSlug {
		return email.Account{}, store.ErrBotForbidden
	}
	return scopedEmailAccount(ctx, t.services, r.Bot.Scope == store.BotScopePersonal)
}

func (t *botEmailTool) Run(ctx tool.Context, args any) (map[string]any, error) {
	if ctx == nil {
		return nil, store.ErrBotForbidden
	}
	auth, ok := ctx.Value(botEmailAuthorizedKey{}).(botEmailAuthorization)
	if !ok || auth.target != t || auth.check == nil {
		return nil, store.ErrBotForbidden
	}
	a, err := t.account(ctx)
	if err != nil {
		return nil, err
	}
	if auth.fingerprint != a.Fingerprint() {
		return nil, store.ErrBotConflict
	}
	if t.Name() == "email_reply" && (auth.snapshot == nil || auth.snapshot.Account.Fingerprint() != a.Fingerprint()) {
		return nil, store.ErrBotForbidden
	}
	raw, err := json.Marshal(args)
	if err != nil {
		return nil, err
	}
	var values map[string]any
	if err = json.Unmarshal(raw, &values); err != nil {
		return nil, err
	}
	if err = common.ValidateToolArguments(t, values); err != nil {
		return nil, err
	}
	cs := t.services.Credentials
	if t.request.Bot.Scope == store.BotScopePersonal {
		cs = t.services.PersonalCredentials
	}
	if cs == nil {
		return nil, store.ErrBotForbidden
	}
	// Only reached after live lease, exact policy authorization and tool budget.
	redactor := credentials.NewRedactor()
	redactor.HydrateFromStore(cs)
	if err := sandbox.SyncSessionCredentialVault(store.WithCredentialStore(ctx, cs), t.backend, nil, t.request.Run.SessionID.String()); err != nil {
		return nil, fmt.Errorf("email vault: %s", redactor.Redact(err.Error()))
	}
	result, err := callScopedEmail(ctx, t.client, t.request.Run.SessionID.String(), a, t.Name(), values, auth.snapshot, func() error {
		current, err := t.account(ctx)
		if err != nil {
			return err
		}
		if current.Fingerprint() != auth.fingerprint {
			return store.ErrBotConflict
		}
		return auth.check(ctx)
	}, redactor)
	if err != nil {
		return nil, err
	}
	if t.Name() == "email_read" {
		if evidence, ok := result["_reply_source"]; ok && t.sources != nil {
			encoded, _ := json.Marshal(evidence)
			var source email.ReplySource
			if json.Unmarshal(encoded, &source) == nil && source.ID != "" && source.Digest != "" {
				t.sources.mu.Lock()
				t.sources.values[a.Fingerprint()+":"+source.ID] = source
				t.sources.mu.Unlock()
			}
		}
	}
	delete(result, "_reply_source")
	return redactor.RedactMap(result), nil
}

// WithBotEmailClassifier binds account identity and every argument (including all
// recipients, subject, HTML, body, IDs and flags) into durable exact approval.
// Read/list/wait expose sensitive body and attachment metadata, so these too need
// exact approval; email tools never acquire an implicit read permission by name.
func WithBotEmailClassifier(base agent.BotActionClassifier, tools []tool.Tool) agent.BotActionClassifier {
	targets := map[string]*botEmailTool{}
	for _, t := range tools {
		if e, ok := t.(*botEmailTool); ok {
			targets[e.Name()] = e
		}
	}
	return botEmailClassifier{base: base, targets: targets}
}

type botEmailClassifier struct {
	base    agent.BotActionClassifier
	targets map[string]*botEmailTool
}

func (c botEmailClassifier) DescribeAction(ctx context.Context, name string, args json.RawMessage) (execution.ActionDescriptor, error) {
	if strings.HasPrefix(name, "email_") {
		t := c.targets[name]
		if t == nil {
			return execution.ActionDescriptor{}, store.ErrBotUnsupported
		}
		a, err := t.account(ctx)
		if err != nil {
			return execution.ActionDescriptor{}, err
		}
		if name == "email_reply" {
			snapshot, err := t.replySnapshot(ctx, a, args)
			if err != nil {
				return execution.ActionDescriptor{}, err
			}
			recipients, _ := json.Marshal(struct{ To, CC []string }{snapshot.Message.To, snapshot.Message.CC})
			evidence, _ := json.Marshal(snapshot)
			return execution.ActionDescriptor{Tool: name, Arguments: args, Effect: execution.EffectSend, TargetFingerprint: snapshot.Fingerprint(), TargetSnapshot: evidence, Recipient: string(recipients)}, nil
		}
		return execution.ActionDescriptor{Tool: name, Arguments: args, Effect: execution.EffectUnknown, TargetFingerprint: a.Fingerprint()}, nil
	}
	if c.base == nil {
		return execution.ActionDescriptor{}, store.ErrBotForbidden
	}
	return c.base.DescribeAction(ctx, name, args)
}

// scopedEmailAccount resolves metadata only, with no daemon/channel fallback.
func scopedEmailAccount(ctx context.Context, services *store.Services, personal bool) (email.Account, error) {
	if services == nil {
		return email.Account{}, store.ErrBotUnsupported
	}
	var settings *store.BotEmailSettings
	if personal {
		if services.PersonalSettings != nil {
			s, err := services.PersonalSettings.Get(ctx)
			if err != nil {
				return email.Account{}, err
			}
			if s != nil {
				settings = s.BotEmail
			}
		}
	} else if services.Settings != nil {
		s, err := services.Settings.Get(ctx)
		if err != nil {
			return email.Account{}, err
		}
		if s != nil {
			settings = s.BotEmail
		}
	}
	if settings == nil {
		return email.Account{}, fmt.Errorf("%w: configure bot_email in the Bot owner's personal or team settings and a same-scope named credential; platform channel mail is never inherited", store.ErrBotUnsupported)
	}
	a := email.Account{Provider: settings.Provider, Address: settings.Address, IMAPServer: settings.IMAPServer, SMTPServer: settings.SMTPServer, Credential: settings.Credential, Folder: settings.Folder}
	if err := a.Validate(); err != nil {
		return email.Account{}, fmt.Errorf("%w: %s", store.ErrBotUnsupported, err)
	}
	return a, nil
}

// callScopedEmail is the account transport shared by interactive and autonomous
// Chat. The caller supplies its authorization guard; transport cannot grant it.
func callScopedEmail(ctx context.Context, client sandbox.ToolNodeClient, sessionID string, account email.Account, name string, args map[string]any, snapshot *email.ReplySnapshot, check func() error, redactor *credentials.Redactor) (map[string]any, error) {
	if client == nil || sessionID == "" || check == nil {
		return nil, store.ErrBotForbidden
	}
	raw, err := json.Marshal(email.NodeRequest{Account: account, Tool: name, Arguments: args, ReplySnapshot: snapshot})
	if err != nil {
		return nil, err
	}
	var values map[string]any
	if err = json.Unmarshal(raw, &values); err != nil {
		return nil, err
	}
	if err = check(); err != nil {
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	raw, err = client.Call(sessionID, email.NodeOperation, values)
	if err != nil {
		return nil, fmt.Errorf("email sandbox: %s", redactor.Redact(err.Error()))
	}
	var result map[string]any
	if err = json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("invalid email node response")
	}
	return redactor.RedactMap(result), nil
}

// ScopedChatEmailOptions binds interactive Chat email to one principal, mailbox
// scope and sandbox session. It does not confer autonomous Bot authorization.
type ScopedChatEmailOptions struct {
	Inputs    *chatruntime.Inputs
	Scope     chatruntime.ResourceScope
	Backend   sandbox.Backend
	Client    sandbox.ToolNodeClient
	SessionID string
}

// ScopedChatEmailTools exposes all eight real Chat schemas without reading a
// credential or contacting a mailbox during catalog construction.
func ScopedChatEmailTools(o ScopedChatEmailOptions) ([]tool.Tool, error) {
	if o.Inputs == nil || o.Backend == nil || o.Client == nil || o.SessionID == "" {
		return nil, store.ErrBotUnsupported
	}
	if o.Scope != chatruntime.ResourceScopePersonal && o.Scope != chatruntime.ResourceScopeTeam {
		return nil, store.ErrBotInvalid
	}
	schemas, err := tools.GetEmailToolsForClient(nil)
	if err != nil {
		return nil, err
	}
	result := make([]tool.Tool, 0, len(schemas))
	for _, schema := range schemas {
		result = append(result, &scopedChatEmailTool{botRunnableTool: schema.(botRunnableTool), options: o})
	}
	return result, nil
}

type scopedChatEmailTool struct {
	botRunnableTool
	options ScopedChatEmailOptions
}

func (t *scopedChatEmailTool) Run(ctx tool.Context, args any) (map[string]any, error) {
	if ctx == nil {
		return nil, store.ErrBotForbidden
	}
	// Autonomous calls must retain the exact-policy BotEmailTools adapter.
	if _, ok := execution.BotRunIdentityFromContext(ctx); ok {
		authorizer, trusted := execution.BotTurnAuthorizerFromContext(ctx)
		if !trusted {
			return nil, store.ErrBotForbidden
		}
		if err := authorizer.AuthorizeTool(ctx, t.Name()); err != nil {
			return nil, err
		}
	}
	bound, err := t.options.Inputs.Bind(ctx)
	if err != nil {
		return nil, err
	}
	services := store.FromContext(bound)
	personal := t.options.Scope == chatruntime.ResourceScopePersonal
	account, err := scopedEmailAccount(bound, services, personal)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(args)
	if err != nil {
		return nil, err
	}
	var values map[string]any
	if err = json.Unmarshal(raw, &values); err != nil {
		return nil, err
	}
	if err = common.ValidateToolArguments(t, values); err != nil {
		return nil, err
	}
	cs := services.Credentials
	if personal {
		cs = services.PersonalCredentials
	}
	if cs == nil {
		return nil, store.ErrBotForbidden
	}
	redactor := credentials.NewRedactor()
	redactor.HydrateFromStore(cs)
	if err = sandbox.SyncSessionCredentialVault(store.WithCredentialStore(bound, cs), t.options.Backend, nil, t.options.SessionID); err != nil {
		return nil, fmt.Errorf("email vault: %s", redactor.Redact(err.Error()))
	}
	result, err := callScopedEmail(bound, t.options.Client, t.options.SessionID, account, tools.ScopedChatEmailPrefix+t.Name(), values, nil, func() error {
		current, e := scopedEmailAccount(bound, services, personal)
		if e != nil {
			return e
		}
		if current.Fingerprint() != account.Fingerprint() {
			return store.ErrBotConflict
		}
		return nil
	}, redactor)
	if result != nil {
		delete(result, "_reply_source")
	}
	return result, err
}
