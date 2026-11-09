package tools

import (
	"fmt"
	"github.com/SAP/astonish/pkg/email"
	"google.golang.org/adk/model"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
	"google.golang.org/genai"
)

// emailClient is the shared email client used by all email tools.
// Set via SetEmailClient before tools are used.
var emailClient email.Client

// SetEmailClient configures the email client for all email tools.
func SetEmailClient(c email.Client) {
	emailClient = c
}

// HasEmailClient returns true if an email client has been set.
func HasEmailClient() bool {
	return emailClient != nil
}

// GetEmailTools creates all email tools sharing a single email client.
// Returns nil tools if no email client is configured.
func GetEmailTools() ([]tool.Tool, error) {
	if emailClient == nil {
		return nil, nil
	}

	return GetEmailToolsForClient(emailClient)
}

// GetEmailToolsForClient uses an explicit request-local client, never global state.
// A nil client is for declarations only; those handlers must not be invoked.
func GetEmailToolsForClient(client email.Client) ([]tool.Tool, error) {
	// --- Read tools ---

	listTool, err := functiontool.New(functiontool.Config{
		Name:        "email_list",
		Description: "List emails in the inbox. Supports filtering by unread status, sender, subject, and date. Returns message summaries (not full bodies).",
	}, EmailList(client))
	if err != nil {
		return nil, err
	}

	readTool, err := functiontool.New(functiontool.Config{
		Name:        "email_read",
		Description: "Read the full content of a specific email by ID. Returns body, headers, links, and extracted verification links (useful for portal registration flows).",
	}, EmailRead(client))
	if err != nil {
		return nil, err
	}

	searchTool, err := functiontool.New(functiontool.Config{
		Name:        "email_search",
		Description: "Search emails by free-text query, sender, recipient, subject, date range, and attachment presence.",
	}, EmailSearch(client))
	if err != nil {
		return nil, err
	}

	// --- Send tools ---

	sendTool, err := functiontool.New(functiontool.Config{
		Name:        "email_send",
		Description: "Compose and send a new email. Supports plain text and optional HTML body.",
	}, EmailSend(client))
	if err != nil {
		return nil, err
	}

	replyTool, err := functiontool.New(functiontool.Config{
		Name:        "email_reply",
		Description: "Reply to an existing email, preserving threading (In-Reply-To and References headers). Can reply to sender only or reply-all.",
	}, EmailReply(client))
	if err != nil {
		return nil, err
	}

	// --- Manage tools ---

	markReadTool, err := functiontool.New(functiontool.Config{
		Name:        "email_mark_read",
		Description: "Mark emails as read or unread.",
	}, EmailMarkRead(client))
	if err != nil {
		return nil, err
	}

	deleteTool, err := functiontool.New(functiontool.Config{
		Name:        "email_delete",
		Description: "Delete emails (move to trash, or permanently delete).",
	}, EmailDelete(client))
	if err != nil {
		return nil, err
	}

	// --- Wait tool (for autonomous flows) ---

	waitTool, err := functiontool.New(functiontool.Config{
		Name:        "email_wait",
		Description: "Wait for a matching email to arrive (polls inbox). Use for registration flows where you need to receive a verification or confirmation email. Supports substring matching on sender and subject. Returns the full message content with extracted verification links when found.",
	}, EmailWait(client))
	if err != nil {
		return nil, err
	}

	registered := []tool.Tool{
		listTool,
		readTool,
		searchTool,
		sendTool,
		replyTool,
		markReadTool,
		deleteTool,
		waitTool,
	}
	if client == nil {
		for i, t := range registered {
			registered[i] = &unconfiguredEmailTool{Tool: t}
		}
	}
	return registered, nil
}

// A declaration-only catalog fails explicitly if invoked without a transport;
// it must never panic or inherit the daemon's global email client.
type unconfiguredEmailTool struct{ tool.Tool }

func (t *unconfiguredEmailTool) Declaration() *genai.FunctionDeclaration {
	return t.Tool.(interface {
		Declaration() *genai.FunctionDeclaration
	}).Declaration()
}
func (t *unconfiguredEmailTool) ProcessRequest(ctx tool.Context, req *model.LLMRequest) error {
	return t.Tool.(interface {
		ProcessRequest(tool.Context, *model.LLMRequest) error
	}).ProcessRequest(ctx, req)
}
func (*unconfiguredEmailTool) Run(tool.Context, any) (map[string]any, error) {
	return nil, fmt.Errorf("scoped email transport required")
}
