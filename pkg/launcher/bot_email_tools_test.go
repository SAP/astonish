package launcher

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/chatruntime"
	"github.com/SAP/astonish/pkg/email"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/sandbox/sessioncreds"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/tools"
	"github.com/google/uuid"
	"google.golang.org/adk/tool"
)

type emailPersonalSettings struct {
	store.PersonalSettingsStore
	settings *store.BotEmailSettings
}

func (s *emailPersonalSettings) Get(context.Context) (*store.PersonalSettings, error) {
	return &store.PersonalSettings{BotEmail: s.settings}, nil
}

type emailTeamSettings struct {
	store.SettingsStore
	settings *store.BotEmailSettings
}

func (s *emailTeamSettings) Get(context.Context) (*store.TeamSettings, error) {
	return &store.TeamSettings{BotEmail: s.settings}, nil
}

type emailCountingCredentials struct {
	store.CredentialStore
	reads int
}

func (s *emailCountingCredentials) Get(ctx context.Context, name string) *store.Credential {
	s.reads++
	return s.CredentialStore.Get(ctx, name)
}
func (s *emailCountingCredentials) List(ctx context.Context) map[string]store.CredentialType {
	s.reads++
	return s.CredentialStore.List(ctx)
}
func emailTestSettings() *store.BotEmailSettings {
	return &store.BotEmailSettings{Provider: "imap", Address: "owner@example.com", IMAPServer: "localhost:993", SMTPServer: "localhost:587", Credential: "mail", Folder: "INBOX"}
}
func emailTestTarget(t *testing.T, f *botFactoryFixture, name string, b *productionCapabilityBackend, c *productionCapabilityClient) (botRunnableTool, tool.Context) {
	return productionFactoryTool(t, f, name, func(context.Context, string) ([]tool.Tool, func(), error) {
		ts, err := BotEmailTools(f.req, &BotChatScope{Services: f.services}, b, c)
		return ts, func() {}, err
	})
}
func emailSendArguments() map[string]any {
	return map[string]any{"to": []string{"recipient@example.com"}, "subject": "Reviewed subject", "body": "Reviewed body"}
}

