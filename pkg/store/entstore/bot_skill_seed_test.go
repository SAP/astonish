package entstore

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	personal "github.com/SAP/astonish/ent/personal"
	team "github.com/SAP/astonish/ent/team"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

func testIntakeSkillSeed() *store.BotSkillSeed {
	return &store.BotSkillSeed{Title: "Weekly research", Responsibility: "Research public updates", OwnerExpectation: "Report changes with source links", Method: []string{"Read public sources", "Summarize changes"}}
}
func seedTestDB(b store.BotStore) botDraftDB {
	switch b := b.(type) {
	case *personalBotStore:
		return personalBotDB{client: b.client}
	case *teamBotStore:
		return teamBotDB{client: b.client}
	}
	panic("unsupported scope")
}
func assertIntakeSeed(t *testing.T, ctx context.Context, bots store.BotStore, b *store.Bot) uuid.UUID {
	t.Helper()
	skills := bots.(store.BotSkillStore)
	list, err := skills.ListSkills(ctx, b.ID, store.BotSkillPage{})
	if err != nil || len(list.Skills) != 1 {
		t.Fatalf("seed missing: %+v %v", list, err)
	}
	s := list.Skills[0]
	v, err := skills.GetSkillRevision(ctx, b.ID, s.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if b.Status != store.BotDraft || s.Status != store.BotSkillDraft || s.BotID != b.ID || s.Legacy != nil || v.ParentRevision != 0 || v.ParentDigest != "" || v.RevertsRevision != nil {
		t.Fatalf("not fresh draft: %+v %+v", s, v)
	}
	if err := validateSkillRevisionSources(ctx, seedTestDB(bots), *v); err != nil {
		t.Fatalf("unresolvable seed source: %v", err)
	}
	for _, p := range append(v.Provenance, v.Expectations[0].Provenance) {
		if p.BotID != b.ID || p.RunID != uuid.Nil || p.SessionID != uuid.Nil || p.EventID != "" || p.Legacy != nil || len(p.EvidenceRefs) != 0 || p.Level == store.BotSkillObserved {
			t.Fatalf("foreign provenance: %+v", p)
		}
	}
	if v.Expectations[0].Text != testIntakeSkillSeed().OwnerExpectation || v.Expectations[0].Provenance.Level != store.BotSkillOwnerSpecified {
		t.Fatalf("owner source lost: %+v", v)
	}
	version, err := seedTestDB(bots).ReadVersion(ctx, b.ID, b.Version)
	if err != nil {
		t.Fatal(err)
	}
	if len(version.Triggers) != 1 || version.Triggers[0].SkillID == nil || *version.Triggers[0].SkillID != s.ID || version.Triggers[0].Invocation == "" || version.Triggers[0].Instructions != "" || version.Triggers[0].Enabled || version.Triggers[0].ScheduleID != nil {
		t.Fatalf("not inactive skill trigger: %+v", version.Triggers)
	}
	return s.ID
}
func TestBotAdaptiveSkillSeedAtomicIsolation(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		for _, proposal := range []bool{false, true} {
			name := "direct"
			if proposal {
				name = "proposal"
			}
			t.Run(string(scope)+"/"+name, func(t *testing.T) {
				ctx, _, _, _, reopen := ApprovalScopeReopenFixture(t, scope)
				bots, _ := reopen()
				db := seedTestDB(bots)
				principal, _ := execution.PrincipalFromContext(ctx)
				jobs := db.(botTriggerDB).triggerScheduler()
				job := &store.ScheduledJob{Name: "Legacy", Mode: "adaptive", Enabled: true, OwnerID: principal.Subject, Schedule: store.JobSchedule{Cron: "0 8 * * *", Timezone: "UTC"}, Payload: store.JobPayload{Instructions: "Old model procedure"}}
				if err := jobs.Add(ctx, job); err != nil {
					t.Fatal(err)
				}
				intake := bots.(store.BotIntakeStore)
				reviewed, err := intake.ReviewAdaptiveJob(ctx, uuid.MustParse(job.ID))
				if err != nil {
					t.Fatal(err)
				}
				fail := true
				switch b := bots.(type) {
				case *personalBotStore:
					b.client.BotSkillRevision.Use(func(next personal.Mutator) personal.Mutator {
						return personal.MutateFunc(func(c context.Context, m personal.Mutation) (personal.Value, error) {
							if fail {
								return nil, errors.New("seed write failed")
							}
							return next.Mutate(c, m)
						})
					})
				case *teamBotStore:
					b.client.BotSkillRevision.Use(func(next team.Mutator) team.Mutator {
						return team.MutateFunc(func(c context.Context, m team.Mutation) (team.Value, error) {
							if fail {
								return nil, errors.New("seed write failed")
							}
							return next.Mutate(c, m)
						})
					})
				}
				q := store.BotConversionRequest{SourceJobID: reviewed.SourceJobID, ExpectedUpdatedAt: reviewed.UpdatedAt, Name: "New", Charter: "Observe", Reviewed: true, ReviewedForTeam: true, IdempotencyKey: "seed", SkillSeed: testIntakeSkillSeed()}
				apply := func() (*store.Bot, error) { return intake.ConvertAdaptiveJob(ctx, q) }
				if proposal {
					src := bots.(store.BotSourceIntakeStore)
					p, e := src.ProposeSourceIntake(ctx, store.BotSourceIntakeRequest{Mode: store.BotIntakeAdaptive, SourceID: q.SourceJobID, Name: q.Name, Charter: q.Charter, IdempotencyKey: q.IdempotencyKey, SkillSeed: q.SkillSeed}, nil)
					if e != nil {
						t.Fatal(e)
					}
					apply = func() (*store.Bot, error) {
						p, e := src.DecideSourceIntake(ctx, p.ID, store.BotSourceIntakeDecision{Digest: p.Digest, Decision: "confirm", ReviewedForTeam: true}, nil, nil)
						if e != nil {
							return nil, e
						}
						if p.BotID == nil {
							return nil, errors.New("no replacement")
						}
						return bots.Get(ctx, *p.BotID)
					}
				}
				if _, err := apply(); err == nil {
					t.Fatal("seed insertion failure did not fail conversion")
				}
				if !jobs.Get(ctx, job.ID).Enabled {
					t.Fatal("original paused on failure")
				}
				switch d := db.(type) {
				case personalBotDB:
					if d.client.Bot.Query().CountX(ctx) != 1 || d.client.BotSkill.Query().CountX(ctx) != 0 || d.client.BotSkillRevision.Query().CountX(ctx) != 0 || d.client.BotGoal.Query().CountX(ctx) != 0 {
						t.Fatal("partial destination survived")
					}
				case teamBotDB:
					if d.client.Bot.Query().CountX(ctx) != 1 || d.client.BotSkill.Query().CountX(ctx) != 0 || d.client.BotSkillRevision.Query().CountX(ctx) != 0 || d.client.BotGoal.Query().CountX(ctx) != 0 {
						t.Fatal("partial destination survived")
					}
				}
				fail = false
				b, err := apply()
				if err != nil {
					t.Fatal(err)
				}
				id := assertIntakeSeed(t, ctx, bots, b)
				if jobs.Get(ctx, job.ID).Enabled {
					t.Fatal("original still active")
				}
				replay, err := apply()
				if err != nil || replay.ID != b.ID {
					t.Fatalf("replay: %+v %v", replay, err)
				}
				if got := assertIntakeSeed(t, ctx, bots, replay); got != id {
					t.Fatal("replay duplicated skill")
				}
				otherVersion := store.DefaultBotVersion()
				otherVersion.Charter = "Unrelated"
				other, err := bots.CreateDraft(ctx, store.BotCreateRequest{Name: "Other", OwnerID: b.OwnerID, IdempotencyKey: "other", Version: otherVersion})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := bots.(store.BotSkillStore).GetSkill(ctx, other.ID, id); !errors.Is(err, store.ErrBotNotFound) {
					t.Fatalf("cross Bot access: %v", err)
				}
				switch d := db.(type) {
				case personalBotDB:
					if d.client.ScheduledJob.Query().CountX(ctx) != 1 {
						t.Fatal("duplicate schedule")
					}
				case teamBotDB:
					if d.client.ScheduledJob.Query().CountX(ctx) != 1 {
						t.Fatal("duplicate schedule")
					}
				}
			})
		}
	}
}

