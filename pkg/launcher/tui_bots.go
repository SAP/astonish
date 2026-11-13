package launcher

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/SAP/astonish/pkg/client"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/tui/backend"
	"github.com/SAP/astonish/pkg/tui/events"
	"github.com/google/uuid"
	adksession "google.golang.org/adk/session"
)

// botPlatformBackend adds observation to remote chat, not a third runtime.
type botPlatformBackend struct {
	*platformBackend
	bots           client.Bots
	botMu          sync.Mutex
	selected       *store.Bot
	sessionID      uuid.UUID
	selectionEpoch uint64
	// Shared terminal reducer orders snapshot/history/live operation evidence.
	// Only snapshot.Bot supplies composer version/revision authority.
	operationHeads *events.Transcript
	botSnapshot    *store.BotSnapshot
}

var _ backend.BotBackend = (*botPlatformBackend)(nil)

func newBotPlatformBackend(b *platformBackend, scope string) *botPlatformBackend {
	if scope == "" {
		scope = string(store.BotScopePersonal)
	}
	return &botPlatformBackend{platformBackend: b, bots: client.Bots{Client: b.client, Scope: store.BotScope(scope)}}
}
func (b *botPlatformBackend) SelectedBot() *store.Bot {
	b.botMu.Lock()
	defer b.botMu.Unlock()
	if b.selected == nil {
		return nil
	}
	v := *b.selected
	return &v
}
func (b *botPlatformBackend) ListBots(ctx context.Context) ([]store.Bot, error) {
	return b.bots.List(ctx)
}
func (b *botPlatformBackend) SelectBot(ctx context.Context, ref string) (*store.Bot, error) {
	b.botMu.Lock()
	b.selectionEpoch++
	epoch := b.selectionEpoch
	b.botMu.Unlock()
	v, err := b.bots.Resolve(ctx, ref)
	if err != nil {
		return nil, err
	}
	b.botMu.Lock()
	if epoch != b.selectionEpoch || ctx.Err() != nil {
		b.botMu.Unlock()
		return nil, fmt.Errorf("Bot selection changed")
	}
	b.selected = v
	b.operationHeads, b.botSnapshot = nil, nil
	b.sessionID = uuid.Nil
	b.botMu.Unlock()
	return v, nil
}
func (b *botPlatformBackend) DetachBot() {
	b.botMu.Lock()
	b.selectionEpoch++
	b.selected = nil
	b.operationHeads, b.botSnapshot = nil, nil
	b.sessionID = uuid.Nil
	b.botMu.Unlock()
}
func (b *botPlatformBackend) watchSelection() (*store.Bot, uuid.UUID) {
	b.botMu.Lock()
	defer b.botMu.Unlock()
	if b.selected == nil {
		return nil, uuid.Nil
	}
	v := *b.selected
	sid := b.sessionID
	if sid == uuid.Nil {
		sid = v.HomeSessionID
	}
	return &v, sid
}

var _ backend.BotSkillBackend = (*botPlatformBackend)(nil)

func (b *botPlatformBackend) BotSkills(ctx context.Context, page store.BotSkillPage) (*store.BotSkillList, error) {
	bot := b.SelectedBot()
	if bot == nil {
		return nil, fmt.Errorf("select a Bot first")
	}
	return b.bots.Skills(ctx, bot.ID.String(), page)
}
func (b *botPlatformBackend) BotSkillRevisions(ctx context.Context, skill uuid.UUID, page store.BotSkillPage) (*store.BotSkillRevisionList, error) {
	bot := b.SelectedBot()
	if bot == nil {
		return nil, fmt.Errorf("select a Bot first")
	}
	return b.bots.SkillRevisions(ctx, bot.ID.String(), skill.String(), page)
}
func (b *botPlatformBackend) BotSkillRuns(ctx context.Context, skill uuid.UUID, page store.BotSkillPage) (*store.BotSkillRunList, error) {
	bot := b.SelectedBot()
	if bot == nil {
		return nil, fmt.Errorf("select a Bot first")
	}
	return b.bots.SkillRuns(ctx, bot.ID.String(), skill.String(), page)
}