func TestScopedChatEmailSchemasNoEagerAccess(t *testing.T) {
	f := newBotFactoryFixture(t)
	inputs, err := chatruntime.Build(context.Background(), chatruntime.BuildOptions{Principal: f.req.Principal, Services: f.services, ResourceScope: chatruntime.ResourceScopeTeam})
	if err != nil {
		t.Fatal(err)
	}
	cs := &emailCountingCredentials{CredentialStore: sessioncreds.NewStore(nil)}
	f.services.Credentials = cs
	b := &productionCapabilityBackend{}
	c := &productionCapabilityClient{backend: b}
	ts, err := ScopedChatEmailTools(ScopedChatEmailOptions{Inputs: inputs, Scope: chatruntime.ResourceScopeTeam, Backend: b, Client: c, SessionID: f.req.Run.SessionID.String()})
	if err != nil {
		t.Fatal(err)
	}
	schemas, err := tools.GetEmailToolsForClient(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(ts) != 8 {
		t.Fatalf("got %d tools", len(ts))
	}
	for i, x := range ts {
		if !reflect.DeepEqual(x.(botRunnableTool).Declaration(), schemas[i].(botRunnableTool).Declaration()) {
			t.Fatal("schema changed")
		}
	}
	if cs.reads != 0 || c.calls != 0 || len(b.vault) != 0 {
		t.Fatal("eager transport access")
	}
}

func TestBotEmailSchemasAndNoEagerCredentials(t *testing.T) {
	f := newBotFactoryFixture(t)
	cs := &emailCountingCredentials{CredentialStore: sessioncreds.NewStore(nil)}
	f.services.PersonalCredentials = cs
	b := &productionCapabilityBackend{}
	c := &productionCapabilityClient{backend: b}
	ts, err := BotEmailTools(f.req, &BotChatScope{Services: f.services}, b, c)
	if err != nil {
		t.Fatal(err)
	}
	original, err := tools.GetEmailToolsForClient(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(ts) != 8 {
		t.Fatalf("got %d email tools", len(ts))
	}
	for i, target := range ts {
		if !reflect.DeepEqual(target.(botRunnableTool).Declaration(), original[i].(botRunnableTool).Declaration()) {
			t.Fatalf("schema changed: %s", target.Name())
		}
	}
	if cs.reads != 0 || c.calls != 0 || len(b.vault) != 0 {
		t.Fatal("eager credential or node access")
	}
}

func TestBotEmailDeniedBeforeCredentials(t *testing.T) {
	for _, mode := range []string{"review-required", "no-permission", "revoked", "budget", "target-changed", "recipient-changed", "cc-changed", "body-changed", "html-changed", "subject-changed", "direct", "foreign-owner", "foreign-run", "foreign-team"} {
		t.Run(mode, func(t *testing.T) {
			f := newBotFactoryFixture(t)
			settings := emailTestSettings()
			f.services.PersonalSettings = &emailPersonalSettings{settings: settings}
			cs := &emailCountingCredentials{CredentialStore: sessioncreds.NewStore(map[string]*store.Credential{"mail": {Type: store.CredPassword, Password: productionTestSecret}})}
			f.services.PersonalCredentials = cs
			b := &productionCapabilityBackend{}
			c := &productionCapabilityClient{backend: b}
			target, ctx := emailTestTarget(t, f, "email_send", b, c)
			args := emailSendArguments()
			if mode != "review-required" && mode != "no-permission" {
				ctx = approveMCPTestTool(t, target, ctx, args)
			}
			switch mode {
			case "no-permission":
				p, _ := execution.NewRunPolicy(execution.RunPolicySpec{Identity: f.req.Policy.Identity(), Level: execution.AutonomyScoped, AllowedTools: []string{"read_file"}})
				ctx = botFactoryHandoffContext{base: execution.WithRunPolicy(ctx, p), session: ctx.SessionID()}
			case "revoked":
				f.req.Bot.Status = store.BotPaused
			case "budget":
				target.(*botBoundTool).binding.req.Budget = &mcpDeniedBudget{}
			case "target-changed":
				settings.SMTPServer = "replacement.invalid:587"
			case "recipient-changed":
				args["to"] = []string{"other@example.com"}
			case "cc-changed":
				args["cc"] = []string{"other@example.com"}
			case "body-changed":
				args["body"] = "replacement"
			case "html-changed":
				args["html"] = "<b>replacement</b>"
			case "subject-changed":
				args["subject"] = "replacement"
			}
			cs.reads = 0
			var err error
			if strings.HasPrefix(mode, "foreign-") {
				bound := target.(*botBoundTool)
				direct := bound.botRunnableTool.(*botEmailTool)
				owner := bound.binding.owner
				switch mode {
				case "foreign-owner":
					p := f.req.Principal
					p.Subject = uuid.NewString()
					owner, _ = execution.WithPrincipal(owner, p)
				case "foreign-run":
					id := f.req.Policy.Identity()
					id.RunID = uuid.New()
					owner = execution.WithBotRunIdentity(owner, id)
				case "foreign-team":
					p := f.req.Principal
					p.TeamSlug = "other-team"
					owner, _ = execution.WithPrincipal(owner, p)
				}
				_, err = direct.account(owner)
			} else if mode == "direct" {
				_, err = target.(*botBoundTool).botRunnableTool.Run(ctx, args)
			} else {
				_, err = target.Run(ctx, args)
			}
			if err == nil || cs.reads != 0 || c.calls != 0 || len(b.vault) != 0 {
				t.Fatalf("denial had effects: err=%v reads=%d calls=%d", err, cs.reads, c.calls)
			}
		})
	}
}

func TestBotEmailMissingAccountNeverInherits(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			f := newBotFactoryFixture(t)
			f.req.Bot.Scope = scope
			// Populate only the opposite scope: it must never be used.
			if scope == store.BotScopePersonal {
				f.services.Settings = &emailTeamSettings{settings: emailTestSettings()}
			} else {
				f.services.PersonalSettings = &emailPersonalSettings{settings: emailTestSettings()}
			}
			b := &productionCapabilityBackend{}
			c := &productionCapabilityClient{backend: b}
			target, ctx := emailTestTarget(t, f, "email_list", b, c)
			_, err := target.Run(ctx, map[string]any{})
			if !errors.Is(err, store.ErrBotUnsupported) || !strings.Contains(err.Error(), "never inherited") || c.calls != 0 || len(b.vault) != 0 {
				t.Fatalf("missing scope fallback: %v", err)
			}
		})
	}
}

