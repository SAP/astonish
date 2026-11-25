package tui

import (
	"context"
	"github.com/SAP/astonish/pkg/client"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/tui/events"
)

type botFilesTestBackend struct {
	testBotBackend
	reads, saves int
	cancelled    bool
}

func (b *botFilesTestBackend) ListBotFiles(ctx context.Context, _ client.BotFileRef, _ string) (*store.BotFilePage, error) {
	b.reads++
	if ctx.Err() != nil {
		b.cancelled = true
		return nil, ctx.Err()
	}
	return &store.BotFilePage{Entries: []store.BotFileEntry{{Path: "MEMORY.md", Kind: store.BotFileRegular, Preview: store.BotFilePreviewText}, {Path: "large.bin", Kind: store.BotFileRegular, Preview: store.BotFilePreviewDownload}}}, nil
}
func (b *botFilesTestBackend) ReadBotFilePreview(context.Context, client.BotFileRef) (*client.BotFilePreview, error) {
	return &client.BotFilePreview{Content: "Memory goals"}, nil
}
func (b *botFilesTestBackend) DownloadBotFile(context.Context, client.BotFileRef, string) error {
	b.saves++
	return nil
}
func TestBotFilesNavigationCancellationAndSaveConsent(t *testing.T) {
	b := &botFilesTestBackend{testBotBackend: testBotBackend{selected: &store.Bot{ID: uuid.New()}}}
	m := newModel(context.Background(), Config{Backend: b})
	m.height = 40
	m.ta.SetValue("unfinished")
	next, cmd := m.botCommand("/bots files")
	m = next.(model)
	if !strings.Contains(m.renderBotsOverlay(), "Loading") {
		t.Fatal("loading absent")
	}
	next, _ = m.Update(cmd())
	m = next.(model)
	next, cmd = m.handleBotFilesKey("enter")
	m = next.(model)
	next, _ = m.Update(cmd())
	m = next.(model)
	if !strings.Contains(m.renderBotsOverlay(), "Memory goals") {
		t.Fatal("preview missing")
	}
	next, _ = m.handleBotFilesKey("esc")
	m = next.(model)
	next, _ = m.handleBotFilesKey("down")
	m = next.(model)
	next, _ = m.handleBotFilesKey("enter")
	m = next.(model)
	if b.saves != 0 || m.bots.files.mode != botFilesSave {
		t.Fatal("saved without destination")
	}
	next, _ = m.handleBotFilesKey("esc")
	m = next.(model)
	next, cmd = m.handleBotFilesKey("r")
	m = next.(model)
	next, _ = m.handleBotFilesKey("esc")
	m = next.(model)
	next, _ = m.Update(cmd())
	m = next.(model)
	if m.bots.open || !b.cancelled || m.ta.Value() != "unfinished" {
		t.Fatal("late callback or lost context")
	}
}
func TestBotFilesAccessLossClearsCachedContent(t *testing.T) {
	for _, status := range []int{401, 403, 404, 409, 410} {
		b := &botFilesTestBackend{testBotBackend: testBotBackend{selected: &store.Bot{ID: uuid.New()}}}
		m := newModel(context.Background(), Config{Backend: b})
		next, cmd := m.openBotFiles()
		m = next.(model)
		msg := cmd().(botFilesMsg)
		m.bots.files.content = "private preview"
		m.bots.files.rows = []botFileRow{{entry: store.BotFileEntry{Path: "private"}}}
		msg.err = &client.BotResponseError{StatusCode: status}
		next, _ = m.applyBotFiles(msg)
		m = next.(model)
		if m.bots.files.content != "" || len(m.bots.files.rows) != 0 {
			t.Fatalf("status %d retained inaccessible data", status)
		}
	}
}

func TestBotFilesFreshnessConflictClearsPreview(t *testing.T) {
	b := &botFilesTestBackend{testBotBackend: testBotBackend{selected: &store.Bot{ID: uuid.New()}}}
	m := newModel(context.Background(), Config{Backend: b})
	next, cmd := m.openBotFiles()
	m = next.(model)
	msg := cmd().(botFilesMsg)
	m.bots.files.content = "stale preview"
	msg.err = store.ErrBotConflict
	next, _ = m.applyBotFiles(msg)
	if next.(model).bots.files.content != "" {
		t.Fatal("freshness conflict retained old preview")
	}
}

