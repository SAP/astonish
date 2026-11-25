package api_test

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/launcher"
	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
)

// composedHTTPFiles keeps controlled source retrieval behind the real confined
// file tool. Source bytes are saved in this test's Home before they are read;
// document reads never count as source-tool calls.
type composedHTTPFiles struct {
	disk  *synthesisHTTPFiles
	repo  store.BotStore
	id    store.BotID
	scope store.BotScope
	fetch func(string, map[string]interface{}) (json.RawMessage, error)
}

var _ store.BotFiles = (*composedHTTPFiles)(nil)

func newComposedHTTPFiles(t *testing.T, root string, repo store.BotStore, b store.Bot, fetch func(string, map[string]interface{}) (json.RawMessage, error)) *composedHTTPFiles {
	t.Helper()
	for _, name := range []string{"SOUL.md", "IDENTITY.md", "USER.md", "CHARTER.md", "GOALS.md", "MEMORY.md"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("# Bot\nUse scoped evidence and answer honestly.\n"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return &composedHTTPFiles{disk: &synthesisHTTPFiles{root: root}, repo: repo, id: b.ID, scope: b.Scope, fetch: fetch}
}
func (f *composedHTTPFiles) install(_ context.Context, _ bot.RunRequest, scope *launcher.BotChatScope) {
	scope.Services.BotFilesFactory = func(ctx context.Context, _ *store.Services, requested store.BotScope) (store.BotFiles, error) {
		if requested != f.scope {
			return nil, store.ErrBotForbidden
		}
		if _, err := f.repo.Get(ctx, f.id); err != nil {
			return nil, err
		}
		return f, nil
	}
}
func (f *composedHTTPFiles) Open(ctx context.Context, id store.BotID, p string, r store.BotFileRange) (*store.BotFileStream, error) {
	if id != f.id {
		return nil, store.ErrBotForbidden
	}
	if _, err := f.repo.Get(ctx, id); err != nil {
		return nil, err
	}
	if filepath.Base(p) != p || p == "." || p == ".." {
		return nil, store.ErrBotForbidden
	}
	switch p {
	case "SOUL.md", "IDENTITY.md", "USER.md", "CHARTER.md", "GOALS.md", "MEMORY.md":
	default:
		if f.fetch != nil {
			raw, err := f.fetch("read_file", map[string]interface{}{"path": p})
			if err != nil {
				return nil, err
			}
			var result struct {
				Content string `json:"content"`
			}
			if err := json.Unmarshal(raw, &result); err != nil {
				return nil, err
			}
			if err := os.WriteFile(filepath.Join(f.disk.root, p), []byte(result.Content), 0600); err != nil {
				return nil, err
			}
		}
	}
	return f.disk.Open(ctx, id, p, r)
}

// composedBrowserBackend supplies retained-file stat for production Home setup.
// All bytes belong to the test-owned disk; unsupported operations still fail closed.
type composedBrowserBackend struct{ synthesisHTTPBackend }

func (b composedBrowserBackend) StatBotHomeFile(ctx context.Context, home *sandbox.BotHomeBinding, p string) (*store.BotFileEntry, error) {
	if sandbox.ValidateBotHomeArtifactPath(p) != nil || filepath.Base(p) != p {
		return nil, store.ErrBotForbidden
	}
	stream, err := b.OpenBotHomeFile(ctx, home, p, store.BotFileRange{})
	if os.IsNotExist(err) {
		return nil, store.ErrBotNotFound
	}
	if err != nil {
		return nil, err
	}
	entry := stream.Entry
	if err := stream.Close(); err != nil {
		return nil, err
	}
	return &entry, nil
}

// Unused capabilities fail explicitly, rather than panic through a nil embedded interface.
func (*composedHTTPFiles) List(context.Context, store.BotID, string, string, int) (*store.BotFilePage, error) {
	return nil, store.ErrBotUnsupported
}
func (*composedHTTPFiles) Stat(context.Context, store.BotID, string) (*store.BotFileEntry, error) {
	return nil, store.ErrBotUnsupported
}
func (*composedHTTPFiles) Write(context.Context, store.BotID, store.BotFileWrite) (*store.BotFileEntry, error) {
	return nil, store.ErrBotUnsupported
}
func (*composedHTTPFiles) PutContent(context.Context, store.BotID, store.BotContentKind, int64, io.Reader, int64, func(func() error) error) (*store.BotContentRef, error) {
	return nil, store.ErrBotUnsupported
}
func (*composedHTTPFiles) OpenContent(context.Context, store.BotContentRef, store.BotFileRange) (*store.BotFileStream, error) {
	return nil, store.ErrBotUnsupported
}
func (*composedHTTPFiles) PublishSnapshot(context.Context, store.BotID, string, string, func(func() error) error) (*store.BotContentRef, error) {
	return nil, store.ErrBotUnsupported
}
