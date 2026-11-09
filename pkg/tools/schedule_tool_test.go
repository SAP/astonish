package tools

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/SAP/astonish/pkg/scheduler"
	"github.com/SAP/astonish/pkg/store"
)

// mockSchedulerAccess is a minimal mock for testing schedule tool functions.
type mockSchedulerAccess struct {
	jobs []*SchedulerJob
}

func (m *mockSchedulerAccess) AddJob(job *SchedulerJob) error {
	job.ID = fmt.Sprintf("mock-%d", len(m.jobs)+1)
	m.jobs = append(m.jobs, job)
	return nil
}

func (m *mockSchedulerAccess) ListJobs() []*SchedulerJob {
	return m.jobs
}

func (m *mockSchedulerAccess) RemoveJob(id string) error {
	for i, j := range m.jobs {
		if j.ID == id {
			m.jobs = append(m.jobs[:i], m.jobs[i+1:]...)
			return nil
		}
	}
	return fmt.Errorf("not found")
}

func (m *mockSchedulerAccess) UpdateJob(job *SchedulerJob) error {
	for i, j := range m.jobs {
		if j.ID == job.ID {
			m.jobs[i] = job
			return nil
		}
	}
	return fmt.Errorf("not found")
}

func (m *mockSchedulerAccess) RunNow(_ context.Context, _ string) (string, error) {
	return "ok", nil
}

func (m *mockSchedulerAccess) GetJobByName(name string) *SchedulerJob {
	for _, j := range m.jobs {
		if j.Name == name {
			return j
		}
	}
	return nil
}

func (m *mockSchedulerAccess) ValidateCron(_ string) error {
	return nil
}

