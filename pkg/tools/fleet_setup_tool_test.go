package tools

import (
	"context"
	"testing"

	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/agent"
	"google.golang.org/adk/memory"
	"google.golang.org/adk/session"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/toolconfirmation"
	"google.golang.org/genai"
)

type setupDraftMemoryStore struct {
	drafts map[string]*store.FleetSetupDraft
}

func newSetupDraftMemoryStore() *setupDraftMemoryStore {
	return &setupDraftMemoryStore{drafts: map[string]*store.FleetSetupDraft{}}
}

func (m *setupDraftMemoryStore) Create(_ context.Context, draft *store.FleetSetupDraft) error {
	copied := *draft
	m.drafts[draft.ID] = &copied
	return nil
}

func (m *setupDraftMemoryStore) Get(_ context.Context, id string) (*store.FleetSetupDraft, bool) {
	draft, ok := m.drafts[id]
	if !ok {
		return nil, false
	}
	copied := *draft
	return &copied, true
}

func (m *setupDraftMemoryStore) Update(_ context.Context, draft *store.FleetSetupDraft) error {
	copied := *draft
	m.drafts[draft.ID] = &copied
	return nil
}

func (m *setupDraftMemoryStore) Delete(_ context.Context, id string) error {
	delete(m.drafts, id)
	return nil
}

type setupToolCtx struct {
	context.Context
}

var _ tool.Context = (*setupToolCtx)(nil)

func (m *setupToolCtx) UserContent() *genai.Content { return nil }
func (m *setupToolCtx) InvocationID() string        { return "test" }
func (m *setupToolCtx) AgentName() string           { return "test" }
func (m *setupToolCtx) ReadonlyState() session.ReadonlyState {
	return nil
}
func (m *setupToolCtx) UserID() string                 { return "" }
func (m *setupToolCtx) AppName() string                { return "" }
func (m *setupToolCtx) SessionID() string              { return "" }
func (m *setupToolCtx) Branch() string                 { return "" }
func (m *setupToolCtx) Artifacts() agent.Artifacts     { return nil }
func (m *setupToolCtx) State() session.State           { return nil }
func (m *setupToolCtx) FunctionCallID() string         { return "" }
func (m *setupToolCtx) Actions() *session.EventActions { return nil }
func (m *setupToolCtx) SearchMemory(_ context.Context, _ string) (*memory.SearchResponse, error) {
	return nil, nil
}
func (m *setupToolCtx) ToolConfirmation() *toolconfirmation.ToolConfirmation { return nil }
func (m *setupToolCtx) RequestConfirmation(_ string, _ any) error            { return nil }

func TestUpdateSetupDraftUsesContextDraftStore(t *testing.T) {
	draftStore := newSetupDraftMemoryStore()
	seedSetupDraft(t, draftStore, "draft-1")

	ctx := store.WithFleetSetupDraftStore(context.Background(), draftStore)
	result, err := updateSetupDraft(&setupToolCtx{Context: ctx}, UpdateSetupDraftArgs{
		DraftID:  "draft-1",
		StepID:   "overview",
		MarkStep: "channel",
		Values: map[string]any{
			"_ack": true,
		},
	})
	if err != nil {
		t.Fatalf("updateSetupDraft error: %v", err)
	}
	if result.Status != "ok" {
		t.Fatalf("status = %q, message = %q", result.Status, result.Message)
	}
	if result.Message == "Setup draft store not available" {
		t.Fatal("setup draft store was not resolved from context")
	}

	assertOverviewAckPersisted(t, draftStore, "draft-1")
	updated, _ := draftStore.Get(context.Background(), "draft-1")
	if updated.CurrentStep != "channel" {
		t.Fatalf("CurrentStep = %q, want channel", updated.CurrentStep)
	}
}

func TestUpdateSetupDraftNormalizesInfoStepAckAliases(t *testing.T) {
	for _, tt := range []struct {
		name   string
		values map[string]any
	}{
		{name: "acknowledged", values: map[string]any{"acknowledged": true}},
		{name: "confirmed", values: map[string]any{"confirmed": true}},
		{name: "ack string", values: map[string]any{"ack": "yes"}},
		{name: "status acknowledged", values: map[string]any{"status": "acknowledged"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			draftStore := newSetupDraftMemoryStore()
			seedSetupDraft(t, draftStore, "draft-"+tt.name)

			ctx := store.WithFleetSetupDraftStore(context.Background(), draftStore)
			result, err := updateSetupDraft(&setupToolCtx{Context: ctx}, UpdateSetupDraftArgs{
				DraftID: "draft-" + tt.name,
				StepID:  "overview",
				Values:  tt.values,
			})
			if err != nil {
				t.Fatalf("updateSetupDraft error: %v", err)
			}
			if result.Status != "ok" {
				t.Fatalf("status = %q, message = %q", result.Status, result.Message)
			}
			assertOverviewAckPersisted(t, draftStore, "draft-"+tt.name)
		})
	}
}

func seedSetupDraft(t *testing.T, draftStore *setupDraftMemoryStore, id string) {
	t.Helper()
	draft := &store.FleetSetupDraft{
		ID:              id,
		TemplateKey:     "software-dev",
		SetupProfileKey: "software-development",
		Collected:       map[string]any{},
	}
	if err := draftStore.Create(context.Background(), draft); err != nil {
		t.Fatalf("Create draft: %v", err)
	}
}

func assertOverviewAckPersisted(t *testing.T, draftStore *setupDraftMemoryStore, id string) {
	t.Helper()
	updated, ok := draftStore.Get(context.Background(), id)
	if !ok {
		t.Fatal("updated draft not found")
	}
	overview, _ := updated.Collected["overview"].(map[string]any)
	if ack, _ := overview["_ack"].(bool); !ack {
		t.Fatalf("overview _ack not persisted: %+v", overview)
	}
}
