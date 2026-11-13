package agent

import (
	"context"
	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"strings"
	"testing"
)

func TestBotSkillDiscoveryPromptOptionalBounded(t *testing.T) {
	for _, scenario := range []string{"heads", "unavailable", "oversized"} {
		t.Run(scenario, func(t *testing.T) {
			req := botSkillPromptFixture(t)
			req.Work = store.NewBotAgentWorkState(req.Work.Contract)
			req.Work.SkillState = &store.BotSkillRunState{Stage: store.BotSkillOriented}
			po := NewBotPromptOverrides(nil, req)
			b := &SystemPromptBuilder{}
			b.applyBotPrompt(po)
			result := &bot.BotWorkReadResult{Work: store.BuildBotWorkProjection(req.Work), Notes: "retained useful note", SkillIndex: []bot.BotWorkSkillHead{{ID: uuid.New(), Title: "Weather discovery", Status: store.BotSkillActive, HeadRevision: 2}}, SkillNext: "next-page"}
			switch scenario {
			case "unavailable":
				result.SkillIndex = nil
				result.SkillNext = ""
				result.SkillIndexUnavailable = true
			case "oversized":
				result.SkillIndex[0].Title = strings.Repeat("x", bot.BotWorkSkillIndexMaxBytes+1)
			}
			ctx := bot.WithBotWorkServices(context.Background(), botPromptWorkReader{read: func(context.Context, bot.BotWorkReadRequest) (*bot.BotWorkReadResult, error) { return result, nil }})
			got := b.BuildBotPrompt(ctx, po)
			for _, want := range []string{"retained useful note", "METHOD-START", "METHOD-END", "or work without one", "as after to bot_skill_list", "never grant permissions"} {
				if !strings.Contains(got, want) {
					t.Errorf("missing %q", want)
				}
			}
			if scenario == "heads" && (!strings.Contains(got, "Weather discovery") || !strings.Contains(got, "next-page")) {
				t.Fatal("index missing")
			}
			if scenario != "heads" && (!strings.Contains(got, "skill_index_unavailable") || strings.Contains(got, strings.Repeat("x", 100))) {
				t.Fatal("optional index damaged prompt")
			}
			if scenario == "oversized" && len(result.SkillIndex[0].Title) != bot.BotWorkSkillIndexMaxBytes+1 {
				t.Fatal("mutated shared read")
			}
		})
	}
}
