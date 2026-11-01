package common

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestNormalizeExecuteToolArgsEnvelopeCases(t *testing.T) {
	args := map[string]any{"path": "/tmp/report", "_caller": "chat"}
	envelope := func(name any, arguments any) map[string]any {
		return map[string]any{"name": name, "arguments": arguments}
	}
	inner := envelope("load_file", args)
	for _, tc := range []struct {
		name     string
		raw      any
		wantName string
		wantArgs map[string]any
	}{
		{"direct", inner, "load_file", args},
		{"trim_name", envelope(" load_file\n", args), "load_file", args},
		{"empty_object", envelope("load_file", map[string]any{}), "load_file", map[string]any{}},
		{"json_arguments", envelope("load_file", `{"path":"/tmp/report","_caller":"chat"}`), "load_file", args},
		{"missing_name_wrapper", map[string]any{"arguments": inner}, "load_file", args},
		{"empty_name_wrapper", envelope("", inner), "load_file", args},
		{"whitespace_name_wrapper", envelope(" \t", inner), "load_file", args},
		{"self_wrapper", envelope(ExecuteToolName, inner), "load_file", args},
		{"json_wrapper", map[string]any{"arguments": `{"name":"load_file","arguments":{"path":"/tmp/report","_caller":"chat"}}`}, "load_file", args},
		{"json_self_wrapper", envelope(ExecuteToolName, `{"name":"load_file","arguments":"{\"path\":\"/tmp/report\",\"_caller\":\"chat\"}"}`), "load_file", args},
		// A real tool's arguments may legitimately look like another envelope.
		{"ordinary_envelope_shaped_arguments", envelope("ordinary_tool", inner), "ordinary_tool", inner},
		{"ordinary_json_strings_untouched", envelope("ordinary_tool", map[string]any{"arguments": `{"name":"load_file"}`, "content": `{"path":"x"}`}), "ordinary_tool", map[string]any{"arguments": `{"name":"load_file"}`, "content": `{"path":"x"}`}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before, err := json.Marshal(tc.raw)
			if err != nil {
				t.Fatal(err)
			}
			name, got, err := NormalizeExecuteToolArgs(tc.raw)
			if err != nil || name != tc.wantName || !reflect.DeepEqual(got, tc.wantArgs) {
				t.Fatalf("got (%q, %#v, %v), want (%q, %#v)", name, got, err, tc.wantName, tc.wantArgs)
			}
			after, err := json.Marshal(tc.raw)
			if err != nil {
				t.Fatal(err)
			}
			if string(before) != string(after) {
				t.Fatal("normalization mutated input")
			}
		})
	}
}

func TestNormalizeExecuteToolArgsRejectsMalformedAndUnboundedEnvelopes(t *testing.T) {
	envelope := func(name any, args any) map[string]any { return map[string]any{"name": name, "arguments": args} }
	inner := envelope("load_file", map[string]any{"path": "/tmp/report"})
	cases := map[string]any{
		"nil": nil, "nil_map": map[string]any(nil), "string_envelope": `{"name":"load_file","arguments":{}}`,
		"array_envelope": []any{}, "number_envelope": 1,
		"missing_arguments": map[string]any{"name": "load_file"}, "nil_arguments": envelope("load_file", nil),
		"nil_map_arguments": envelope("load_file", map[string]any(nil)), "array_arguments": envelope("load_file", []any{}),
		"numeric_arguments": envelope("load_file", 1), "bool_arguments": envelope("load_file", false),
		"broken_json": envelope("load_file", `{"path":`), "json_null": envelope("load_file", "null"),
		"json_array": envelope("load_file", "[]"), "json_number": envelope("load_file", "1"),
		"json_string": envelope("load_file", `"text"`), "json_bool": envelope("load_file", "false"),
		"numeric_name": envelope(1, inner), "nil_name": envelope(nil, inner),
		"missing_inner_name":      map[string]any{"arguments": map[string]any{}},
		"missing_inner_arguments": envelope(ExecuteToolName, map[string]any{"name": "load_file"}),
		"two_self_wrappers":       envelope(ExecuteToolName, envelope(ExecuteToolName, inner)),
		"two_nameless_wrappers":   map[string]any{"arguments": map[string]any{"arguments": inner}},
		"mixed_wrappers":          envelope(ExecuteToolName, map[string]any{"arguments": inner}),
	}
	for label, raw := range cases {
		t.Run(label, func(t *testing.T) {
			name, args, err := NormalizeExecuteToolArgs(raw)
			if err == nil || name != "" || args != nil {
				t.Fatalf("got (%q, %#v, %v), want empty result and error", name, args, err)
			}
			for _, field := range []string{"name", "arguments"} {
				if !strings.Contains(err.Error(), field) {
					t.Errorf("error missing %s: %v", field, err)
				}
			}
		})
	}
}