func TestBotFilesHomeChangeClosesBeforeInteraction(t *testing.T) {
	b := &botFilesTestBackend{testBotBackend: testBotBackend{selected: &store.Bot{ID: uuid.New()}}}
	m := newModel(context.Background(), Config{Backend: b})
	next, cmd := m.openBotFiles()
	m = next.(model)
	next, _ = m.applyBotFiles(cmd().(botFilesMsg))
	m = next.(model)
	b.selected.Home.ID = uuid.New()
	next, cmd = m.handleBotFilesKey("enter")
	m = next.(model)
	if m.bots.open || cmd != nil {
		t.Fatal("old Home remained actionable")
	}
}

func TestBotFilesOrdinaryArtifactCannotAuthorizePublishedRead(t *testing.T) {
	b := &botFilesTestBackend{testBotBackend: testBotBackend{selected: &store.Bot{ID: uuid.New()}}}
	m := newModel(context.Background(), Config{Backend: b})
	m.tr.Apply(events.Event{Kind: events.KindArtifact, Artifact: &events.Artifact{Path: "private/receipt", FileName: "arbitrary"}})
	next, cmd := m.openBotFiles()
	m = next.(model)
	next, _ = m.applyBotFiles(cmd().(botFilesMsg))
	m = next.(model)
	next, cmd = m.handleBotFilesKey("p")
	m = next.(model)
	next, _ = m.applyBotFiles(cmd().(botFilesMsg))
	m = next.(model)
	if len(m.bots.files.rows) != 0 || b.saves != 0 {
		t.Fatal("ordinary artifact became Bot authority")
	}
}

type botFilesPagingBackend struct {
	botFilesTestBackend
	requests []string
}

func (b *botFilesPagingBackend) ListBotFiles(_ context.Context, ref client.BotFileRef, cursor string) (*store.BotFilePage, error) {
	b.requests = append(b.requests, ref.Path+":"+cursor)
	if ref.Path == "folder" {
		return &store.BotFilePage{}, nil
	}
	if cursor == "page2" {
		return &store.BotFilePage{Entries: []store.BotFileEntry{{Path: "folder", Kind: store.BotFileDirectory}}}, nil
	}
	return &store.BotFilePage{Entries: []store.BotFileEntry{{Path: "first", Kind: store.BotFileRegular}}, Next: "page2"}, nil
}

func TestBotFilesPagingFoldersAndRefreshPreserveStatus(t *testing.T) {
	b := &botFilesPagingBackend{botFilesTestBackend: botFilesTestBackend{testBotBackend: testBotBackend{selected: &store.Bot{ID: uuid.New(), Status: store.BotStopped}}}}
	m := newModel(context.Background(), Config{Backend: b})
	m.tr.Status = "Unrelated work running"
	m.tr.Streaming = true
	next, cmd := m.openBotFiles()
	m = next.(model)
	next, _ = m.applyBotFiles(cmd().(botFilesMsg))
	m = next.(model)
	for _, key := range []string{"n", "enter", "left", "r"} {
		next, cmd = m.handleBotFilesKey(key)
		m = next.(model)
		if cmd == nil {
			t.Fatalf("%s did not load", key)
		}
		next, _ = m.applyBotFiles(cmd().(botFilesMsg))
		m = next.(model)
	}
	if strings.Join(b.requests, ",") != ":,:page2,folder:,:,:" {
		t.Fatal(b.requests)
	}
	if m.tr.Status != "Unrelated work running" || !m.tr.Streaming || b.selected.Status != store.BotStopped {
		t.Fatal("file observation changed work/lifecycle status")
	}
}

func TestBotFilesDisplaySanitizesControls(t *testing.T) {
	if strings.Contains(botFileDisplay("\x1b]52;clipboard\a\ntext"), "\x1b") {
		t.Fatal("escape leaked")
	}
}
