package api

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/SAP/astonish/pkg/store"
)

// This is negative acceptance evidence, not an implementation of test-once.
// It uses real HTTP, tenant middleware and SQLite in both supported scopes.
func TestBotSupervisedPurposeHTTPFailClosed(t *testing.T) {
	f := newReviewedHTTPFixture(t)
	for _, scope := range []string{"personal", "team"} {
		t.Run(scope, func(t *testing.T) {
			suffix := "?scope=" + scope
			var b store.Bot
			if err := json.Unmarshal(f.request("POST", "/api/studio/bots"+suffix, `{"name":"Supervised boundary","idempotency_key":"supervised-boundary","definition":{"charter":"Only act with separate consent"}}`, 201), &b); err != nil {
				t.Fatal(err)
			}
			path := "/api/studio/bots/" + b.ID.String()
			f.request("POST", path+"/runs"+suffix, `{"version":1,"idempotency_key":"manual","instructions":"Test once"}`, 403)
			for _, field := range []string{`"purpose":"test_once"`, `"purpose":"owner_conversation"`, `"reviewed":true`, `"consent":true`, `"trigger":"cron"`} {
				f.request("POST", path+"/runs"+suffix, `{"version":1,"idempotency_key":"forged","instructions":"Test once",`+field+`}`, 400)
			}
			var after store.Bot
			if err := json.Unmarshal(f.request("GET", path+suffix, nil, 200), &after); err != nil {
				t.Fatal(err)
			}
			if after.Status != store.BotDraft || after.Version != b.Version || after.Revision != b.Revision {
				t.Fatalf("request activated or mutated Bot: %+v", after)
			}
			var runs struct {
				Runs []store.BotRun `json:"runs"`
			}
			if err := json.Unmarshal(f.request("GET", path+"/runs"+suffix, nil, 200), &runs); err != nil {
				t.Fatal(err)
			}
			if len(runs.Runs) != 0 {
				t.Fatalf("unsupported consent created fake run: %+v", runs.Runs)
			}
			if jobs := f.jobs(scope).List(context.Background()); len(jobs) != 0 {
				t.Fatalf("unsupported consent created schedules: %+v", jobs)
			}
		})
	}
}
