package main

import (
	"encoding/json"
	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/functiontool"
	"google.golang.org/genai"
	"testing"
)

func TestLiveScheduleSchemaUUIDMismatch(t *testing.T) {
	type args struct {
		Payload store.BotControlPayload `json:"payload"`
	}
	f, err := functiontool.New(functiontool.Config{Name: "probe", Description: "read-only schema probe"}, func(tool.Context, args) (map[string]any, error) { return nil, nil })
	if err != nil {
		t.Fatal(err)
	}
	d := f.(interface {
		Declaration() *genai.FunctionDeclaration
	}).Declaration()
	b, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	t.Log(string(b))
	var payload store.BotControlPayload
	err = json.Unmarshal([]byte(`{"operation":"schedule_create","reason":"live replay decode only","schedule":{"expected_revision":9,"trigger":{"id":[0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0],"kind":"cron","cron":"* * * * *","timezone":"Etc/UTC","enabled":false,"instructions":"report only"}}}`), &payload)
	if err == nil {
		t.Fatal("expected live numeric-array UUID decoding failure")
	}
	t.Logf("LIVE PAYLOAD DECODE: %v", err)
}