// SMTP is a controlled local protocol server, not a replacement email.Client.
// The production node dispatcher, registry, EmailSend and SendSMTP all execute.
func emailSMTPFixture(t *testing.T, username, password string) (string, <-chan string) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	messages := make(chan string, 4)
	go func() {
		for {
			conn, err := l.Accept()
			if err != nil {
				return
			}
			func() {
				defer conn.Close()
				conn.SetDeadline(time.Now().Add(10 * time.Second))
				r := bufio.NewReader(conn)
				fmt.Fprint(conn, "220 localhost ESMTP\r\n")
				for {
					line, err := r.ReadString('\n')
					if err != nil {
						return
					}
					line = strings.TrimSpace(line)
					switch {
					case strings.HasPrefix(line, "EHLO"):
						fmt.Fprint(conn, "250-localhost\r\n250 AUTH PLAIN\r\n")
					case strings.HasPrefix(line, "AUTH PLAIN "):
						raw, e := base64.StdEncoding.DecodeString(strings.TrimPrefix(line, "AUTH PLAIN "))
						if e != nil || string(raw) != "\x00"+username+"\x00"+password {
							t.Error("SMTP received wrong scoped credential")
							fmt.Fprint(conn, "535 wrong identity\r\n")
							return
						}
						fmt.Fprint(conn, "235 authenticated\r\n")
					case strings.HasPrefix(line, "MAIL FROM:"):
						if line != "MAIL FROM:<owner@example.com>" {
							t.Errorf("wrong sender: %s", line)
						}
						fmt.Fprint(conn, "250 OK\r\n")
					case strings.HasPrefix(line, "RCPT TO:"):
						if line != "RCPT TO:<recipient@example.com>" {
							t.Errorf("wrong recipient: %s", line)
						}
						fmt.Fprint(conn, "250 OK\r\n")
					case line == "DATA":
						fmt.Fprint(conn, "354 continue\r\n")
						var body strings.Builder
						for {
							s, e := r.ReadString('\n')
							if e != nil {
								return
							}
							if s == ".\r\n" {
								break
							}
							body.WriteString(s)
						}
						messages <- body.String()
						fmt.Fprint(conn, "250 queued\r\n")
					case line == "QUIT":
						fmt.Fprint(conn, "221 bye\r\n")
						return
					default:
						fmt.Fprint(conn, "500 unsupported\r\n")
					}
				}
			}()
		}
	}()
	return l.Addr().String(), messages
}