func TestUpdateScheduledJob_ByID(t *testing.T) {
	mock := &mockSchedulerAccess{}
	schedulerAccessVar = mock
	defer func() { schedulerAccessVar = nil }()

	// Create a job
	mock.AddJob(&SchedulerJob{
		Name:    "My Report",
		Mode:    "adaptive",
		Cron:    "0 9 * * *",
		Enabled: false,
	})

	enabled := true
	result, err := updateScheduledJob(nil, UpdateScheduledJobArgs{
		JobID:   "mock-1",
		Enabled: &enabled,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != "updated" {
		t.Errorf("Status = %q, want %q (message: %s)", result.Status, "updated", result.Message)
	}
	if !mock.jobs[0].Enabled {
		t.Error("expected job to be enabled after update")
	}
}

func TestUpdateScheduledJob_ByName(t *testing.T) {
	mock := &mockSchedulerAccess{}
	schedulerAccessVar = mock
	defer func() { schedulerAccessVar = nil }()

	mock.AddJob(&SchedulerJob{
		Name:    "Proxmox Memory Report",
		Mode:    "adaptive",
		Cron:    "30 18 * * *",
		Enabled: false,
	})

	// Pass the name in the job_id field — the LLM sometimes does this
	enabled := true
	result, err := updateScheduledJob(nil, UpdateScheduledJobArgs{
		JobID:   "Proxmox Memory Report",
		Enabled: &enabled,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != "updated" {
		t.Errorf("Status = %q, want %q (message: %s)", result.Status, "updated", result.Message)
	}
	if !mock.jobs[0].Enabled {
		t.Error("expected job to be enabled after name-based lookup")
	}
}

func TestUpdateScheduledJob_NotFound(t *testing.T) {
	mock := &mockSchedulerAccess{}
	schedulerAccessVar = mock
	defer func() { schedulerAccessVar = nil }()

	enabled := true
	result, err := updateScheduledJob(nil, UpdateScheduledJobArgs{
		JobID:   "nonexistent",
		Enabled: &enabled,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != "error" {
		t.Errorf("Status = %q, want %q", result.Status, "error")
	}
}

func TestUpdateScheduledJob_ScheduleChange(t *testing.T) {
	mock := &mockSchedulerAccess{}
	schedulerAccessVar = mock
	defer func() { schedulerAccessVar = nil }()

	mock.AddJob(&SchedulerJob{
		Name:    "Test Job",
		Mode:    "adaptive",
		Cron:    "0 9 * * *",
		Enabled: true,
	})

	result, err := updateScheduledJob(nil, UpdateScheduledJobArgs{
		JobID:    "mock-1",
		Schedule: "0 18 * * *",
		Timezone: "America/Sao_Paulo",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != "updated" {
		t.Errorf("Status = %q, want %q (message: %s)", result.Status, "updated", result.Message)
	}
	if mock.jobs[0].Cron != "0 18 * * *" {
		t.Errorf("Cron = %q, want %q", mock.jobs[0].Cron, "0 18 * * *")
	}
	if mock.jobs[0].Timezone != "America/Sao_Paulo" {
		t.Errorf("Timezone = %q, want %q", mock.jobs[0].Timezone, "America/Sao_Paulo")
	}
}

func TestUpdateScheduledJob_NoScheduler(t *testing.T) {
	schedulerAccessVar = nil

	result, err := updateScheduledJob(nil, UpdateScheduledJobArgs{
		JobID: "anything",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != "error" {
		t.Errorf("Status = %q, want %q", result.Status, "error")
	}
}

func TestScheduleJob_RejectsDuplicateName(t *testing.T) {
	mock := &mockSchedulerAccess{}
	schedulerAccessVar = mock
	defer func() { schedulerAccessVar = nil }()

	// Create a job (simulates the test_first job that's already in the store)
	mock.AddJob(&SchedulerJob{
		Name:    "Daily Report",
		Mode:    "adaptive",
		Cron:    "0 9 * * *",
		Enabled: false,
	})

	// Attempt to create another job with the same name — should be rejected
	result, err := scheduleJob(nil, ScheduleJobArgs{
		Name:         "Daily Report",
		Mode:         "adaptive",
		Schedule:     "0 18 * * *",
		Instructions: "Do something",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Status != "error" {
		t.Errorf("Status = %q, want %q", result.Status, "error")
	}
	if len(mock.jobs) != 1 {
		t.Errorf("expected 1 job, got %d (duplicate was created)", len(mock.jobs))
	}
}

type memSchedulerStore struct {
	jobs map[string]*store.ScheduledJob
}

func newMemSchedulerStore() *memSchedulerStore {
	return &memSchedulerStore{jobs: make(map[string]*store.ScheduledJob)}
}

func (m *memSchedulerStore) List(context.Context) []*store.ScheduledJob {
	out := make([]*store.ScheduledJob, 0, len(m.jobs))
	for _, j := range m.jobs {
		out = append(out, j)
	}
	return out
}
func (m *memSchedulerStore) Get(_ context.Context, id string) *store.ScheduledJob {
	return m.jobs[id]
}
func (m *memSchedulerStore) GetByName(_ context.Context, name string) *store.ScheduledJob {
	for _, j := range m.jobs {
		if j.Name == name {
			return j
		}
	}
	return nil
}
func (m *memSchedulerStore) Add(_ context.Context, job *store.ScheduledJob) error {
	if job.ID == "" {
		job.ID = fmt.Sprintf("job-%d", len(m.jobs)+1)
	}
	cp := *job
	m.jobs[job.ID] = &cp
	return nil
}
func (m *memSchedulerStore) Update(_ context.Context, job *store.ScheduledJob) error {
	cp := *job
	m.jobs[job.ID] = &cp
	return nil
}
func (m *memSchedulerStore) Remove(_ context.Context, id string) error {
	delete(m.jobs, id)
	return nil
}

func TestResolveSchedulerStore_DefaultsToPersonal(t *testing.T) {
	for _, requested := range []string{"", store.JobScopePersonal, store.JobScopeTeam} {
		t.Run("scope="+requested, func(t *testing.T) {
			personal, team := newMemSchedulerStore(), newMemSchedulerStore()
			ctx := store.WithPersonalSchedulerStore(context.Background(), personal)
			ctx = store.WithSchedulerStore(ctx, team)
			want, other, wantScope := personal, team, store.JobScopePersonal
			if requested == store.JobScopeTeam {
				want, other, wantScope = team, personal, store.JobScopeTeam
			}
			ss, scope := resolveSchedulerStore(ctx, requested)
			if ss == nil || scope != wantScope {
				t.Fatalf("store=%v scope=%q, want non-nil %q store", ss, scope, wantScope)
			}
			// Security decorators need not preserve store pointer identity.
			job := &store.ScheduledJob{ID: "selected", Name: "ordinary"}
			if err := ss.Add(ctx, job); err != nil {
				t.Fatal(err)
			}
			if want.Get(ctx, job.ID) == nil || other.Get(ctx, job.ID) != nil {
				t.Fatal("Add must reach only the selected scope")
			}
			if err := other.Add(ctx, &store.ScheduledJob{ID: "other", Name: "other"}); err != nil {
				t.Fatal(err)
			}
			if ss.Get(ctx, "other") != nil || ss.GetByName(ctx, "other") != nil || len(ss.List(ctx)) != 1 {
				t.Fatal("reads crossed scopes")
			}
			job.Name = "updated"
			if err := ss.Update(ctx, job); err != nil {
				t.Fatal(err)
			}
			if got := want.Get(ctx, job.ID); got == nil || got.Name != job.Name {
				t.Fatalf("Update did not reach selected store: %v", got)
			}
			if err := ss.Remove(ctx, job.ID); err != nil {
				t.Fatal(err)
			}
			if want.Get(ctx, job.ID) != nil || other.Get(ctx, job.ID) != nil || other.Get(ctx, "other") == nil {
				t.Fatal("mutations did not remain isolated")
			}
		})
	}
}

func TestResolveSchedulerStore_SingleScope(t *testing.T) {
	for _, requested := range []string{"", store.JobScopePersonal, store.JobScopeTeam} {
		team := newMemSchedulerStore()
		ctx := store.WithSchedulerStore(context.Background(), team)
		ss, scope := resolveSchedulerStore(ctx, requested)
		if ss == nil || scope != store.JobScopeTeam {
			t.Fatalf("expected team fallback for %q, got store=%v scope=%q", requested, ss, scope)
		}
		if err := ss.Add(ctx, &store.ScheduledJob{ID: "team-job"}); err != nil {
			t.Fatal(err)
		}
		if team.Get(ctx, "team-job") == nil {
			t.Fatal("fallback Add did not reach team store")
		}
	}
	ctx := store.WithPersonalSchedulerStore(context.Background(), newMemSchedulerStore())
	if ss, scope := resolveSchedulerStore(ctx, store.JobScopeTeam); ss != nil || scope != store.JobScopeTeam {
		t.Fatalf("explicit team scope must not fall back to personal: store=%v scope=%q", ss, scope)
	}
	for _, ctx := range []context.Context{nil, context.Background()} {
		if ss, _ := resolveSchedulerStore(ctx, ""); ss != nil {
			t.Fatal("missing context stores must remain nil")
		}
	}
}

func TestFindJobAcrossScopes_PrefersPersonal(t *testing.T) {
	for _, lookup := range []string{"same", "shared-id"} {
		t.Run(lookup, func(t *testing.T) {
			personal, team := newMemSchedulerStore(), newMemSchedulerStore()
			ctx := store.WithPersonalSchedulerStore(context.Background(), personal)
			ctx = store.WithSchedulerStore(ctx, team)
			for _, ss := range []*memSchedulerStore{personal, team} {
				if err := ss.Add(ctx, &store.ScheduledJob{ID: "shared-id", Name: "same"}); err != nil {
					t.Fatal(err)
				}
			}
			job, ss, scope := findJobAcrossScopes(ctx, lookup)
			if job == nil || job.ID != "shared-id" || job.Scope != store.JobScopePersonal || scope != store.JobScopePersonal || ss == nil {
				t.Fatalf("expected personal job, got scope=%q job=%v", scope, job)
			}
			updated := *job
			updated.Enabled = true
			if err := ss.Update(ctx, &updated); err != nil {
				t.Fatal(err)
			}
			if !personal.Get(ctx, job.ID).Enabled || team.Get(ctx, job.ID).Enabled {
				t.Fatal("Update must affect personal job only")
			}
			if err := ss.Remove(ctx, job.ID); err != nil {
				t.Fatal(err)
			}
			if personal.Get(ctx, job.ID) != nil || team.Get(ctx, job.ID) == nil {
				t.Fatal("Remove must affect personal job only")
			}
			job, ss, scope = findJobAcrossScopes(ctx, lookup)
			if job == nil || job.Scope != store.JobScopeTeam || scope != store.JobScopeTeam || ss == nil {
				t.Fatalf("expected team fallback, got scope=%q job=%v", scope, job)
			}
			if err := ss.Remove(ctx, job.ID); err != nil {
				t.Fatal(err)
			}
			if team.Get(ctx, job.ID) != nil {
				t.Fatal("fallback Remove did not reach team store")
			}
			if job, ss, scope := findJobAcrossScopes(ctx, lookup); job != nil || ss != nil || scope != "" {
				t.Fatalf("expected missing lookup, got job=%v store=%v scope=%q", job, ss, scope)
			}
		})
	}
}

func TestSchedulerRouting_DeniesBotMutationsWithoutCrossScopeFallback(t *testing.T) {
	ownership := []struct {
		name string
		job  store.ScheduledJob
	}{
		{"mode", store.ScheduledJob{Mode: string(scheduler.ModeBotTrigger)}},
		{"bot-binding", store.ScheduledJob{Mode: string(scheduler.ModeAdaptive), Payload: store.JobPayload{BotID: "bot-1"}}},
		{"trigger-binding", store.ScheduledJob{Mode: string(scheduler.ModeAdaptive), Payload: store.JobPayload{BotTriggerID: "trigger-1"}}},
	}
	for _, scope := range []string{store.JobScopePersonal, store.JobScopeTeam} {
		for _, tc := range ownership {
			t.Run(scope+"/"+tc.name, func(t *testing.T) {
				personal, team := newMemSchedulerStore(), newMemSchedulerStore()
				ctx := store.WithPersonalSchedulerStore(context.Background(), personal)
				ctx = store.WithSchedulerStore(ctx, team)
				selected, other := personal, team
				if scope == store.JobScopeTeam {
					selected, other = team, personal
				}
				bot := tc.job
				bot.ID, bot.Name = "bot-job", "Bot schedule"
				if err := selected.Add(ctx, &bot); err != nil {
					t.Fatal(err)
				}
				ordinary := &store.ScheduledJob{ID: "ordinary", Name: "ordinary", Mode: string(scheduler.ModeAdaptive)}
				if err := selected.Add(ctx, ordinary); err != nil {
					t.Fatal(err)
				}
				// Denial of a personal Bot match must not fall through to an
				// ordinary team job with the same ID/name.
				if scope == store.JobScopePersonal {
					if err := other.Add(ctx, &store.ScheduledJob{ID: bot.ID, Name: bot.Name, Mode: string(scheduler.ModeAdaptive)}); err != nil {
						t.Fatal(err)
					}
				}
				resolved, _ := resolveSchedulerStore(ctx, scope)
				for _, route := range []string{"resolve", "id", "name"} {
					t.Run(route, func(t *testing.T) {
						ss := resolved
						if route != "resolve" {
							lookup := bot.ID
							if route == "name" {
								lookup = bot.Name
							}
							job, foundStore, foundScope := findJobAcrossScopes(ctx, lookup)
							if job == nil || foundStore == nil || foundScope != scope {
								t.Fatalf("Bot lookup changed scope: job=%v scope=%q", job, foundScope)
							}
							ss = foundStore
						}
						before := *selected.Get(ctx, bot.ID)
						created, promoted := bot, bot
						created.ID, promoted.ID = "new-bot", ordinary.ID
						disguised := &store.ScheduledJob{ID: bot.ID, Mode: string(scheduler.ModeAdaptive), Enabled: true}
						for operation, err := range map[string]error{
							"add Bot":          ss.Add(ctx, &created),
							"update Bot":       ss.Update(ctx, &bot),
							"disguise Bot":     ss.Update(ctx, disguised),
							"promote ordinary": ss.Update(ctx, &promoted),
							"remove Bot":       ss.Remove(ctx, bot.ID),
						} {
							if !errors.Is(err, store.ErrBotForbidden) {
								t.Errorf("%s: got %v, want ErrBotForbidden", operation, err)
							}
						}
						if got := selected.Get(ctx, bot.ID); got == nil || !reflect.DeepEqual(*got, before) {
							t.Fatalf("denied mutation changed Bot job: %v", got)
						}
						if selected.Get(ctx, created.ID) != nil || !reflect.DeepEqual(selected.Get(ctx, ordinary.ID), ordinary) {
							t.Fatal("denied mutation created or promoted a job")
						}
						if scope == store.JobScopePersonal {
							got := other.Get(ctx, bot.ID)
							if got == nil || got.Name != bot.Name || got.Enabled || store.BotOwnedSchedule(got) || len(other.List(ctx)) != 1 {
								t.Fatalf("denied mutation fell through to other scope: %v", got)
							}
						} else if len(other.List(ctx)) != 0 {
							t.Fatal("denied mutation wrote to other scope")
						}
					})
				}
			})
		}
	}
}
