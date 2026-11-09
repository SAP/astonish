package email

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/mail"
	"strings"
)

// Account is non-secret request-scoped mailbox configuration. Credentials names
// are resolved only from the run's node vault, never the daemon channel identity.
type Account struct {
	Provider   string `json:"provider"`
	Address    string `json:"address"`
	IMAPServer string `json:"imap_server,omitempty"`
	SMTPServer string `json:"smtp_server,omitempty"`
	Credential string `json:"credential"`
	Folder     string `json:"folder,omitempty"`
}

func (a Account) Validate() error {
	if a.Credential == "" || strings.ContainsAny(a.Address, "\r\n") {
		return fmt.Errorf("scoped email address and credential are required")
	}
	if _, err := mail.ParseAddress(a.Address); err != nil {
		return fmt.Errorf("invalid scoped email address")
	}
	switch a.Provider {
	case "imap", "gmail":
		for _, endpoint := range []string{a.IMAPServer, a.SMTPServer} {
			host, port, err := net.SplitHostPort(endpoint)
			if err != nil || host == "" || port == "" {
				return fmt.Errorf("scoped email requires IMAP and SMTP host:port endpoints")
			}
		}
	case "msgraph":
	default:
		return fmt.Errorf("unsupported scoped email provider")
	}
	return nil
}

func (a Account) Fingerprint() string {
	raw, _ := json.Marshal(a)
	sum := sha256.Sum256(raw)
	return "email-account-v1:" + hex.EncodeToString(sum[:])
}

// NodeOperation is internal to the authenticated sandbox node protocol. It is
// not a model tool and does not permit choosing an account through tool arguments.
const NodeOperation = "__astonish_email"

type NodeRequest struct {
	Account       Account        `json:"account"`
	Tool          string         `json:"tool"`
	Arguments     map[string]any `json:"arguments"`
	ReplySnapshot *ReplySnapshot `json:"reply_snapshot,omitempty"`
}
