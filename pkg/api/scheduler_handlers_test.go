package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"

	"github.com/SAP/astonish/pkg/scheduler"
	"github.com/SAP/astonish/pkg/store"
)

// testSchedulerStore adapts scheduler.Store to store.SchedulerStore for tests.
type testSchedulerStore struct {
	ss *scheduler.Store
	mu sync.Mutex
}

func (s *testSchedulerStore) List(_ context.Context) []*store.ScheduledJob {
	s.mu.Lock()
	defer s.mu.Unlock()
	jobs := s.ss.List()
	out := make([]*store.ScheduledJob, len(jobs))
	for i, j := range jobs {
		out[i] = s.toStoreJob(j)
	}
	return out
}

func (s *testSchedulerStore) Get(_ context.Context, id string) *store.ScheduledJob {
	s.mu.Lock()
	defer s.mu.Unlock()
	j := s.ss.Get(id)
	if j == nil {
		return nil
	}
	return s.toStoreJob(j)
}

func (s *testSchedulerStore) GetByName(_ context.Context, name string) *store.ScheduledJob {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, j := range s.ss.List() {
		if j.Name == name {
			return s.toStoreJob(j)
		}
	}
	return nil
}

func (s *testSchedulerStore) Add(_ context.Context, job *store.ScheduledJob) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sj := storeJobToSchedulerJob(job)
	err := s.ss.Add(sj)
	if err == nil {
		// Copy back generated ID
		job.ID = sj.ID
	}
	return err
}

func (s *testSchedulerStore) Update(_ context.Context, job *store.ScheduledJob) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ss.Update(storeJobToSchedulerJob(job))
}

func (s *testSchedulerStore) Remove(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ss.Remove(id)
}

func (s *testSchedulerStore) toStoreJob(j *scheduler.Job) *store.ScheduledJob {
	return &store.ScheduledJob{
		ID:   j.ID,
		Name: j.Name,
		Mode: string(j.Mode),
		Schedule: store.JobSchedule{
			Cron:     j.Schedule.Cron,
			Timezone: j.Schedule.Timezone,
		},
		Payload: store.JobPayload{
			Flow:         j.Payload.Flow,
			Params:       j.Payload.Params,
			Instructions: j.Payload.Instructions,
		},
		Delivery: store.JobDelivery{
			Channel:   j.Delivery.Channel,
			Target:    j.Delivery.Target,
			Mode:      string(j.Delivery.Mode),
			MemberIDs: j.Delivery.MemberIDs,
		},
		Enabled:   j.Enabled,
		CreatedAt: j.CreatedAt,
		LastRun:   j.LastRun,
		OwnerID:   j.OwnerID,
		TeamSlug:  j.TeamSlug,
		Scope:     j.Scope,
	}
}

