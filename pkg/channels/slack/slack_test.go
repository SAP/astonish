package slack

import (
	"context"
	"log"
	"testing"

	"github.com/SAP/astonish/pkg/channels"
	slackapi "github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
)

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

func TestHandleSlashCommandRequiresLinkCommand(t *testing.T) {
	ch := New(&Config{}, log.Default())
	called := false
	ch.SetLinkHandler(func(context.Context, string, string, string) (bool, string) {
		called = true
		return true, "linked"
	})

	got := ch.HandleSlashCommand(context.Background(), slackapi.SlashCommand{
		Command: "/status",
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
