package email

import (
	"reflect"
	"strings"
	"testing"
)

func TestReplySnapshotExactRecipientsAndDigest(t *testing.T) {
	a := Account{Provider: "msgraph", Address: "owner@example.com", Credential: "mail"}
	msg := &Message{ID: "42", From: "Sender <sender@example.com>", To: []string{"owner@example.com", "copy@example.com"}, CC: []string{"COPY@example.com", "other@example.com"}, Subject: "Topic", Body: "original", Headers: map[string]string{"Reply-To": "Reply <reply@example.com>", "Message-ID": "<id@example.com>", "References": "<ancestor@example.com>"}}
	source, err := NewReplySource(msg)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := NewReplySnapshot(a, source, "reply", "<b>reply</b>", true)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(snapshot.Message.To, []string{"reply@example.com"}) || !reflect.DeepEqual(snapshot.Message.CC, []string{"copy@example.com", "other@example.com"}) || len(snapshot.Message.Attachments) != 0 {
		t.Fatalf("wrong recipients: %+v", snapshot)
	}
	if snapshot.References != "<ancestor@example.com> <id@example.com>" {
		t.Fatal("thread not preserved")
	}
	msg.Unread = true
	same, _ := NewReplySource(msg)
	if same.Digest != source.Digest {
		t.Fatal("read state invalidated snapshot")
	}
	msg.Body = "changed"
	changed, _ := NewReplySource(msg)
	if changed.Digest == source.Digest {
		t.Fatal("body not bound")
	}
	for _, mutate := range []func(*ReplySnapshot){
		func(s *ReplySnapshot) { s.Account.Address = "other@example.com" },
		func(s *ReplySnapshot) { s.OriginalDigest = strings.Repeat("a", 64) },
		func(s *ReplySnapshot) { s.Message.To = []string{"other@example.com"} },
		func(s *ReplySnapshot) { s.Message.CC = []string{"other@example.com"} },
		func(s *ReplySnapshot) { s.Message.Body = "changed" },
		func(s *ReplySnapshot) { s.Message.HTML = "changed" },
		func(s *ReplySnapshot) { s.Message.Subject = "changed" },
		func(s *ReplySnapshot) { s.InReplyTo = "changed" },
	} {
		copy := snapshot
		mutate(&copy)
		if copy.Fingerprint() == snapshot.Fingerprint() {
			t.Fatal("target mutation not bound")
		}
	}
}

func TestReplySnapshotRejectsAmbiguousAndInjectedHeaders(t *testing.T) {
	for _, headers := range []map[string]string{
		{"Reply-To": "a@example.com", "reply-to": "b@example.com"},
		{"Message-ID": "a", "message-id": "b"},
	} {
		if _, err := NewReplySource(&Message{ID: "42", Headers: headers}); err == nil {
			t.Fatal("ambiguous header accepted")
		}
	}
	for _, values := range []map[string][]string{
		{"Reply-To": {"a@example.com", "b@example.com"}},
		{"Reply-To": {"a@example.com"}, "reply-to": {"b@example.com"}},
	} {
		if _, err := NewReplySource(&Message{ID: "42", HeaderValues: values}); err == nil {
			t.Fatal("ambiguous occurrences accepted")
		}
	}
	a := Account{Provider: "msgraph", Address: "owner@example.com", Credential: "mail"}
	for _, source := range []ReplySource{
		{ID: "42", Digest: strings.Repeat("a", 64), From: "a@example.com\r\nBcc: attacker@example.com"},
		{ID: "42", Digest: strings.Repeat("a", 64), From: "a@example.com", Subject: "topic\r\nBcc: attacker@example.com"},
		{ID: "42", Digest: strings.Repeat("a", 64), From: "a@example.com", Headers: map[string]string{"Message-ID": "id\r\nBcc: attacker@example.com"}},
		{ID: "42", Digest: strings.Repeat("a", 64), From: "a@example.com", Headers: map[string]string{"Reply-To": "a@example.com, b@example.com"}},
	} {
		if _, err := NewReplySnapshot(a, source, "reply", "", true); err == nil {
			t.Fatal("injected or ambiguous header accepted")
		}
	}
}
