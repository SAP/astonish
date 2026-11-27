package api

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

func TestBotPersonalOnlyCreationHTTP(t *testing.T) {
	for _, role := range []string{"member", "admin"} {
		t.Run(role, func(t *testing.T) {
			f := newReviewedHTTPFixture(t, role)
			root := "/api/studio/bots"
			for _, title := range []bool{false, true} {
				raw := f.request("POST", root+"?scope=team", map[string]any{"name": "Denied", "idempotency_key": uuid.NewString(), "start_from_title": title}, 403)
				if !strings.Contains(string(raw), "personal scope") {
					t.Fatal(string(raw))
				}
			}
			f.request("POST", root+"?scope=other", `{"name":"Invalid"}`, 400)
			f.request("POST", root+"?scope=team&scope=personal", `{"name":"Invalid"}`, 400)
			f.request("POST", root+"?scope=personal", `{"name":"Personal","idempotency_key":"personal"}`, 201)
			f.request("POST", root, `{"name":"Default personal","idempotency_key":"default"}`, 201)

			for _, mode := range []store.BotIntakeMode{store.BotIntakeChat, store.BotIntakeAdaptive} {
				t.Run(string(mode), func(t *testing.T) {
					var source uuid.UUID
					var before any
					if mode == store.BotIntakeChat {
						source = uuid.New()
						if err := f.sessions("team").AddSessionMeta(f.ctx, store.SessionMeta{ID: source.String(), UserID: f.owner, Purpose: store.SessionPurposeChat, Title: "Existing Chat", CreatedAt: time.Now(), UpdatedAt: time.Now()}); err != nil {
							t.Fatal(err)
						}
						before, _ = f.sessions("team").GetSessionMeta(f.ctx, source.String())
						f.request("POST", root+"/promote-chat/"+source.String()+"?scope=team", store.BotPromotionRequest{Name: "Denied", Charter: "Observe", Reviewed: true, ReviewedForTeam: true, IdempotencyKey: uuid.NewString()}, 403)
					} else {
						job := &store.ScheduledJob{Name: "Existing adaptive", Mode: "adaptive", Enabled: true, OwnerID: f.owner, Schedule: store.JobSchedule{Cron: "0 9 * * *", Timezone: "UTC"}, Payload: store.JobPayload{Instructions: "Observe"}}
						if err := f.jobs("team").Add(f.ctx, job); err != nil {
							t.Fatal(err)
						}
						source = uuid.MustParse(job.ID)
						before = f.jobs("team").Get(f.ctx, job.ID)
						var review store.BotAdaptiveReview
						if err := json.Unmarshal(f.request("GET", root+"/convert-adaptive/"+source.String()+"?scope=team", nil, 200), &review); err != nil {
							t.Fatal(err)
						}
						f.request("POST", root+"/convert-adaptive/"+source.String()+"?scope=team", store.BotConversionRequest{Name: "Denied", Charter: "Observe", Reviewed: true, ReviewedForTeam: true, ExpectedUpdatedAt: review.UpdatedAt, IdempotencyKey: uuid.NewString()}, 403)
					}
					q := store.BotSourceIntakeRequest{Mode: mode, SourceID: source, Name: "Denied proposal", Charter: "Observe", IdempotencyKey: uuid.NewString()}
					f.request("POST", root+"/source-proposals?scope=team", q, 403)
					// Seed an owned durable proposal as saved by an older product version.
					intake := f.bots("team").(store.BotSourceIntakeStore)
					p, err := intake.ProposeSourceIntake(f.ctx, q, nil)
					if err != nil {
						t.Fatal(err)
					}
					path := root + "/source-proposals/" + p.ID.String()
					f.request("GET", path+"?scope=team", nil, 200)
					for _, reviewed := range []bool{false, true} {
						f.request("POST", path+"/decision?scope=team", store.BotSourceIntakeDecision{Digest: p.Digest, Decision: "confirm", ReviewedForTeam: reviewed}, 403)
					}
					unchanged, err := intake.GetSourceIntake(f.ctx, p.ID)
					if err != nil || !reflect.DeepEqual(p, unchanged) {
						t.Fatalf("denial mutated proposal: %+v %v", unchanged, err)
					}
					var after any
					if mode == store.BotIntakeChat {
						after, _ = f.sessions("team").GetSessionMeta(f.ctx, source.String())
					} else {
						after = f.jobs("team").Get(f.ctx, source.String())
					}
					if !reflect.DeepEqual(before, after) {
						t.Fatalf("denial mutated source: before=%+v after=%+v", before, after)
					}
					f.request("POST", path+"/decision?scope=team", store.BotSourceIntakeDecision{Digest: p.Digest, Decision: "reject"}, 200)
				})
			}
			rows, err := f.bots("team").List(f.ctx, store.BotPage{})
			if err != nil || len(rows) != 0 {
				t.Fatalf("team bots created: %+v %v", rows, err)
			}
			// Existing team Bots are data/runtime fixtures, not newly admitted HTTP creation.
			v := store.DefaultBotVersion()
			v.Charter = "Existing team Bot"
			existing, err := f.bots("team").Create(f.ctx, store.BotCreateRequest{Name: "Existing", OwnerID: uuid.MustParse(f.owner), Version: v, IdempotencyKey: "existing"})
			if err != nil {
				t.Fatal(err)
			}
			f.request("GET", root+"/"+existing.ID.String()+"?scope=team", nil, 200)
			f.request("GET", root+"?scope=team", nil, 200)
		})
	}
}
