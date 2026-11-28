// Package scheduler implements a job scheduler for Astonish.
// It supports routine flows, adaptive chat, fleet polling, and Bot admission.
package scheduler

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/google/uuid"
)

// JobMode defines how a scheduled job executes.
type JobMode string

const (
	// ModeRoutine runs a flow YAML with fixed parameters through the flow engine.
	// Same inputs, same execution path, every time.
	ModeRoutine JobMode = "routine"

	// ModeAdaptive runs a free-form agentic chat turn. The LLM receives
	// stored instructions and can reason, adapt, and use tools dynamically.
	ModeAdaptive JobMode = "adaptive"

	// ModeFleetPoll polls an external channel (e.g., GitHub Issues) for a fleet plan
	// and starts headless fleet sessions for new work items. The job's Payload.Flow
	// field holds the fleet plan key.
	ModeFleetPoll JobMode = "fleet_poll"

	// ModeBotTrigger admits a scheduled occurrence through a trusted callback.
	// It does not execute a Bot or produce a run report.
	ModeBotTrigger JobMode = "bot_trigger"
)

// JobSchedule defines when a job runs.
type JobSchedule struct {
	// Cron is a standard 5-field cron expression (e.g., "0 9 * * *").
	Cron string `json:"cron"`
	// Timezone is an IANA timezone name (e.g., "America/Sao_Paulo").
	// Empty defaults to the system's local timezone.
	Timezone string `json:"timezone,omitempty"`
}

// JobPayload holds mode-specific execution data.
type JobPayload struct {
	// Flow is the flow name for routine mode (resolved at runtime).
	Flow string `json:"flow,omitempty"`
	// Params are the flow parameters for routine mode.
	Params map[string]string `json:"params,omitempty"`
	// Instructions is the prompt/task description for adaptive mode.
	Instructions string `json:"instructions,omitempty"`
	// BotID, BotVersion and BotTriggerID bind a Bot-trigger row to a saved
	// immutable version/trigger. The callback must revalidate this binding.
	BotID        string `json:"bot_id,omitempty"`
	BotVersion   int64  `json:"bot_version,omitempty"`
	BotTriggerID string `json:"bot_trigger_id,omitempty"`
}

// JobDelivery defines where to send job results.
type JobDelivery struct {
	// Channel is the channel adapter ID (e.g., "telegram").
	// Used in "target" mode for direct delivery to a specific chat.
	Channel string `json:"channel"`
	// Target is the chat/conversation ID to deliver to.
	// Used in "target" mode for direct delivery to a specific chat.
	Target string `json:"target"`
	// Mode controls how delivery targets are resolved:
	//   - "owner"   : deliver only to the job owner's linked channels
	//   - "team"    : deliver to all team members' linked channels
	//   - "members" : deliver to specific members listed in MemberIDs
	//   - "target"  : deliver to Channel+Target directly (legacy)
	//   - ""        : fallback — uses Channel+Target if set, otherwise broadcasts
	Mode DeliveryMode `json:"mode,omitempty"`
	// MemberIDs is the list of platform user IDs for "members" mode delivery.
	MemberIDs []string `json:"member_ids,omitempty"`
	// ChannelFilter restricts delivery to specific channel types (e.g., ["email", "telegram"]).
	// When set, only linked channels matching these types are used for delivery.
	// When empty, all linked channels are used.
	ChannelFilter []string `json:"channel_filter,omitempty"`
	// MemberChannels maps user IDs to their allowed channel types for this job.
	// Per-member override that takes precedence over ChannelFilter.
	// e.g., {"user-uuid-1": ["telegram", "email"], "user-uuid-2": ["telegram"]}
	MemberChannels map[string][]string `json:"member_channels,omitempty"`
}

// DeliveryMode defines how a job's output is routed to recipients.
type DeliveryMode string

const (
	// DeliveryModeOwner delivers only to the job owner's linked channels.
	DeliveryModeOwner DeliveryMode = "owner"
	// DeliveryModeTeam delivers to all members of the job's team.
	DeliveryModeTeam DeliveryMode = "team"
	// DeliveryModeMembers delivers to an explicit list of user IDs.
	DeliveryModeMembers DeliveryMode = "members"
	// DeliveryModeTarget delivers to a specific channel+target (legacy direct routing).
	DeliveryModeTarget DeliveryMode = "target"
)

// JobStatus represents the outcome of the last execution.
type JobStatus string

const (
	StatusPending JobStatus = "pending" // Never run yet
	StatusSuccess JobStatus = "success"
	StatusFailed  JobStatus = "failed"
)

