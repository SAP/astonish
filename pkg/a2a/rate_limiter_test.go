package a2a

import (
	"testing"
)

func TestAgentRateLimiter_AllowRequest_Unlimited(t *testing.T) {
	rl := NewAgentRateLimiter()
	defer rl.Close()

	// Set rate limit to 0 (unlimited)
	rl.SetAgentLimits("agent:user1", 0, 0)

	// Should always allow
	for i := 0; i < 1000; i++ {
		if !rl.AllowRequest("agent:user1") {
			t.Fatalf("expected AllowRequest to return true for unlimited agent, iteration %d", i)
		}
	}
}

func TestAgentRateLimiter_AllowRequest_Limited(t *testing.T) {
	rl := NewAgentRateLimiter()
	defer rl.Close()

	// Set rate limit to 5 requests per minute
	rl.SetAgentLimits("agent:user1", 5, 0)

	// First 5 requests should be allowed
	for i := 0; i < 5; i++ {
		if !rl.AllowRequest("agent:user1") {
			t.Fatalf("expected AllowRequest to return true, iteration %d", i)
		}
	}

	// 6th request should be denied
	if rl.AllowRequest("agent:user1") {
		t.Fatal("expected AllowRequest to return false after exceeding rate limit")
	}

	// Additional requests should also be denied
	if rl.AllowRequest("agent:user1") {
		t.Fatal("expected AllowRequest to return false after exceeding rate limit (2nd attempt)")
	}
}

func TestAgentRateLimiter_AcquireRelease(t *testing.T) {
	rl := NewAgentRateLimiter()
	defer rl.Close()

	// Set max concurrent tasks to 2
	rl.SetAgentLimits("agent:user1", 0, 2)

	// Acquire first task
	if !rl.AcquireTask("agent:user1") {
		t.Fatal("expected AcquireTask to return true for first task")
	}

	// Acquire second task
	if !rl.AcquireTask("agent:user1") {
		t.Fatal("expected AcquireTask to return true for second task")
	}

	// Third task should be denied
	if rl.AcquireTask("agent:user1") {
		t.Fatal("expected AcquireTask to return false when at max concurrent tasks")
	}

	// Release one task
	rl.ReleaseTask("agent:user1")

	// Now should be able to acquire again
	if !rl.AcquireTask("agent:user1") {
		t.Fatal("expected AcquireTask to return true after releasing a task")
	}

	// Should be denied again (back at max)
	if rl.AcquireTask("agent:user1") {
		t.Fatal("expected AcquireTask to return false when at max concurrent tasks again")
	}
}

func TestAgentRateLimiter_AcquireTask_Unlimited(t *testing.T) {
	rl := NewAgentRateLimiter()
	defer rl.Close()

	// Set max tasks to 0 (unlimited)
	rl.SetAgentLimits("agent:user1", 0, 0)

	// Should always allow
	for i := 0; i < 1000; i++ {
		if !rl.AcquireTask("agent:user1") {
			t.Fatalf("expected AcquireTask to return true for unlimited agent, iteration %d", i)
		}
	}
}

func TestAgentRateLimiter_UnknownAgent(t *testing.T) {
	rl := NewAgentRateLimiter()
	defer rl.Close()

	// Don't configure any limits for this agent

	// AllowRequest should return true for unknown agents (unlimited by default)
	for i := 0; i < 100; i++ {
		if !rl.AllowRequest("unknown:agent") {
			t.Fatalf("expected AllowRequest to return true for unknown agent, iteration %d", i)
		}
	}

	// AcquireTask should return true for unknown agents (unlimited by default)
	for i := 0; i < 100; i++ {
		if !rl.AcquireTask("unknown:agent") {
			t.Fatalf("expected AcquireTask to return true for unknown agent, iteration %d", i)
		}
	}
}
