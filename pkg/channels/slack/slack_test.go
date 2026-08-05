package slack

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/channels"
	slackapi "github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
	"github.com/slack-go/slack/socketmode"
)

func TestSlashCommandThreadTSFromSocketRequest(t *testing.T) {
	t.Parallel()
	req := &socketmode.Request{Payload: []byte(`{"type":"slash_commands","command":"/astonish-help","thread_ts":"1355517523.000005"}`)}
	if got := slashCommandThreadTSFromSocketRequest(req); got != "1355517523.000005" {
		t.Fatalf("thread timestamp = %q, want payload thread_ts", got)
	}

	req = &socketmode.Request{Payload: []byte(`{"type":"slash_commands","container":{"message_ts":"2468013579.000002"}}`)}
	if got := slashCommandThreadTSFromSocketRequest(req); got != "2468013579.000002" {
		t.Fatalf("thread timestamp = %q, want container message_ts", got)
	}
}

func TestSlackThreadTimestampIgnoresSlashCommandTriggerIDs(t *testing.T) {
	t.Parallel()
	if got := slackThreadTimestamp("", "TRIGGER123"); got != "" {
		t.Fatalf("slash command trigger ID resolved as thread timestamp: %q", got)
	}
	if got := slackThreadTimestamp("", "/astonish-help:U123"); got != "" {
		t.Fatalf("synthetic command ID resolved as thread timestamp: %q", got)
	}
	if got := slackThreadTimestamp("1355517523.000005", "TRIGGER123"); got != "1355517523.000005" {
		t.Fatalf("target thread timestamp = %q", got)
	}
	if got := slackThreadTimestamp("", "1355517523.000005"); got != "1355517523.000005" {
		t.Fatalf("reply timestamp = %q", got)
	}
}

func TestHandleSlashCommandLinksAccount(t *testing.T) {
	ch := New(&Config{}, log.Default())
	ch.SetLinkHandler(func(_ context.Context, senderID, senderName, code string) (bool, string) {
		if senderID != "U123" {
			t.Fatalf("senderID = %q, want U123", senderID)
		}
		if senderName != "alice" {
			t.Fatalf("senderName = %q, want alice", senderName)
		}
		if code != "ABC123" {
			t.Fatalf("code = %q, want ABC123", code)
		}
		return true, "linked"
	})

	got := ch.HandleSlashCommand(context.Background(), slackapi.SlashCommand{
		Command:  "/link",
		Text:     " ABC123 ",
		UserID:   "U123",
		UserName: "alice",
	})
	if got != "linked" {
		t.Fatalf("response = %q, want linked", got)
	}
	if !ch.isAllowed("U123") {
		t.Fatal("expected successful link to add the Slack user to the allowlist")
	}
}

func TestHandleSlashCommandWithThreadRoutesRegistryCommandToThread(t *testing.T) {
	registry := channels.NewCommandRegistry()
	registry.Register(&channels.Command{Name: "status", Description: "Show status"})
	ch := New(&Config{AllowFrom: []string{"U123"}, Commands: registry}, log.Default())

	inboundCh := make(chan channels.InboundMessage, 1)
	ch.handler = func(_ context.Context, msg channels.InboundMessage) error {
		inboundCh <- msg
		return nil
	}

	got := ch.handleSlashCommandWithThread(context.Background(), slackapi.SlashCommand{
		Command:     "/astonish-status",
		UserID:      "U123",
		UserName:    "alice",
		ChannelID:   "C123",
		ChannelName: "general",
		TriggerID:   "TRIGGER123",
	}, "1355517523.000005")
	if got != "Running /astonish-status…" {
		t.Fatalf("response = %q, want running acknowledgement", got)
	}

	select {
	case inbound := <-inboundCh:
		if inbound.ThreadID != "1355517523.000005" {
			t.Fatalf("thread ID = %q, want raw Slack thread timestamp", inbound.ThreadID)
		}
		if inbound.ChatType != channels.ChatTypeChannel {
			t.Fatalf("chat type = %q, want channel", inbound.ChatType)
		}
	case <-time.After(time.Second):
		t.Fatal("expected slash command to be routed to channel handler")
	}
}

