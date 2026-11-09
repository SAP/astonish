package api

import (
	"context"
	"encoding/json"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"strings"
	"testing"
)

func TestBotSourceIntakeAdaptiveHTTP(t *testing.T) {
	f := newReviewedHTTPFixture(t)
	for _, scope := range []string{"personal", "team"} {
		t.Run(scope, func(t *testing.T) {
			job := &store.ScheduledJob{Name: "Adaptive", Mode: "adaptive", Enabled: true, OwnerID: f.owner, Schedule: store.JobSchedule{Cron: "0 9 * * *", Timezone: "UTC"}, Payload: store.JobPayload{Instructions: "Observe", Params: map[string]string{"secret": "never-copy"}}}
			if err := f.jobs(scope).Add(context.Background(), job); err != nil {
				t.Fatal(err)
			}
			path := "/api/studio/bots/source-proposals"
			suffix := "?scope=" + scope
			var p store.BotSourceIntakeProposal
			raw := f.request("POST", path+suffix, store.BotSourceIntakeRequest{Mode: store.BotIntakeAdaptive, SourceID: uuid.MustParse(job.ID), Name: "Replacement", Charter: "Watch", IdempotencyKey: uuid.NewString()}, 201)
			if strings.Contains(string(raw), "never-copy") {
				t.Fatal("secret copied")
			}
			json.Unmarshal(raw, &p)
			if !f.jobs(scope).Get(context.Background(), job.ID).Enabled {
				t.Fatal("proposal paused original")
			}
			decision := path + "/" + p.ID.String() + "/decision" + suffix
			var applied store.BotSourceIntakeProposal
			q := store.BotSourceIntakeDecision{Digest: p.Digest, Decision: "confirm", ReviewedForTeam: true}
			json.Unmarshal(f.request("POST", decision, q, 200), &applied)
			if applied.Status != store.BotSourceIntakeApplied || applied.BotID == nil || f.jobs(scope).Get(context.Background(), job.ID).Enabled {
				t.Fatalf("conversion failed: %+v", applied)
			}
			var retry store.BotSourceIntakeProposal
			json.Unmarshal(f.request("POST", decision, q, 200), &retry)
			if *retry.BotID != *applied.BotID {
				t.Fatal("duplicate")
			}
			var b store.Bot
			json.Unmarshal(f.request("GET", "/api/studio/bots/"+applied.BotID.String()+suffix, nil, 200), &b)
			if b.Status != store.BotDraft {
				t.Fatal("activated")
			}
		})
	}
}
func TestBotSourceIntakeTemplateHTTP(t *testing.T) {
	f := newReviewedHTTPFixture(t, "admin")
	var template store.BotTemplate
	def := store.BotTemplateDefinition{SchemaVersion: store.BotSchemaVersion, Name: "Template proposal", Charter: "Observe"}
	json.Unmarshal(f.request("POST", "/api/studio/bots/templates?scope=team", map[string]any{"reviewed_for_team": true, "definition": def}, 201), &template)
	path := "/api/studio/bots/source-proposals"
	suffix := "?scope=team"
	body := store.BotSourceIntakeRequest{Mode: store.BotIntakeTemplate, SourceID: template.ID, TemplateRevision: template.Revision, Name: "Private copy", IdempotencyKey: uuid.NewString()}
	var p store.BotSourceIntakeProposal
	json.Unmarshal(f.request("POST", path+suffix, body, 201), &p)
	var applied store.BotSourceIntakeProposal
	q := store.BotSourceIntakeDecision{Digest: p.Digest, Decision: "confirm"}
	decision := path + "/" + p.ID.String() + "/decision" + suffix
	json.Unmarshal(f.request("POST", decision, q, 200), &applied)
	if applied.Status != store.BotSourceIntakeApplied || applied.BotID == nil {
		t.Fatalf("fork: %+v", applied)
	}
	f.request("GET", "/api/studio/bots/"+applied.BotID.String()+"?scope=personal", nil, 200)
	f.request("GET", "/api/studio/bots/"+applied.BotID.String()+suffix, nil, 404)
	body.IdempotencyKey = uuid.NewString()
	var stale store.BotSourceIntakeProposal
	json.Unmarshal(f.request("POST", path+suffix, body, 201), &stale)
	def.Charter = "New revision"
	f.request("POST", "/api/studio/bots/templates?scope=team", map[string]any{"reviewed_for_team": true, "definition": def, "expected_revision": template.Revision}, 200)
	f.request("POST", path+"/"+stale.ID.String()+"/decision"+suffix, store.BotSourceIntakeDecision{Digest: stale.Digest, Decision: "confirm"}, 409)
	// A terminal receipt can be replayed after template changes.
	var retry store.BotSourceIntakeProposal
	json.Unmarshal(f.request("POST", decision, q, 200), &retry)
	if retry.BotID == nil || *retry.BotID != *applied.BotID {
		t.Fatal("fork retry lost")
	}
}
