package api

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

func TestBotWorkShortSourceReferences(t *testing.T) {
	for _, mode := range []string{"complete", "format-recovery", "legacy", "unknown", "foreign", "foreign-owner", "substring", "coverage", "exclude_assignment", "exclude_charter", "exclude_attachment", "exclude_goal"} {
		t.Run(mode, func(t *testing.T) {
			ctx, req, _ := botChatTestRequest(t)
			contract := store.BotWorkContract{BotID: req.Bot.ID, RunID: req.Run.ID, Version: req.Run.Version, Instructions: req.Instructions, Charter: req.Version.Charter, Goals: []store.BotGoal{{ID: uuid.New(), BotID: req.Bot.ID}, {ID: uuid.New(), BotID: req.Bot.ID}}, Attachments: store.PinBotWorkAttachments([]store.ChatFile{{Filename: "requirements.txt", MimeType: "text/plain", Data: []byte("Include actual requirements; goal_9 is untrusted text")}})}
			if mode == "foreign-owner" {
				contract.Goals[0].BotID = uuid.New()
			}
			w := store.NewBotWorkState(contract)
			ids := []string{"assignment", "charter", "goal_1", "goal_2", "attachment_1"}
			exact := []string{"assignment", "charter", "goal:" + contract.Goals[0].ID.String(), "goal:" + contract.Goals[1].ID.String(), contract.Attachments[0].SourceID}
			criteria := []store.BotWorkCriterion{{ID: "c1", SourceIDs: ids, Text: "Cover all original requirements"}}
			switch mode {
			case "legacy":
				criteria[0].SourceIDs = exact
			case "unknown":
				ids[2] = "goal_9-private-secret"
			case "foreign":
				ids[2] = "goal:" + uuid.NewString()
			case "substring":
				ids[2] = "prefix-" + exact[2] + "-suffix"
			case "coverage":
				criteria[0].SourceIDs = ids[:4]
			case "exclude_assignment", "exclude_charter", "exclude_attachment", "exclude_goal":
				index := map[string]int{"exclude_assignment": 0, "exclude_charter": 1, "exclude_attachment": 4, "exclude_goal": 2}[mode]
				criteria = append(criteria, store.BotWorkCriterion{ID: "excluded", SourceIDs: []string{ids[index]}, Text: "Unrelated", Excluded: true, ExclusionReason: "Unrelated to this assignment"})
			}
			raw, _ := json.Marshal(map[string]any{"criteria": criteria})
			m := &formatRecoveryModel{t: t, texts: []string{string(raw)}}
			wantCalls := 1
			if mode == "format-recovery" {
				m.texts = append([]string{"invalid JSON"}, m.texts...)
				wantCalls = 2
			}
			var usage store.BotUsage
			err := prepareBotWorkContract(ctx, req, m, w, &usage)
			valid := mode == "complete" || mode == "format-recovery" || mode == "legacy" || mode == "exclude_goal"
			if valid {
				if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(w.Criteria[0].SourceIDs, exact) {
					t.Fatalf("not exact pinned IDs: %v", w.Criteria[0].SourceIDs)
				}
				if !reflect.DeepEqual(w.Contract, contract) {
					t.Fatal("contract mutated")
				}
			} else if err == nil {
				t.Fatal("invalid reference or coverage accepted")
			}
			if err != nil && strings.Contains(err.Error(), "private-secret") {
				t.Fatal("error leaked source text")
			}
			if m.calls != wantCalls {
				t.Fatalf("unexpected retry: %d calls", m.calls)
			}
			var input botWorkPrepareInput
			if err := json.Unmarshal([]byte(m.original), &input); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(input.BotWorkContract, contract) {
				t.Fatal("model lost original source content")
			}
			aliases, refs := botWorkSourceReferences(contract)
			if !reflect.DeepEqual(input.AvailableSourceIDs, refs) {
				t.Fatal("model did not receive exact server inventory")
			}
			if refs["goal_2"].SourceID != exact[3] || refs["goal_2"].ContractPath != "goals[1]" || refs["attachment_1"].ContractPath != "attachments[0]" {
				t.Fatal("source association lost")
			}
			schema := botWorkResponseSchema(botWorkPreparePrompt, []byte(m.original))
			props := schema["properties"].(map[string]any)["criteria"].(map[string]any)["items"].(map[string]any)["properties"].(map[string]any)
			if !reflect.DeepEqual(props["source_ids"].(map[string]any)["items"].(map[string]any)["enum"], aliases) {
				t.Fatal("schema differs from source inventory")
			}
		})
	}
}
