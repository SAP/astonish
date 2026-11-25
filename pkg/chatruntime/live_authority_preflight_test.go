package chatruntime

import (
	"context"
	"errors"
	"iter"
	"testing"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store/entstore"
	"google.golang.org/adk/model"
)

type authorityPreflightModel struct {
	model.LLM
	t         *testing.T
	supported bool
	calls     int
}

func (m *authorityPreflightModel) SupportsModelPreflight() bool { return m.supported }
func (m *authorityPreflightModel) GenerateContent(ctx context.Context, _ *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	return func(yield func(*model.LLMResponse, error) bool) {
		m.calls++
		if entstore.WithBotContentReadOperationFromContext(context.Background(), ctx) == context.Background() {
			m.t.Fatal("authority wrapper discarded preflight reads before delegate checks")
		}
		execution.EndModelPreflight(ctx)
		if entstore.WithBotContentReadOperationFromContext(context.Background(), ctx) != context.Background() {
			m.t.Fatal("reads survived provider boundary")
		}
		yield(&model.LLMResponse{}, nil)
	}
}
func TestLiveAuthorityModelPreflightDenialClosesReads(t *testing.T) {
	for _, cancelCall := range []bool{false, true} {
		f := newLiveAuthorityFixture(t, ResourceScopeTeam, true)
		in, err := Build(t.Context(), f.opts)
		mustLiveAuthority(t, err)
		defer in.Close()
		raw := &authorityPreflightModel{t: t, supported: true}
		guarded := in.GuardModel(raw)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		ctx, end := entstore.WithBotContentReadOperation(ctx)
		defer end()
		ctx = execution.WithModelPreflightEnd(ctx, end)
		seq := guarded.GenerateContent(ctx, &model.LLMRequest{}, false)
		want := ErrLiveAuthorityDenied
		if cancelCall {
			cancel()
			want = context.Canceled
		} else {
			mustLiveAuthority(t, f.teams.RemoveMember(t.Context(), f.user.ID, f.team.ID))
		}
		denied := false
		for response, err := range seq {
			if response != nil || !errors.Is(err, want) {
				t.Fatalf("response=%v err=%v", response, err)
			}
			denied = true
			if entstore.WithBotContentReadOperationFromContext(context.Background(), ctx) != context.Background() {
				t.Fatal("denial retained preflight bytes")
			}
		}
		if !denied || raw.calls != 0 {
			t.Fatal("denial invoked delegate")
		}
	}
}

func TestLiveAuthorityModelPreflightReadLifetime(t *testing.T) {
	in := &Inputs{}
	raw := &authorityPreflightModel{t: t, supported: true}
	guarded := in.GuardModel(raw)
	ctx, end := entstore.WithBotContentReadOperation(t.Context())
	defer end()
	ctx = execution.WithModelPreflightEnd(ctx, end)
	if next, ok := guarded.(interface{ SupportsModelPreflight() bool }); !ok || !next.SupportsModelPreflight() {
		end()
	}
	for _, err := range guarded.GenerateContent(ctx, &model.LLMRequest{}, false) {
		if err != nil {
			t.Fatal(err)
		}
	}
	if raw.calls != 1 {
		t.Fatalf("calls=%d", raw.calls)
	}
	for _, llm := range []model.LLM{&liveAuthorityModel{}, &authorityPreflightModel{supported: false}} {
		guarded := in.GuardModel(llm)
		if next, ok := guarded.(interface{ SupportsModelPreflight() bool }); ok && next.SupportsModelPreflight() {
			t.Fatal("invented preflight support")
		}
	}
}
