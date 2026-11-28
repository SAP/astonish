package launcher

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

type deliveryMandateRuntime struct {
	factoryMandateRuntime
	charges int64
}

func (r *deliveryMandateRuntime) ConsumeBotMandateQuota(_ context.Context, _ store.BotMandateRef, _ uuid.UUID, tools, _, _ int64) error {
	r.charges += tools
	return nil
}

func capturedDeliveryConstraints(t *testing.T) []execution.BotActionResourceConstraint {
	t.Helper()
	p := os.Getenv("ASTONISH_DELIVERY_CAPTURE")
	if p == "" {
		return nil
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	var root any
	if err := json.Unmarshal(raw, &root); err != nil {
		t.Fatal(err)
	}
	var found []execution.BotActionResourceConstraint
	var walk func(any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			if constraints, ok := x["constraints"]; ok {
				b, _ := json.Marshal(constraints)
				var cs []execution.BotActionResourceConstraint
				if json.Unmarshal(b, &cs) == nil {
					for _, c := range cs {
						for _, name := range c.Resources {
							if name == "write_file" {
								found = cs
							}
						}
					}
				}
			}
			for _, child := range x {
				walk(child)
			}
		case []any:
			for _, child := range x {
				walk(child)
			}
		case string:
			if strings.HasPrefix(x, "{") || strings.HasPrefix(x, "[") {
				var child any
				if json.Unmarshal([]byte(x), &child) == nil {
					walk(child)
				}
			}
		}
	}
	walk(root)
	if len(found) == 0 {
		t.Fatal("no captured file grant")
	}
	t.Log("using unchanged action/resource constraints from private retained mandate (test identity and expiry only rebound)")
	return found
}

func TestBotFileDeliveryRecoveryMandateBudget(t *testing.T) {
	for _, mode := range []string{"complete", "no-write", "no-publish", "revoked", "stale-lease"} {
		t.Run(mode, func(t *testing.T) {
			f := newBotFactoryFixture(t)
			id := f.req.Policy.Identity()
			names := []string{"read_file", "write_file", "publish_file"}
			if mode == "no-write" {
				names = []string{"read_file", "publish_file"}
			}
			if mode == "no-publish" {
				names = []string{"read_file", "write_file"}
			}
			runtime := &deliveryMandateRuntime{factoryMandateRuntime: factoryMandateRuntime{saved: execution.BotMandate{Ref: execution.BotMandateRef{ID: uuid.New(), BotID: id.BotID, OwnerID: id.ActorID, OrgSlug: id.OrgSlug, TeamSlug: id.TeamSlug, Revision: 1}, Purpose: execution.BotMandatePurposeTask, ExpiresAt: time.Now().Add(time.Hour), Scopes: []string{"chat", "tool:execute"}, Constraints: []execution.BotActionResourceConstraint{{Action: execution.CapabilityChat, Resources: []string{"task"}}, {Action: execution.CapabilityToolExecute, Resources: names}}, Limits: execution.BotMandateLimits(config.DefaultBotMandateConfig())}}}
			if mode == "complete" {
				if captured := capturedDeliveryConstraints(t); captured != nil {
					runtime.saved.Constraints = captured
				}
			}
			auth, err := execution.VerifyBotMandateAuthorization(f.ctx, runtime, runtime.saved.Ref, runtime.saved.Purpose)
			if err != nil {
				t.Fatal(err)
			}
			f.req.Mandate = &bot.MandateExecution{Authorization: auth, Runtime: runtime, Identity: id}
			meter, err := bot.NewRunBudget(f.req.Version.Budget, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			f.req.Budget = &bot.MandateBudget{SharedRunBudget: meter, Mandate: f.req.Mandate}
			// Keep run policy permissive to prove that the SAVED mandate is the ceiling.
			f.req.Policy, err = execution.NewRunPolicy(execution.RunPolicySpec{Identity: id, Level: execution.AutonomySupervised, AllowedTools: []string{"read_file", "write_file", "publish_file"}})
			if err != nil {
				t.Fatal(err)
			}
			binding := &botFactoryBinding{req: f.req, owner: execution.WithBotRunIdentity(store.WithServices(f.ctx, f.services), id), verifyIsolation: func(context.Context) error { return nil }}
			files := &confinedTestFiles{root: t.TempDir(), scope: store.BotContentScope{BotID: id.BotID, OwnerID: id.ActorID}, snapshots: map[string][]byte{}}
			scope := &BotChatScope{Services: &store.Services{BotFilesFactory: func(context.Context, *store.Services, store.BotScope) (store.BotFiles, error) { return files, nil }}}
			source := strings.Repeat("Complete retained design.\n", 2100)
			if p := os.Getenv("ASTONISH_DELIVERY_REPLAY_REPORT"); p != "" {
				raw, e := os.ReadFile(p)
				if e != nil {
					t.Fatal(e)
				}
				source = string(raw)
				if len(raw) != 50546 || fmt.Sprintf("%x", sha256.Sum256(raw)) != "e06f168f9613fd156b0c47d59dddead935a31f78e6868ce18f840edd01aa3dd9" {
					t.Fatal("captured report changed")
				}
			}
			if mode == "revoked" {
				runtime.revoked = true
			}
			if mode == "stale-lease" {
				f.req.Run.Lease.Fence++
			}
			receipts := &BotFileReceipts{}
			recover := binding.fileDeliveryRecovery(scope, receipts)
			artifacts, err := recover(f.ctx, source)
			if mode != "complete" {
				if err == nil || files.calls != 0 || len(receipts.Snapshot()) != 0 {
					t.Fatalf("unauthorized IO: err=%v calls=%d", err, files.calls)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			p := "outputs/" + id.RunID.String() + "-report.md"
			raw, err := os.ReadFile(filepath.Join(files.root, p))
			if err != nil || string(raw) != source || len(artifacts) != 1 || runtime.charges != 2 {
				t.Fatalf("delivery: err=%v artifacts=%d charges=%d", err, len(artifacts), runtime.charges)
			}
			if err := os.WriteFile(filepath.Join(files.root, p), []byte("later edit"), 0600); err != nil {
				t.Fatal(err)
			}
			if string(files.snapshots["runs/"+id.RunID.String()+"/completion-report"]) != source {
				t.Fatal("snapshot changed with working file")
			}
			if _, err := recover(f.ctx, source); err == nil {
				t.Fatal("replayed recovery")
			}
			if out := os.Getenv("ASTONISH_DELIVERY_REPLAY_OUTPUT"); out != "" {
				if err := os.MkdirAll(out, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(out, "report.md"), raw, 0600); err != nil {
					t.Fatal(err)
				}
				envelope, _ := json.MarshalIndent(artifacts, "", "  ")
				if err := os.WriteFile(filepath.Join(out, "local-recovery-artifacts.json"), envelope, 0600); err != nil {
					t.Fatal(err)
				}
			}
			t.Logf("saved and snapshotted %d bytes sha256=%x; two exact mandate charges", len(raw), sha256.Sum256(raw))
		})
	}
}
