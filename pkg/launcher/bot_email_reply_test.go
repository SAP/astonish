package launcher

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/mail"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/email"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/sandbox/sessioncreds"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/tools"
	"github.com/google/uuid"
	"google.golang.org/adk/tool"
)

func TestBotEmailReplyGraphSnapshot(t *testing.T) {
	tools.SetAllowPrivateNetworks(true)
	defer tools.SetAllowPrivateNetworks(false)
	for _, mode := range []string{"once", "provider-error", "resume", "resume-tampered", "recipient", "cc", "source-body", "source-tail", "source-subject", "source-thread", "attachment", "body", "html", "reply-all", "id", "account", "child", "revoked-during-vault", "snapshot-recipient"} {
		t.Run(mode, func(t *testing.T) {
			var changed atomic.Bool
			var sends atomic.Int32
			wire := make(chan string, 1)
			addr := func(s string) any { return map[string]any{"emailAddress": map[string]any{"address": s}} }
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer "+productionTestSecret {
					t.Error("wrong credential")
				}
				if r.Method == http.MethodPost && r.URL.Path == "/v1.0/me/sendMail" {
					sends.Add(1)
					raw, _ := io.ReadAll(r.Body)
					decoded, err := base64.StdEncoding.DecodeString(string(raw))
					if err != nil {
						t.Error(err)
					}
					select {
					case wire <- string(decoded):
					default:
						t.Error("duplicate provider send")
					}
					if mode == "provider-error" {
						// The provider received the message, but its response cannot
						// establish delivery. The consumed grant must not retry it.
						http.Error(w, "ambiguous provider result", http.StatusGatewayTimeout)
						return
					}
					w.WriteHeader(http.StatusAccepted)
					return
				}
				if r.Method != http.MethodGet || r.URL.Path != "/v1.0/me/messages/42" {
					t.Errorf("unexpected provider action: %s %s", r.Method, r.URL.Path)
					w.WriteHeader(400)
					return
				}
				recipient, cc, subject, thread := "recipient@example.com", "copy@example.com", "Original subject", "<original@example.com>"
				body := strings.Repeat("x", 60000) + "original tail"
				attachment := "report.pdf"
				if changed.Load() {
					switch mode {
					case "recipient":
						recipient = "attacker@example.com"
					case "cc":
						cc = "attacker@example.com"
					case "source-body":
						body = "changed"
					case "source-tail":
						body = strings.Repeat("x", 60000) + "changed tail"
					case "source-subject":
						subject = "changed"
					case "source-thread":
						thread = "<changed@example.com>"
					case "attachment":
						attachment = "changed.pdf"
					}
				}
				json.NewEncoder(w).Encode(map[string]any{
					"id": "42", "subject": subject, "receivedDateTime": "2026-01-01T00:00:00Z", "isRead": changed.Load(),
					"from": addr("sender@example.com"), "toRecipients": []any{addr("owner@example.com"), addr(cc)}, "ccRecipients": []any{addr(cc)},
					"body": map[string]any{"contentType": "text", "content": body}, "internetMessageId": thread,
					"internetMessageHeaders": []any{map[string]string{"name": "Reply-To", "value": recipient}, map[string]string{"name": "References", "value": "<ancestor@example.com>"}},
					"attachments":            []any{map[string]any{"id": "a", "name": attachment, "size": 10, "contentType": "application/pdf"}},
				})
			}))
			defer srv.Close()
			endpoint, _ := url.Parse(srv.URL)
			originalTransport := http.DefaultTransport
			http.DefaultTransport = emailGraphFixtureTransport{endpoint: endpoint, base: originalTransport}
			defer func() { http.DefaultTransport = originalTransport }()
			f := newBotFactoryFixture(t)
			settings := &store.BotEmailSettings{Provider: "msgraph", Address: "owner@example.com", Credential: "mail"}
			f.services.PersonalSettings = &emailPersonalSettings{settings: settings}
			cs := &emailCountingCredentials{CredentialStore: sessioncreds.NewStore(map[string]*store.Credential{"mail": {Type: store.CredBearer, Token: productionTestSecret}})}
			f.services.PersonalCredentials = cs
			b := &productionCapabilityBackend{}
			c := &productionCapabilityClient{backend: b}
			var catalogue []tool.Tool
			target, ctx := productionFactoryTool(t, f, "email_reply", func(context.Context, string) ([]tool.Tool, func(), error) {
				var err error
				catalogue, err = BotEmailTools(f.req, &BotChatScope{Services: f.services}, b, c)
				return catalogue, func() {}, err
			}, "email_read")
			bound := target.(*botBoundTool)
			var read *botBoundTool
			for _, candidate := range catalogue {
				if candidate.Name() == "email_read" {
					read = &botBoundTool{botRunnableTool: candidate.(botRunnableTool), binding: bound.binding}
				}
			}
			readArgs := map[string]any{"id": "42"}
			readCtx := approveMCPTestTool(t, read, ctx, readArgs)
			out, err := read.Run(readCtx, readArgs)
			if err != nil {
				t.Fatal(err)
			}
			if _, leaked := out["_reply_source"]; leaked {
				t.Fatal("internal evidence leaked")
			}
			if len(out["body"].(string)) != email.DefaultConfig().MaxBodyChars {
				t.Fatal("display limit changed")
			}
			args := map[string]any{"id": "42", "body": "Reviewed reply", "html": "<b>Reviewed reply</b>", "reply_all": true}
			ctx = approveMCPTestTool(t, target, ctx, args)
			before := c.calls
			cs.reads = 0
			changed.Store(true)
			hostDenial := false
			if mode == "resume" || mode == "resume-tampered" {
				raw, _ := json.Marshal(args)
				runtime := agent.BotPolicyRuntimeFromContext(bound.binding.owner)
				action, err := runtime.Classifier.DescribeAction(bound.binding.owner, "email_reply", raw)
				if err != nil {
					t.Fatal(err)
				}
				digest, _ := action.Digest()
				row := store.BotApproval{ID: uuid.New(), BotID: f.req.Bot.ID, RunID: f.req.Run.ID, Version: f.req.Run.Version, Action: action, ActionDigest: digest}
				e := bound.botRunnableTool.(*botEmailTool)
				e.request.Run.Checkpoint, _ = json.Marshal(bot.ApprovalCheckpoint{Format: bot.ApprovalCheckpointFormat, ApprovalID: row.ID, BotID: row.BotID, RunID: row.RunID, SessionID: f.req.Run.SessionID, Version: row.Version, ActionDigest: digest, Fence: 1})
				e.sources.values = map[string]email.ReplySource{}
				if mode == "resume-tampered" {
					row.Action.TargetSnapshot = json.RawMessage(`{}`)
					hostDenial = true
				}
				bound.binding.owner = bot.WithApprovalStore(bound.binding.owner, emailReplyApprovalStore{row: row})
			}
			switch mode {
			case "body":
				args["body"] = "changed"
				hostDenial = true
			case "html":
				args["html"] = "changed"
				hostDenial = true
			case "reply-all":
				args["reply_all"] = false
				hostDenial = true
			case "id":
				args["id"] = "43"
				hostDenial = true
			case "account":
				settings.Address = "changed@example.com"
				hostDenial = true
			case "child":
				p, _ := execution.RunPolicyFromContext(ctx).Narrow([]string{"email_reply"})
				ctx = botFactoryHandoffContext{base: execution.WithRunPolicy(ctx, p), session: "child"}
				hostDenial = true
			case "snapshot-recipient":
				e := bound.botRunnableTool.(*botEmailTool)
				e.sources.mu.Lock()
				for key, source := range e.sources.values {
					source.Headers["Reply-To"] = "attacker@example.com"
					e.sources.values[key] = source
				}
				e.sources.mu.Unlock()
				hostDenial = true
			}
			if mode == "revoked-during-vault" {
				blocking := &emailBlockingPush{productionCapabilityBackend: b, entered: make(chan struct{}), release: make(chan struct{})}
				bound.botRunnableTool.(*botEmailTool).backend = blocking
				done := make(chan error, 1)
				go func() { _, e := target.Run(ctx, args); done <- e }()
				select {
				case <-blocking.entered:
				case <-time.After(5 * time.Second):
					t.Fatal("no vault push")
				}
				f.req.Bot.Status = store.BotPaused
				close(blocking.release)
				select {
				case err = <-done:
				case <-time.After(5 * time.Second):
					t.Fatal("reply dispatch stuck after vault release")
				}
			} else {
				out, err = target.Run(ctx, args)
			}
			if mode == "once" || mode == "resume" {
				if err != nil || out["success"] != true || sends.Load() != 1 {
					t.Fatalf("reply: %v %v sends=%d", out, err, sends.Load())
				}
				assertReplyWire(t, <-wire, true)
			} else if mode == "provider-error" {
				if err == nil || sends.Load() != 1 {
					t.Fatalf("ambiguous response: %v sends=%d", err, sends.Load())
				}
				assertReplyWire(t, <-wire, true)
			} else {
				if err == nil || sends.Load() != 0 {
					t.Fatalf("changed target sent: %v sends=%d", err, sends.Load())
				}
				if hostDenial && (c.calls != before || cs.reads != 0) {
					t.Fatalf("host denial had effects: calls=%d reads=%d", c.calls, cs.reads)
				}
				if mode == "revoked-during-vault" && c.calls != before {
					t.Fatal("revoked reply reached node")
				}
			}
			calls := c.calls
			if _, err = target.Run(ctx, args); err == nil || c.calls != calls {
				t.Fatal("approval replay reached node")
			}
		})
	}
}

