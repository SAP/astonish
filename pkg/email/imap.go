package email

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/mail"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

// IMAPSMTPClient implements the Client interface using IMAP for reading
// and SMTP for sending.
type IMAPSMTPClient struct {
	cfg       *Config
	mu        sync.Mutex
	imapConn  *imapclient.Client
	connected bool
}

// NewIMAPSMTPClient creates a new IMAP/SMTP client with the given config.
func NewIMAPSMTPClient(cfg *Config) *IMAPSMTPClient {
	return &IMAPSMTPClient{cfg: cfg}
}

// Connect establishes the IMAP connection.
func (c *IMAPSMTPClient) Connect(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.connected && c.imapConn != nil {
		return nil
	}

	return c.connectLocked()
}

// connectLocked does the actual connection. Caller must hold c.mu.
func (c *IMAPSMTPClient) connectLocked() error {
	host, _, err := net.SplitHostPort(c.cfg.IMAPServer)
	if err != nil {
		host = c.cfg.IMAPServer
	}

	tlsCfg := &tls.Config{
		ServerName: host,
	}

	opts := &imapclient.Options{
		TLSConfig: tlsCfg,
	}

	client, err := imapclient.DialTLS(c.cfg.IMAPServer, opts)
	if err != nil {
		return fmt.Errorf("failed to connect to IMAP server %s: %w", c.cfg.IMAPServer, err)
	}

	username := c.cfg.Username
	if username == "" {
		username = c.cfg.Address
	}

	if err := client.Login(username, c.cfg.Password).Wait(); err != nil {
		_ = client.Close() // best-effort cleanup
		return fmt.Errorf("IMAP login failed: %w", err)
	}

	c.imapConn = client
	c.connected = true
	return nil
}

// Close shuts down the IMAP connection.
func (c *IMAPSMTPClient) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.imapConn != nil {
		_ = c.imapConn.Logout().Wait()
		c.imapConn.Close()
		c.imapConn = nil
	}
	c.connected = false
	return nil
}

// IsConnected returns whether the client has an active IMAP connection.
func (c *IMAPSMTPClient) IsConnected() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.connected
}

// Address returns the configured email address.
func (c *IMAPSMTPClient) Address() string {
	return c.cfg.Address
}

// ensureConnectedLocked reconnects if needed. Caller must hold c.mu.
func (c *IMAPSMTPClient) ensureConnectedLocked(ctx context.Context) error {
	if c.connected && c.imapConn != nil {
		if err := c.imapConn.Noop().Wait(); err != nil {
			c.connected = false
			c.imapConn = nil
		} else {
			return nil
		}
	}
	return c.connectLocked()
}

// selectFolder selects an IMAP folder (mailbox). Caller must hold c.mu.
func (c *IMAPSMTPClient) selectFolder(folder string) (*imap.SelectData, error) {
	if folder == "" {
		folder = "INBOX"
	}
	data, err := c.imapConn.Select(folder, nil).Wait()
	if err != nil {
		return nil, fmt.Errorf("failed to select folder %q: %w", folder, err)
	}
	return data, nil
}

// resolveFolder returns the effective folder name from opts or config defaults.
func (c *IMAPSMTPClient) resolveFolder(folder string) string {
	if folder != "" {
		return folder
	}
	if c.cfg.Folder != "" {
		return c.cfg.Folder
	}
	return "INBOX"
}

