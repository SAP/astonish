package launcher

import (
	"encoding/json"
	"testing"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/google/jsonschema-go/jsonschema"
)

func TestBotProposalGenerationHealthSchema(t *testing.T) {
	for name, schema := range map[string]*jsonschema.Schema{
		"legacy_proposal":   botProposalInputSchema(),
		"mandated_proposal": botMandatedProposalInputSchema(),
	} {
		t.Run(name, func(t *testing.T) {
			policySchema := schema.Properties["payload"].Properties["configuration"].Properties["budget"].Properties["runtime_policy"]
			resolved, err := policySchema.Resolve(nil)
			if err != nil {
				t.Fatal(err)
			}
			const progress = `"schema_version":1,"mode":"progress_v1","evaluation_interval_seconds":300,"tool_call_timeout_seconds":120`
			for _, tc := range []struct {
				name, wire string
				valid      bool
			}{
				{"health", `{` + progress + `,"generation_health":{"idle_timeout_seconds":120,"check_interval_seconds":10}}`, true},
				{"explicit_zero_deadline", `{` + progress + `,"model_call_timeout_seconds":0,"generation_health":{"idle_timeout_seconds":120,"check_interval_seconds":10}}`, true},
				{"saved_deadline", `{` + progress + `,"model_call_timeout_seconds":120}`, true},
				{"null_health_saved_deadline", `{` + progress + `,"model_call_timeout_seconds":120,"generation_health":null}`, true},
				{"legacy", `{"schema_version":1,"mode":"legacy"}`, true},
				{"absent", `null`, true},
				{"deadline_conflict", `{` + progress + `,"model_call_timeout_seconds":120,"generation_health":{"idle_timeout_seconds":120,"check_interval_seconds":10}}`, false},
				{"legacy_health", `{"schema_version":1,"mode":"legacy","generation_health":{"idle_timeout_seconds":120,"check_interval_seconds":10}}`, false},
				{"unknown_switch", `{` + progress + `,"generation_health":{"idle_timeout_seconds":120,"check_interval_seconds":10,"disabled":true}}`, false},
				{"missing_idle", `{` + progress + `,"generation_health":{"check_interval_seconds":10}}`, false},
				{"missing_check", `{` + progress + `,"generation_health":{"idle_timeout_seconds":120}}`, false},
				{"zero_idle", `{` + progress + `,"generation_health":{"idle_timeout_seconds":0,"check_interval_seconds":10}}`, false},
				{"negative_check", `{` + progress + `,"generation_health":{"idle_timeout_seconds":120,"check_interval_seconds":-1}}`, false},
				{"fractional_check", `{` + progress + `,"generation_health":{"idle_timeout_seconds":120,"check_interval_seconds":0.5}}`, false},
				{"string_idle", `{` + progress + `,"generation_health":{"idle_timeout_seconds":"120","check_interval_seconds":10}}`, false},
				{"null_check", `{` + progress + `,"generation_health":{"idle_timeout_seconds":120,"check_interval_seconds":null}}`, false},
				{"duration_overflow", `{` + progress + `,"generation_health":{"idle_timeout_seconds":9223372037,"check_interval_seconds":10}}`, false},
			} {
				t.Run(tc.name, func(t *testing.T) {
					var wire any
					if err := json.Unmarshal([]byte(tc.wire), &wire); err != nil {
						t.Fatal(err)
					}
					if err := resolved.Validate(wire); (err == nil) != tc.valid {
						t.Fatalf("schema valid=%v: %v", tc.valid, err)
					}
					var policy *execution.BotRuntimePolicy
					err := json.Unmarshal([]byte(tc.wire), &policy)
					if err == nil {
						err = policy.Validate()
					}
					if (err == nil) != tc.valid {
						t.Fatalf("runtime validation disagrees: %v", err)
					}
					if !tc.valid {
						return
					}
					encoded, err := json.Marshal(policy)
					if err != nil {
						t.Fatal(err)
					}
					if err := json.Unmarshal(encoded, &wire); err != nil {
						t.Fatal(err)
					}
					if err := resolved.Validate(wire); err != nil {
						t.Fatalf("schema rejects serialized runtime policy: %v", err)
					}
				})
			}
		})
	}
}
