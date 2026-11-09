package launcher

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/store"
	"github.com/google/jsonschema-go/jsonschema"
	"google.golang.org/adk/model"
)

func TestBotProposalSchemaCoversWireTypes(t *testing.T) {
	var walk func(reflect.Type, *jsonschema.Schema, string)
	walk = func(typ reflect.Type, s *jsonschema.Schema, path string) {
		t.Helper()
		if s == nil {
			t.Fatalf("missing schema: %s", path)
		}
		if typ.Kind() == reflect.Pointer {
			typ = typ.Elem()
		}
		hasType := func(want string) bool {
			if s.Type == want {
				return true
			}
			for _, v := range s.Types {
				if v == want {
					return true
				}
			}
			return false
		}
		if typ == reflect.TypeOf(uuid.UUID{}) || typ == reflect.TypeOf(time.Time{}) {
			format := "uuid"
			if typ == reflect.TypeOf(time.Time{}) {
				format = "date-time"
			}
			if !hasType("string") || s.Format != format {
				t.Fatalf("%s must be string/%s", path, format)
			}
			return
		}
		switch typ.Kind() {
		case reflect.Struct:
			if !hasType("object") || s.AdditionalProperties == nil {
				t.Fatalf("object not closed: %s", path)
			}
			for i := 0; i < typ.NumField(); i++ {
				field := typ.Field(i)
				name := strings.Split(field.Tag.Get("json"), ",")[0]
				if name != "" && name != "-" {
					walk(field.Type, s.Properties[name], path+"."+name)
				}
			}
		case reflect.Slice, reflect.Array:
			walk(typ.Elem(), s.Items, path+"[]")
		}
	}
	walk(reflect.TypeOf(botProposalArgs{}), botProposalInputSchema(), "args")
}

func TestBotProposalRequiredRevision(t *testing.T) {
	for _, tc := range []struct {
		op             store.BotControlOperation
		branch, fields string
	}{
		{store.BotControlMemoryRemember, "memory", `"content":"The smoke marker label is WORKSPACE_CHECK.","category":"fact"`},
		{store.BotControlMemoryCorrect, "memory", `"memory_id":"fbb2a51a-e27b-4440-a367-35ac91ad0b28","content":"Corrected"`},
		{store.BotControlMemoryForget, "memory", `"memory_id":"fbb2a51a-e27b-4440-a367-35ac91ad0b28"`},
		{store.BotControlGoalEdit, "goal", `"goal":{"id":"fbb2a51a-e27b-4440-a367-35ac91ad0b28","title":"Review","completion_criteria":"Reviewed"}`},
		{store.BotControlScheduleCreate, "schedule", `"trigger":{"kind":"cron","instructions":"Check","cron":"0 9 * * *","timezone":"UTC","enabled":true}`},
		{store.BotControlScheduleEdit, "schedule", `"trigger":{"id":"fbb2a51a-e27b-4440-a367-35ac91ad0b28"}`},
		{store.BotControlSchedulePause, "schedule", `"trigger":{"id":"fbb2a51a-e27b-4440-a367-35ac91ad0b28"}`},
		{store.BotControlScheduleRemove, "schedule", `"trigger":{"id":"fbb2a51a-e27b-4440-a367-35ac91ad0b28"}`},
	} {
		t.Run(string(tc.op), func(t *testing.T) {
			f := newBotFactoryFixture(t)
			repo := &proposalTestRepo{botFactoryTestRepo: botFactoryTestRepo{fixture: f}}
			f.services.Bots, f.services.PersonalBots = repo, repo
			binding := &botFactoryBinding{owner: store.WithServices(botFactoryOwnerContext(f.ctx, f.req), f.services), req: f.req, verifyIsolation: func(context.Context) error { return nil }}
			raw, err := newBotProposalTool(binding)
			if err != nil {
				t.Fatal(err)
			}
			tool := raw.(*botProposalTool)
			ctx := proposalTestContext{botFactoryToolContext: botFactoryToolContext{base: context.Background()}, sid: f.req.Run.SessionID.String()}
			req := &model.LLMRequest{}
			if err := tool.ProcessRequest(ctx, req); err != nil {
				t.Fatal(err)
			}
			data, err := json.Marshal(req.Config.Tools[0].FunctionDeclarations[0].ParametersJsonSchema)
			if err != nil {
				t.Fatal(err)
			}
			var schema jsonschema.Schema
			if err := json.Unmarshal(data, &schema); err != nil {
				t.Fatal(err)
			}
			resolved, err := schema.Resolve(nil)
			if err != nil {
				t.Fatal(err)
			}
			for _, revision := range []string{"", `,"expected_revision":null`, `,"expected_revision":"6"`, `,"expected_revision":6`} {
				var args map[string]any
				wire := fmt.Sprintf(`{"payload":{"operation":%q,"reason":"Owner requested change","%s":{%s%s}}}`, tc.op, tc.branch, tc.fields, revision)
				if err := json.Unmarshal([]byte(wire), &args); err != nil {
					t.Fatal(err)
				}
				valid := revision == `,"expected_revision":6`
				if err := resolved.Validate(args); (err == nil) != valid {
					t.Errorf("declared schema revision %q: %v", revision, err)
				}
				out, err := tool.Run(ctx, args)
				if valid {
					if err != nil || out["proposal_id"] == nil || repo.calls != 1 {
						t.Fatalf("valid revision rejected: %v %v calls=%d", out, err, repo.calls)
					}
					var got int64
					switch tc.branch {
					case "memory":
						got = repo.request.Payload.Memory.ExpectedRevision
					case "goal":
						got = repo.request.Payload.Goal.ExpectedRevision
					case "schedule":
						got = repo.request.Payload.Schedule.ExpectedRevision
					}
					if got != 6 {
						t.Fatalf("revision silently changed: %d", got)
					}
				} else {
					if !errors.Is(err, store.ErrBotInvalid) || !strings.Contains(err.Error(), "expected_revision") {
						t.Errorf("need actionable typed revision error, got %v", err)
					}
					if repo.calls != 0 {
						t.Fatalf("invalid revision reached repository: %d", repo.calls)
					}
				}
			}
		})
	}
}

