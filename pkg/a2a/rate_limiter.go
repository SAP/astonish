package a2a

import (
	"sync"
	"time"
)

const (
	// rateLimitWindow is the sliding window size for rate limiting (1 minute).
	rateLimitWindow = time.Minute

	// cleanupInterval is how often the background goroutine removes stale entries.
	cleanupInterval = 5 * time.Minute
)

// agentLimits holds the configured limits for a specific agent.
type agentLimits struct {
	rateLimit int // max requests per minute (0 = unlimited)
	maxTasks  int // max concurrent tasks (0 = unlimited)
}

// agentState holds the runtime state for a specific agent.
type agentState struct {
	requests    []time.Time // timestamps of requests within the window
	activeTasks int         // current number of concurrent tasks
	lastAccess  time.Time   // last time this agent was accessed (for cleanup)
}

// AgentRateLimiter provides per-agent rate limiting and concurrency control.
type AgentRateLimiter struct {
	mu     sync.Mutex
	limits map[string]*agentLimits
	state  map[string]*agentState
	stopCh chan struct{}
}

// NewAgentRateLimiter creates a new AgentRateLimiter and starts the background cleanup goroutine.
func NewAgentRateLimiter() *AgentRateLimiter {
	rl := &AgentRateLimiter{
		limits: make(map[string]*agentLimits),
		state:  make(map[string]*agentState),
		stopCh: make(chan struct{}),
	}
	go rl.cleanup()
	return rl
}

// SetAgentLimits configures rate and concurrency limits for a specific agent.
// rateLimit is max requests per minute (0 = unlimited).
// maxTasks is max concurrent tasks (0 = unlimited).
func (rl *AgentRateLimiter) SetAgentLimits(agentID string, rateLimit int, maxTasks int) {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	rl.limits[agentID] = &agentLimits{
		rateLimit: rateLimit,
		maxTasks:  maxTasks,
	}
}

// AllowRequest checks if the agent is within its rate limit.
// Returns true if the request is allowed (or if no limit is configured).
// A rateLimit of 0 means unlimited.
func (rl *AgentRateLimiter) AllowRequest(agentID string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	limits := rl.limits[agentID]
	if limits == nil || limits.rateLimit == 0 {
		// No limits configured or unlimited
		return true
	}

	now := time.Now()
	state := rl.getOrCreateState(agentID)
	state.lastAccess = now

	// Slide the window: remove requests older than 1 minute
	cutoff := now.Add(-rateLimitWindow)
	valid := state.requests[:0]
	for _, t := range state.requests {
		if t.After(cutoff) {
			valid = append(valid, t)
		}
	}
	state.requests = valid

	// Check if we're at the limit
	if len(state.requests) >= limits.rateLimit {
		return false
	}

	// Record this request
	state.requests = append(state.requests, now)
	return true
}

// AcquireTask attempts to acquire a task slot for the agent.
// Returns true if a slot is available (or if no limit is configured).
// A maxTasks of 0 means unlimited.
func (rl *AgentRateLimiter) AcquireTask(agentID string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	limits := rl.limits[agentID]
	if limits == nil || limits.maxTasks == 0 {
		// No limits configured or unlimited
		return true
	}

	state := rl.getOrCreateState(agentID)
	state.lastAccess = time.Now()

	if state.activeTasks >= limits.maxTasks {
		return false
	}

	state.activeTasks++
	return true
}

// ReleaseTask decrements the concurrent task count for the agent.
func (rl *AgentRateLimiter) ReleaseTask(agentID string) {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	state := rl.state[agentID]
	if state == nil {
		return
	}

	if state.activeTasks > 0 {
		state.activeTasks--
	}
	state.lastAccess = time.Now()
}

// Close stops the background cleanup goroutine.
func (rl *AgentRateLimiter) Close() {
	close(rl.stopCh)
}

// getOrCreateState returns the state for an agent, creating it if necessary.
// Must be called with rl.mu held.
func (rl *AgentRateLimiter) getOrCreateState(agentID string) *agentState {
	state := rl.state[agentID]
	if state == nil {
		state = &agentState{}
		rl.state[agentID] = state
	}
	return state
}

// cleanup periodically removes stale agent state entries.
func (rl *AgentRateLimiter) cleanup() {
	ticker := time.NewTicker(cleanupInterval)
	defer ticker.Stop()

	for {
		select {
		case <-rl.stopCh:
			return
		case <-ticker.C:
			rl.removeStaleEntries()
		}
	}
}

// removeStaleEntries removes agent state entries that haven't been accessed recently
// and have no active tasks.
func (rl *AgentRateLimiter) removeStaleEntries() {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	cutoff := time.Now().Add(-cleanupInterval)
	for agentID, state := range rl.state {
		if state.lastAccess.Before(cutoff) && state.activeTasks == 0 {
			delete(rl.state, agentID)
		}
	}
}