// Job is a scheduled task with all configuration and runtime state.
type Job struct {
	ID        string      `json:"id"`
	Name      string      `json:"name"`
	Mode      JobMode     `json:"mode"`
	Schedule  JobSchedule `json:"schedule"`
	Payload   JobPayload  `json:"payload"`
	Delivery  JobDelivery `json:"delivery"`
	Enabled   bool        `json:"enabled"`
	CreatedAt time.Time   `json:"created_at"`

	// OwnerID is the platform user ID who created this job.
	// In personal mode this is empty. In platform mode it controls
	// "owner" delivery mode routing and job visibility.
	OwnerID string `json:"owner_id,omitempty"`

	// Scope is "personal" or "team". Personal jobs run as OwnerID with
	// MergedCredentialStore; team jobs run headless with team credentials.
	Scope string `json:"scope,omitempty"`

	// TeamSlug is the team context for personal jobs (credential/flow fallback).
	TeamSlug string `json:"team_slug,omitempty"`

	// Runtime state (updated by the scheduler after each run)
	LastRun             *time.Time `json:"last_run,omitempty"`
	LastStatus          JobStatus  `json:"last_status"`
	LastError           string     `json:"last_error,omitempty"`
	NextRun             *time.Time `json:"next_run,omitempty"`
	ConsecutiveFailures int        `json:"consecutive_failures"`
}

// JobStore is the interface that any scheduler job backend must implement.
// The scheduler engine operates on this interface, allowing both file-based
// (personal mode) and PostgreSQL-backed (platform mode) implementations.
type JobStore interface {
	List() []*Job
	Get(id string) *Job
	GetByName(name string) *Job
	Add(job *Job) error
	Update(job *Job) error
	Remove(id string) error
}

// storeData is the JSON file root structure.
type storeData struct {
	Jobs []*Job `json:"jobs"`
}

// Store persists scheduled jobs to a JSON file with atomic writes.
// It implements the JobStore interface.
type Store struct {
	path string
	mu   sync.RWMutex
	jobs map[string]*Job // keyed by ID
}

// NewStore creates or loads a job store at the given file path.
// If the file doesn't exist, an empty store is created.
func NewStore(path string) (*Store, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("create store directory: %w", err)
	}

	s := &Store{
		path: path,
		jobs: make(map[string]*Job),
	}

	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

// DefaultStorePath returns the default job store path:
// ~/.config/astonish/scheduler/jobs.json
func DefaultStorePath() (string, error) {
	configDir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(configDir, "astonish", "scheduler", "jobs.json"), nil
}

// List returns a copy of all jobs.
func (s *Store) List() []*Job {
	s.mu.RLock()
	defer s.mu.RUnlock()

	jobs := make([]*Job, 0, len(s.jobs))
	for _, j := range s.jobs {
		cp := *j
		jobs = append(jobs, &cp)
	}
	return jobs
}

// Get returns a job by ID. Returns nil if not found.
func (s *Store) Get(id string) *Job {
	s.mu.RLock()
	defer s.mu.RUnlock()

	j, ok := s.jobs[id]
	if !ok {
		return nil
	}
	cp := *j
	return &cp
}

// GetByName returns a job by name (case-insensitive). Returns nil if not found.
func (s *Store) GetByName(name string) *Job {
	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, j := range s.jobs {
		if equalFold(j.Name, name) {
			cp := *j
			return &cp
		}
	}
	return nil
}

// Add persists a new job. The job ID is generated if empty.
func (s *Store) Add(job *Job) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if job.ID == "" {
		job.ID = uuid.New().String()
	}
	if job.CreatedAt.IsZero() {
		job.CreatedAt = time.Now()
	}
	if job.LastStatus == "" {
		job.LastStatus = StatusPending
	}

	s.jobs[job.ID] = job
	return s.save()
}

// Update replaces an existing job. Returns error if not found.
func (s *Store) Update(job *Job) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.jobs[job.ID]; !ok {
		return fmt.Errorf("job %s not found", job.ID)
	}
	s.jobs[job.ID] = job
	return s.save()
}

// Remove deletes a job by ID. Returns error if not found.
func (s *Store) Remove(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.jobs[id]; !ok {
		return fmt.Errorf("job %s not found", id)
	}
	delete(s.jobs, id)
	return s.save()
}

// load reads the JSON file into memory.
func (s *Store) load() error {
	data, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		return nil // Empty store
	}
	if err != nil {
		return fmt.Errorf("read store: %w", err)
	}

	// Handle empty file
	if len(data) == 0 {
		return nil
	}

	var sd storeData
	if err := json.Unmarshal(data, &sd); err != nil {
		return fmt.Errorf("parse store: %w", err)
	}

	for _, j := range sd.Jobs {
		s.jobs[j.ID] = j
	}
	return nil
}

// save writes the store to disk atomically (write temp + rename).
func (s *Store) save() error {
	sd := storeData{Jobs: make([]*Job, 0, len(s.jobs))}
	for _, j := range s.jobs {
		sd.Jobs = append(sd.Jobs, j)
	}

	data, err := json.MarshalIndent(sd, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal store: %w", err)
	}

	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return fmt.Errorf("write temp store: %w", err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		_ = os.Remove(tmp) // best-effort cleanup
		return fmt.Errorf("rename store: %w", err)
	}
	return nil
}

// equalFold is a simple case-insensitive string comparison.
func equalFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		ca, cb := a[i], b[i]
		if ca >= 'A' && ca <= 'Z' {
			ca += 'a' - 'A'
		}
		if cb >= 'A' && cb <= 'Z' {
			cb += 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}

// Compile-time check that *Store implements JobStore.
var _ JobStore = (*Store)(nil)
