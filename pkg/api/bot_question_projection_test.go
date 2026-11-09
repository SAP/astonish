package api

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/session"
	"google.golang.org/genai"
)

func TestBostonQuestionProjection(t *testing.T) {
	data, err := os.ReadFile("testdata/boston-question-history.sse")
	if err != nil {
		t.Fatal(err)
	}
	approvalsData, err := os.ReadFile("testdata/boston-question-approvals.json")
	if err != nil {
		t.Fatal(err)
	}
	var saved struct {
		Approvals []store.BotApproval `json:"approvals"`
	}
	if err := json.Unmarshal(approvalsData, &saved); err != nil {
		t.Fatal(err)
	}
	hidden := 0
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(line, "data: {") || !strings.Contains(line, `"Author"`) {
			continue
		}
		var e session.Event
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &e); err != nil {
			t.Fatal(err)
		}
		projected := projectBotQuestionTranscript(&e, saved.Approvals)
		if projectBotQuestionTranscript(&e, nil) != &e {
			t.Fatal("hidden without canonical review")
		}
		if projected != &e {
			answered := append([]store.BotApproval(nil), saved.Approvals...)
			answered[0].Status = store.ApprovalApproved
			if projectBotQuestionTranscript(&e, answered).Content != nil {
				t.Fatal("old matched source reappeared after answer")
			}
			unrelated := e
			unrelated.InvocationID = "unrelated-invocation"
			if projectBotQuestionTranscript(&unrelated, saved.Approvals) != &unrelated {
				t.Fatal("hid unrelated source")
			}
			if e.Author == "model" {
				quoted := e
				content := *e.Content
				part := *content.Parts[0]
				part.Text = "A legitimate quoted marker: `" + part.Text + "`"
				content.Parts = []*genai.Part{&part}
				quoted.Content = &content
				if projectBotQuestionTranscript(&quoted, saved.Approvals) != &quoted {
					t.Fatal("hid ordinary quoted marker")
				}
			}
			user := e
			user.Author = "user"
			if projectBotQuestionTranscript(&user, saved.Approvals) != &user {
				t.Fatal("hid human quotation")
			}
			if e.Author == "assistant" {
				failure := e
				content := *e.Content
				failure.Content = &content
				part := *content.Parts[0]
				part.Text += "\nGenuine failure"
				content.Parts = []*genai.Part{&part}
				if projectBotQuestionTranscript(&failure, saved.Approvals) != &failure {
					t.Fatal("hid genuine failure")
				}
			}
			hidden++
			if projected.Content != nil {
				t.Fatal("technical content still visible")
			}
			if e.Content == nil {
				t.Fatal("mutated stored event")
			}
		}
	}
	if hidden != 2 {
		t.Fatalf("hidden=%d, want marker and waiting report only", hidden)
	}
}
