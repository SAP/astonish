package api

import (
	"testing"

	"github.com/SAP/astonish/pkg/agent"
)

func TestMaybeEmitTutorialBlueprint_ReturnsTrueOnAwaitingApproval(t *testing.T) {
	cr := newChatRunner("sess-bp", "user1", studioChatAppName, false)
	chatAgent := &agent.ChatAgent{}

	resp := map[string]any{
		"status":                     "awaiting_approval",
		"present_tutorial_blueprint": true,
		"blueprint_yaml":             "type: tutorial_blueprint\nsuite: demo\ntitle: Open Studio\nscenes: []\n",
		"title":                      "Open Studio",
		"suite":                      "demo",
		"scenes": []any{
			map[string]any{
				"id": "hook", "title": "Hook", "voiceover": "Hi.",
				"visual": map[string]any{"kind": "avatar", "description": "Presenter"},
			},
		},
	}

	if !cr.maybeEmitTutorialBlueprint(chatAgent, nil, "present_tutorial_blueprint", resp) {
		t.Fatal("expected true when card is emitted")
	}
	if !chatAgent.HasPendingTutorialBlueprint("sess-bp") {
		t.Fatal("expected pending blueprint to be set")
	}
	events := cr.snapshotEvents()
	found := false
	for _, ev := range events {
		if ev.Type == "tutorial_blueprint_preview" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected tutorial_blueprint_preview event, got %#v", events)
	}
}

func TestMaybeEmitTutorialBlueprint_ReturnsFalseOtherwise(t *testing.T) {
	cr := newChatRunner("sess-bp2", "user1", studioChatAppName, false)
	chatAgent := &agent.ChatAgent{}

	cases := []struct {
		name     string
		toolName string
		resp     map[string]any
	}{
		{"wrong tool", "run_drill", map[string]any{"status": "awaiting_approval", "present_tutorial_blueprint": true, "blueprint_yaml": "x"}},
		{"nil resp", "present_tutorial_blueprint", nil},
		{"error status", "present_tutorial_blueprint", map[string]any{"status": "error", "present_tutorial_blueprint": true, "blueprint_yaml": "x"}},
		{"present false", "present_tutorial_blueprint", map[string]any{"status": "awaiting_approval", "present_tutorial_blueprint": false, "blueprint_yaml": "x"}},
		{"empty yaml", "present_tutorial_blueprint", map[string]any{"status": "awaiting_approval", "present_tutorial_blueprint": true, "blueprint_yaml": ""}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if cr.maybeEmitTutorialBlueprint(chatAgent, nil, tc.toolName, tc.resp) {
				t.Fatal("expected false")
			}
		})
	}
}

func TestMaybeEmitTutorialBlueprint_AcceptsLegacyOkStatus(t *testing.T) {
	cr := newChatRunner("sess-bp3", "user1", studioChatAppName, false)
	chatAgent := &agent.ChatAgent{}
	resp := map[string]any{
		"status":                     "ok",
		"present_tutorial_blueprint": true,
		"blueprint_yaml":             "type: tutorial_blueprint\nsuite: s\ntitle: T\n",
		"title":                      "T",
		"suite":                      "s",
	}
	if !cr.maybeEmitTutorialBlueprint(chatAgent, nil, "present_tutorial_blueprint", resp) {
		t.Fatal("expected true for legacy status=ok")
	}
}

func (cr *ChatRunner) snapshotEvents() []ChatEvent {
	cr.eventsMu.RLock()
	defer cr.eventsMu.RUnlock()
	out := make([]ChatEvent, len(cr.events))
	copy(out, cr.events)
	return out
}
