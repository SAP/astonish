package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/mail"
	"strings"
	"time"

	"github.com/SAP/astonish/pkg/common"
	"github.com/SAP/astonish/pkg/credentials"
	"github.com/SAP/astonish/pkg/email"
	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/toolconfirmation"
)

// ScopedChatEmailPrefix is an internal node protocol marker selected only by
// the interactive scoped adapter, never forwarded from model arguments.
const ScopedChatEmailPrefix = "chat:"

// nodeEmail is only reachable through the authenticated node protocol. Reuses
// Chat handlers and pkg/email; each operation owns and closes its own client.
func nodeEmail(ctx context.Context, request email.NodeRequest) (out map[string]any, err error) {
	if ctx == nil || ctx.Err() != nil {
		return nil, fmt.Errorf("email request canceled")
	}
	// IMAP/SMTP use raw sockets, so require the node network-policy mode. Never
	// dial from an ordinary daemon ExecuteTool call or fall back to global config.
	if !httpReqAllowPrivateNetworks {
		return nil, fmt.Errorf("email requires sandbox node network policy")
	}
	cs := store.CredentialStoreFromContext(ctx)
	if cs == nil {
		return nil, fmt.Errorf("scoped email credential vault required")
	}
	if err := request.Account.Validate(); err != nil {
		return nil, err
	}
	interactive := strings.HasPrefix(request.Tool, ScopedChatEmailPrefix)
	if interactive {
		request.Tool = strings.TrimPrefix(request.Tool, ScopedChatEmailPrefix)
	}
	declarations, err := GetEmailToolsForClient(nil)
	if err != nil {
		return nil, err
	}
	var found tool.Tool
	for _, t := range declarations {
		if t.Name() == request.Tool {
			found = t
			break
		}
	}
	if found == nil {
		return nil, fmt.Errorf("unsupported email operation")
	}
	if err := common.ValidateToolArguments(found, request.Arguments); err != nil {
		return nil, err
	}
	if request.Tool == "email_reply" && !interactive && request.ReplySnapshot == nil {
		return nil, fmt.Errorf("email_reply requires a recipient-bound reply snapshot")
	}
	if request.Tool == "email_send" {
		raw, _ := json.Marshal(request.Arguments)
		var a EmailSendArgs
		if err := json.Unmarshal(raw, &a); err != nil {
			return nil, err
		}
		if strings.ContainsAny(a.Subject+a.ReplyTo, "\r\n") {
			return nil, fmt.Errorf("invalid email headers")
		}
		for _, addr := range append(append([]string{}, a.To...), a.CC...) {
			parsed, e := mail.ParseAddress(addr)
			if e != nil || parsed.Address != addr || strings.ContainsAny(addr, "\r\n") {
				return nil, fmt.Errorf("recipients must be plain email addresses")
			}
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 310*time.Second)
	defer cancel()
	redactor := credentials.NewRedactor()
	redactor.HydrateFromStore(cs)
	defer func() {
		if err != nil {
			err = fmt.Errorf("email node: %s", redactor.Redact(err.Error()))
		}
		if out != nil {
			out = redactor.RedactMap(out)
		}
	}()
	a := request.Account
	cfg := email.DefaultConfig()
	cfg.Provider, cfg.Address, cfg.IMAPServer, cfg.SMTPServer, cfg.Folder = a.Provider, a.Address, a.IMAPServer, a.SMTPServer, a.Folder
	// Read operations must not implicitly mark messages read.
	cfg.MarkRead = false
	cred := cs.Get(ctx, a.Credential)
	if cred == nil {
		return nil, fmt.Errorf("scoped mailbox credential not found")
	}
	if a.Provider == "msgraph" {
		cfg.TokenFunc = func() (string, error) {
			key, value, e := cs.Resolve(ctx, a.Credential)
			if e != nil {
				return "", fmt.Errorf("mailbox token resolution failed")
			}
			if key != "Authorization" || !strings.HasPrefix(value, "Bearer ") {
				return "", fmt.Errorf("mailbox requires bearer credential")
			}
			token := strings.TrimPrefix(value, "Bearer ")
			redactor.AddSecret("email-token", token)
			return token, nil
		}
	} else {
		if (cred.Type != store.CredPassword && cred.Type != store.CredBasic) || cred.Password == "" {
			return nil, fmt.Errorf("mailbox requires password credential")
		}
		cfg.Username, cfg.Password = cred.Username, cred.Password
	}
	// Capture the complete parsed content for reply evidence, not the display
	// truncation prefix. The read wrapper restores the normal output limit.
	displayBodyLimit := cfg.MaxBodyChars
	if request.Tool == "email_read" || request.Tool == "email_reply" {
		cfg.MaxBodyChars = int(^uint(0) >> 1)
	}
	client, err := email.NewClient(cfg)
	if err != nil {
		return nil, err
	}
	defer client.Close()
	if request.Tool == "email_reply" && !interactive {
		raw, _ := json.Marshal(request.Arguments)
		var args EmailReplyArgs
		if err := json.Unmarshal(raw, &args); err != nil {
			return nil, err
		}
		if request.ReplySnapshot.Account.Fingerprint() != a.Fingerprint() || request.ReplySnapshot.OriginalID != args.ID {
			return nil, fmt.Errorf("reply snapshot account or original changed")
		}
		original, err := client.ReadMessage(ctx, args.ID)
		if err != nil {
			return nil, err
		}
		source, err := email.NewReplySource(original)
		if err != nil {
			return nil, err
		}
		current, err := email.NewReplySnapshot(a, source, args.Body, args.HTML, args.ReplyAll)
		if err != nil {
			return nil, err
		}
		if current.Fingerprint() != request.ReplySnapshot.Fingerprint() {
			return nil, fmt.Errorf("reply source changed since approval")
		}
		id, err := email.SendReplySnapshot(ctx, cfg, *request.ReplySnapshot)
		if err != nil {
			return nil, err
		}
		return map[string]any{"message_id": id, "success": true}, nil
	}
	reader := &nodeReplySourceClient{Client: client, displayBodyLimit: displayBodyLimit}
	if request.Tool == "email_read" {
		client = reader
	}
	registered, err := GetEmailToolsForClient(client)
	if err != nil {
		return nil, err
	}
	for _, t := range registered {
		if t.Name() == request.Tool {
			result, err := t.(interface {
				Run(tool.Context, any) (map[string]any, error)
			}).Run(nodeEmailContext{ctx: ctx}, request.Arguments)
			if err == nil && reader.source != nil {
				result["_reply_source"] = reader.source
			}
			return result, err
		}
	}
	return nil, fmt.Errorf("unsupported email operation")
}

// Capture evidence from the very same authorized read, not a second mailbox lookup.
type nodeReplySourceClient struct {
	email.Client
	source           *email.ReplySource
	displayBodyLimit int
}

func (c *nodeReplySourceClient) ReadMessage(ctx context.Context, id string) (*email.Message, error) {
	msg, err := c.Client.ReadMessage(ctx, id)
	if err != nil {
		return nil, err
	}
	if source, e := email.NewReplySource(msg); e == nil {
		c.source = &source
	}
	if c.displayBodyLimit > 0 {
		if len(msg.Body) > c.displayBodyLimit {
			msg.Body = msg.Body[:c.displayBodyLimit]
		}
		if len(msg.HTML) > c.displayBodyLimit {
			msg.HTML = msg.HTML[:c.displayBodyLimit]
		}
	}
	return msg, nil
}

type nodeEmailContext struct {
	tool.Context
	ctx context.Context
}

// Exact Bot authorization is consumed on the host before node dispatch. The
// node has no ADK interaction/session; these handlers do not request confirmation.
func (nodeEmailContext) ToolConfirmation() *toolconfirmation.ToolConfirmation { return nil }

func (c nodeEmailContext) Deadline() (time.Time, bool) { return c.ctx.Deadline() }
func (c nodeEmailContext) Done() <-chan struct{}       { return c.ctx.Done() }
func (c nodeEmailContext) Err() error                  { return c.ctx.Err() }
func (c nodeEmailContext) Value(k any) any             { return c.ctx.Value(k) }