func TestHandleSlashCommandRoutesRegistryCommand(t *testing.T) {
	registry := channels.NewCommandRegistry()
	registry.Register(&channels.Command{Name: "status", Description: "Show status"})
	ch := New(&Config{AllowFrom: []string{"U123"}, Commands: registry}, log.Default())

	inboundCh := make(chan channels.InboundMessage, 1)
	ch.handler = func(_ context.Context, msg channels.InboundMessage) error {
		inboundCh <- msg
		return nil
	}

	got := ch.HandleSlashCommand(context.Background(), slackapi.SlashCommand{
		Command:     "/astonish-status",
		Text:        "verbose",
		UserID:      "U123",
		UserName:    "alice",
		ChannelID:   "D123",
		ChannelName: "directmessage",
		TriggerID:   "TRIGGER123",
		ResponseURL: "https://hooks.slack.test/commands/123",
	})
	if got != "Running /astonish-status…" {
		t.Fatalf("response = %q, want running acknowledgement", got)
	}

	select {
	case inbound := <-inboundCh:
		if inbound.Text != "/status verbose" {
			t.Fatalf("inbound text = %q", inbound.Text)
		}
		if inbound.ChatType != channels.ChatTypeDirect {
			t.Fatalf("chat type = %q, want direct", inbound.ChatType)
		}
		if inbound.SenderID != "U123" || inbound.ChatID != "D123" {
			t.Fatalf("unexpected inbound: %#v", inbound)
		}
		if got := ch.takeResponseURL(inbound.ID); got != "https://hooks.slack.test/commands/123" {
			t.Fatalf("stored response URL = %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("expected slash command to be routed to channel handler")
	}
}

func TestSendUsesSlashCommandResponseURLWithoutAPIClient(t *testing.T) {
	var got slackResponseURLPayload
	requestCh := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s, want POST", r.Method)
		}
		if contentType := r.Header.Get("Content-Type"); contentType != "application/json" {
			t.Fatalf("content type = %q, want application/json", contentType)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode response URL payload: %v", err)
		}
		requestCh <- struct{}{}
	}))
	defer server.Close()

	ch := New(&Config{}, log.Default())
	ch.storeResponseURL("TRIGGER123", server.URL)

	err := ch.Send(context.Background(), channels.Target{
		ChannelID: "slack",
		ChatID:    "C123",
	}, channels.OutboundMessage{
		Text:    "command result",
		ReplyTo: "TRIGGER123",
		Format:  channels.FormatText,
	})
	if err != nil {
		t.Fatalf("Send returned error: %v", err)
	}

	select {
	case <-requestCh:
	case <-time.After(time.Second):
		t.Fatal("expected response URL request")
	}
	if got.ResponseType != "ephemeral" {
		t.Fatalf("response type = %q, want ephemeral", got.ResponseType)
	}
	if got.Text != "command result" {
		t.Fatalf("response text = %q, want command result", got.Text)
	}
	if got := ch.takeResponseURL("TRIGGER123"); got != "" {
		t.Fatalf("response URL was not consumed: %q", got)
	}
}

func TestHandleSlashCommandRequiresKnownRegisteredCommand(t *testing.T) {
	ch := New(&Config{}, log.Default())
	called := false
	ch.SetLinkHandler(func(context.Context, string, string, string) (bool, string) {
		called = true
		return true, "linked"
	})

	got := ch.HandleSlashCommand(context.Background(), slackapi.SlashCommand{
		Command: "/unknown",
		Text:    "ABC123",
		UserID:  "U123",
	})
	if called {
		t.Fatal("link handler should not be called for unsupported slash commands")
	}
	if got == "" {
		t.Fatal("expected an explanatory response for unsupported slash commands")
	}
}

func TestHandleSlashCommandRejectsSessionScopedCommands(t *testing.T) {
	registry := channels.NewCommandRegistry()
	registry.Register(&channels.Command{Name: "new", Description: "Start fresh", SessionScoped: true})
	ch := New(&Config{AllowFrom: []string{"U123"}, Commands: registry}, log.Default())
	ch.handler = func(context.Context, channels.InboundMessage) error {
		t.Fatal("handler should not be called for a session-scoped Slack slash command")
		return nil
	}

	got := ch.HandleSlashCommand(context.Background(), slackapi.SlashCommand{
		Command: "/astonish-new",
		UserID:  "U123",
	})
	if got == "" || got == "Running /astonish-new…" {
		t.Fatalf("response = %q, want unsupported-command guidance", got)
	}
}