func TestBotEmailSMTPScopedSend(t *testing.T) {
	tools.SetAllowPrivateNetworks(true)
	defer tools.SetAllowPrivateNetworks(false)
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			endpoint, messages := emailSMTPFixture(t, "owner", productionTestSecret)
			f := newBotFactoryFixture(t)
			f.req.Bot.Scope = scope
			settings := emailTestSettings()
			settings.SMTPServer = endpoint
			own := sessioncreds.NewStore(map[string]*store.Credential{"mail": {Type: store.CredPassword, Username: "owner", Password: productionTestSecret}})
			other := sessioncreds.NewStore(map[string]*store.Credential{"mail": {Type: store.CredPassword, Username: "viewer", Password: "wrong-scope-secret"}})
			if scope == store.BotScopePersonal {
				f.services.PersonalSettings = &emailPersonalSettings{settings: settings}
				f.services.PersonalCredentials = own
				f.services.Credentials = other
			} else {
				f.services.Settings = &emailTeamSettings{settings: settings}
				f.services.Credentials = own
				f.services.PersonalCredentials = other
			}
			b := &productionCapabilityBackend{}
			c := &productionCapabilityClient{backend: b}
			target, ctx := emailTestTarget(t, f, "email_send", b, c)
			args := emailSendArguments()
			ctx = approveMCPTestTool(t, target, ctx, args)
			out, err := target.Run(ctx, args)
			if err != nil {
				t.Fatal(err)
			}
			if out["success"] != true || c.calls != 1 || b.owner != f.req.Run.SessionID.String() {
				t.Fatalf("send not scoped: %v", out)
			}
			select {
			case msg := <-messages:
				if !strings.Contains(msg, "Reviewed subject") || !strings.Contains(msg, "Reviewed body") {
					t.Fatalf("unexpected message: %s", msg)
				}
			case <-time.After(time.Second):
				t.Fatal("SMTP message not received")
			}
			wire, _ := json.Marshal(c.args)
			if strings.Contains(string(wire), productionTestSecret) || strings.Contains(string(b.vault), "wrong-scope-secret") {
				t.Fatal("secret leaked or other scope exported")
			}
			if _, err = target.Run(ctx, args); err == nil || c.calls != 1 {
				t.Fatal("approval replay dispatched")
			}
		})
	}
}

func TestBotEmailMissingCredentialAndReplyFailClosed(t *testing.T) {
	tools.SetAllowPrivateNetworks(true)
	defer tools.SetAllowPrivateNetworks(false)
	for _, name := range []string{"email_send", "email_reply"} {
		t.Run(name, func(t *testing.T) {
			f := newBotFactoryFixture(t)
			f.services.PersonalSettings = &emailPersonalSettings{settings: emailTestSettings()}
			f.services.PersonalCredentials = sessioncreds.NewStore(nil)
			// Same name in team scope must not satisfy a personal mailbox prerequisite.
			f.services.Credentials = sessioncreds.NewStore(map[string]*store.Credential{"mail": {Type: store.CredPassword, Password: productionTestSecret}})
			b := &productionCapabilityBackend{}
			c := &productionCapabilityClient{backend: b}
			target, ctx := emailTestTarget(t, f, name, b, c)
			args := emailSendArguments()
			if name == "email_reply" {
				args = map[string]any{"id": "42", "body": "reply"}
			}
			if name == "email_send" {
				ctx = approveMCPTestTool(t, target, ctx, args)
			}
			_, err := target.Run(ctx, args)
			if err == nil {
				t.Fatal("missing prerequisite succeeded")
			}
			if name == "email_reply" {
				if !errors.Is(err, store.ErrBotForbidden) || c.calls != 0 || len(b.vault) != 0 {
					t.Fatalf("unsafe reply: %v", err)
				}
			} else if !strings.Contains(err.Error(), "credential not found") {
				t.Fatalf("wrong credential failure: %v", err)
			}
		})
	}
}

// emailGraphFixtureTransport redirects only the fixed Graph endpoint to a local
// controlled service. No production account endpoint or client is substituted.
type emailGraphFixtureTransport struct {
	endpoint *url.URL
	base     http.RoundTripper
}

func (x emailGraphFixtureTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Host != "graph.microsoft.com" {
		return nil, fmt.Errorf("unexpected network target")
	}
	copy := r.Clone(r.Context())
	u := *r.URL
	u.Scheme = x.endpoint.Scheme
	u.Host = x.endpoint.Host
	copy.URL = &u
	return x.base.RoundTrip(copy)
}

