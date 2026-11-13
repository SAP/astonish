package api

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/SAP/astonish/pkg/backup"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/entstore"
	"github.com/google/uuid"
)

// Real loopback HTTP, production routes/middleware and two independent SQLite
// deployments. Cleanup is a controlled attestation, not physical-home evidence.
func TestBotSkillRecoveryHTTPAcceptance(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			source := newSkillStorageHTTPFixture(t)
			b := source.bot(scope)
			worker, q, first := source.create(b)
			source.finish(b, worker, q.Mutation.Lease)
			worker, update := source.updateOrientation(b, first.Invocation.Snapshot)
			second, err := source.skills(scope).OrientSkill(worker, update)
			if err != nil {
				t.Fatal(err)
			}
			source.finish(b, worker, update.Mutation.Lease)
			// A different Bot remains running, demonstrating restore quiescence and
			// exact-target purge independently of any physical teardown assumption.
			other := source.bot(scope)
			_, otherQ, otherSkill := source.create(other)
			archive := filepath.Join(t.TempDir(), "skills.astonish-backup")
			if err := source.db.ExportPlatformBackup(context.Background(), archive, entstore.PlatformBackupExportOptions{Backend: "sqlite"}); err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			org, team := *source.org, *source.team
			org.Slug, team.Slug = "recovered-skills", "recovered-team"
			dest := &skillStorageHTTPFixture{t: t, cfg: entstore.Config{DSN: filepath.Join(dir, "platform.db"), DataDir: dir}, owner: uuid.NewString(), member: source.member, org: &org, team: &team}
			dest.open()
			opts := entstore.PlatformRestoreOptions{IncludeTransient: true, EnableScheduledJobs: true, MapOrg: map[string]string{source.org.Slug: org.Slug}, MapTeam: map[string]string{source.org.Slug + "/" + source.team.Slug: org.Slug + "/" + team.Slug}, MapUser: map[string]string{source.org.Slug + "/" + source.owner: org.Slug + "/" + dest.owner}}
			if _, err := dest.db.RestorePlatformBackup(context.Background(), archive, opts); err != nil {
				t.Fatal(err)
			}
			dest.start()
			t.Cleanup(func() { dest.server.Close(); _ = dest.db.Close() })
			for i := 0; i < 2; i++ {
				dest.reopen()
				head := dest.readHead(b, second.Invocation.SkillID)
				if !reflect.DeepEqual(head, second.Invocation.Snapshot) {
					t.Fatal("mapped restore changed immutable head/provenance")
				}
				old := skillDecode[store.BotSkillRevision](t, dest.request("GET", skillPath(b, "/skills/"+head.SkillID.String()+"/revisions/1"), dest.owner, "", 200))
				if !reflect.DeepEqual(old, first.Invocation.Snapshot) {
					t.Fatal("history changed")
				}
				runs := skillDecode[store.BotSkillRunList](t, dest.request("GET", skillPath(b, "/skills/"+head.SkillID.String()+"/runs"), dest.owner, "", 200))
				if len(runs.Runs) != 2 {
					t.Fatalf("lost pinned runs: %d", len(runs.Runs))
				}
				for _, pinned := range runs.Runs {
					original, err := source.repo(scope).GetRun(source.context(source.owner), b.ID, pinned.ID)
					if err != nil || !reflect.DeepEqual(pinned.Checkpoint, original.Checkpoint) {
						t.Fatalf("run-pinned checkpoint changed: %v", err)
					}
				}
				restored := skillDecode[store.Bot](t, dest.request("GET", skillPath(other, ""), dest.owner, "", 200))
				if restored.Status != store.BotPaused {
					t.Fatal("active Bot resumed on restore")
				}
				history, err := dest.repo(scope).ListRuns(dest.context(dest.owner), other.ID, store.BotPage{})
				if err != nil || len(history) != 1 || history[0].Status != store.RunInterrupted || history[0].Lease.Fence <= otherQ.Mutation.Lease.Fence {
					t.Fatalf("run not quiesced: %+v %v", history, err)
				}
				dest.request("GET", skillPath(other, "/skills/"+head.SkillID.String()+"/head"), dest.owner, "", 404)
				dest.request("GET", skillPath(b, "/skills"), source.owner, "", 403)
				wrong := *b
				if scope == store.BotScopePersonal {
					wrong.Scope = store.BotScopeTeam
				} else {
					wrong.Scope = store.BotScopePersonal
				}
				dest.request("GET", skillPath(&wrong, "/skills"), dest.owner, "", 404)
			}
			current := skillDecode[store.Bot](t, dest.request("GET", skillPath(b, ""), dest.owner, "", 200))
			body := fmt.Sprintf(`{"expected_revision":%d,"confirm":true}`, current.Revision)
			dest.request("POST", skillPath(b, "/permanent-delete"), dest.owner, body, 202)
			deleted, err := dest.repo(scope).Get(dest.context(dest.owner), b.ID)
			if err != nil {
				t.Fatal(err)
			}
			service, err := execution.WithPrincipal(context.Background(), execution.Principal{Kind: execution.PrincipalKindService, ClientID: "recovery-test", Authentication: execution.AuthMethod("internal"), Surface: execution.SurfaceCode, OrgSlug: org.Slug, TeamSlug: team.Slug, Authenticated: true, Scopes: []string{store.BotCleanupScope}})
			if err != nil {
				t.Fatal(err)
			}
			claim, err := dest.repo(scope).(store.BotHomeCleanupStore).ReserveHomeCleanup(service, deleted.Home, deleted.Revision)
			if err != nil {
				t.Fatal(err)
			}
			service = store.WithBotCleanupVerifier(service, func(_ context.Context, got store.BotCleanupRequest) error {
				if got != claim.Request {
					return fmt.Errorf("wrong cleanup target")
				}
				return nil
			})
			if err := dest.repo(scope).(store.BotCleanupStore).ConfirmCleanup(service, claim.Request); err != nil {
				t.Fatal(err)
			}
			dest.request("POST", skillPath(b, "/permanent-delete"), dest.owner, fmt.Sprintf(`{"expected_revision":%d,"confirm":true}`, deleted.Revision), 204)
			dest.reopen()
			dest.request("GET", skillPath(b, "/skills"), dest.owner, "", 404)
			if head := dest.readHead(other, otherSkill.Invocation.SkillID); head.Digest != otherSkill.Invocation.Digest {
				t.Fatal("purge affected other Bot")
			}
			// A fresh logical export proves the purge removed the underlying rows,
			// not merely that the HTTP route hides an orphaned skill aggregate.
			purgedArchive := filepath.Join(t.TempDir(), "after-purge.astonish-backup")
			if err := dest.db.ExportPlatformBackup(context.Background(), purgedArchive, entstore.PlatformBackupExportOptions{Backend: "sqlite"}); err != nil {
				t.Fatal(err)
			}
			for _, check := range []struct {
				path             string
				roots, revisions int64
			}{{purgedArchive, 1, 1}, {archive, 2, 3}} {
				summary, err := backup.Verify(check.path)
				if err != nil {
					t.Fatal(err)
				}
				counts := map[string]int64{}
				for _, entry := range summary.Manifest.Entries {
					counts[entry.Entity] += entry.Records
				}
				if counts["bot_skills"] != check.roots || counts["bot_skill_revisions"] != check.revisions {
					t.Fatalf("skill rows after exact purge: roots=%d revisions=%d want %d/%d", counts["bot_skills"], counts["bot_skill_revisions"], check.roots, check.revisions)
				}
			}
			// Source deployment and archive are not swept by destination purge.
			source.readHead(b, first.Invocation.SkillID)
		})
	}
}