// ListMessages lists emails matching the given options.
func (c *IMAPSMTPClient) ListMessages(ctx context.Context, opts ListOpts) ([]MessageSummary, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := c.ensureConnectedLocked(ctx); err != nil {
		return nil, err
	}

	folder := c.resolveFolder(opts.Folder)
	mboxData, err := c.selectFolder(folder)
	if err != nil {
		return nil, err
	}

	if mboxData.NumMessages == 0 {
		return nil, nil
	}

	limit := opts.Limit
	if limit <= 0 {
		limit = 20
	}

	// Build search criteria
	var criteria imap.SearchCriteria
	if opts.Unread {
		criteria.NotFlag = append(criteria.NotFlag, imap.FlagSeen)
	}
	if !opts.Since.IsZero() {
		criteria.Since = opts.Since
	}

	// Use UID SEARCH
	searchData, err := c.imapConn.UIDSearch(&criteria, nil).Wait()
	if err != nil {
		return nil, fmt.Errorf("search failed: %w", err)
	}

	uids := searchData.AllUIDs()
	if len(uids) == 0 {
		return nil, nil
	}

	// Sort descending (newest first)
	sort.Slice(uids, func(i, j int) bool {
		return uids[i] > uids[j]
	})

	// Apply limit
	if len(uids) > limit {
		uids = uids[:limit]
	}

	// Build UID set
	uidSet := imap.UIDSetNum(uids...)

	// Fetch envelope and flags
	fetchOpts := &imap.FetchOptions{
		Envelope: true,
		Flags:    true,
		UID:      true,
		BodyStructure: &imap.FetchItemBodyStructure{
			Extended: false,
		},
	}

	fetchCmd := c.imapConn.Fetch(uidSet, fetchOpts)
	results, err := fetchCmd.Collect()
	if err != nil {
		return nil, fmt.Errorf("fetch failed: %w", err)
	}

	var messages []MessageSummary
	for _, buf := range results {
		var summary MessageSummary
		summary.ID = strconv.FormatUint(uint64(buf.UID), 10)

		if buf.Envelope != nil {
			env := buf.Envelope
			if len(env.From) > 0 {
				summary.From = formatAddress(env.From[0])
			}
			for _, addr := range env.To {
				summary.To = append(summary.To, formatAddress(addr))
			}
			summary.Subject = env.Subject
			summary.Date = env.Date
			summary.MessageID = env.MessageID
			if len(env.InReplyTo) > 0 {
				summary.InReplyTo = env.InReplyTo[0]
			}
		}

		// Check read/unread status
		summary.Unread = true
		for _, f := range buf.Flags {
			if f == imap.FlagSeen {
				summary.Unread = false
				break
			}
		}

		// Check for attachments from body structure
		if buf.BodyStructure != nil {
			summary.HasAttachments = hasAttachmentInStructure(buf.BodyStructure)
		}

		// Apply client-side filters
		if opts.From != "" && !strings.Contains(strings.ToLower(summary.From), strings.ToLower(opts.From)) {
			continue
		}
		if opts.Subject != "" && !strings.Contains(strings.ToLower(summary.Subject), strings.ToLower(opts.Subject)) {
			continue
		}

		messages = append(messages, summary)
	}

	return messages, nil
}

// ReadMessage reads the full content of a specific email.
func (c *IMAPSMTPClient) ReadMessage(ctx context.Context, id string) (*Message, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := c.ensureConnectedLocked(ctx); err != nil {
		return nil, err
	}

	uid, err := strconv.ParseUint(id, 10, 32)
	if err != nil {
		return nil, fmt.Errorf("invalid message ID %q: %w", id, err)
	}

	folder := c.resolveFolder("")
	if _, err := c.selectFolder(folder); err != nil {
		return nil, err
	}

	uidSet := imap.UIDSetNum(imap.UID(uid))

	// BODY.PEEK preserves unread state for request-scoped read-only clients.
	// DefaultConfig keeps the historical mark-read behavior for channel clients.
	bodySection := &imap.FetchItemBodySection{Peek: !c.cfg.MarkRead}

	fetchOpts := &imap.FetchOptions{
		Envelope:    true,
		Flags:       true,
		UID:         true,
		BodySection: []*imap.FetchItemBodySection{bodySection},
	}

	fetchCmd := c.imapConn.Fetch(uidSet, fetchOpts)
	results, err := fetchCmd.Collect()
	if err != nil {
		return nil, fmt.Errorf("failed to fetch message: %w", err)
	}

	if len(results) == 0 {
		return nil, fmt.Errorf("message %s not found", id)
	}

	buf := results[0]
	result := &Message{
		ID:      id,
		Headers: make(map[string]string),
	}

	// Parse envelope
	if buf.Envelope != nil {
		env := buf.Envelope
		if len(env.From) > 0 {
			result.From = formatAddress(env.From[0])
		}
		for _, addr := range env.To {
			result.To = append(result.To, formatAddress(addr))
		}
		for _, addr := range env.Cc {
			result.CC = append(result.CC, formatAddress(addr))
		}
		result.Subject = env.Subject
		result.Date = env.Date
		result.Headers["Message-ID"] = env.MessageID
		if len(env.InReplyTo) > 0 {
			result.Headers["In-Reply-To"] = env.InReplyTo[0]
		}
	}

	// Check read status
	result.Unread = true
	for _, f := range buf.Flags {
		if f == imap.FlagSeen {
			result.Unread = false
			break
		}
	}

	// Parse MIME body from the body section
	bodyBytes := buf.FindBodySection(bodySection)
	if bodyBytes != nil && len(bodyBytes) > 0 {
		mailMsg, parseErr := mail.ReadMessage(bytes.NewReader(bodyBytes))
		if parseErr == nil {
			// Preserve the headers used for threading and inbound sender authentication.
			// Authentication-Results is added by the receiving mail system and is
			// evaluated by the channel before it authorizes the sender.
			for name, values := range mailMsg.Header {
				for _, value := range values {
					preserveHeader(result, name, value)
				}
			}

			maxChars := c.cfg.MaxBodyChars
			if maxChars <= 0 {
				maxChars = 50000
			}
			parsed, parseBodyErr := ParseMIMEBody(mailMsg, maxChars)
			if parseBodyErr == nil {
				result.Body = parsed.Text
				result.HTML = truncate(parsed.HTML, maxChars)
				result.Attachments = parsed.Attachments
			}
		}
	}

	// Extract links and classify verification links
	result.Links = ExtractLinks(result.Body, result.HTML)
	senderDomain := ExtractSenderDomain(result.From)
	result.VerificationLinks = ClassifyVerificationLinks(result.Links, result.Subject, senderDomain)

	return result, nil
}

