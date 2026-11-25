//go:build e2e

package e2eboot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/backup"
	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/entstore"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// This wrapper records acknowledged journaled allocations, never adopts a Home,
// and refuses source deletion even if a malformed recovery journal requests it.
type retainedHomeRestoreOwner struct {
	entstore.BotRestoreBackend
	source  *sandbox.BotHomeBinding
	targets map[string]*sandbox.BotHomeBinding
}

func (b *retainedHomeRestoreOwner) CreateBotHomeRestoreTargetJournaled(c context.Context, s sandbox.BotHomeScope, reserve func(*sandbox.BotHomeBinding) error) (*sandbox.BotHomeBinding, error) {
	j, ok := b.BotRestoreBackend.(interface {
		CreateBotHomeRestoreTargetJournaled(context.Context, sandbox.BotHomeScope, func(*sandbox.BotHomeBinding) error) (*sandbox.BotHomeBinding, error)
	})
	if !ok || s != b.source.Scope {
		return nil, store.ErrBotForbidden
	}
	return j.CreateBotHomeRestoreTargetJournaled(c, s, func(h *sandbox.BotHomeBinding) error {
		if h == nil || h.Validate() != nil || h.Scope != s || h.Ref == b.source.Ref {
			return store.ErrBotForbidden
		}
		if err := reserve(h); err != nil {
			return err
		}
		b.targets[h.Ref] = h.Clone()
		return nil
	})
}
func (b *retainedHomeRestoreOwner) DeleteBotHome(c context.Context, h *sandbox.BotHomeBinding) error {
	if h == nil || h.Ref == b.source.Ref || !b.targets[h.Ref].Equal(h) {
		return store.ErrBotForbidden
	}
	d, ok := b.BotRestoreBackend.(sandbox.BotHomeDeleteBackend)
	if !ok {
		return store.ErrBotUnsupported
	}
	return d.DeleteBotHome(c, h)
}

type retainedHomePublicImporter interface {
	PlanPlatformRestore(context.Context, string, entstore.PlatformRestoreOptions) (*backup.RestorePlan, error)
	RestorePlatformBackup(context.Context, string, entstore.PlatformRestoreOptions) (*backup.RestoreResult, error)
	CleanupFileFirstRestore(context.Context, string, entstore.PlatformRestoreOptions) error
}

func retainedHomePublicRestore(c context.Context, s retainedHomePublicImporter, archive string, opts entstore.PlatformRestoreOptions) error {
	plan, err := s.PlanPlatformRestore(c, archive, opts)
	if err != nil {
		return err
	}
	if plan == nil || !plan.TargetEmpty || len(plan.Blockers) > 0 {
		return fmt.Errorf("restore target not empty or blocked: %+v", plan)
	}
	result, err := s.RestorePlatformBackup(c, archive, opts)
	if err != nil {
		return err
	}
	if result == nil || result.RestoredRecords == 0 {
		return errors.New("public restore returned no records")
	}
	return nil
}

type retainedHomeSQLReceipt struct {
	name, comment string
	oid           int64
}

func retainedHomeCheckSQLReceipt(c context.Context, conn *pgx.Conn, r retainedHomeSQLReceipt) error {
	var name, comment string
	var oid int64
	var owned bool
	err := conn.QueryRow(c, `SELECT datname, oid, COALESCE(shobj_description(oid,'pg_database'),''), datdba=(SELECT oid FROM pg_roles WHERE rolname=current_user) FROM pg_database WHERE datname=$1`, r.name).Scan(&name, &oid, &comment, &owned)
	if err != nil {
		return err
	}
	return validateRetainedHomeSQLIdentity(r, name, oid, comment, owned)
}

func validateRetainedHomeSQLIdentity(r retainedHomeSQLReceipt, name string, oid int64, comment string, owned bool) error {
	if r.oid <= 0 || oid != r.oid {
		return store.ErrBotConflict
	}
	return validateRetainedHomeDatabaseReceipt(name, r.name, comment, r.comment, owned)
}

func retainedHomeCleanupUncommitted(c context.Context, target retainedHomePublicImporter, archive string, files entstore.BotRestoreBackend) error {
	return target.CleanupFileFirstRestore(c, archive, entstore.PlatformRestoreOptions{FileBackend: func(context.Context) (entstore.BotRestoreBackend, error) { return files, nil }})
}

