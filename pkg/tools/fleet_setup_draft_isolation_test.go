package tools

import (
	"context"
	"errors"
	"io/fs"
	"testing"

	"github.com/SAP/astonish/pkg/store"
)

func TestGetSetupProfileDraftIsolation(t *testing.T) {
	owned := newSetupDraftMemoryStore()
	seedSetupDraft(t, owned, "owned")
	owned.drafts["owned"].Collected = map[string]any{"overview": map[string]any{"_ack": true}}
	foreign := newSetupDraftMemoryStore()
	seedSetupDraft(t, foreign, "foreign")
	// The injected tenant store must take precedence, even when the service
	// fallback contains the requested ID in another scope.
	base := store.WithServices(context.Background(), &store.Services{FleetSetupDrafts: foreign})
	scoped := store.WithFleetSetupDraftStore(base, owned)
	for _, tt := range []struct {
		name    string
		ctx     context.Context
		draftID string
		wantErr error
	}{
		{"missing", scoped, "missing", fs.ErrNotExist},
		{"foreign", scoped, "foreign", fs.ErrNotExist},
		{"unavailable", context.Background(), "owned", store.ErrUnsupported},
		{"owned", scoped, " owned ", nil},
		{"profile_only", context.Background(), "", nil},
		{"blank_draft", context.Background(), "  ", nil},
		{"service_store", store.WithServices(context.Background(), &store.Services{FleetSetupDrafts: owned}), "owned", nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := getSetupProfile(&setupToolCtx{Context: tt.ctx}, GetSetupProfileArgs{ProfileKey: "software-development", DraftID: tt.draftID})
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("error = %v, want %v (status %q)", err, tt.wantErr, got.Status)
			}
			if tt.wantErr != nil {
				if got.Status != "error" || got.Message == "" || len(got.Steps) != 0 || len(got.Completion) != 0 || got.StepPrompt != "" {
					t.Fatalf("failed draft lookup returned profile data: %+v", got)
				}
				return
			}
			if got.Status != "ok" || got.ProfileKey != "software-development" || len(got.Steps) == 0 || len(got.Completion) == 0 {
				t.Fatalf("profile lookup failed: %+v", got)
			}
			if tt.name == "owned" || tt.name == "service_store" {
				if got.CurrentStep == "overview" {
					t.Fatal("owned draft completion was ignored")
				}
			} else if got.CurrentStep != "overview" {
				t.Fatalf("profile-only current step = %q, want overview", got.CurrentStep)
			}
		})
	}
}
