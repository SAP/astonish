package launcher

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/SAP/astonish/pkg/client"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/tui/backend"
	"github.com/SAP/astonish/pkg/tui/events"
	adksession "google.golang.org/adk/session"
)

var _ backend.BotFilesBackend = (*botPlatformBackend)(nil)

func (b *botPlatformBackend) checkBotFile(ref client.BotFileRef) error {
	selected := b.SelectedBot()
	if selected == nil || selected.ID != ref.BotID || selected.HomeSessionID != ref.HomeSessionID || selected.Home.ID != ref.HomeID {
		return store.ErrBotConflict
	}
	return nil
}
func (b *botPlatformBackend) ListBotFiles(ctx context.Context, ref client.BotFileRef, cursor string) (*store.BotFilePage, error) {
	if err := b.checkBotFile(ref); err != nil {
		return nil, err
	}
	return b.bots.ListBotFiles(ctx, ref.BotID, ref.Path, cursor)
}
func (b *botPlatformBackend) ReadBotFilePreview(ctx context.Context, ref client.BotFileRef) (*client.BotFilePreview, error) {
	if err := b.checkBotFile(ref); err != nil {
		return nil, err
	}
	return b.bots.ReadBotFilePreview(ctx, ref)
}
func (b *botPlatformBackend) DownloadBotFile(ctx context.Context, ref client.BotFileRef, destination string) error {
	if err := b.checkBotFile(ref); err != nil {
		return err
	}
	return b.bots.DownloadBotFile(ctx, ref, destination)
}

func botFilePublicationEvents(row *adksession.Event) ([]events.Event, bool, error) {
	raw, ok := row.Actions.StateDelta[store.BotFilePublicationKey]
	if !ok {
		return nil, false, nil
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return nil, true, err
	}
	var p store.BotFilePublication
	if json.Unmarshal(data, &p) != nil || p.Validate() != nil || row.ID != p.EventID || row.InvocationID != "bot-report:"+p.RunID.String() || row.Author != "assistant" || row.Partial || row.Content == nil || row.Content.Role != "model" || len(row.Content.Parts) != 1 || row.Content.Parts[0] == nil || row.Content.Parts[0].Text != p.Summary || row.Content.Parts[0].Thought {
		return nil, true, fmt.Errorf("invalid Bot file publication")
	}
	ev := events.NewBotReport(row.InvocationID, row.ID, p.Summary)
	ev.BotFilePublication = &p
	return []events.Event{ev}, true, nil
}
