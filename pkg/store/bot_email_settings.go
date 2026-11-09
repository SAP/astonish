package store

// BotEmailSettings is mailbox configuration only: no passwords, access tokens or
// platform channel identity. The credential must exist in the Bot's own scope.
type BotEmailSettings struct {
	Provider   string `json:"provider"`
	Address    string `json:"address"`
	IMAPServer string `json:"imap_server,omitempty"`
	SMTPServer string `json:"smtp_server,omitempty"`
	Credential string `json:"credential"`
	Folder     string `json:"folder,omitempty"`
}
