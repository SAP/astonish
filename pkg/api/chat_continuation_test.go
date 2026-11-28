package api

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/SAP/astonish/pkg/agent"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/provider"
	"google.golang.org/adk/session"
)

type continuationProbe struct{ discarded atomic.Int32 }

func (p *continuationProbe) Discard() { p.discarded.Add(1) }

func TestChatContinuationOwnershipAndDeletion(t *testing.T) {
	cm := &ChatManager{}
	p := execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: "owner", OrgSlug: "org", TeamSlug: "team", Authenticated: true}
	ctx, _ := execution.WithPrincipal(t.Context(), p)
	data, publish, err := cm.BeginContinuation(ctx, "astonish", "session")
	if err != nil || data != nil {
		t.Fatalf("begin: %v %v", data, err)
	}
	original := &continuationProbe{}
	publish(original)
	for _, change := range []func(*execution.Principal){func(p *execution.Principal) { p.Subject = "foreign" }, func(p *execution.Principal) { p.OrgSlug = "foreign" }, func(p *execution.Principal) { p.TeamSlug = "foreign" }} {
		foreign := p
		change(&foreign)
		fc, _ := execution.WithPrincipal(t.Context(), foreign)
		got, finish, err := cm.BeginContinuation(fc, "astonish", "session")
		if err != nil || got != nil {
			t.Fatal("foreign continuation exposed", err)
		}
		finish(nil)
		cm.ForgetContinuation(fc, "astonish", "session")
	}
	for _, key := range []struct{ app, session string }{{"astonish_code", "session"}, {"astonish", "other-session"}} {
		got, finish, err := cm.BeginContinuation(ctx, key.app, key.session)
		if err != nil || got != nil {
			t.Fatal("app/session continuation exposed", err)
		}
		finish(nil)
		cm.ForgetContinuation(ctx, key.app, key.session)
	}
	got, late, err := cm.BeginContinuation(ctx, "astonish", "session")
	if err != nil || got != original {
		t.Fatal("owner continuation lost", err)
	}
	cm.ForgetContinuation(ctx, "astonish", "session")
	late(original)
	if original.discarded.Load() != 1 {
		t.Fatal("deleted continuation resurrected")
	}
	got, finish, err := cm.BeginContinuation(ctx, "astonish", "session")
	if err != nil || got != nil {
		t.Fatal("deleted state visible", err)
	}
	finish(nil)
	if _, _, err := cm.BeginContinuation(context.Background(), "astonish", "session"); err == nil {
		t.Fatal("anonymous checkout allowed")
	}
}

func TestChatContinuationReplacementFencesOldAttempt(t *testing.T) {
	cm := &ChatManager{}
	ctx, _ := execution.WithPrincipal(t.Context(), execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: "owner", OrgSlug: "org", TeamSlug: "team", Authenticated: true})
	_, stale, _ := cm.BeginContinuation(ctx, "astonish", "session")
	_, current, _ := cm.BeginContinuation(ctx, "astonish", "session")
	old, next := &continuationProbe{}, &continuationProbe{}
	stale(old)
	current(next)
	got, finish, _ := cm.BeginContinuation(ctx, "astonish", "session")
	if got != next || old.discarded.Load() != 1 {
		t.Fatal("stale attempt overwrote replacement")
	}
	finish(nil)
	next.Discard()
}

func TestChatContinuationResetDiscardsAndFences(t *testing.T) {
	cm := &ChatManager{}
	ctx, _ := execution.WithPrincipal(t.Context(), execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: "owner", OrgSlug: "org", TeamSlug: "team", Authenticated: true})
	_, publish, _ := cm.BeginContinuation(ctx, "astonish", "paused")
	paused := &continuationProbe{}
	publish(paused)
	_, stale, _ := cm.BeginContinuation(ctx, "astonish", "running")
	cm.Reset()
	late := &continuationProbe{}
	stale(late)
	stale(late) // Cleanup publishers are idempotent.
	if paused.discarded.Load() != 1 || late.discarded.Load() != 1 {
		t.Fatal("reset did not release paused/late state exactly once")
	}
	for _, id := range []string{"paused", "running"} {
		got, finish, err := cm.BeginContinuation(ctx, "astonish", id)
		if err != nil || got != nil {
			t.Fatal("reset continuation resurrected", err)
		}
		finish(nil)
	}
}

func TestChatContinuationConversationData(t *testing.T) {
	owner := agent.NewChatAgent(provider.NewPlaceholderLLM(), nil, nil, session.InMemoryService(), &agent.SystemPromptBuilder{}, false, true)
	attempt := agent.NewChatAgent(NewMockLLM(TextTurn("attempt")), nil, nil, session.InMemoryService(), &agent.SystemPromptBuilder{}, false, true)
	attempt.ShareConversationState(owner)
	attempt.SetPendingTutorialBlueprint("session", &agent.TutorialBlueprintPending{YAML: "retained"})
	if !owner.HasPendingTutorialBlueprint("session") || owner.HasPendingTutorialBlueprint("other") {
		t.Fatal("private data continuity/isolation lost")
	}
	if attempt.LLM == owner.LLM || attempt.SessionService == owner.SessionService {
		t.Fatal("execution dependencies shared")
	}
	owner.ForgetConversationState("session")
	if attempt.HasPendingTutorialBlueprint("session") {
		t.Fatal("deleted conversation still present")
	}
}