// SearchMessages searches emails with the given criteria.
func (c *IMAPSMTPClient) SearchMessages(ctx context.Context, query SearchQuery) ([]MessageSummary, error) {
	opts := ListOpts{
		Folder:  query.Folder,
		From:    query.From,
		Subject: query.Subject,
		Since:   query.Since,
		Limit:   query.Limit,
	}

	messages, err := c.ListMessages(ctx, opts)
	if err != nil {
		return nil, err
	}

	// Apply additional client-side filters
	if query.Query != "" {
		queryLower := strings.ToLower(query.Query)
		var filtered []MessageSummary
		for _, m := range messages {
			if strings.Contains(strings.ToLower(m.Subject), queryLower) ||
				strings.Contains(strings.ToLower(m.From), queryLower) {
				filtered = append(filtered, m)
			}
		}
		messages = filtered
	}

	if !query.Before.IsZero() {
		var filtered []MessageSummary
		for _, m := range messages {
			if m.Date.Before(query.Before) {
				filtered = append(filtered, m)
			}
		}
		messages = filtered
	}

	if query.To != "" {
		toLower := strings.ToLower(query.To)
		var filtered []MessageSummary
		for _, m := range messages {
			for _, addr := range m.To {
				if strings.Contains(strings.ToLower(addr), toLower) {
					filtered = append(filtered, m)
					break
				}
			}
		}
		messages = filtered
	}

	return messages, nil
}

// MarkRead marks messages as read (\Seen flag).
func (c *IMAPSMTPClient) MarkRead(ctx context.Context, ids []string) error {
	return c.setFlags(ctx, ids, true)
}

// MarkUnread removes the \Seen flag from messages.
func (c *IMAPSMTPClient) MarkUnread(ctx context.Context, ids []string) error {
	return c.setFlags(ctx, ids, false)
}

// setFlags adds or removes the \Seen flag on messages.
func (c *IMAPSMTPClient) setFlags(ctx context.Context, ids []string, seen bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := c.ensureConnectedLocked(ctx); err != nil {
		return err
	}

	folder := c.resolveFolder("")
	if _, err := c.selectFolder(folder); err != nil {
		return err
	}

	uidSet, err := parseUIDSet(ids)
	if err != nil {
		return err
	}

	op := imap.StoreFlagsAdd
	if !seen {
		op = imap.StoreFlagsDel
	}

	storeFlags := &imap.StoreFlags{
		Op:    op,
		Flags: []imap.Flag{imap.FlagSeen},
	}

	// Store returns a FetchCommand — must drain it
	storeCmd := c.imapConn.Store(uidSet, storeFlags, nil)
	if err := storeCmd.Close(); err != nil {
		return fmt.Errorf("failed to update flags: %w", err)
	}

	return nil
}