func TestHandleSlashCommandRequiresLinkedAccountForRegistryCommand(t *testing.T) {
	registry := channels.NewCommandRegistry()
	registry.Register(&channels.Command{Name: "status", Description: "Show status"})
	ch := New(&Config{Commands: registry}, log.Default())
	ch.handler = func(context.Context, channels.InboundMessage) error {
		t.Fatal("handler should not be called for an unauthorized sender")
		return nil
	}

	got := ch.HandleSlashCommand(context.Background(), slackapi.SlashCommand{
		Command: "/status",
		UserID:  "U123",
	})
	if got == "" || got == "Running /status…" {
		t.Fatalf("response = %q, want link-first guidance", got)
	}
}

func TestHandleAppMentionRoutesBareCommandInThread(t *testing.T) {
	registry := channels.NewCommandRegistry()
	registry.Register(&channels.Command{Name: "status", Description: "Show status"})
	ch := New(&Config{AllowFrom: []string{"U123"}, Commands: registry}, log.Default())
	ch.botUserID = "UBOT"

	inboundCh := make(chan channels.InboundMessage, 1)
	ch.handler = func(_ context.Context, msg channels.InboundMessage) error {
		inboundCh <- msg
		return nil
	}

	ch.handleAppMention(context.Background(), &slackevents.AppMentionEvent{
		User:            "U123",
		Text:            "<@UBOT> status verbose",
		TimeStamp:       "2000.000001",
		ThreadTimeStamp: "1000.000001",
		Channel:         "C123",
	}, "T123")

	select {
	case got := <-inboundCh:
		if got.Text != "/status verbose" {
			t.Fatalf("mention command text = %q, want normalized slash command", got.Text)
		}
		if got.ThreadID != "1000.000001" {
			t.Fatalf("thread ID = %q, want Slack thread timestamp", got.ThreadID)
		}
		if got.ChatType != channels.ChatTypeChannel {
			t.Fatalf("chat type = %q, want channel", got.ChatType)
		}
	case <-time.After(time.Second):
		t.Fatal("expected app mention command to route to handler")
	}
}

func TestHandleAppMentionRoutesSlackAliasCommandInThread(t *testing.T) {
	registry := channels.NewCommandRegistry()
	registry.Register(&channels.Command{Name: "status", Description: "Show status"})
	ch := New(&Config{AllowFrom: []string{"U123"}, Commands: registry}, log.Default())
	ch.botUserID = "UBOT"

	inboundCh := make(chan channels.InboundMessage, 1)
	ch.handler = func(_ context.Context, msg channels.InboundMessage) error {
		inboundCh <- msg
		return nil
	}

	ch.handleAppMention(context.Background(), &slackevents.AppMentionEvent{
		User:            "U123",
		Text:            "<@UBOT> /astonish-status verbose",
		TimeStamp:       "2000.000001",
		ThreadTimeStamp: "1000.000001",
		Channel:         "C123",
	}, "T123")

	select {
	case got := <-inboundCh:
		if got.Text != "/status verbose" {
			t.Fatalf("mention command text = %q, want normalized slash command", got.Text)
		}
		if got.ThreadID != "1000.000001" {
			t.Fatalf("thread ID = %q, want Slack thread timestamp", got.ThreadID)
		}
	case <-time.After(time.Second):
		t.Fatal("expected app mention command to route to handler")
	}
}

func TestHandleAppMentionLeavesNonCommandPromptsUntouched(t *testing.T) {
	registry := channels.NewCommandRegistry()
	registry.Register(&channels.Command{Name: "status", Description: "Show status"})
	ch := New(&Config{AllowFrom: []string{"U123"}, Commands: registry}, log.Default())
	ch.botUserID = "UBOT"

	inboundCh := make(chan channels.InboundMessage, 1)
	ch.handler = func(_ context.Context, msg channels.InboundMessage) error {
		inboundCh <- msg
		return nil
	}

	ch.handleAppMention(context.Background(), &slackevents.AppMentionEvent{
		User:            "U123",
		Text:            "<@UBOT> summarize this thread",
		TimeStamp:       "2000.000001",
		ThreadTimeStamp: "1000.000001",
		Channel:         "C123",
	}, "T123")

	select {
	case got := <-inboundCh:
		if got.Text != "summarize this thread" {
			t.Fatalf("mention text = %q, want non-command prompt unchanged", got.Text)
		}
	case <-time.After(time.Second):
		t.Fatal("expected app mention prompt to route to handler")
	}
}