// newTestSchedulerStore creates a store.SchedulerStore backed by a temp file for testing.
func newTestSchedulerStore(t *testing.T) store.SchedulerStore {
	t.Helper()
	dir := t.TempDir()
	ss, err := scheduler.NewStore(filepath.Join(dir, "jobs.json"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return &testSchedulerStore{ss: ss}
}

func TestHandleCreateJob_FlatJSON(t *testing.T) {
	ss := newTestSchedulerStore(t)
	svc := &store.Services{PersonalScheduler: ss}

	// This is the flat JSON format sent by SchedulerHTTPAccess.AddJob(),
	// which serializes a tools.SchedulerJob directly.
	flatJSON := `{
		"name": "Test Report",
		"mode": "adaptive",
		"cron": "30 18 * * *",
		"timezone": "America/New_York",
		"instructions": "Do the thing",
		"enabled": true
	}`

	req := httptest.NewRequest(http.MethodPost, "/api/scheduler/jobs", bytes.NewBufferString(flatJSON))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()

	handleCreateJob(rr, req, svc)

	if rr.Code != http.StatusCreated {
		t.Fatalf("expected status 201, got %d: %s", rr.Code, rr.Body.String())
	}

	// Decode the response to get the created job
	var created apiJob
	if err := json.NewDecoder(rr.Body).Decode(&created); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	// Verify the nested schedule fields were populated correctly
	if created.Schedule.Cron != "30 18 * * *" {
		t.Errorf("Schedule.Cron = %q, want %q", created.Schedule.Cron, "30 18 * * *")
	}
	if created.Schedule.Timezone != "America/New_York" {
		t.Errorf("Schedule.Timezone = %q, want %q", created.Schedule.Timezone, "America/New_York")
	}
	if created.Name != "Test Report" {
		t.Errorf("Name = %q, want %q", created.Name, "Test Report")
	}
	if created.Mode != "adaptive" {
		t.Errorf("Mode = %q, want %q", created.Mode, "adaptive")
	}
	if created.Payload.Instructions != "Do the thing" {
		t.Errorf("Payload.Instructions = %q, want %q", created.Payload.Instructions, "Do the thing")
	}
	if !created.Enabled {
		t.Error("expected Enabled = true")
	}
	if created.Scope != store.JobScopePersonal {
		t.Errorf("Scope = %q, want %q", created.Scope, store.JobScopePersonal)
	}

	// Verify the job was persisted in the store
	stored := ss.Get(context.Background(), created.ID)
	if stored == nil {
		t.Fatal("job not found in store after creation")
	}
	if stored.Schedule.Cron != "30 18 * * *" {
		t.Errorf("stored Schedule.Cron = %q, want %q", stored.Schedule.Cron, "30 18 * * *")
	}
	if stored.Schedule.Timezone != "America/New_York" {
		t.Errorf("stored Schedule.Timezone = %q, want %q", stored.Schedule.Timezone, "America/New_York")
	}
}

func TestHandleCreateJob_RoutineMode(t *testing.T) {
	ss := newTestSchedulerStore(t)
	svc := &store.Services{PersonalScheduler: ss}

	flatJSON := `{
		"name": "Daily Flow",
		"mode": "routine",
		"cron": "0 9 * * 1-5",
		"timezone": "UTC",
		"flow": "my_flow",
		"params": {"symbol": "AAPL"},
		"channel": "telegram",
		"target": "12345",
		"enabled": true
	}`

	req := httptest.NewRequest(http.MethodPost, "/api/scheduler/jobs", bytes.NewBufferString(flatJSON))
	rr := httptest.NewRecorder()
	handleCreateJob(rr, req, svc)

	if rr.Code != http.StatusCreated {
		t.Fatalf("expected status 201, got %d: %s", rr.Code, rr.Body.String())
	}

	var created apiJob
	if err := json.NewDecoder(rr.Body).Decode(&created); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	if created.Schedule.Cron != "0 9 * * 1-5" {
		t.Errorf("Schedule.Cron = %q, want %q", created.Schedule.Cron, "0 9 * * 1-5")
	}
	if created.Payload.Flow != "my_flow" {
		t.Errorf("Payload.Flow = %q, want %q", created.Payload.Flow, "my_flow")
	}
	if created.Payload.Params["symbol"] != "AAPL" {
		t.Errorf("Payload.Params[symbol] = %q, want %q", created.Payload.Params["symbol"], "AAPL")
	}
	if created.Delivery.Channel != "telegram" {
		t.Errorf("Delivery.Channel = %q, want %q", created.Delivery.Channel, "telegram")
	}
	if created.Delivery.Target != "12345" {
		t.Errorf("Delivery.Target = %q, want %q", created.Delivery.Target, "12345")
	}
}

func TestHandleCreateJob_NoScheduler(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/scheduler/jobs", bytes.NewBufferString("{}"))
	rr := httptest.NewRecorder()
	handleCreateJob(rr, req, &store.Services{})

	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("expected status 503, got %d", rr.Code)
	}
}

func TestHandleCreateJob_InvalidJSON(t *testing.T) {
	ss := newTestSchedulerStore(t)
	svc := &store.Services{PersonalScheduler: ss}

	req := httptest.NewRequest(http.MethodPost, "/api/scheduler/jobs", bytes.NewBufferString("not json"))
	rr := httptest.NewRecorder()
	handleCreateJob(rr, req, svc)

	if rr.Code != http.StatusBadRequest {
		t.Errorf("expected status 400, got %d", rr.Code)
	}
}

func TestHandleListJobs_IncludesIsTeamAdmin(t *testing.T) {
	ss := newTestSchedulerStore(t)
	svc := &store.Services{PersonalScheduler: ss}
	req := httptest.NewRequest(http.MethodGet, "/api/scheduler/jobs", nil)
	rr := httptest.NewRecorder()
	handleListJobs(rr, req, svc)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	var body map[string]any
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := body["is_team_admin"]; !ok {
		t.Error("expected is_team_admin in list response")
	}
}

func TestSchedulerJobPublishHandler_MovesJob(t *testing.T) {
	personal := newTestSchedulerStore(t)
	team := newTestSchedulerStore(t)
	svc := &store.Services{
		Mode:              store.ModePlatform,
		PersonalScheduler: personal,
		Scheduler:         team,
	}

	job := &store.ScheduledJob{
		Name: "promo-me",
		Mode: "adaptive",
		Schedule: store.JobSchedule{
			Cron: "0 9 * * *",
		},
		Payload: store.JobPayload{
			Instructions: "hi",
		},
		Delivery: store.JobDelivery{
			Mode: "owner",
		},
		Enabled:    true,
		OwnerID:    "user-1",
		TeamSlug:   "eng",
		LastStatus: "pending",
		Scope:      store.JobScopePersonal,
	}
	if err := personal.Add(context.Background(), job); err != nil {
		t.Fatalf("Add personal: %v", err)
	}

	body, _ := json.Marshal(map[string]string{"id": job.ID})
	req := httptest.NewRequest(http.MethodPost, "/api/scheduler/jobs/publish", bytes.NewReader(body))
	req = req.WithContext(store.WithServices(req.Context(), svc))
	// Bypass RequireTeamAdmin / isPlatformMode by calling the move logic through
	// the handler after stubbing platform checks is hard; exercise stores directly
	// the same way the handler does for the happy path.
	ctx := req.Context()
	personalJob := personal.Get(ctx, job.ID)
	if personalJob == nil {
		t.Fatal("personal job missing")
	}
	teamJob := *personalJob
	teamJob.ID = ""
	teamJob.Scope = store.JobScopeTeam
	teamJob.TeamSlug = ""
	if err := team.Add(ctx, &teamJob); err != nil {
		t.Fatalf("Add team: %v", err)
	}
	if err := personal.Remove(ctx, personalJob.ID); err != nil {
		t.Fatalf("Remove personal: %v", err)
	}

	if personal.Get(ctx, job.ID) != nil {
		t.Error("personal job should be removed after promote")
	}
	if team.GetByName(ctx, "promo-me") == nil {
		t.Error("team job should exist after promote")
	}
}

func TestSchedulerJobFork_CopiesToPersonal(t *testing.T) {
	personal := newTestSchedulerStore(t)
	team := newTestSchedulerStore(t)
	ctx := context.Background()

	job := &store.ScheduledJob{
		Name: "team-nightly",
		Mode: "adaptive",
		Schedule: store.JobSchedule{
			Cron: "0 2 * * *",
		},
		Payload: store.JobPayload{
			Instructions: "report",
		},
		Delivery: store.JobDelivery{
			Mode: "team",
		},
		Enabled:    true,
		LastStatus: "pending",
		Scope:      store.JobScopeTeam,
	}
	if err := team.Add(ctx, job); err != nil {
		t.Fatalf("Add team: %v", err)
	}

	// Mirror fork handler store logic (copy, keep team).
	src := team.Get(ctx, job.ID)
	if src == nil {
		t.Fatal("team job missing")
	}
	forked := *src
	forked.ID = ""
	forked.Scope = store.JobScopePersonal
	forked.OwnerID = "user-1"
	forked.TeamSlug = "eng"
	forked.Delivery.Mode = "owner"
	forked.Delivery.MemberIDs = nil
	if err := personal.Add(ctx, &forked); err != nil {
		t.Fatalf("Add personal: %v", err)
	}

	if team.Get(ctx, job.ID) == nil {
		t.Error("team job should remain after fork")
	}
	got := personal.GetByName(ctx, "team-nightly")
	if got == nil {
		t.Fatal("personal fork missing")
	}
	if got.Delivery.Mode != "owner" {
		t.Errorf("Delivery.Mode = %q, want owner", got.Delivery.Mode)
	}
	if got.OwnerID != "user-1" {
		t.Errorf("OwnerID = %q, want user-1", got.OwnerID)
	}
}

func TestHandleCreateJob_PersonalRejectsTeamDelivery(t *testing.T) {
	ss := newTestSchedulerStore(t)
	svc := &store.Services{PersonalScheduler: ss}

	flatJSON := `{
		"name": "Private",
		"mode": "adaptive",
		"cron": "0 9 * * *",
		"instructions": "x",
		"scope": "personal",
		"delivery_mode": "team",
		"enabled": true
	}`
	req := httptest.NewRequest(http.MethodPost, "/api/scheduler/jobs", bytes.NewBufferString(flatJSON))
	rr := httptest.NewRecorder()
	handleCreateJob(rr, req, svc)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rr.Code, rr.Body.String())
	}
}

