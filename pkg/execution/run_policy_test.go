package execution

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func botTestPrincipal(t *testing.T) context.Context {
	t.Helper()
	ctx, err := WithPrincipal(context.Background(), Principal{Kind: PrincipalKindUser, Authentication: AuthMethodPlatformJWT, Surface: SurfaceStudio, Subject: "owner", OrgSlug: "org", TeamSlug: "team", Authenticated: true})
	if err != nil {
		t.Fatal(err)
	}
	return ctx
}

func TestBotMissingWritePermissionRequiresReviewedConfiguration(t *testing.T) {
	id := BotRunIdentity{BotID: uuid.New(), RunID: uuid.New(), Version: 1, ActorID: "owner", OrgSlug: "org", TeamSlug: "team"}
	p, err := NewRunPolicy(RunPolicySpec{Identity: id, Level: AutonomySupervised})
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithRunPolicy(WithBotRunIdentity(botTestPrincipal(t), id), p)
	action := ActionDescriptor{Tool: "write_file", Effect: EffectReversibleWrite, ConstraintsVerified: true, Arguments: json.RawMessage(`{"path":"/workspace/proof.txt","content":"proof"}`)}
	got := EvaluateRunPolicy(ctx, action, time.Now())
	if got.Kind != PolicyDeny || got.ActionDigest != "" || got.GrantID != uuid.Nil {
		t.Fatalf("missing allowlist must deny, not fabricate an approval: %+v", got)
	}
	for _, text := range []string{"write_file", "reviewed configuration proposal", "new task proposal"} {
		if !strings.Contains(got.Reason, text) {
			t.Errorf("missing useful prerequisite %q: %s", text, got.Reason)
		}
	}
}

