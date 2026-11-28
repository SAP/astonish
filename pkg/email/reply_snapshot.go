package email

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/mail"
	"strings"
)

// ReplySource is obtained only by a separately authorized read. It contains no
// credentials or original body; Digest binds the parsed original, including its
// headers, content and attachment metadata (but not the mutable unread flag).
type ReplySource struct {
	ID      string            `json:"id"`
	Digest  string            `json:"digest"`
	From    string            `json:"from"`
	To      []string          `json:"to"`
	CC      []string          `json:"cc,omitempty"`
	Subject string            `json:"subject"`
	Headers map[string]string `json:"headers"`
}

func NewReplySource(original *Message) (ReplySource, error) {
	if original == nil || original.ID == "" {
		return ReplySource{}, fmt.Errorf("missing reply source")
	}
	copy := *original
	copy.Unread = false
	raw, err := json.Marshal(copy)
	if err != nil {
		return ReplySource{}, err
	}
	sum := sha256.Sum256(raw)
	headers := map[string]string{}
	for _, key := range []string{"Message-ID", "References", "Reply-To"} {
		count := 0
		for name, value := range original.Headers {
			if strings.EqualFold(name, key) {
				count++
				headers[key] = value
			}
		}
		if count > 1 {
			return ReplySource{}, fmt.Errorf("ambiguous reply header")
		}
		count = 0
		for name, values := range original.HeaderValues {
			if strings.EqualFold(name, key) {
				count += len(values)
			}
		}
		if count > 1 {
			return ReplySource{}, fmt.Errorf("ambiguous reply header")
		}
	}
	return ReplySource{ID: original.ID, Digest: hex.EncodeToString(sum[:]), From: original.From, To: append([]string(nil), original.To...), CC: append([]string(nil), original.CC...), Subject: original.Subject, Headers: headers}, nil
}

// ReplySnapshot is internal node/approval evidence, never a model argument.
// The public reply schema has no attachments, so the outgoing set is empty.
type ReplySnapshot struct {
	OriginalID     string          `json:"original_id"`
	OriginalDigest string          `json:"original_digest"`
	Account        Account         `json:"account"`
	Message        OutgoingMessage `json:"message"`
	InReplyTo      string          `json:"in_reply_to"`
	References     string          `json:"references"`
}

func NewReplySnapshot(a Account, source ReplySource, body, html string, all bool) (ReplySnapshot, error) {
	s := ReplySnapshot{OriginalID: source.ID, OriginalDigest: source.Digest, Account: a, Message: OutgoingMessage{Body: body, HTML: html}}
	if err := a.Validate(); err != nil {
		return s, err
	}
	if source.ID == "" || len(source.Digest) != 64 || body == "" {
		return s, fmt.Errorf("reply requires an authorized original read and body")
	}
	from := source.From
	if source.Headers["Reply-To"] != "" {
		from = source.Headers["Reply-To"]
	}
	parse := func(value string) (string, error) {
		if strings.ContainsAny(value, "\r\n") {
			return "", fmt.Errorf("invalid reply address")
		}
		addr, err := mail.ParseAddress(value)
		if err != nil {
			return "", fmt.Errorf("invalid reply address")
		}
		return addr.Address, nil
	}
	recipient, err := parse(from)
	if err != nil {
		return s, err
	}
	owner, err := parse(a.Address)
	if err != nil {
		return s, err
	}
	s.Message.To = []string{recipient}
	seen := map[string]bool{strings.ToLower(owner): true, strings.ToLower(recipient): true}
	if all {
		for _, value := range append(append([]string{}, source.To...), source.CC...) {
			addr, err := parse(value)
			if err != nil {
				return s, err
			}
			if !seen[strings.ToLower(addr)] {
				s.Message.CC = append(s.Message.CC, addr)
				seen[strings.ToLower(addr)] = true
			}
		}
	}
	s.Message.Subject = source.Subject
	if !strings.HasPrefix(strings.ToLower(s.Message.Subject), "re:") {
		s.Message.Subject = "Re: " + s.Message.Subject
	}
	s.InReplyTo = source.Headers["Message-ID"]
	if s.InReplyTo != "" {
		s.References = strings.TrimSpace(source.Headers["References"] + " " + s.InReplyTo)
	}
	if strings.ContainsAny(s.Message.Subject+s.InReplyTo+s.References, "\r\n") {
		return s, fmt.Errorf("invalid reply headers")
	}
	return s, nil
}

func (s ReplySnapshot) Fingerprint() string {
	raw, _ := json.Marshal(s)
	sum := sha256.Sum256(raw)
	return "email-reply-v1:" + hex.EncodeToString(sum[:])
}

// SendReplySnapshot never asks a provider to resolve reply recipients or draft
// defaults. Both transports send exactly the reviewed message and thread headers.
func SendReplySnapshot(ctx context.Context, cfg *Config, snapshot ReplySnapshot) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	msg := snapshot.Message
	if len(msg.To) == 0 || len(msg.Attachments) != 0 {
		return "", fmt.Errorf("invalid reply snapshot")
	}
	id := BuildMessageID(ExtractSenderDomain(cfg.Address))
	headers := map[string]string{"Message-ID": id}
	if snapshot.InReplyTo != "" {
		headers["In-Reply-To"] = snapshot.InReplyTo
		headers["References"] = snapshot.References
	}
	if cfg.Provider != "msgraph" {
		return id, SendSMTP(cfg, msg, headers)
	}
	// Graph's JSON sendMail only permits custom x-* internet headers. MIME is
	// required for RFC threading without provider-generated recipient defaults.
	var mime strings.Builder
	fmt.Fprintf(&mime, "From: %s\r\nTo: %s\r\n", cfg.Address, strings.Join(msg.To, ", "))
	if len(msg.CC) != 0 {
		fmt.Fprintf(&mime, "Cc: %s\r\n", strings.Join(msg.CC, ", "))
	}
	fmt.Fprintf(&mime, "Subject: %s\r\nMIME-Version: 1.0\r\n", encodeSubject(msg.Subject))
	for key, value := range headers {
		fmt.Fprintf(&mime, "%s: %s\r\n", key, value)
	}
	writeBodyPart(&mime, msg)
	token, err := cfg.TokenFunc()
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, graphBaseURL+"/me/sendMail", strings.NewReader(base64.StdEncoding.EncodeToString([]byte(mime.String()))))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "text/plain")
	client := &http.Client{Timeout: graphTimeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		return "", fmt.Errorf("msgraph snapshot send returned HTTP %d", resp.StatusCode)
	}
	return id, nil
}
