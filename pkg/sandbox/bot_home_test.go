package sandbox

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"testing"
)

func TestBotHomeScopeKeys(t *testing.T) {
	scopes := []BotHomeScope{
		{UserID: "u", BotID: "b"}, {OrgID: "o", TeamID: "t", UserID: "u", BotID: "b"},
		{OrgID: "o2", TeamID: "t", UserID: "u", BotID: "b"}, {OrgID: "o", TeamID: "t2", UserID: "u", BotID: "b"},
		{UserID: "u2", BotID: "b"}, {UserID: "u", BotID: "b2"}, {UserID: "../u:/,=", BotID: "../../b"},
		{UserID: "ab", BotID: "c"}, {UserID: "a", BotID: "bc"},
	}
	seen := map[string]bool{}
	for _, s := range scopes {
		if err := s.Validate(); err != nil {
			t.Fatal(err)
		}
		key := s.Key()
		if !regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(key) || seen[key] {
			t.Fatalf("unsafe or colliding scope key: %q", key)
		}
		seen[key] = true
		if key != s.Key() {
			t.Fatal("unstable key")
		}
	}
	for _, s := range []BotHomeScope{{}, {UserID: "u"}, {UserID: "u", BotID: "b", OrgID: "o"}, {UserID: "u\n", BotID: "b"}} {
		if s.Validate() == nil {
			t.Fatalf("accepted invalid scope: %+v", s)
		}
	}
}

func TestBotHomeBindingOptionalAndClone(t *testing.T) {
	if err := BindBotHome(nil, "s", nil); err != nil {
		t.Fatal(err)
	}
	b := &BotHomeBinding{Scope: BotHomeScope{UserID: "u", BotID: "b"}, Backend: BackendKindDocker, Ref: "opaque"}
	if !errors.Is(BindBotHome(nil, "s", b), ErrBotHomeUnsupported) {
		t.Fatal("unsupported pool silently accepted home")
	}
	c := b.Clone()
	if !c.Equal(b) {
		t.Fatal("clone mismatch")
	}
	c.Ref = "other"
	if c.Equal(b) {
		t.Fatal("shared clone")
	}
	raw, err := json.Marshal(SessionSpec{SessionID: "ordinary"})
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	_ = json.Unmarshal(raw, &fields)
	if _, ok := fields["bot_home"]; ok {
		t.Fatal("ordinary launch acquired bot_home")
	}
}

type botHomePoolBackend struct {
	processNodeBackend
	specs chan SessionSpec
}

func (b *botHomePoolBackend) Kind() BackendKind { return BackendKindDocker }
func (b *botHomePoolBackend) EnsureBotHome(ctx context.Context, s BotHomeScope) (*BotHomeBinding, error) {
	return &BotHomeBinding{Scope: s, Backend: BackendKindDocker, Ref: "opaque"}, ctx.Err()
}
func (b *botHomePoolBackend) CreateSession(ctx context.Context, s SessionSpec) (*Session, error) {
	b.specs <- s
	return &Session{SessionID: s.SessionID}, ctx.Err()
}

func TestBotHomePoolPinsBeforePrepare(t *testing.T) {
	b := &botHomePoolBackend{specs: make(chan SessionSpec, 2)}
	p := NewBackendPool(b, ResourceLimits{}).(*backendPool)
	binding, _ := b.EnsureBotHome(t.Context(), BotHomeScope{UserID: "u", BotID: "b"})
	original := binding.Clone()
	if err := BindBotHome(p, "owner", binding); err != nil {
		t.Fatal(err)
	}
	// Staging the binding must not pin @base before template resolution.
	p.GetOrCreateWithTemplate("owner", "template")
	binding.Ref = "mutated"
	p.Alias("child", "owner")
	c := p.GetOrCreate("child").(*backendNodeClient)
	if err := c.EnsureReadyContext(t.Context(), "child"); err != nil {
		t.Fatal(err)
	}
	spec := <-b.specs
	if !spec.BotHome.Equal(original) || spec.SessionID != "owner" || spec.TemplateID != "template" {
		t.Fatalf("wrong pinned spec: %+v", spec)
	}
	if err := BindBotHome(p, "child", original); err != nil {
		t.Fatalf("same binding not idempotent: %v", err)
	}
	if err := BindBotHome(p, "child", binding); err == nil {
		t.Fatal("allowed rebind after prepare")
	}
	c.discard()
}

func TestBotHomePoolRejectsLateAndUnsupported(t *testing.T) {
	binding := &BotHomeBinding{Scope: BotHomeScope{UserID: "u", BotID: "b"}, Backend: BackendKindDocker, Ref: "opaque"}
	ordinary := NewBackendPool(&processNodeBackend{}, ResourceLimits{})
	if !errors.Is(BindBotHome(ordinary, "s", binding), ErrBotHomeUnsupported) {
		t.Fatal("unsupported backend accepted home")
	}
	b := &botHomePoolBackend{specs: make(chan SessionSpec, 1)}
	p := NewBackendPool(b, ResourceLimits{}).(*backendPool)
	c := p.GetOrCreate("s").(*backendNodeClient)
	if err := c.EnsureReadyContext(t.Context(), "s"); err != nil {
		t.Fatal(err)
	}
	if spec := <-b.specs; spec.BotHome != nil {
		t.Fatal("ordinary session acquired home")
	}
	if err := BindBotHome(p, "s", binding); err == nil {
		t.Fatal("accepted binding after ordinary prepare")
	}
	c.discard()
}