func TestBotPolicyFailClosed(t *testing.T) {
	id := BotRunIdentity{BotID: uuid.New(), RunID: uuid.New(), Version: 1, ActorID: "owner", OrgSlug: "org", TeamSlug: "team"}
	ctx := WithBotRunIdentity(botTestPrincipal(t), id)
	action := ActionDescriptor{Tool: "read_file", Effect: EffectRead, ConstraintsVerified: true, Arguments: json.RawMessage(`{"path":"notes.md"}`)}
	if got := EvaluateRunPolicy(ctx, action, time.Now()); got.Kind != PolicyDeny {
		t.Fatalf("missing policy: %+v", got)
	}
	if got := EvaluateRunPolicy(context.Background(), action, time.Now()); got.Kind != PolicyNotApplicable {
		t.Fatalf("ordinary chat changed: %+v", got)
	}
	p, err := NewRunPolicy(RunPolicySpec{Identity: id, Level: AutonomySupervised, AllowedTools: []string{"read_file"}, DeniedTools: []string{"read_file"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := EvaluateRunPolicy(WithRunPolicy(ctx, p), action, time.Now()); got.Kind != PolicyDeny {
		t.Fatalf("deny precedence: %+v", got)
	}
}

func TestBotPolicyImmutableAndChildNarrowing(t *testing.T) {
	id := BotRunIdentity{BotID: uuid.New(), RunID: uuid.New(), Version: 1, ActorID: "owner", OrgSlug: "org", TeamSlug: "team"}
	tools := []string{"read_file"}
	p, err := NewRunPolicy(RunPolicySpec{Identity: id, Level: AutonomySupervised, AllowedTools: tools})
	if err != nil {
		t.Fatal(err)
	}
	tools[0] = "shell_command"
	ctx := WithRunPolicy(WithBotRunIdentity(botTestPrincipal(t), id), p)
	read := ActionDescriptor{Tool: "read_file", Effect: EffectRead, ConstraintsVerified: true, Arguments: json.RawMessage(`{}`)}
	if got := EvaluateRunPolicy(ctx, read, time.Now()); got.Kind != PolicyAllow {
		t.Fatalf("policy mutated: %+v", got)
	}
	child, err := p.Narrow([]string{"read_file", "shell_command"})
	if err != nil {
		t.Fatal(err)
	}
	ctx = WithRunPolicy(ctx, child)
	if got := EvaluateRunPolicy(ctx, ActionDescriptor{Tool: "shell_command", Effect: EffectUnknown, Arguments: json.RawMessage(`{}`)}, time.Now()); got.Kind != PolicyDeny {
		t.Fatalf("child widened: %+v", got)
	}
	if got := EvaluateRunPolicy(ctx, ActionDescriptor{Tool: "read_file", Effect: EffectSend, Arguments: json.RawMessage(`{}`)}, time.Now()); got.Kind != PolicyDeny {
		t.Fatalf("child may send: %+v", got)
	}
}

func TestBotActionDigestBindsExactAction(t *testing.T) {
	a := ActionDescriptor{Tool: "send", Effect: EffectSend, Recipient: "alice", Arguments: json.RawMessage(`{"body":"hello","n":9007199254740993}`)}
	d, err := a.Digest()
	if err != nil {
		t.Fatal(err)
	}
	b := a
	b.Arguments = json.RawMessage(`{ "n":9007199254740993, "body":"hello" }`)
	same, err := b.Digest()
	if err != nil || same != d {
		t.Fatalf("canonical digest: %s %v", same, err)
	}
	b.Recipient = "bob"
	changed, _ := b.Digest()
	if changed == d {
		t.Fatal("recipient not bound")
	}
	b = a
	b.Arguments = json.RawMessage(`{"body":"changed","n":9007199254740993}`)
	changed, _ = b.Digest()
	if changed == d {
		t.Fatal("content not bound")
	}
}

func TestBotPolicyPrincipalAndLevels(t *testing.T) {
	id := BotRunIdentity{BotID: uuid.New(), RunID: uuid.New(), Version: 1, ActorID: "owner", OrgSlug: "org", TeamSlug: "team"}
	for _, level := range []AutonomyLevel{AutonomyAsk, AutonomySupervised, AutonomyReversible, AutonomyScoped} {
		p, err := NewRunPolicy(RunPolicySpec{Identity: id, Level: level, AllowedTools: []string{"tool"}})
		if err != nil {
			t.Fatal(err)
		}
		ctx := WithRunPolicy(WithBotRunIdentity(botTestPrincipal(t), id), p)
		a := ActionDescriptor{Tool: "tool", Effect: EffectReversibleWrite, Arguments: json.RawMessage(`{}`), ConstraintsVerified: true}
		want := PolicyAllow
		if level == AutonomyAsk {
			want = PolicyRequestApproval
		}
		if got := EvaluateRunPolicy(ctx, a, time.Now()); got.Kind != want {
			t.Fatalf("%s: %+v", level, got)
		}
		a.ConstraintsVerified = false
		if got := EvaluateRunPolicy(ctx, a, time.Now()); got.Kind != PolicyRequestApproval {
			t.Fatalf("unverified effect: %+v", got)
		}
		a.Effect = EffectSpend
		if got := EvaluateRunPolicy(ctx, a, time.Now()); got.Kind != PolicyRequestApproval {
			t.Fatalf("unapproved spend: %+v", got)
		}
		if got := EvaluateRunPolicy(WithRunPolicy(WithBotRunIdentity(context.Background(), id), p), a, time.Now()); got.Kind != PolicyDeny {
			t.Fatal("unauthenticated execution")
		}
		changed := id
		changed.TeamSlug = "other"
		if got := EvaluateRunPolicy(WithRunPolicy(WithBotRunIdentity(botTestPrincipal(t), changed), p), a, time.Now()); got.Kind != PolicyDeny {
			t.Fatal("cross-team execution")
		}
	}
}

func TestBotActionDigestRejectsInvalidArguments(t *testing.T) {
	for _, args := range []string{"", "null", "[]", "{} {}", "{broken"} {
		if _, err := (ActionDescriptor{Tool: "tool", Effect: EffectRead, Arguments: json.RawMessage(args)}).Digest(); err == nil {
			t.Errorf("accepted %q", args)
		}
	}
}

func TestBotExactGrantRequiresConsumption(t *testing.T) {
	now := time.Now().UTC()
	id := BotRunIdentity{BotID: uuid.New(), RunID: uuid.New(), Version: 1, ActorID: "owner", OrgSlug: "org", TeamSlug: "team"}
	a := ActionDescriptor{Tool: "send", Effect: EffectSend, Recipient: "alice", Arguments: json.RawMessage(`{"body":"hello"}`)}
	digest, _ := a.Digest()
	grant := ScopedGrant{ID: uuid.New(), Identity: id, ActionDigest: digest, ExpiresAt: now.Add(time.Minute)}
	p, err := NewRunPolicy(RunPolicySpec{Identity: id, Level: AutonomySupervised, AllowedTools: []string{"send"}, Grants: []ScopedGrant{grant}})
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithRunPolicy(WithBotRunIdentity(botTestPrincipal(t), id), p)
	if got := EvaluateRunPolicy(ctx, a, now); got.Kind != PolicyConsumeGrant || got.GrantID != grant.ID {
		t.Fatalf("grant bypassed atomic consume: %+v", got)
	}
	if got := EvaluateRunPolicy(ctx, a, now.Add(time.Minute)); got.Kind != PolicyRequestApproval {
		t.Fatalf("expired grant: %+v", got)
	}
	a.Recipient = "bob"
	if got := EvaluateRunPolicy(ctx, a, now); got.Kind != PolicyRequestApproval {
		t.Fatalf("grant widened: %+v", got)
	}
}
