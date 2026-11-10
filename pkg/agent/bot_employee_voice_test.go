package agent

import (
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
)

// These contracts verify hidden guidance and mode boundaries, not model judgment.
func TestBotEmployeeVoiceModes(t *testing.T) {
	for _, tc := range []struct {
		name       string
		req        bot.RunRequest
		followUp   bool
		background bool
		control    bool
	}{
		{name: "interactive-chat", req: bot.RunRequest{Run: store.BotRun{ExecutionMode: execution.BotExecutionModeChatInteractive}}, followUp: true},
		{name: "home-owner-turn", req: bot.RunRequest{Run: store.BotRun{Intent: store.BotIntentRegular, Presentation: store.BotRunPresentationHomeInteractive}}, followUp: true},
		{name: "interactive-request", req: bot.RunRequest{Interactive: true}, followUp: true},
		{name: "scheduled", req: bot.RunRequest{Run: store.BotRun{Trigger: store.TriggerCron}}, background: true},
		{name: "autonomous", background: true},
		{name: "control-only", req: bot.RunRequest{Run: store.BotRun{Intent: store.BotIntentDraftConversation}}, control: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			po := NewBotPromptOverrides(nil, tc.req)
			b := &SystemPromptBuilder{}
			if !b.applyBotPrompt(po) {
				t.Fatal("Bot prompt not applied")
			}
			prompt := b.Build()
			for _, want := range []string{
				"Communicate like a capable employee",
				"lead with the useful requested information",
				"Be concise and natural",
				"run-report/status headers, quality-criterion dumps, tool logs and internal IDs out of user-facing prose",
				"preserve useful citations, required canonical control references and genuine limitations",
				"scope you chose as an assumption, not a user requirement",
				"US markets for an unspecified market briefing",
				"not redundant confirmation or mandatory sample approval",
				"Honor typed runtime policy, approval requirements and budget denials",
				"Never parse prose as consent",
				"Only a committed receipt proves application",
				"unknown effects are never allowed",
				"neither owner sample approval nor mandatory skill creation",
			} {
				if !strings.Contains(prompt, want) {
					t.Errorf("missing voice or authority contract %q", want)
				}
			}
			for _, want := range []string{
				"For an initial broad interactive assignment, after delivering useful work",
				"optionally suggest one relevant refinement or offer daily/weekly updates",
				"do not append a canned question or a rote 'is it good?' check",
				"Skip the offer when recurrence is already configured, the owner declined, or it adds no value",
				"An offer is not consent",
				"an actual owner request and a known task, cadence/time and timezone",
				"using trusted context for details already supplied",
				"do not ask the owner to reconfirm a clear request",
				"All existing mandate, runtime authorization and committed-receipt requirements still apply",
			} {
				if strings.Contains(prompt, want) != tc.followUp {
					t.Errorf("followUp=%v: contract %q", tc.followUp, want)
				}
			}
			const background = "do not solicit feedback, offer recurring updates or repeat scheduling upsells"
			if strings.Contains(prompt, background) != tc.background {
				t.Errorf("background=%v: wrong follow-up suppression", tc.background)
			}
			if tc.control && !strings.Contains(prompt, "Do not call external tools, claim external work was performed, or treat conversation as consent") {
				t.Error("lost control-only restriction")
			}
			if tc.req.Run.IsInteractiveChat() && !strings.Contains(prompt, "call the actual ask_user tool for one focused clarification") {
				t.Error("lost structured clarification guidance")
			}
			if buildTurnContextContent(po, "", "") != nil {
				t.Error("voice guidance leaked into visible user turn")
			}
		})
	}
}

func TestBotEmployeeVoiceExcludesCodeAndOrdinaryChat(t *testing.T) {
	for _, codeMode := range []bool{false, true} {
		b := &SystemPromptBuilder{CodeMode: codeMode, SchedulerHints: "existing hints"}
		before := b.Build()
		po := &PromptOverrides{}
		if codeMode {
			po = NewBotPromptOverrides(nil, bot.RunRequest{Interactive: true})
		}
		if b.applyBotPrompt(po) || b.Build() != before {
			t.Fatalf("Bot voice changed non-Bot prompt (CodeMode=%v)", codeMode)
		}
		if strings.Contains(b.Build(), "Communicate like a capable employee") {
			t.Fatalf("Bot voice leaked (CodeMode=%v)", codeMode)
		}
	}
}