// Restore uses a separate platform database and the importer's own fresh tenant
// generations. No ProvisionOrg, seeded owner, existing route or SQL reset is used.
func newRetainedHomeRestoreSQL(t *testing.T, in RetainedHomeKubernetesInputs, sourceDSN string, files *retainedHomeRestoreOwner, archive string) (*entstore.Store, entstore.Config) {
	t.Helper()
	checked, err := retainedHomeKubernetesInputs(os.Getenv)
	if err != nil || checked.DSN != in.DSN || checked.Owner != in.Owner {
		t.Fatal("BLOCKED: dedicated PostgreSQL instance receipt changed before restore")
	}
	suffix := "e2erestore" + uuid.NewString()[:8]
	cfg := entstore.Config{DSN: buildDSN(t, in.DSN, suffix), InstanceSuffix: suffix, DataDir: t.TempDir(), MaxOpenConns: 5, MaxIdleConns: 1}
	if cfg.DSN == sourceDSN {
		t.Fatal("restore target aliases source")
	}
	c := context.Background()
	conn, err := pgx.Connect(c, in.DSN)
	if err != nil {
		t.Fatal("BLOCKED: dedicated restore PostgreSQL connection")
	}
	defer conn.Close(c)
	r := retainedHomeSQLReceipt{name: config.PlatformDBName(suffix), comment: "astonish-retained-restore:" + in.Owner + ":" + uuid.NewString()}
	if _, err = conn.Exec(c, "CREATE DATABASE "+pgx.Identifier{r.name}.Sanitize()+" TEMPLATE template0"); err != nil {
		t.Fatal("BLOCKED: fresh restore database creation not acknowledged; no adoption")
	}
	if _, err = conn.Exec(c, "COMMENT ON DATABASE "+pgx.Identifier{r.name}.Sanitize()+" IS '"+r.comment+"'"); err != nil {
		t.Fatal("BLOCKED: restore ownership receipt missing; retain target")
	}
	if err = conn.QueryRow(c, "SELECT oid FROM pg_database WHERE datname=$1", r.name).Scan(&r.oid); err != nil || retainedHomeCheckSQLReceipt(c, conn, r) != nil {
		t.Fatal("BLOCKED: restore OID/comment receipt invalid")
	}
	var target *entstore.Store
	t.Cleanup(func() {
		clean, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		identityConn, identityErr := pgx.Connect(clean, in.DSN)
		if identityErr != nil {
			t.Error("restore cleanup instance unavailable; retain target")
			return
		}
		identityErr = retainedHomeCheckSQLReceipt(clean, identityConn, r)
		identityConn.Close(clean)
		if identityErr != nil {
			t.Error("restore platform OID/comment changed; retain all targets")
			return
		}
		// Close the reopened org and HTTP clients before this callback. Uncommitted
		// failures use public recovery; committed destinations require exact receipts.
		if target != nil {
			raw, e := os.ReadFile(filepath.Join(cfg.DataDir, ".filefirst-restore", "journal.json"))
			if e == nil {
				var j struct {
					ID                string `json:"id"`
					ArchiveSHA256     string `json:"archive_sha256"`
					Phase             string `json:"phase"`
					PostgresDatabases []struct {
						Name       string `json:"name"`
						Generation string `json:"generation"`
						Instance   string `json:"instance"`
						OID        int64  `json:"oid"`
						Created    bool   `json:"created"`
						Ready      bool   `json:"ready"`
					} `json:"postgres_databases"`
				}
				if json.Unmarshal(raw, &j) != nil {
					t.Error("restore journal invalid; retain target")
					return
				}
				if j.Phase != "committed" {
					if e = retainedHomeCleanupUncommitted(clean, target, archive, files); e != nil {
						t.Errorf("owned public restore cleanup refused; retain target: %v", e)
						return
					}
				} else {
					admin, e := pgx.Connect(clean, in.DSN)
					if e != nil {
						t.Error("restore cleanup connection blocked")
						return
					}
					defer admin.Close(clean)
					for _, d := range j.PostgresDatabases {
						g, e := uuid.Parse(d.Generation)
						if e != nil || g.String() != d.Generation || d.Name != "astonish_restore_"+strings.ReplaceAll(d.Generation, "-", "") || d.Instance != suffix || !d.Created || !d.Ready || j.ID == "" || len(j.ArchiveSHA256) != 64 {
							t.Error("restore generation receipt invalid; retain target")
							return
						}
						receipt := retainedHomeSQLReceipt{name: d.Name, oid: d.OID, comment: "astonish-filefirst:" + j.ID + ":" + j.ArchiveSHA256 + ":" + d.Instance + ":" + d.Generation}
						if e = retainedHomeCheckSQLReceipt(clean, admin, receipt); e != nil {
							t.Error("restore generation changed; retain target")
							return
						}
						if _, e = admin.Exec(clean, "DROP DATABASE "+pgx.Identifier{d.Name}.Sanitize()); e != nil {
							t.Errorf("owned restore generation busy; no force: %v", e)
							return
						}
					}
					for _, h := range files.targets {
						if e = files.DeleteBotHome(clean, h); e != nil {
							t.Errorf("owned restore Home retained: %v", e)
							return
						}
					}
				}
			} else if !errors.Is(e, os.ErrNotExist) {
				t.Error("restore journal inaccessible; retain target")
				return
			}
			if e := target.Close(); e != nil {
				t.Error(e)
				return
			}
		}
		admin, e := pgx.Connect(clean, in.DSN)
		if e != nil {
			t.Error("target cleanup blocked")
			return
		}
		defer admin.Close(clean)
		if retainedHomeCheckSQLReceipt(clean, admin, r) != nil {
			t.Error("target receipt changed; no drop")
			return
		}
		if _, e = admin.Exec(clean, "DROP DATABASE "+pgx.Identifier{r.name}.Sanitize()); e != nil {
			t.Errorf("owned target busy; no force: %v", e)
		}
	})
	if err = entstore.BootstrapPlatform(c, cfg, nil); err != nil {
		t.Fatalf("BLOCKED: restore platform bootstrap: %v", err)
	}
	_, target, err = entstore.NewPlatformServices(c, cfg)
	if err != nil {
		t.Fatal(err)
	}
	return target, cfg
}

