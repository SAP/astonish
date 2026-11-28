package api_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/store"
)

func TestBotDefaultCharterHTTPAcceptance(t *testing.T) {
	f := newInteractiveHTTPFixture(t, "read_file", map[string]any{"path": "/isolated/receipt.txt"}, []string{"chat", "tool:execute"}, store.BotScopePersonal)
	const defaultCharter = "Take responsibility for the owner's goals and deliver useful, verified outcomes. Perform routine work within your authorized mandate and existing permissions without extra review. Escalate genuine ambiguity or actions outside that scope for clarification or authorization. A request or this charter does not itself grant permission for external side effects."
	const custom = "  Review every proposal explicitly.\nCustom responsibility.  "
	for _, tc := range []struct {
		name    string
		title   bool
		charter string
	}{
		{"default", false, ""}, {"title default", true, ""},
		{"custom", false, custom}, {"title custom", true, custom},
		{"legacy text supplied", false, "Discuss your goals and draft proposals for explicit review. Do not take external actions without separately reviewed permissions."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload, err := json.Marshal(map[string]any{"name": "New employee", "idempotency_key": tc.name, "start_from_title": tc.title, "definition": map[string]any{"charter": tc.charter}})
			if err != nil {
				t.Fatal(err)
			}
			var receipt titleHTTPReceipt
			if err := json.Unmarshal(f.request("POST", "/api/studio/bots", string(payload), 201), &receipt); err != nil {
				t.Fatal(err)
			}
			version, err := f.repo.GetVersion(f.ctx, receipt.ID, 1)
			if err != nil {
				t.Fatal(err)
			}
			want := tc.charter
			if strings.TrimSpace(want) == "" {
				want = defaultCharter
				if tc.title {
					want = "New employee\n\n" + want
				}
			}
			if version.Charter != want {
				t.Fatalf("charter = %q, want %q", version.Charter, want)
			}
			if !reflect.DeepEqual(version.Autonomy, store.DefaultBotVersion().Autonomy) {
				t.Fatalf("creation changed permissions: %+v", version.Autonomy)
			}
			if (receipt.InitialTurn != nil) != tc.title {
				t.Fatal("charter changed start behavior")
			}
			if f.model.called {
				t.Fatal("creation executed provider work")
			}
			// Replay and reread must preserve the immutable version, including legacy/custom text.
			f.request("POST", "/api/studio/bots", string(payload), 201)
			reread, err := f.repo.GetVersion(f.ctx, receipt.ID, 1)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(version, reread) {
				t.Fatal("persisted version rewritten")
			}
		})
	}
}
