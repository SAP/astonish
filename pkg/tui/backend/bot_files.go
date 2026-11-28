package backend

import (
	"context"
	"github.com/SAP/astonish/pkg/client"
	"github.com/SAP/astonish/pkg/store"
)

// BotFilesBackend is optional and exclusive to scoped remote Bot backends.
// All methods are observations; none may provision compute or admit work.
type BotFilesBackend interface {
	ListBotFiles(context.Context, client.BotFileRef, string) (*store.BotFilePage, error)
	ReadBotFilePreview(context.Context, client.BotFileRef) (*client.BotFilePreview, error)
	DownloadBotFile(context.Context, client.BotFileRef, string) error
}