func TestBotSkillSeedOwnerSourceAndLegacyCompatibility(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			ctx, _, _, _, reopen := ApprovalScopeReopenFixture(t, scope)
			bots, _ := reopen()
			principal, _ := execution.PrincipalFromContext(ctx)
			jobs := seedTestDB(bots).(botTriggerDB).triggerScheduler()
			job := &store.ScheduledJob{Name: "Legacy", Mode: "adaptive", Enabled: true, OwnerID: principal.Subject, Schedule: store.JobSchedule{Cron: "0 8 * * *", Timezone: "UTC"}, Payload: store.JobPayload{Instructions: "Original procedure"}}
			if err := jobs.Add(ctx, job); err != nil {
				t.Fatal(err)
			}
			intake := bots.(store.BotIntakeStore)
			review, err := intake.ReviewAdaptiveJob(ctx, uuid.MustParse(job.ID))
			if err != nil {
				t.Fatal(err)
			}
			if !jobs.Get(ctx, job.ID).Enabled {
				t.Fatal("review mutated source")
			}
			seed := testIntakeSkillSeed()
			seed.OwnerExpectation = ""
			q := store.BotConversionRequest{SourceJobID: review.SourceJobID, ExpectedUpdatedAt: review.UpdatedAt, Name: "No inference", Charter: "Observe", Reviewed: true, ReviewedForTeam: true, IdempotencyKey: "legacy", SkillSeed: seed}
			if _, err := intake.ConvertAdaptiveJob(ctx, q); !errors.Is(err, store.ErrBotInvalid) {
				t.Fatalf("fabricated owner expectation: %v", err)
			}
			if !jobs.Get(ctx, job.ID).Enabled {
				t.Fatal("invalid seed paused source")
			}
			q.SkillSeed = nil
			b, err := intake.ConvertAdaptiveJob(ctx, q)
			if err != nil {
				t.Fatal(err)
			}
			list, err := bots.(store.BotSkillStore).ListSkills(ctx, b.ID, store.BotSkillPage{})
			if err != nil || len(list.Skills) != 0 {
				t.Fatalf("implicit migration %+v %v", list, err)
			}
			v, err := seedTestDB(bots).ReadVersion(ctx, b.ID, b.Version)
			if err != nil {
				t.Fatal(err)
			}
			if v.Triggers[0].SkillID != nil || v.Triggers[0].Instructions != "Original procedure" {
				t.Fatalf("legacy changed %+v", v.Triggers)
			}
		})
	}
	for _, field := range []string{"bot_id", "skill_id", "provenance", "history", "authority_constraint_ref", "credentials"} {
		t.Run("reject-"+field, func(t *testing.T) {
			var seed store.BotSkillSeed
			d := json.NewDecoder(strings.NewReader(`{"title":"safe","` + field + `":"foreign"}`))
			d.DisallowUnknownFields()
			if err := d.Decode(&seed); err == nil {
				t.Fatalf("seed accepted %s", field)
			}
		})
	}
}