func TestBotProposalDeclaredSchema(t *testing.T) {
	f := newBotFactoryFixture(t)
	repo := &proposalTestRepo{botFactoryTestRepo: botFactoryTestRepo{fixture: f}}
	f.services.Bots, f.services.PersonalBots = repo, repo
	binding := &botFactoryBinding{owner: store.WithServices(botFactoryOwnerContext(f.ctx, f.req), f.services), req: f.req, verifyIsolation: func(context.Context) error { return nil }}
	raw, err := newBotProposalTool(binding)
	if err != nil {
		t.Fatal(err)
	}
	tool := raw.(*botProposalTool)
	ctx := proposalTestContext{botFactoryToolContext: botFactoryToolContext{base: context.Background()}, sid: f.req.Run.SessionID.String()}
	req := &model.LLMRequest{}
	if err := tool.ProcessRequest(ctx, req); err != nil {
		t.Fatal(err)
	}
	var schema jsonschema.Schema
	data, err := json.Marshal(req.Config.Tools[0].FunctionDeclarations[0].ParametersJsonSchema)
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(data, &schema); err != nil {
		t.Fatal(err)
	}
	payload := schema.Properties["payload"]
	trigger := payload.Properties["schedule"].Properties["trigger"]
	if trigger.Properties["id"].Type != "string" || trigger.Properties["id"].Format != "uuid" {
		t.Fatalf("schedule ID must be a UUID string, got %s", trigger.Properties["id"])
	}
	for _, key := range trigger.Required {
		if key == "id" {
			t.Fatal("new schedule must allow omitted ID")
		}
	}
	for _, key := range payload.Required {
		if key != "operation" && key != "reason" {
			t.Fatalf("unrelated union branch required: %s", key)
		}
	}
	resolved, err := schema.Resolve(nil)
	if err != nil {
		t.Fatal(err)
	}
	examples := []string{
		`{"payload":{"operation":"schedule_create","reason":"Owner requested daily check","schedule":{"expected_revision":1,"trigger":{"kind":"cron","instructions":"Check notices","cron":"0 9 * * *","timezone":"UTC","enabled":true}}}}`,
		`{"payload":{"operation":"schedule_edit","reason":"Move check","schedule":{"expected_revision":1,"trigger":{"id":"fbb2a51a-e27b-4440-a367-35ac91ad0b28","schedule_id":"a5a23412-3824-414a-a706-c0d4b679a52c","kind":"cron","instructions":"Check notices","cron":"0 10 * * *","timezone":"UTC","enabled":true}}}}`,
		`{"payload":{"operation":"memory_remember","reason":"Remember preference","memory":{"expected_revision":1,"content":"Prefer concise answers","category":"preference"}}}`,
		`{"payload":{"operation":"goal_create","reason":"Track progress","goal":{"goal":{"title":"Review notices","description":"Daily review","completion_criteria":"Reviewed","due_at":"2026-12-01T09:00:00Z"}}}}`,
	}
	for _, example := range examples {
		var args map[string]any
		if err = json.Unmarshal([]byte(example), &args); err != nil {
			t.Fatal(err)
		}
		if err = resolved.Validate(args); err != nil {
			t.Fatalf("declared schema rejects example: %v", err)
		}
		out, err := tool.Run(ctx, args)
		if err != nil || out["proposal_id"] == nil || out["status"] != store.BotControlProposed {
			t.Fatalf("tool failed: %v %v", out, err)
		}
	}
	if repo.calls != 4 {
		t.Fatalf("captures=%d", repo.calls)
	}
	for _, example := range []string{
		`{"payload":{"operation":"schedule_create","reason":"x","scope":"team","schedule":{"trigger":{}}}}`,
		`{"payload":{"operation":"schedule_create","reason":"x","schedule":{"trigger":{"grant":"all"}}}}`,
		`{"payload":{"operation":"memory_remember","reason":"x","schedule":{"trigger":{}}}}`,
		`{"payload":{"operation":"schedule_create","reason":"x","schedule":{"trigger":{}},"memory":{"content":"x"}}}`,
		`{"payload":{"operation":"schedule_create","reason":"x","schedule":{"trigger":{"id":[0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0]}}}}`,
		`{"payload":{"operation":"goal_create","reason":"x","goal":{"goal":{"due_at":"tomorrow"}}}}`,
	} {
		tool.attempts = 0
		var args map[string]any
		if err := json.Unmarshal([]byte(example), &args); err != nil {
			t.Fatal(err)
		}
		if _, err := tool.Run(ctx, args); err == nil {
			t.Fatalf("invalid request accepted: %s", example)
		}
		if repo.calls != 4 {
			t.Fatal("invalid request reached repository")
		}
	}
}
