package launcher

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/client"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/tui/events"
	"github.com/google/uuid"
	"google.golang.org/adk/session"
	"google.golang.org/genai"
)

func TestBotFilePublicationLiveReplay(t *testing.T) {
	run, home := uuid.New(), uuid.New()
	p := store.BotFilePublication{SchemaVersion: 1, BotID: uuid.New(), RunID: run, HomeSessionID: uuid.New(), HomeID: home, ReportSequence: 1, EventID: store.BotPublicationEventID(run, 1), Summary: "Summary only", Artifacts: []store.BotPublishedArtifact{{ID: uuid.New(), HomeID: home, RelativePath: "report.md", SHA256: strings.Repeat("a", 64), Preview: store.BotFilePreviewText}}}
	raw, _ := json.Marshal(p)
	frame := canonicalBotFrame(t, p.HomeSessionID.String(), p.EventID, store.BotFilePublicationKey, string(raw), 1)
	mapper := botTranscriptMapper{}
	live, err := mapper.mapFrame(frame)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := botTranscriptEvents(frame)
	if err != nil || !reflect.DeepEqual(live, replay) {
		t.Fatal("different replay", err)
	}
	tr := events.NewTranscript()
	for _, ev := range append(live, replay...) {
		tr.Apply(ev)
	}
	if len(tr.Items) != 1 || tr.Items[0].BotFilePublication.Artifacts[0].ID != p.Artifacts[0].ID || strings.Count(tr.Items[0].Content, "File: report.md") != 1 {
		t.Fatal(tr.Items)
	}
}
func TestBotFilePublicationRejectsUnboundMetadata(t *testing.T) {
	run, home := uuid.New(), uuid.New()
	p := store.BotFilePublication{SchemaVersion: 1, BotID: uuid.New(), RunID: run, HomeSessionID: uuid.New(), HomeID: home, ReportSequence: 1, EventID: store.BotPublicationEventID(run, 1), Summary: "Summary", Artifacts: []store.BotPublishedArtifact{{ID: uuid.New(), HomeID: home, RelativePath: "report.md", SHA256: strings.Repeat("a", 64), Preview: store.BotFilePreviewText}}}
	for _, bad := range []string{"event", "invocation", "author", "partial", "body", "tool"} {
		t.Run(bad, func(t *testing.T) {
			row := session.Event{ID: p.EventID, InvocationID: "bot-report:" + run.String(), Author: "assistant", Actions: session.EventActions{StateDelta: map[string]any{store.BotFilePublicationKey: p}}}
			row.Content = genai.NewContentFromText(p.Summary, "model")
			switch bad {
			case "event":
				row.ID = "uncommitted"
			case "invocation":
				row.InvocationID = "model-turn"
			case "author":
				row.Author = "user"
			case "partial":
				row.Partial = true
			case "body":
				row.Content.Parts[0].Text = "not committed summary"
			case "tool":
				row.Content.Parts = append(row.Content.Parts, &genai.Part{FunctionCall: &genai.FunctionCall{Name: "shell_command"}})
			}
			out, recognized, err := botFilePublicationEvents(&row)
			if !recognized || err == nil || len(out) != 0 {
				t.Fatal("unbound metadata accepted", out, err)
			}
		})
	}
}

func TestBotFilesBackendRejectsForeignSelection(t *testing.T) {
	b := &botPlatformBackend{selected: &store.Bot{ID: uuid.New()}}
	if _, err := b.ListBotFiles(context.Background(), client.BotFileRef{BotID: uuid.New()}, ""); err != store.ErrBotConflict {
		t.Fatal(err)
	}
	if _, err := b.ReadBotFilePreview(context.Background(), client.BotFileRef{}); err != store.ErrBotConflict {
		t.Fatal(err)
	}
	if err := b.DownloadBotFile(context.Background(), client.BotFileRef{}, "unused"); err != store.ErrBotConflict {
		t.Fatal(err)
	}
}
