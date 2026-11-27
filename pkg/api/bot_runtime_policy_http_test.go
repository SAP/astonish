package api

import (
	"context"
	"encoding/json"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"testing"
)

// Loopback HTTP and isolated SQLite prove policy selection, not employee execution.
func TestBotRuntimePolicyHTTPAcceptance(t *testing.T) {
	f := newReviewedHTTPFixture(t)
	cfg := &config.AppConfig{}
	*f.policy = func(context.Context, store.BotMandateRef) (store.BotMandatePolicy, error) {
		p, err := cfg.EffectiveBotRuntimePolicy()
		return store.BotMandatePolicy{RuntimePolicy: p}, err
	}
	for _, scope := range []string{"personal", "team"} {
		t.Run(scope, func(t *testing.T) {
			suffix := "?scope=" + scope
			var b store.Bot
			if err := json.Unmarshal(f.createExisting(t, scope, `{"name":"Long assignment","idempotency_key":"runtime-policy"}`), &b); err != nil {
				t.Fatal(err)
			}
			path := "/api/studio/bots/" + b.ID.String()
			read := func(n string) store.BotVersion {
				var v store.BotVersion
				if err := json.Unmarshal(f.request("GET", path+"/versions/"+n+suffix, nil, 200), &v); err != nil {
					t.Fatal(err)
				}
				return v
			}
			v := read("1")
			if v.Budget.RuntimePolicy.EffectiveMode() != execution.BotRuntimeModeProgressV1 || v.Budget.RuntimePolicy.MaxActiveSeconds != nil {
				t.Fatal("new Bot did not select progress default")
			}
			if err := json.Unmarshal(f.request("POST", path+"/pause"+suffix, map[string]any{"expected_revision": b.Revision, "reason": "Review runtime limits"}, 200), &b); err != nil {
				t.Fatal(err)
			}
			q := store.BotConfigurationRequest{ExpectedRevision: b.Revision, ExpectedVersion: b.Version, Reviewed: true, Reason: "explicit legacy duration", Autonomy: v.Autonomy, Budget: store.BudgetSpec{MaxRuntimeSeconds: 300}, Delivery: v.Delivery}
			f.request("PUT", path+"/configuration"+suffix, q, 200)
			if old := read("2"); old.Budget.RuntimePolicy != nil || old.Budget.MaxRuntimeSeconds != 300 {
				t.Fatal("legacy 300 rewritten")
			}
			q.ExpectedRevision, q.ExpectedVersion = 3, 2
			q.Budget.MaxRuntimeSeconds = 900
			f.request("PUT", path+"/configuration"+suffix, q, 200)
			if old := read("3"); old.Budget.RuntimePolicy != nil || old.Budget.MaxRuntimeSeconds != 900 {
				t.Fatal("legacy 900 rewritten")
			}
			q.ExpectedRevision, q.ExpectedVersion = 4, 3
			q.Budget.RuntimePolicy = execution.NewBotProgressRuntimePolicy()
			q.Reviewed = false
			f.request("PUT", path+"/configuration"+suffix, q, 403)
			q.Reviewed = true
			cfg.BotRuntimePolicy = &execution.BotRuntimePolicy{SchemaVersion: execution.BotRuntimePolicySchemaVersion, Mode: execution.BotRuntimeModeLegacy}
			f.request("PUT", path+"/configuration"+suffix, q, 403)
			cfg.BotRuntimePolicy = execution.NewBotProgressRuntimePolicy()
			f.request("PUT", path+"/configuration"+suffix, q, 200)
			if read("4").Budget.RuntimePolicy.EffectiveMode() != execution.BotRuntimeModeProgressV1 {
				t.Fatal("explicit reviewed opt-in missing")
			}
			if read("3").Budget.RuntimePolicy != nil {
				t.Fatal("historical version changed")
			}
			cfg.BotRuntimePolicy = nil
			limits := config.DefaultBotMandateConfig()
			cfg.BotMandates = &limits
			var legacy store.Bot
			if err := json.Unmarshal(f.createExisting(t, scope, `{"name":"Explicit operator ceiling","idempotency_key":"legacy-operator-policy"}`), &legacy); err != nil {
				t.Fatal(err)
			}
			var before store.BotVersion
			legacyVersionPath := "/api/studio/bots/" + legacy.ID.String() + "/versions/1" + suffix
			if err := json.Unmarshal(f.request("GET", legacyVersionPath, nil, 200), &before); err != nil {
				t.Fatal(err)
			}
			cfg.BotMandates = nil // changing defaults must not mutate loaded legacy versions
			var saved store.BotVersion
			if err := json.Unmarshal(f.request("GET", legacyVersionPath, nil, 200), &saved); err != nil {
				t.Fatal(err)
			}
			if before.Budget.RuntimePolicy != nil || saved.Budget.RuntimePolicy != nil || saved.Budget.MaxRuntimeSeconds != before.Budget.MaxRuntimeSeconds || saved.Budget.MaxRuntimeSeconds <= 0 {
				t.Fatalf("explicit operator version upgraded on load: %+v", saved.Budget)
			}
		})
	}
}
