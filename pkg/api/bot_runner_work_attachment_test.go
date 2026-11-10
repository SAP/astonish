package api

import (
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/store"
	"testing"
)

func TestBotWorkAttachmentAssessmentBoundary(t *testing.T) {
	for _, mime := range []string{"text/plain", "application/pdf"} {
		t.Run(mime, func(t *testing.T) {
			ctx, req, _ := botChatTestRequest(t)
			req.Work = store.NewBotWorkState(store.BotWorkContract{BotID: req.Bot.ID, RunID: req.Run.ID, Version: req.Run.Version, Instructions: req.Instructions, Charter: req.Version.Charter, Attachments: store.PinBotWorkAttachments([]store.ChatFile{{Filename: "spec", MimeType: mime, Data: []byte("Must include audited revenue")}})})
			ws := &workMemoryStore{state: req.Work.Clone()}
			mdl := &workAssessmentModel{t: t}
			produced := false
			result, err := runBotWork(ctx, req, &BotChatResources{Work: ws}, mdl, false, func(bot.RunRequest, *ChatCompletion) (bot.RunResult, error) {
				produced = true
				return bot.RunResult{}, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if produced || result.Status != store.RunPartial || ws.state.Phase != store.BotWorkBlocked {
				t.Fatalf("uncovered/uninspectable attachment escaped: %+v", result)
			}
			if mime == "application/pdf" && mdl.calls != 0 {
				t.Fatal("unsupported attachment sent for pretend assessment")
			}
			// The controlled prepare model omits attachment source IDs: text input must
			// fail coverage validation, rather than silently evaluating only the prose.
			if mime == "text/plain" && mdl.calls != 1 {
				t.Fatal("supported text did not reach preparation")
			}
		})
	}
}
