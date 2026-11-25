package launcher

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
)

func TestBotFileDeliveryRecoverySavedBytes(t *testing.T) {
	for _, mode := range []string{"complete", "cancelled", "corrupt", "oversize", "denied", "approval-required"} {
		t.Run(mode, func(t *testing.T) {
			f := newBotFactoryFixture(t)
			level := execution.AutonomySupervised
			allowed := []string{"read_file", "write_file", "publish_file"}
			if mode == "denied" {
				allowed = []string{"read_file"}
			}
			if mode == "approval-required" {
				level = execution.AutonomyAsk
			}
			var err error
			f.req.Policy, err = execution.NewRunPolicy(execution.RunPolicySpec{Identity: f.req.Policy.Identity(), Level: level, AllowedTools: allowed})
			if err != nil {
				t.Fatal(err)
			}
			got, err := f.construct(t)
			if err != nil {
				t.Fatal(err)
			}
			defer got.Cleanup()
			binding := botFactoryModelForTest(t, got.Agent.LLM).binding
			files := &confinedTestFiles{root: t.TempDir(), scope: store.BotContentScope{BotID: f.req.Bot.ID, OwnerID: f.req.Bot.OwnerID.String()}, snapshots: map[string][]byte{}}
			scope := &BotChatScope{Services: &store.Services{BotFilesFactory: func(context.Context, *store.Services, store.BotScope) (store.BotFiles, error) { return files, nil }}}
			receipts := &BotFileReceipts{}
			recover := binding.fileDeliveryRecovery(scope, receipts)
			ctx, cancel := context.WithCancel(f.ctx)
			defer cancel()
			source := strings.Repeat("Full useful report evidence.\n", 3000) + "END OF FULL ANSWER"
			if mode == "cancelled" {
				cancel()
			}
			if mode == "oversize" {
				source = strings.Repeat("x", botFileEditMaxBytes+1)
			}
			p := "outputs/" + f.req.Run.ID.String() + "-report.md"
			if mode == "corrupt" {
				files.openOverride = func() {
					if err := os.WriteFile(filepath.Join(files.root, p), []byte("corrupted"), 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			artifacts, err := recover(ctx, source)
			if mode != "complete" {
				if err == nil || len(receipts.Snapshot()) != 0 {
					t.Fatalf("unsafe receipt: %v %+v", err, artifacts)
				}
			} else {
				if err != nil || len(artifacts) != 1 {
					t.Fatalf("save: %v %+v", err, artifacts)
				}
				bytes, err := os.ReadFile(filepath.Join(files.root, p))
				if err != nil {
					t.Fatal(err)
				}
				if string(bytes) != source || string(files.snapshots["runs/"+f.req.Run.ID.String()+"/completion-report"]) != source {
					t.Fatal("source truncated or snapshot differs")
				}
				if got.RepairFileDelivery == nil {
					t.Fatal("repair callback unwired")
				}
				repair := binding.fileDeliveryRepair(scope, receipts)
				for revision := 1; revision <= store.BotWorkMaxRepairs; revision++ {
					revised, err := repair(ctx, "qualified revised report", revision)
					if err != nil || len(revised) != 1 || revised[0].ID == artifacts[0].ID {
						t.Fatalf("repair snapshot: %v %+v", err, revised)
					}
					if string(files.snapshots["runs/"+f.req.Run.ID.String()+"/completion-report"]) != source {
						t.Fatal("original snapshot overwritten")
					}
					before := files.calls
					if _, err := repair(ctx, "duplicate", revision); !errors.Is(err, store.ErrBotConflict) || before != files.calls {
						t.Fatal("repair replayed")
					}
				}
				if _, err := repair(ctx, "exhausted", store.BotWorkMaxRepairs+1); !errors.Is(err, store.ErrBotConflict) {
					t.Fatal("repair limit bypass")
				}
			}
			if (mode == "denied" || mode == "approval-required") && files.calls != 0 {
				t.Fatal("recovery bypassed reviewed policy")
			}
			before := files.calls
			if _, err := recover(f.ctx, source); !errors.Is(err, store.ErrBotConflict) || files.calls != before {
				t.Fatalf("second attempt: %v", err)
			}
		})
	}
}
