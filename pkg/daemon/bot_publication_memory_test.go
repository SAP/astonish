package daemon

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

// Real scoped publication receipts and retained files, controlled compute only.
// No model, notification transport or HTTP artifact delivery is involved.
func TestBotPublicationMemorySettledInterruptions(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			db, owner := setupChannelResolverFixture(t, "memory-interrupt", "team", "telegram", "linked")
			org, err := db.ForOrg("memory-interrupt")
			if err != nil {
				t.Fatal(err)
			}
			repo := org.ForTeam("team").Bots()
			if scope == store.BotScopePersonal {
				repo = org.ForUser(owner).Bots()
			}
			backend := &fileFirstHTTPBackend{setupHomeFilesBackend: setupHomeFilesBackend{documentsRoot: t.TempDir()}}
			worker := &BotWorker{config: BotWorkerConfig{Backend: db, Sandbox: backend, Pool: workerTestPool{}, WorkerID: "memory-interrupt"}}
			auth, _, err := worker.authenticateOwner(t.Context(), BotSchedulerScope{"memory-interrupt", "team", owner})
			if err != nil {
				t.Fatal(err)
			}
			version := store.DefaultBotVersion()
			version.Charter = "Finish the design"
			b, err := repo.CreateDraft(auth, store.BotCreateRequest{Name: "Interrupted memory", OwnerID: uuid.MustParse(owner), Version: version, IdempotencyKey: "draft"})
			if err != nil {
				t.Fatal(err)
			}
			b, err = worker.provisionHome(auth, repo, b.ID, b.Revision)
			if err != nil {
				t.Fatal(err)
			}
			db.(interface {
				SetBotActivationReadiness(store.BotActivationReadiness)
			}).SetBotActivationReadiness(func(context.Context, store.Bot, store.BotVersion) error { return nil })
			b, err = repo.Activate(auth, store.BotLifecycleRequest{BotID: b.ID, ActorID: uuid.MustParse(owner), ExpectedRevision: b.Revision, Reason: "test"})
			if err != nil {
				t.Fatal(err)
			}
			memoryPath := filepath.Join(backend.documentsRoot, b.ID.String(), "MEMORY.md")
			for _, status := range []store.RunStatus{store.RunCancelled, store.RunInterrupted} {
				t.Run(string(status), func(t *testing.T) {
					run, err := repo.RequestRun(auth, store.BotRunRequest{BotID: b.ID, Version: b.Version, Trigger: store.TriggerManual, Instructions: "Finish the design", IdempotencyKey: string(status)})
					if err != nil {
						t.Fatal(err)
					}
					runCtx := execution.WithBotRunIdentity(auth, execution.BotRunIdentity{BotID: b.ID, RunID: run.ID, Version: b.Version, ActorID: owner, OrgSlug: "memory-interrupt", TeamSlug: "team"})
					lease, err := repo.ClaimRun(runCtx, store.BotClaimRequest{BotID: b.ID, RunID: run.ID, WorkerID: worker.config.WorkerID, ExpiresAt: time.Now().Add(time.Minute)})
					if err != nil {
						t.Fatal(err)
					}
					before, err := os.ReadFile(memoryPath)
					if err != nil {
						t.Fatal(err)
					}
					current, err := repo.(store.BotWorkStore).LoadWork(runCtx, lease)
					if err != nil {
						t.Fatal(err)
					}
					report := store.BotReport{Summary: "Stopped before producing a deliverable", Work: store.BuildBotWorkProjection(current)}
					if err = repo.CommitRun(runCtx, store.BotRunCommit{Lease: lease, Status: status, Report: report}); err != nil {
						t.Fatal(err)
					}
					publication, err := repo.(store.BotPublicationStore).PublishReport(auth, store.BotPublicationRequest{BotID: b.ID, RunID: run.ID})
					if err != nil {
						t.Fatal(err)
					}
					if len(publication.Artifacts) != 0 {
						t.Fatal("cancellation fabricated files")
					}
					cancelled, cancel := context.WithCancel(auth)
					cancel()
					if err = worker.indexPublication(cancelled, repo, publication); !errors.Is(err, context.Canceled) {
						t.Fatal("cancelled maintenance did not stop", err)
					}
					after, err := os.ReadFile(memoryPath)
					if err != nil || !bytes.Equal(before, after) {
						t.Fatal("cancelled index mutated memory", err)
					}
					if err = worker.reconcilePublications(auth, repo, b.ID); err != nil {
						t.Fatal(err)
					}
					after, err = os.ReadFile(memoryPath)
					if err != nil {
						t.Fatal(err)
					}
					if strings.Count(string(after), run.ID.String()) != 1 || !strings.Contains(string(after), `"status": "interrupted"`) || !strings.Contains(string(after), "No verified published file deliverable") || strings.Contains(string(after), "delivered") {
						t.Fatalf("overstated or missing interruption: %s", after)
					}
					if err = worker.reconcilePublications(auth, repo, b.ID); err != nil {
						t.Fatal(err)
					}
					replay, err := os.ReadFile(memoryPath)
					if err != nil || !bytes.Equal(after, replay) {
						t.Fatal("replayed receipt rewrote memory", err)
					}
				})
			}
		})
	}
}
