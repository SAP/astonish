package daemon

import (
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/model"
	"google.golang.org/genai"
)

func TestSkillScheduleDeniedFixturePreservesUnfinishedWork(t *testing.T) {
	w := &store.BotWorkState{Protocol: store.BotWorkProtocolAgentLedV1, Revision: 9}
	m := &skillScheduleModel{t: t, denied: true, stage: 3, work: func() *store.BotWorkState { return w }}
	for _, kind := range []bot.BotWorkChangeKind{bot.BotWorkSetRemaining, bot.BotWorkSetDisposition} {
		for response, err := range m.agentContent() {
			if err != nil {
				t.Fatal(err)
			}
			call := response.Content.Parts[0].FunctionCall
			if call == nil || call.Name != "execute_tool" || call.Args["name"] != "bot_work_update" {
				t.Fatalf("missing real work update: %+v", call)
			}
			args := call.Args["arguments"].(map[string]any)
			if args["expected_revision"] != w.Revision || args["kind"] != string(kind) {
				t.Fatalf("wrong update: %+v", args)
			}
			if kind == bot.BotWorkSetRemaining && len(args["remaining"].([]map[string]any)) != 1 {
				t.Fatal("lost obligation")
			}
			if kind == bot.BotWorkSetDisposition && args["disposition"] != string(store.AgentWorkBlocked) {
				t.Fatal("lost blocked disposition")
			}
		}
	}
	for response, err := range m.agentContent() {
		if err != nil || !strings.Contains(response.Content.Parts[0].Text, "couldn't create") || response.Content.Parts[0].FunctionCall != nil {
			t.Fatalf("untruthful final: %+v %v", response, err)
		}
	}
}

func TestDraftApprovalFixtureRoutesActionThroughPrivateWorker(t *testing.T) {
	inner := &draftApprovalHTTPModel{attempt: true}
	m := mediateDaemonModel(inner)
	req := &model.LLMRequest{Tools: map[string]any{"employee_chat": struct{}{}}}
	for response, err := range m.GenerateContent(t.Context(), req, false) {
		if err != nil || response.Content.Parts[0].FunctionCall.Name != "employee_chat" || !inner.attempt {
			t.Fatal("owner executed action directly")
		}
	}
	for response, err := range m.GenerateContent(t.Context(), &model.LLMRequest{}, false) {
		if err != nil || response.Content.Parts[0].FunctionCall.Name != "execute_tool" {
			t.Fatal("private action not attempted")
		}
	}
	req.Contents = []*genai.Content{{Role: "tool", Parts: []*genai.Part{{FunctionResponse: &genai.FunctionResponse{Name: "employee_chat", Response: map[string]any{"private_text": "actual private result"}}}}}}
	for response, err := range m.GenerateContent(t.Context(), req, false) {
		if err != nil || response.Content.Parts[0].Text != "actual private result" {
			t.Fatal("owner fabricated final")
		}
	}
}
