package api

import (
	"context"
	"encoding/json"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestBotReviewedPromotionHTTP(t *testing.T) {
	f := newReviewedHTTPFixture(t)
	ctx := context.Background()
	for _, scope := range []string{"personal", "team"} {
		t.Run(scope, func(t *testing.T) {
			sid := uuid.New()
			sessions := f.sessions(scope)
			if err := sessions.AddSessionMeta(ctx, store.SessionMeta{ID: sid.String(), UserID: f.owner, Title: "Original Chat", Purpose: store.SessionPurposeChat, CreatedAt: time.Now(), UpdatedAt: time.Now()}); err != nil {
				t.Fatal(err)
			}
			before, err := sessions.GetSessionMeta(ctx, sid.String())
			if err != nil {
				t.Fatal(err)
			}
			suffix := "?scope=" + scope
			path := "/api/studio/bots/promote-chat/" + sid.String() + suffix
			q := store.BotPromotionRequest{Name: "Promoted", IdempotencyKey: "promote", Charter: "Observe", Reviewed: true, ReviewedForTeam: true, Carryover: store.BotCarryover{Summary: "Reviewed summary only", Memories: []store.BotCarryoverMemory{{Content: "Selected fact", Category: "fact"}}, Goals: []store.BotCarryoverGoal{{Title: "Watch", CompletionCriteria: "Report changes"}}, Artifacts: []store.BotCarryoverArtifact{{Title: "Selected note", Content: "Reviewed text snapshot"}}}}
			f.request("POST", path, `{"credentials":{"token":"never-copy"}}`, 400)
			q.Reviewed = false
			f.request("POST", path, q, 403)
			q.Reviewed = true
			if scope == "team" {
				q.ReviewedForTeam = false
				f.request("POST", path, q, 403)
				q.ReviewedForTeam = true
			}
			var b store.Bot
			if err := json.Unmarshal(f.request("POST", path, q, 201), &b); err != nil {
				t.Fatal(err)
			}
			if b.HomeSessionID == sid || b.Status != store.BotDraft {
				t.Fatalf("source replaced or activated: %+v", b)
			}
			var replay store.Bot
			if err := json.Unmarshal(f.request("POST", path, q, 201), &replay); err != nil {
				t.Fatal(err)
			}
			if replay.ID != b.ID {
				t.Fatal("duplicate promotion")
			}
			q.Carryover.Summary = "changed"
			f.request("POST", path, q, 409)
			after, err := sessions.GetSessionMeta(ctx, sid.String())
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(before, after) {
				t.Fatalf("source modified: %+v %+v", before, after)
			}
			bp := "/api/studio/bots/" + b.ID.String()
			memories := string(f.request("GET", bp+"/memory"+suffix, nil, 200))
			if !strings.Contains(memories, "Selected fact") || !strings.Contains(memories, "Reviewed summary only") {
				t.Fatal(memories)
			}
			goals := string(f.request("GET", bp+"/goals"+suffix, nil, 200))
			if !strings.Contains(goals, "Report changes") {
				t.Fatal(goals)
			}
			// No private -> team fallback even with an explicit review acknowledgment.
			other := "team"
			if scope == "team" {
				other = "personal"
			}
			f.request("POST", "/api/studio/bots/promote-chat/"+sid.String()+"?scope="+other, q, 404)
		})
	}
}
func TestBotReviewedAdaptiveConversionHTTP(t *testing.T) {
	f := newReviewedHTTPFixture(t)
	ctx := context.Background()
	for _, scope := range []string{"personal", "team"} {
		t.Run(scope, func(t *testing.T) {
			jobs := f.jobs(scope)
			job := &store.ScheduledJob{Name: "Original adaptive", Mode: "adaptive", Enabled: true, OwnerID: f.owner, Schedule: store.JobSchedule{Cron: "0 9 * * *", Timezone: "UTC"}, Payload: store.JobPayload{Instructions: "Review public changes", Params: map[string]string{"credential": "never-copy"}}, Delivery: store.JobDelivery{Channel: "email", Target: "never-copy@example.test"}}
			if err := jobs.Add(ctx, job); err != nil {
				t.Fatal(err)
			}
			suffix := "?scope=" + scope
			path := "/api/studio/bots/convert-adaptive/" + job.ID + suffix
			var review store.BotAdaptiveReview
			if err := json.Unmarshal(f.request("GET", path, nil, 200), &review); err != nil {
				t.Fatal(err)
			}
			q := store.BotConversionRequest{Name: "Replacement", IdempotencyKey: "conversion", Charter: "Observe", Reviewed: true, ReviewedForTeam: true, ExpectedUpdatedAt: review.UpdatedAt}
			q.ExpectedUpdatedAt = q.ExpectedUpdatedAt.Add(-time.Second)
			f.request("POST", path, q, 409)
			q.ExpectedUpdatedAt = review.UpdatedAt
			if !jobs.Get(ctx, job.ID).Enabled {
				t.Fatal("stale conversion paused original")
			}
			q.Charter = ""
			f.request("POST", path, q, 400)
			q.Charter = "Observe"
			if !jobs.Get(ctx, job.ID).Enabled {
				t.Fatal("invalid replacement paused original")
			}
			var b store.Bot
			if err := json.Unmarshal(f.request("POST", path, q, 201), &b); err != nil {
				t.Fatal(err)
			}
			if b.Status != store.BotDraft || jobs.Get(ctx, job.ID).Enabled {
				t.Fatal("conversion not inert/atomic")
			}
			var replay store.Bot
			if err := json.Unmarshal(f.request("POST", path, q, 201), &replay); err != nil {
				t.Fatal(err)
			}
			if replay.ID != b.ID {
				t.Fatal("duplicate conversion")
			}
			var v store.BotVersion
			data := f.request("GET", "/api/studio/bots/"+b.ID.String()+"/versions/1"+suffix, nil, 200)
			if err := json.Unmarshal(data, &v); err != nil {
				t.Fatal(err)
			}
			if len(v.Triggers) != 1 || v.Triggers[0].Enabled || v.Triggers[0].ScheduleID != nil || v.Triggers[0].CredentialRef != "" || len(v.Delivery.Bindings) != 0 || strings.Contains(string(data), "never-copy") {
				t.Fatalf("authority copied: %s", data)
			}
		})
	}
}

func TestBotReviewedIntakeGuardsHTTP(t *testing.T) {
	f := newReviewedHTTPFixture(t)
	ctx := context.Background()
	for _, scope := range []string{"personal", "team"} {
		t.Run(scope, func(t *testing.T) {
			suffix := "?scope=" + scope
			sessions := f.sessions(scope)
			foreign := uuid.NewString()
			if err := sessions.AddSessionMeta(ctx, store.SessionMeta{ID: foreign, UserID: uuid.NewString(), Purpose: store.SessionPurposeChat, Title: "Other owner's Chat", CreatedAt: time.Now(), UpdatedAt: time.Now()}); err != nil {
				t.Fatal(err)
			}
			promotion := store.BotPromotionRequest{Name: "Denied", IdempotencyKey: "foreign", Charter: "Observe", Reviewed: true, ReviewedForTeam: true}
			f.request("POST", "/api/studio/bots/promote-chat/"+foreign+suffix, promotion, 403)
			jobs := f.jobs(scope)
			create := func(mode, owner string) *store.ScheduledJob {
				job := &store.ScheduledJob{Name: "Source", Mode: mode, Enabled: true, OwnerID: owner, Schedule: store.JobSchedule{Cron: "0 9 * * *", Timezone: "UTC"}, Payload: store.JobPayload{Instructions: "Observe", Params: map[string]string{"credential": "never-copy"}}, Delivery: store.JobDelivery{Channel: "email", Target: "never-copy@example.test"}}
				if err := jobs.Add(ctx, job); err != nil {
					t.Fatal(err)
				}
				return job
			}
			foreignJob := create("adaptive", uuid.NewString())
			f.request("GET", "/api/studio/bots/convert-adaptive/"+foreignJob.ID+suffix, nil, 403)
			wrongMode := create("routine", f.owner)
			f.request("GET", "/api/studio/bots/convert-adaptive/"+wrongMode.ID+suffix, nil, 409)
			job := create("adaptive", f.owner)
			path := "/api/studio/bots/convert-adaptive/" + job.ID + suffix
			var review store.BotAdaptiveReview
			data := f.request("GET", path, nil, 200)
			if strings.Contains(string(data), "never-copy") {
				t.Fatalf("review leaked authority: %s", data)
			}
			if err := json.Unmarshal(data, &review); err != nil {
				t.Fatal(err)
			}
			q := store.BotConversionRequest{Name: "Replacement", IdempotencyKey: "guard-conversion", Charter: "Observe", ExpectedUpdatedAt: review.UpdatedAt, ReviewedForTeam: true}
			f.request("POST", path, q, 403)
			q.Reviewed = true
			if scope == "team" {
				q.ReviewedForTeam = false
				f.request("POST", path, q, 403)
				q.ReviewedForTeam = true
			}
			if !jobs.Get(ctx, job.ID).Enabled {
				t.Fatal("unreviewed conversion paused source")
			}
			f.request("POST", path, q, 201)
			second := create("adaptive", f.owner)
			f.request("POST", "/api/studio/bots/convert-adaptive/"+second.ID+suffix, q, 409)
			if !jobs.Get(ctx, second.ID).Enabled {
				t.Fatal("idempotency source conflict paused source")
			}
		})
	}
}