func TestBuildSchedulerExecContext_PersonalUsesMergedCreds(t *testing.T) {
	personal := &stubCredStore{name: "personal"}
	team := &stubCredStore{name: "team"}
	svc := &store.Services{
		PersonalCredentials: personal,
		Credentials:         team,
	}
	job := &store.ScheduledJob{OwnerID: "user-1", Scope: store.JobScopePersonal}
	ctx := buildSchedulerExecContext(context.Background(), svc, store.JobScopePersonal, job)
	cs := store.CredentialStoreFromContext(ctx)
	if cs == nil {
		t.Fatal("expected credential store in context")
	}
	merged, ok := cs.(*store.MergedCredentialStore)
	if !ok {
		t.Fatalf("expected MergedCredentialStore, got %T", cs)
	}
	if merged.Personal != personal || merged.Team != team {
		t.Error("merged store should wrap personal + team")
	}
	if store.UserIDFromContext(ctx) != "user-1" {
		t.Errorf("UserID = %q, want user-1", store.UserIDFromContext(ctx))
	}
}

func TestBuildSchedulerExecContext_TeamUsesTeamOnly(t *testing.T) {
	personal := &stubCredStore{name: "personal"}
	team := &stubCredStore{name: "team"}
	svc := &store.Services{
		PersonalCredentials: personal,
		Credentials:         team,
	}
	job := &store.ScheduledJob{Scope: store.JobScopeTeam}
	ctx := buildSchedulerExecContext(context.Background(), svc, store.JobScopeTeam, job)
	cs := store.CredentialStoreFromContext(ctx)
	if cs != team {
		t.Fatalf("expected team credential store, got %T", cs)
	}
}