func (b *botPlatformBackend) BotRuns(ctx context.Context) ([]store.BotRun, error) {
	bot := b.SelectedBot()
	if bot == nil {
		return nil, fmt.Errorf("select a Bot first")
	}
	return b.bots.Runs(ctx, bot.ID.String())
}
func (b *botPlatformBackend) SelectBotSession(ctx context.Context, sid uuid.UUID) error {
	b.botMu.Lock()
	bot := b.selected
	b.selectionEpoch++
	epoch := b.selectionEpoch
	b.botMu.Unlock()
	if bot == nil {
		return fmt.Errorf("select a Bot first")
	}
	if sid == uuid.Nil {
		fresh, err := b.bots.Get(ctx, bot.ID.String())
		if err != nil {
			return err
		}
		bot = fresh
	} else {
		runs, err := b.bots.Runs(ctx, bot.ID.String())
		if err != nil {
			return err
		}
		found := false
		for _, r := range runs {
			if r.BotID == bot.ID && r.SessionID == sid {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("run session does not belong to selected Bot")
		}
	}
	b.botMu.Lock()
	defer b.botMu.Unlock()
	if b.selected == nil || b.selected.ID != bot.ID || epoch != b.selectionEpoch || ctx.Err() != nil {
		return fmt.Errorf("Bot selection changed")
	}
	b.selected = bot
	b.sessionID = sid
	return nil
}
func (b *botPlatformBackend) AnswerBotApproval(ctx context.Context, a store.BotApproval, r store.BotInteractionResponse) error {
	bot := b.SelectedBot()
	if bot == nil || bot.ID != a.BotID || a.Interaction == nil || a.Interaction.ValidateResponse(&r) != nil {
		return fmt.Errorf("invalid bound Bot interaction answer")
	}
	_, err := b.bots.Decide(ctx, bot.ID.String(), a.ID.String(), client.BotDecision{ActionDigest: a.ActionDigest, Status: store.ApprovalApproved, Response: &r})
	return err
}
func (b *botPlatformBackend) Info() backend.Info {
	v := b.platformBackend.Info()
	if bot, sid := b.watchSelection(); bot != nil {
		v.Title = "Bot: " + bot.Name + " [" + string(bot.Status) + "]"
		v.SessionID = sid.String()
		if sid != bot.HomeSessionID {
			v.Title += " — run trace"
		}
		v.IsResumed = false
		v.AutoApprove = false
		v.Notices = append(v.Notices, "Watching "+v.Title+". /bots approvals for durable decisions; /bots detach leaves work running.")
	}
	return v
}
func (b *botPlatformBackend) Open(ctx context.Context) error {
	if bot := b.SelectedBot(); bot != nil {
		_, err := b.bots.Get(ctx, bot.ID.String())
		return err
	}
	return b.platformBackend.Open(ctx)
}
func (b *botPlatformBackend) RunTurn(ctx context.Context, text string, opts backend.TurnOptions) (<-chan events.Event, error) {
	if bot := b.SelectedBot(); bot != nil {
		b.botMu.Lock()
		runSelected := b.sessionID != uuid.Nil
		epoch := b.selectionEpoch
		b.botMu.Unlock()
		if runSelected {
			return nil, fmt.Errorf("selected run trace is read-only; /bots home to converse")
		}
		if len(opts.Attachments) > 0 {
			return nil, fmt.Errorf("Bot terminal input is text-only; use Studio for attachments")
		}
		if bot.Version < 1 {
			return nil, fmt.Errorf("re-select Bot to review its current version")
		}
		if strings.TrimSpace(text) == "" {
			return nil, fmt.Errorf("enter a message")
		}
		var err error
		bot, err = b.reconcileBotComposer(ctx, bot, epoch)
		if err != nil {
			return nil, err
		}
		if bot.Status != store.BotDraft && bot.Status != store.BotPaused && bot.Status != store.BotActive {
			return nil, fmt.Errorf("Bot %s cannot converse", bot.Status)
		}
		_, err = b.bots.SendMessage(ctx, bot.ID.String(), bot.Version, uuid.NewString(), text)
		if err != nil {
			return nil, err
		}
		// Receipt is admission only; the unchanged home watch owns all chronology.
		ch := make(chan events.Event, 1)
		ch <- events.NewSystem("Message accepted. Reply will appear here.")
		close(ch)
		return ch, nil
	}
	return b.platformBackend.RunTurn(ctx, text, opts)
}
func (b *botPlatformBackend) LoadHistory(ctx context.Context) ([]backend.HistoryEntry, error) {
	if b.SelectedBot() != nil {
		return nil, nil
	}
	return b.platformBackend.LoadHistory(ctx)
}
func (b *botPlatformBackend) ResumeSession(ctx context.Context, id string) ([]backend.HistoryEntry, error) {
	b.DetachBot()
	return b.platformBackend.ResumeSession(ctx, id)
}
func (b *botPlatformBackend) NewSession() { b.DetachBot(); b.platformBackend.NewSession() }
func (b *botPlatformBackend) SetModelPin(ctx context.Context, p, m string) (string, string, error) {
	if b.SelectedBot() != nil {
		return "", "", fmt.Errorf("Bot model configuration must be reviewed on the platform")
	}
	return b.platformBackend.SetModelPin(ctx, p, m)
}
func (b *botPlatformBackend) BotApprovals(ctx context.Context) ([]store.BotApproval, error) {
	bot := b.SelectedBot()
	if bot == nil {
		return nil, fmt.Errorf("select a Bot first")
	}
	return b.bots.Approvals(ctx, bot.ID.String())
}
func (b *botPlatformBackend) DecideBotApproval(ctx context.Context, a store.BotApproval, status store.ApprovalStatus) error {
	bot := b.SelectedBot()
	if bot == nil || bot.ID != a.BotID {
		return fmt.Errorf("approval does not belong to selected Bot")
	}
	if a.Interaction != nil && status == store.ApprovalApproved {
		return fmt.Errorf("typed interaction requires astonish bots approval with --response JSON")
	}
	_, err := b.bots.Decide(ctx, bot.ID.String(), a.ID.String(), client.BotDecision{ActionDigest: a.ActionDigest, Status: status})
	return err
}
func (b *botPlatformBackend) WatchBot(ctx context.Context, emit func(events.Event) error) error {
	b.botMu.Lock()
	epoch := b.selectionEpoch
	b.botMu.Unlock()
	bot, sid := b.watchSelection()
	if bot == nil {
		return nil
	}
	if sid == uuid.Nil {
		if err := emit(events.NewSystem("Bot has no transcript yet. Waiting for admitted work…")); err != nil {
			return err
		}
		for sid == uuid.Nil {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(250 * time.Millisecond):
			}
			fresh, err := b.bots.Get(ctx, bot.ID.String())
			if err != nil {
				return err
			}
			b.botMu.Lock()
			if b.selected == nil || b.selected.ID != bot.ID || epoch != b.selectionEpoch || ctx.Err() != nil {
				b.botMu.Unlock()
				return context.Canceled
			}
			b.selected = fresh
			b.botMu.Unlock()
			sid = fresh.HomeSessionID
		}
	}
	mapper := botTranscriptMapper{}
	return b.bots.Watch(ctx, bot.ID.String(), sid.String(), "", true, func(ev client.SSEEvent) error {
		mapped, err := mapper.mapFrame(ev)
		if err != nil {
			return err
		}
		for _, e := range mapped {
			b.botMu.Lock()
			if b.selected == nil || b.selected.ID != bot.ID || epoch != b.selectionEpoch || ctx.Err() != nil {
				b.botMu.Unlock()
				return context.Canceled
			}
			if sid == b.selected.HomeSessionID && e.Kind == events.KindBotOperation {
				b.observeBotOperationLocked(e.BotOperation)
			}
			b.botMu.Unlock()
			if err := emit(e); err != nil {
				return err
			}
		}
		return nil
	})
}