func TestNormalizeExecuteToolArgsDiscoveryDoesNotMutateOrRecoverExecution(t *testing.T) {
	for _, discovery := range []string{DescribeToolsName, SearchToolsName} {
		for _, wrapped := range []bool{false, true} {
			t.Run(discovery+map[bool]string{false: "/direct", true: "/wrapped"}[wrapped], func(t *testing.T) {
				raw := map[string]any{"name": discovery, "arguments": map[string]any{"name": "write_file", "arguments": map[string]any{"file_path": "/tmp/must-not-write", "content": "no"}, "_caller": "chat"}}
				if wrapped {
					raw = map[string]any{"name": ExecuteToolName, "arguments": raw}
				}
				before, _ := json.Marshal(raw)
				name, args, err := NormalizeExecuteToolArgs(raw)
				if err == nil || !strings.Contains(err.Error(), "call "+discovery+" directly") || name != "" || args != nil {
					t.Fatalf("discovery promoted to execution: %q %#v %v", name, args, err)
				}
				after, _ := json.Marshal(raw)
				if string(before) != string(after) {
					t.Fatal("rejected discovery mutated input")
				}
			})
		}
	}
}

func TestNormalizeExecuteToolArgsRetainsArgumentIdentity(t *testing.T) {
	for _, wrapped := range []bool{false, true} {
		args := map[string]any{"content": "{{CREDENTIAL:test:token}}"}
		raw := map[string]any{"name": "write_file", "arguments": args}
		if wrapped {
			raw = map[string]any{"name": ExecuteToolName, "arguments": raw}
		}
		_, got, err := NormalizeExecuteToolArgs(raw)
		if err != nil {
			t.Fatal(err)
		}
		got["content"] = "substituted"
		if args["content"] != "substituted" {
			t.Fatal("normalizer copied arguments, breaking credential restoration")
		}
	}
}

func TestToolResultSucceeded(t *testing.T) {
	for _, tc := range []struct {
		name   string
		result map[string]any
		err    error
		want   bool
	}{
		{"legacy_success", map[string]any{"path": "/tmp/report"}, nil, true},
		{"empty_result", map[string]any{}, nil, true},
		{"explicit_success", map[string]any{"success": true}, nil, true},
		{"success_status", map[string]any{"status": "success"}, nil, true},
		{"failed_success", map[string]any{"success": false}, nil, false},
		{"error_field", map[string]any{"error": "failed"}, nil, false},
		{"nil_error_field", map[string]any{"error": nil}, nil, false},
		{"empty_error_field", map[string]any{"error": ""}, nil, false},
		{"go_error", map[string]any{"success": true}, errors.New("failed"), false},
		{"nil_result", nil, nil, false},
		{"error_overrides_success", map[string]any{"success": true, "error": "failed"}, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ToolResultSucceeded(tc.result, tc.err); got != tc.want {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
	for _, status := range []string{"pending_approval", "approval_required", "pending_authorization", "error", "failed", "failure", "denied", "cancelled", "canceled"} {
		t.Run(status, func(t *testing.T) {
			if ToolResultSucceeded(map[string]any{"status": status, "success": true}, nil) {
				t.Fatalf("%s published as successful", status)
			}
		})
	}
}
