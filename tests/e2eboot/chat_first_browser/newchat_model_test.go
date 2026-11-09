package main

import (
	"github.com/SAP/astonish/pkg/store"
	"strings"
	"testing"
)

const actualStudioWeb = `{"messages":[{"type":"tool_call","toolName":"web_fetch","toolId":"a"},{"type":"tool_result","toolName":"web_fetch","toolId":"a","toolResult":{"status":200,"url":"https://weather.example/Orlando","content":"Current Orlando weather retrieved from the upstream service"}},{"type":"agent","content":"Orlando weather currently shows clear skies with comfortable temperatures and gentle winds. [Source](https://weather.example/Orlando)"}]}`

func TestChatModelPermissionScope(t *testing.T) {
	p := store.BotControlProposal{Payload: store.BotControlPayload{Configuration: &store.BotControlConfiguration{Autonomy: store.BotAutonomy{AllowedTools: []string{"web_fetch"}, AllowedHosts: []string{"wttr.in"}}}}}
	if validateChatModelWeatherPermission(p) != nil {
		t.Fatal("bounded read-only permission rejected")
	}
	p.Payload.Configuration.Autonomy.CanPublish = true
	if validateChatModelWeatherPermission(p) == nil {
		t.Fatal("publishing accepted")
	}
}

func TestChatModelRejectsPartialReceipts(t *testing.T) {
	if validateChatModelReceipts(map[string]smokeReceipt{}, "candidate") == nil {
		t.Fatal("partial passed")
	}
}
func TestChatModelRequiresCorrelatedWebResult(t *testing.T) {
	if !hasRealWebEvidence(actualStudioWeb) {
		t.Fatal("actual Studio schema rejected")
	}
	for _, raw := range []string{`{"messages":[{"type":"agent","content":"I used web_fetch"}]}`, strings.Replace(actualStudioWeb, `"toolId":"a","toolResult"`, `"toolId":"b","toolResult"`, 1), strings.Replace(actualStudioWeb, `"status":200`, `"status":500`, 1), strings.Replace(actualStudioWeb, `"status":200`, `"error":"failed"`, 1)} {
		if hasRealWebEvidence(raw) {
			t.Fatal("false evidence", raw)
		}
	}
}
func TestChatModelFiniteSSE(t *testing.T) {
	raw := "event: transcript\ndata: {\"content\":{\"parts\":[{\"functionCall\":{\"id\":\"a\",\"name\":\"web_fetch\"}}]}}\n\nevent: transcript\ndata: {\"content\":{\"parts\":[{\"functionResponse\":{\"id\":\"a\",\"name\":\"web_fetch\",\"response\":{\"status\":200,\"content\":\"Actual upstream weather response from Orlando Florida\"}}}]}}\n\n"
	if hasRealWebEvidence(raw) {
		t.Fatal("incomplete replay accepted")
	}
	if !hasRealWebEvidence(raw + "event: done\ndata: {\"persisted\":true}\n\n") {
		t.Fatal("real raw event shape rejected")
	}
}
func TestChatModelSourcesWithoutProseCitation(t *testing.T) {
	visible := "Orlando weather currently shows clear skies with comfortable temperatures and gentle winds."
	raw := strings.Replace(actualStudioWeb, " [Source](https://weather.example/Orlando)", "", 1)
	if !chatModelVisibleAnswer(raw, visible, []string{"https://weather.example/Orlando"}) {
		t.Fatal("existing Sources link must suffice without prose citation")
	}
	for name, bad := range map[string]string{
		"wrong invocation": strings.Replace(raw, `"type":"agent"`, `"invocationId":"other","type":"agent"`, 1),
		"earlier turn":     strings.Replace(raw, `{"type":"agent"`, `{"type":"user","content":"new request"},{"type":"agent"`, 1),
		"memory only":      strings.ReplaceAll(raw, "web_fetch", "memory_search"),
		"wrong call":       strings.Replace(raw, `"toolId":"a","toolResult"`, `"toolId":"b","toolResult"`, 1),
		"failed fetch":     strings.Replace(raw, `"status":200`, `"status":500`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			if chatModelVisibleAnswer(bad, visible, []string{"https://weather.example/Orlando"}) {
				t.Fatal("unrelated source accepted")
			}
		})
	}
	if chatModelVisibleAnswer(raw, visible+" 8 sources", nil) {
		t.Fatal("source count is not URL evidence")
	}
}

func TestChatModelVisibleSource(t *testing.T) {
	visible := "Orlando weather currently shows clear skies with comfortable temperatures and gentle winds. Source"
	if !chatModelVisibleAnswer(actualStudioWeb, visible, []string{"https://weather.example/Orlando"}) {
		t.Fatal("visible actual answer rejected")
	}
	if chatModelVisibleAnswer(actualStudioWeb, visible, []string{"https://unrelated.example"}) || chatModelVisibleAnswer(actualStudioWeb, "Orlando", []string{"https://weather.example/Orlando"}) {
		t.Fatal("unrelated DOM accepted")
	}
}