// BotSnapshot is the authoritative read projection; no client-side schedule prediction.
func (b *botPlatformBackend) BotSnapshot(ctx context.Context) (*store.BotSnapshot, error) {
	b.botMu.Lock()
	bot, epoch := b.selected, b.selectionEpoch
	b.botMu.Unlock()
	if bot == nil {
		return nil, fmt.Errorf("select a Bot first")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	snap, err := b.bots.Snapshot(ctx, bot.ID.String())
	if err != nil {
		return nil, err
	}
	b.botMu.Lock()
	defer b.botMu.Unlock()
	if b.selected == nil || b.selected.ID != bot.ID || snap.Bot.ID != bot.ID || epoch != b.selectionEpoch || ctx.Err() != nil {
		return nil, fmt.Errorf("Bot selection changed")
	}
	if snap.Bot.Version < b.selected.Version || snap.Bot.Revision < b.selected.Revision ||
		(b.botSnapshot != nil && (snap.HistoryCursor < b.botSnapshot.HistoryCursor || snap.LatestSequence < b.botSnapshot.LatestSequence)) {
		return nil, fmt.Errorf("Bot snapshot regressed; review before retrying")
	}
	b.selected = &snap.Bot
	b.botSnapshot = snap
	for i := range snap.Operations {
		b.observeBotOperationLocked(&snap.Operations[i])
	}
	return snap, nil
}
func (b *botPlatformBackend) BotGoals(ctx context.Context) ([]store.BotGoal, error) {
	bot := b.SelectedBot()
	if bot == nil {
		return nil, fmt.Errorf("select a Bot first")
	}
	return b.bots.Goals(ctx, bot.ID.String())
}
func (b *botPlatformBackend) BotStudioURL() string {
	bot := b.SelectedBot()
	if bot == nil {
		return b.client.BaseURL() + "/#/chat"
	}
	return b.client.BaseURL() + "/#/chat/bots/" + url.PathEscape(string(b.bots.Scope)) + "/" + bot.ID.String()
}

// ADK persisted content is normalized into the same transcript events used by
// chat. Durable approvals are fetched separately, never inferred from text or
// replayed legacy tool-approval deltas.
func botTranscriptEvents(frame client.SSEEvent) ([]events.Event, error) {
	var e adksession.Event
	if err := json.Unmarshal([]byte(frame.Data), &e); err != nil {
		return nil, fmt.Errorf("decode Bot transcript: %w", err)
	}
	if botHiddenTurnContext(&e) {
		return nil, nil
	}
	if raw, ok := e.Actions.StateDelta[botRuntimeStateKey]; ok {
		return mapBotRuntimeEnvelope(raw)
	}
	if events.IsBotReportInvocation(e.InvocationID) && e.Author != "user" && e.Content != nil && e.Content.Role == "model" {
		// Publications are complete immutable rows, not tool-loop text chunks.
		// Join parts before emitting so replay dedup applies to the entire row.
		var text strings.Builder
		for _, part := range e.Content.Parts {
			if part != nil && !part.Thought {
				text.WriteString(part.Text)
			}
		}
		if text.Len() == 0 {
			return nil, nil
		}
		return []events.Event{events.NewBotReport(e.InvocationID, e.ID, text.String())}, nil
	}
	out := []events.Event{}
	if e.Content != nil {
		for _, p := range e.Content.Parts {
			if p == nil {
				continue
			}
			if p.Text != "" {
				switch {
				case e.Author == "user" || e.Content.Role == "user":
					out = append(out, events.NewUser(botOwnerAnswerText(p.Text)))
				case p.Thought:
					out = append(out, events.Event{Kind: events.KindThinking, Text: p.Text})
				default:
					out = append(out, events.NewText(p.Text))
				}
			}
			if fc := p.FunctionCall; fc != nil {
				out = append(out, events.NewToolCall(fc.Name, fc.ID, fc.Args))
			}
			if fr := p.FunctionResponse; fr != nil {
				out = append(out, events.NewToolResult(fr.Name, fr.ID, fr.Response))
			}
		}
	}
	for path := range e.Actions.ArtifactDelta {
		out = append(out, events.Event{Kind: events.KindArtifact, Artifact: &events.Artifact{Path: path, FileName: filepath.Base(path)}})
	}
	if e.UsageMetadata != nil {
		out = append(out, events.Event{Kind: events.KindUsage, Usage: &events.Usage{Input: int64(e.UsageMetadata.PromptTokenCount), Output: int64(e.UsageMetadata.CandidatesTokenCount), Total: int64(e.UsageMetadata.TotalTokenCount)}})
	}
	// Metadata-only ADK rows are not text boundaries. Runtime envelopes above
	// carry their own explicit done event.
	if !e.Partial && len(out) > 0 {
		out = append(out, events.Event{Kind: events.KindDone})
	}
	return out, nil
}