type emailReplyApprovalStore struct {
	store.BotStore
	row store.BotApproval
}

func (s emailReplyApprovalStore) ListApprovals(context.Context, store.BotID, store.BotPage) ([]store.BotApproval, error) {
	return []store.BotApproval{s.row}, nil
}

func assertReplyWire(t *testing.T, raw string, all bool) {
	t.Helper()
	msg, err := mail.ReadMessage(strings.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{"To": "recipient@example.com", "Subject": "Re: Original subject", "In-Reply-To": "<original@example.com>", "References": "<ancestor@example.com> <original@example.com>"} {
		if msg.Header.Get(key) != want {
			t.Errorf("%s=%q want %q", key, msg.Header.Get(key), want)
		}
	}
	if all && msg.Header.Get("Cc") != "copy@example.com" {
		t.Errorf("CC=%q", msg.Header.Get("Cc"))
	}
	if !all && msg.Header.Get("Cc") != "" {
		t.Error("unexpected CC")
	}
	if strings.Contains(raw, "report.pdf") || !strings.Contains(raw, "Reviewed reply") {
		t.Fatal("wrong content or attachments")
	}
}

func TestBotEmailReplySMTPSnapshot(t *testing.T) {
	endpoint, messages := emailSMTPFixture(t, "owner", productionTestSecret)
	account := email.Account{Provider: "imap", Address: "owner@example.com", IMAPServer: "localhost:993", SMTPServer: endpoint, Credential: "mail"}
	source, err := email.NewReplySource(&email.Message{ID: "42", From: "recipient@example.com", Subject: "Original subject", Headers: map[string]string{"Message-ID": "<original@example.com>", "References": "<ancestor@example.com>"}})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := email.NewReplySnapshot(account, source, "Reviewed reply", "", false)
	if err != nil {
		t.Fatal(err)
	}
	cfg := email.DefaultConfig()
	cfg.Provider = account.Provider
	cfg.Address = account.Address
	cfg.SMTPServer = endpoint
	cfg.Username = "owner"
	cfg.Password = productionTestSecret
	if _, err = email.SendReplySnapshot(context.Background(), cfg, snapshot); err != nil {
		t.Fatal(err)
	}
	select {
	case msg := <-messages:
		assertReplyWire(t, msg, false)
	case <-time.After(time.Second):
		t.Fatal("no SMTP reply")
	}
}