func retainedHomePausedSchedules(source, restored []*store.ScheduledJob) error {
	if len(source) == 0 || len(source) != len(restored) {
		return errors.New("cold restore requires existing schedules and exact count")
	}
	expected := map[string]*store.ScheduledJob{}
	for _, j := range source {
		if j == nil {
			return store.ErrBotInvalid
		}
		expected[j.ID] = j
	}
	for _, j := range restored {
		if j == nil || expected[j.ID] == nil || j.Enabled {
			return errors.New("restored schedule absent, foreign or active")
		}
		original := expected[j.ID]
		if j.Name != original.Name || j.OwnerID != original.OwnerID || !reflect.DeepEqual(j.Payload, original.Payload) || j.Schedule != original.Schedule {
			return errors.New("restored schedule identity changed")
		}
		delete(expected, j.ID)
	}
	if len(expected) != 0 {
		return errors.New("restored schedule inventory differs")
	}
	return nil
}

func validateRetainedHomeRestoredRoute(c context.Context, in RetainedHomeKubernetesInputs, cfg entstore.Config, name string) error {
	raw, err := os.ReadFile(filepath.Join(cfg.DataDir, ".filefirst-restore", "journal.json"))
	if err != nil {
		return err
	}
	var j struct {
		ID                string `json:"id"`
		ArchiveSHA256     string `json:"archive_sha256"`
		Phase             string `json:"phase"`
		PostgresDatabases []struct {
			Name       string `json:"name"`
			Instance   string `json:"instance"`
			Generation string `json:"generation"`
			OID        int64  `json:"oid"`
			Created    bool   `json:"created"`
			Ready      bool   `json:"ready"`
		} `json:"postgres_databases"`
	}
	if json.Unmarshal(raw, &j) != nil || j.Phase != "committed" {
		return store.ErrBotConflict
	}
	for _, d := range j.PostgresDatabases {
		if d.Name != name {
			continue
		}
		g, e := uuid.Parse(d.Generation)
		if e != nil || g.String() != d.Generation || d.Name != "astonish_restore_"+strings.ReplaceAll(d.Generation, "-", "") || d.Instance != cfg.InstanceSuffix || !d.Created || !d.Ready || j.ID == "" || len(j.ArchiveSHA256) != 64 {
			return store.ErrBotConflict
		}
		conn, e := pgx.Connect(c, in.DSN)
		if e != nil {
			return e
		}
		defer conn.Close(c)
		return retainedHomeCheckSQLReceipt(c, conn, retainedHomeSQLReceipt{name: name, oid: d.OID, comment: "astonish-filefirst:" + j.ID + ":" + j.ArchiveSHA256 + ":" + d.Instance + ":" + d.Generation})
	}
	return store.ErrBotConflict
}