func TestBotEmailGraphReadAndManage(t *testing.T) {
	tools.SetAllowPrivateNetworks(true)
	defer tools.SetAllowPrivateNetworks(false)
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Header.Get("Authorization") != "Bearer "+productionTestSecret {
			t.Error("wrong Graph credential")
		}
		w.Header().Set("Content-Type", "application/json")
		if r.Method != "GET" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		message := map[string]any{"id": "42", "subject": "Reviewed subject", "receivedDateTime": "2026-01-01T00:00:00Z", "isRead": false, "hasAttachments": true, "from": map[string]any{"emailAddress": map[string]any{"address": "sender@example.com"}}, "toRecipients": []any{map[string]any{"emailAddress": map[string]any{"address": "owner@example.com"}}}, "body": map[string]any{"contentType": "text", "content": "secret: " + productionTestSecret}, "attachments": []any{map[string]any{"id": "a", "name": "report.pdf", "size": 100, "contentType": "application/pdf"}}}
		if strings.Contains(r.URL.Path, "/messages/42") {
			json.NewEncoder(w).Encode(message)
		} else {
			json.NewEncoder(w).Encode(map[string]any{"value": []any{message}})
		}
	}))
	defer srv.Close()
	endpoint, _ := url.Parse(srv.URL)
	original := http.DefaultTransport
	http.DefaultTransport = emailGraphFixtureTransport{endpoint: endpoint, base: original}
	defer func() { http.DefaultTransport = original }()
	for _, name := range []string{"email_list", "email_read", "email_search", "email_wait", "email_mark_read", "email_delete"} {
		t.Run(name, func(t *testing.T) {
			f := newBotFactoryFixture(t)
			f.services.PersonalSettings = &emailPersonalSettings{settings: &store.BotEmailSettings{Provider: "msgraph", Address: "owner@example.com", Credential: "mail"}}
			cs := &emailCountingCredentials{CredentialStore: sessioncreds.NewStore(map[string]*store.Credential{"mail": {Type: store.CredBearer, Token: productionTestSecret}})}
			f.services.PersonalCredentials = cs
			b := &productionCapabilityBackend{}
			c := &productionCapabilityClient{backend: b}
			target, ctx := emailTestTarget(t, f, name, b, c)
			args := map[string]any{}
			switch name {
			case "email_read":
				args["id"] = "42"
			case "email_wait":
				args["subject"] = "Reviewed"
			case "email_mark_read", "email_delete":
				args["ids"] = []string{"42"}
			}
			before := calls
			cs.reads = 0
			if _, err := target.Run(ctx, args); err == nil || calls != before || cs.reads != 0 {
				t.Fatal("read/manage lacked exact approval")
			}
			ctx = approveMCPTestTool(t, target, ctx, args)
			out, err := target.Run(ctx, args)
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(out)
			if calls <= before || strings.Contains(string(raw), productionTestSecret) {
				t.Fatalf("no Graph execution or secret leaked: %s", raw)
			}
			if name == "email_read" || name == "email_wait" {
				if !strings.Contains(string(raw), "report.pdf") {
					t.Fatalf("attachment metadata missing: %s", raw)
				}
			}
		})
	}
}

func TestBotEmailNodeRejectsDaemonExecution(t *testing.T) {
	args := map[string]any{"account": email.Account{Provider: "imap", Address: "owner@example.com", IMAPServer: "localhost:993", SMTPServer: "localhost:587", Credential: "mail"}, "tool": "email_send", "arguments": emailSendArguments()}
	_, err := tools.ExecuteTool(store.WithCredentialStore(context.Background(), sessioncreds.NewStore(nil)), email.NodeOperation, args, "")
	if err == nil || !strings.Contains(err.Error(), "sandbox node network policy") {
		t.Fatalf("daemon node dispatch accepted: %v", err)
	}
}