func TestHandleMessageAcceptsAssistantAppThreadSubtype(t *testing.T) {
	ch := New(&Config{AllowFrom: []string{"U123"}}, log.Default())
	var got channels.InboundMessage
	ch.handler = func(_ context.Context, msg channels.InboundMessage) error {
		got = msg
		return nil
	}

	ch.handleMessage(context.Background(), &slackevents.MessageEvent{
		User:        "U123",
		Text:        "hello from app agent DM",
		TimeStamp:   "1355517523.000005",
		Channel:     "D123",
		ChannelType: "im",
		SubType:     slackapi.MsgSubTypeAssistantAppThread,
	}, "T123")

	if got.Text != "hello from app agent DM" {
		t.Fatalf("message text = %q, want assistant-thread DM text", got.Text)
	}
	if got.ChatType != channels.ChatTypeDirect {
		t.Fatalf("chat type = %q, want direct", got.ChatType)
	}
}

func TestHandleMessageScopesSessionToSlackThread(t *testing.T) {
	ch := New(&Config{AllowFrom: []string{"U123"}}, log.Default())
	inboundCh := make(chan channels.InboundMessage, 1)
	ch.handler = func(_ context.Context, msg channels.InboundMessage) error {
		inboundCh <- msg
		return nil
	}

	ch.handleMessage(context.Background(), &slackevents.MessageEvent{
		User:            "U123",
		Text:            "thread reply",
		TimeStamp:       "2000.000001",
		ThreadTimeStamp: "1000.000001",
		Channel:         "D123",
		ChannelType:     "im",
	}, "T123")

	select {
	case got := <-inboundCh:
		if got.ThreadID != "1000.000001" {
			t.Fatalf("thread reply ThreadID = %q, want parent thread timestamp", got.ThreadID)
		}
	case <-time.After(time.Second):
		t.Fatal("expected inbound Slack message")
	}

	ch.handleMessage(context.Background(), &slackevents.MessageEvent{
		User:        "U123",
		Text:        "new top-level message",
		TimeStamp:   "3000.000001",
		Channel:     "D123",
		ChannelType: "im",
	}, "T123")

	select {
	case got := <-inboundCh:
		if got.ThreadID != "3000.000001" {
			t.Fatalf("top-level message ThreadID = %q, want message timestamp", got.ThreadID)
		}
	case <-time.After(time.Second):
		t.Fatal("expected inbound Slack message")
	}
}

func TestHandleMessageAcceptsDirectMessageChannelFallback(t *testing.T) {
	ch := New(&Config{AllowFrom: []string{"U123"}}, log.Default())
	var got channels.InboundMessage
	ch.handler = func(_ context.Context, msg channels.InboundMessage) error {
		got = msg
		return nil
	}

	ch.handleMessage(context.Background(), &slackevents.MessageEvent{
		User:      "U123",
		Text:      "hello without channel type",
		TimeStamp: "1355517523.000005",
		Channel:   "D123",
	}, "T123")

	if got.Text != "hello without channel type" {
		t.Fatalf("message text = %q, want DM text", got.Text)
	}
}

func TestHandleSlashCommandReportsMissingCode(t *testing.T) {
	ch := New(&Config{}, log.Default())
	called := false
	ch.SetLinkHandler(func(context.Context, string, string, string) (bool, string) {
		called = true
		return true, "linked"
	})

	got := ch.HandleSlashCommand(context.Background(), slackapi.SlashCommand{
		Command: "/link",
		UserID:  "U123",
	})
	if called {
		t.Fatal("link handler should not be called without a code")
	}
	if want := "Usage: /link CODE\n\nGet a link code from the Astonish Settings → Channels page."; got != want {
		t.Fatalf("response = %q, want %q", got, want)
	}
}