// Delete moves messages to trash or permanently deletes them.
func (c *IMAPSMTPClient) Delete(ctx context.Context, ids []string, permanent bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if err := c.ensureConnectedLocked(ctx); err != nil {
		return err
	}

	folder := c.resolveFolder("")
	if _, err := c.selectFolder(folder); err != nil {
		return err
	}

	uidSet, err := parseUIDSet(ids)
	if err != nil {
		return err
	}

	// Set the \Deleted flag
	storeFlags := &imap.StoreFlags{
		Op:    imap.StoreFlagsAdd,
		Flags: []imap.Flag{imap.FlagDeleted},
	}
	storeCmd := c.imapConn.Store(uidSet, storeFlags, nil)
	if err := storeCmd.Close(); err != nil {
		return fmt.Errorf("failed to mark as deleted: %w", err)
	}

	// Expunge to permanently remove
	if permanent {
		if err := c.imapConn.Expunge().Close(); err != nil {
			return fmt.Errorf("expunge failed: %w", err)
		}
	}

	return nil
}

// Send sends a new email via SMTP.
func (c *IMAPSMTPClient) Send(ctx context.Context, msg OutgoingMessage) (string, error) {
	domain := ExtractSenderDomain(c.cfg.Address)
	messageID := BuildMessageID(domain)

	headers := map[string]string{
		"Message-ID": messageID,
	}
	if msg.ReplyTo != "" {
		headers["Reply-To"] = msg.ReplyTo
	}

	return messageID, SendSMTP(c.cfg, msg, headers)
}

// Reply sends a reply to an existing email, preserving threading.
func (c *IMAPSMTPClient) Reply(ctx context.Context, replyToID string, replyAll bool, msg OutgoingMessage) (string, error) {
	// Read the original message to get threading headers
	original, err := c.ReadMessage(ctx, replyToID)
	if err != nil {
		return "", fmt.Errorf("failed to read original message for reply: %w", err)
	}

	domain := ExtractSenderDomain(c.cfg.Address)
	messageID := BuildMessageID(domain)

	headers := map[string]string{
		"Message-ID": messageID,
	}

	origMsgID := original.Headers["Message-ID"]
	if origMsgID != "" {
		headers["In-Reply-To"] = origMsgID
		refs := original.Headers["References"]
		if refs != "" {
			headers["References"] = refs + " " + origMsgID
		} else {
			headers["References"] = origMsgID
		}
	}

	// Set recipients if not specified
	if len(msg.To) == 0 {
		msg.To = []string{original.From}
	}
	if replyAll && len(msg.CC) == 0 {
		for _, addr := range original.To {
			if !strings.EqualFold(addr, c.cfg.Address) {
				msg.CC = append(msg.CC, addr)
			}
		}
		for _, addr := range original.CC {
			if !strings.EqualFold(addr, c.cfg.Address) {
				msg.CC = append(msg.CC, addr)
			}
		}
	}

	// Set subject if not specified
	if msg.Subject == "" {
		if strings.HasPrefix(strings.ToLower(original.Subject), "re:") {
			msg.Subject = original.Subject
		} else {
			msg.Subject = "Re: " + original.Subject
		}
	}

	return messageID, SendSMTP(c.cfg, msg, headers)
}

// formatAddress converts an imap.Address to a string.
func formatAddress(addr imap.Address) string {
	if addr.Name != "" {
		return fmt.Sprintf("%s <%s>", addr.Name, addr.Addr())
	}
	return addr.Addr()
}

// hasAttachmentInStructure recursively checks for attachment parts.
func hasAttachmentInStructure(bs imap.BodyStructure) bool {
	if bs == nil {
		return false
	}
	if sp, ok := bs.(*imap.BodyStructureSinglePart); ok {
		disp := sp.Disposition()
		if disp != nil && strings.EqualFold(disp.Value, "attachment") {
			return true
		}
		return false
	}
	if mp, ok := bs.(*imap.BodyStructureMultiPart); ok {
		for _, child := range mp.Children {
			if hasAttachmentInStructure(child) {
				return true
			}
		}
	}
	return false
}

// parseUIDSet converts string IDs to an IMAP UID set.
func parseUIDSet(ids []string) (imap.UIDSet, error) {
	uids := make([]imap.UID, 0, len(ids))
	for _, id := range ids {
		uid, err := strconv.ParseUint(id, 10, 32)
		if err != nil {
			return nil, fmt.Errorf("invalid message ID %q: %w", id, err)
		}
		uids = append(uids, imap.UID(uid))
	}
	return imap.UIDSetNum(uids...), nil
}

// truncate returns at most maxLen characters of s.
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "...(truncated)"
}