func TestBotTemplateSkillSeedFreshCopies(t *testing.T) {
	ctx, _, _, _, reopen := ApprovalScopeReopenFixture(t, store.BotScopePersonal)
	destination, _ := reopen()
	tc := newTeamClient(t)
	auth := botRepositoryAuth{orgSlug: "org", teamSlug: "team", scope: store.BotScopeTeam, check: func(context.Context, string) (string, error) { return "admin", nil }}
	source := &teamBotStore{client: tc, auth: auth}
	templates := &teamBotTemplateStore{client: tc, auth: auth}
	def := store.BotTemplateDefinition{SchemaVersion: store.BotSchemaVersion, Name: "Seed template", Charter: "Observe", SuggestedSchedules: []store.BotScheduleSuggestion{{Cron: "0 8 * * *", Timezone: "UTC", SkillSeed: testIntakeSkillSeed()}}}
	template, err := templates.Publish(ctx, def, 0)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[uuid.UUID]bool{}
	fail := true
	pc := destination.(*personalBotStore).client
	pc.BotSkillRevision.Use(func(next personal.Mutator) personal.Mutator {
		return personal.MutateFunc(func(c context.Context, m personal.Mutation) (personal.Value, error) {
			if fail {
				return nil, errors.New("template seed failure")
			}
			return next.Mutate(c, m)
		})
	})
	if _, e := templates.ForkToPrivate(ctx, destination, store.BotTemplateForkRequest{TemplateID: template.ID, TemplateRevision: template.Revision, Name: "rollback", IdempotencyKey: "rollback"}); e == nil {
		t.Fatal("missing template failure")
	}
	if pc.Bot.Query().CountX(ctx) != 1 || pc.BotSkill.Query().CountX(ctx) != 0 || pc.BotSkillRevision.Query().CountX(ctx) != 0 || pc.BotGoal.Query().CountX(ctx) != 0 {
		t.Fatal("partial template survived")
	}
	fail = false
	for _, key := range []string{"first", "second", "proposal"} {
		var b *store.Bot
		if key == "proposal" {
			p, e := source.ProposeSourceIntake(ctx, store.BotSourceIntakeRequest{Mode: store.BotIntakeTemplate, SourceID: template.ID, TemplateRevision: template.Revision, Name: key, IdempotencyKey: key}, templates)
			if e != nil {
				t.Fatal(e)
			}
			applied, e := source.DecideSourceIntake(ctx, p.ID, store.BotSourceIntakeDecision{Digest: p.Digest, Decision: "confirm"}, templates, destination)
			if e != nil {
				t.Fatal(e)
			}
			b, err = destination.Get(ctx, *applied.BotID)
		} else {
			q := store.BotTemplateForkRequest{TemplateID: template.ID, TemplateRevision: template.Revision, Name: key, IdempotencyKey: key}
			b, err = templates.ForkToPrivate(ctx, destination, q)
			if err == nil {
				again, e := templates.ForkToPrivate(ctx, destination, q)
				if e != nil || again.ID != b.ID {
					t.Fatalf("template retry: %+v %v", again, e)
				}
			}
		}
		if err != nil {
			t.Fatal(err)
		}
		id := assertIntakeSeed(t, ctx, destination, b)
		if seen[id] {
			t.Fatal("template shared skill identity")
		}
		seen[id] = true
	}
	// Templates published before seeds remain legacy declarations, not implicit imports.
	def.Name = "Legacy template"
	def.SuggestedSchedules[0].SkillSeed = nil
	def.SuggestedSchedules[0].Instructions = "Original template procedure"
	legacy, err := templates.Publish(ctx, def, 0)
	if err != nil {
		t.Fatal(err)
	}
	b, err := templates.ForkToPrivate(ctx, destination, store.BotTemplateForkRequest{TemplateID: legacy.ID, TemplateRevision: legacy.Revision, Name: "legacy", IdempotencyKey: "legacy"})
	if err != nil {
		t.Fatal(err)
	}
	list, err := destination.(store.BotSkillStore).ListSkills(ctx, b.ID, store.BotSkillPage{})
	if err != nil || len(list.Skills) != 0 {
		t.Fatalf("legacy template implicitly imported: %+v %v", list, err)
	}
	v, err := seedTestDB(destination).ReadVersion(ctx, b.ID, 1)
	if err != nil || v.Triggers[0].SkillID != nil || v.Triggers[0].Instructions != "Original template procedure" {
		t.Fatalf("legacy template changed: %+v %v", v, err)
	}
	if tc.BotSkill.Query().CountX(ctx) != 0 {
		t.Fatal("source template acquired private skills")
	}
	if destination.(*personalBotStore).client.ScheduledJob.Query().CountX(ctx) != 0 {
		t.Fatal("draft scheduled prematurely")
	}
}