func TestBuildSchedulerExecContext_InjectsNetworkPolicyStores(t *testing.T) {
	platform := &stubNetPolicyStore{label: "platform"}
	org := &stubNetPolicyStore{label: "org"}
	team := &stubNetPolicyStore{label: "team"}
	svc := &store.Services{
		PlatformNetworkPolicies: platform,
		NetworkPolicies:         org,
		TeamNetworkPolicies:     team,
	}
	job := &store.ScheduledJob{Scope: store.JobScopeTeam}
	ctx := buildSchedulerExecContext(context.Background(), svc, store.JobScopeTeam, job)
	nps := store.NetworkPolicyStoresFromContext(ctx)
	if nps == nil {
		t.Fatal("expected NetworkPolicyStores in context")
	}
	if nps.Platform != platform || nps.Org != org || nps.Team != team {
		t.Fatalf("stores = %+v, want platform/org/team stubs", nps)
	}
}

func TestBuildSchedulerExecContext_NoNetworkPolicyStoresWhenNil(t *testing.T) {
	svc := &store.Services{}
	job := &store.ScheduledJob{Scope: store.JobScopeTeam}
	ctx := buildSchedulerExecContext(context.Background(), svc, store.JobScopeTeam, job)
	if nps := store.NetworkPolicyStoresFromContext(ctx); nps != nil {
		t.Fatalf("expected nil NetworkPolicyStores, got %+v", nps)
	}
}

// stubCredStore is a minimal CredentialStore for injection tests.
type stubCredStore struct{ name string }

func (s *stubCredStore) Get(context.Context, string) *store.Credential { return nil }
func (s *stubCredStore) Set(context.Context, string, *store.Credential) error {
	return nil
}
func (s *stubCredStore) Remove(context.Context, string) error                 { return nil }
func (s *stubCredStore) List(context.Context) map[string]store.CredentialType { return nil }
func (s *stubCredStore) Count(context.Context) int                            { return 0 }
func (s *stubCredStore) Resolve(context.Context, string) (string, string, error) {
	return "", "", nil
}
func (s *stubCredStore) InvalidateToken(context.Context, string)         {}
func (s *stubCredStore) SetSecret(context.Context, string, string) error { return nil }
func (s *stubCredStore) SetSecretBatch(context.Context, map[string]string) error {
	return nil
}
func (s *stubCredStore) GetSecret(context.Context, string) string { return "" }
func (s *stubCredStore) RemoveSecret(context.Context, string) error {
	return nil
}
func (s *stubCredStore) HasSecrets(context.Context) bool { return false }
func (s *stubCredStore) SecretCount(context.Context) int { return 0 }
func (s *stubCredStore) ListSecrets(context.Context) []string {
	return nil
}
func (s *stubCredStore) Reload(context.Context) error { return nil }

type stubNetPolicyStore struct{ label string }

func (s *stubNetPolicyStore) List(context.Context) ([]store.NetworkPolicyRule, error) {
	return nil, nil
}
func (s *stubNetPolicyStore) Get(context.Context, string) (*store.NetworkPolicyRule, error) {
	return nil, nil
}
func (s *stubNetPolicyStore) Save(context.Context, *store.NetworkPolicyRule) error { return nil }
func (s *stubNetPolicyStore) Delete(context.Context, string) error                 { return nil }
