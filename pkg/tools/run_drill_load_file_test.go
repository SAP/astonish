package tools

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/SAP/astonish/pkg/sandbox"
)

type drillLoadFileClient struct {
	sandbox.ToolNodeClient
	sessionID, name string
	args            map[string]any
}

func (c *drillLoadFileClient) Call(sessionID, name string, args map[string]any) (json.RawMessage, error) {
	c.sessionID, c.name, c.args = sessionID, name, args
	return json.RawMessage(`{"path":"/sandbox/report.md","source":"sandbox"}`), nil
}

func TestCompositeExecutor_LoadFileRoutesToSandbox(t *testing.T) {
	client := &drillLoadFileClient{}
	executor := buildTestExecutor(nil, &runDrillDeps{toolClient: client, sessionID: "drill-session"})
	defer executor.Close()
	args := map[string]any{"path": "/sandbox/report.md", "title": "Report"}
	result, err := executor.Execute(context.Background(), "load_file", args)
	if err != nil {
		t.Fatalf("load_file failed: %v", err)
	}
	if client.sessionID != "drill-session" || client.name != "load_file" || !reflect.DeepEqual(client.args, args) {
		t.Fatalf("load_file did not reach session sandbox: %+v", client)
	}
	if got, ok := result.(map[string]any); !ok || got["source"] != "sandbox" {
		t.Fatalf("load_file result = %#v, want sandbox response", result)
	}
}
