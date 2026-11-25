//go:build e2e

package e2eboot

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"testing"

	"github.com/SAP/astonish/pkg/backup"
	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/entstore"
	"github.com/google/uuid"
)

type retainedRestoreImporterFake struct {
	calls         []string
	blocked, fail bool
}

func (s *retainedRestoreImporterFake) PlanPlatformRestore(context.Context, string, entstore.PlatformRestoreOptions) (*backup.RestorePlan, error) {
	s.calls = append(s.calls, "plan")
	p := &backup.RestorePlan{TargetEmpty: true}
	if s.blocked {
		p.Blockers = []string{"blocked"}
	}
	return p, nil
}
func (s *retainedRestoreImporterFake) RestorePlatformBackup(context.Context, string, entstore.PlatformRestoreOptions) (*backup.RestoreResult, error) {
	s.calls = append(s.calls, "restore")
	if s.fail {
		return nil, errors.New("failure")
	}
	return &backup.RestoreResult{RestoredRecords: 1}, nil
}
func (s *retainedRestoreImporterFake) CleanupFileFirstRestore(context.Context, string, entstore.PlatformRestoreOptions) error {
	s.calls = append(s.calls, "cleanup")
	return nil
}
func TestRetainedHomePublicRestoreCallsPlanAndRestore(t *testing.T) {
	for _, blocked := range []bool{false, true} {
		s := &retainedRestoreImporterFake{blocked: blocked}
		err := retainedHomePublicRestore(context.Background(), s, "archive", entstore.PlatformRestoreOptions{})
		want := []string{"plan", "restore"}
		if blocked {
			want = []string{"plan"}
		}
		if (err != nil) != blocked || !reflect.DeepEqual(s.calls, want) {
			t.Fatalf("calls=%v err=%v", s.calls, err)
		}
	}
}

type retainedRestoreFilesFake struct {
	entstore.BotRestoreBackend
	target *sandbox.BotHomeBinding
	calls  []string
}

func (f *retainedRestoreFilesFake) CreateBotHomeRestoreTargetJournaled(c context.Context, s sandbox.BotHomeScope, reserve func(*sandbox.BotHomeBinding) error) (*sandbox.BotHomeBinding, error) {
	f.calls = append(f.calls, "reserve")
	if err := reserve(f.target); err != nil {
		return nil, err
	}
	f.calls = append(f.calls, "allocate")
	return f.target, nil
}
func (f *retainedRestoreFilesFake) DeleteBotHome(context.Context, *sandbox.BotHomeBinding) error {
	f.calls = append(f.calls, "delete")
	return nil
}
func TestRetainedHomeRestoreOwnershipCleanupOrderSourcePreserved(t *testing.T) {
	scope := sandbox.BotHomeScope{OrgID: "org", TeamID: "team", UserID: uuid.NewString(), BotID: uuid.NewString()}
	// Kubernetes uses a derived generation ref. Use production binding validation.
	source := &sandbox.BotHomeBinding{Scope: scope, Backend: sandbox.BackendKindK8s, Ref: scope.Key()}
	target := source.Clone()
	target.Ref = scope.Key() + "/generations/" + uuid.NewString()
	f := &retainedRestoreFilesFake{target: target}
	owner := &retainedHomeRestoreOwner{BotRestoreBackend: f, source: source, targets: map[string]*sandbox.BotHomeBinding{}}
	if source.Validate() != nil || target.Validate() != nil {
		t.Fatalf("invalid test bindings: %v %v", source.Validate(), target.Validate())
	}
	c := context.Background()
	_, err := owner.CreateBotHomeRestoreTargetJournaled(c, scope, func(*sandbox.BotHomeBinding) error { f.calls = append(f.calls, "journal"); return nil })
	if err != nil {
		t.Fatal(err)
	}
	if err = owner.DeleteBotHome(c, source); err != store.ErrBotForbidden {
		t.Fatal("source deletion authorized")
	}
	foreign := target.Clone()
	foreign.Ref = scope.Key() + "/generations/" + uuid.NewString()
	if err = owner.DeleteBotHome(c, foreign); err != store.ErrBotForbidden {
		t.Fatal("unowned deletion authorized")
	}
	if err = owner.DeleteBotHome(c, target); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(f.calls, []string{"reserve", "journal", "allocate", "delete"}) {
		t.Fatalf("cleanup/allocation order: %v", f.calls)
	}
	f.calls = nil
	_, err = owner.CreateBotHomeRestoreTargetJournaled(c, scope, func(*sandbox.BotHomeBinding) error { return errors.New("journal failed") })
	if err == nil || !reflect.DeepEqual(f.calls, []string{"reserve"}) {
		t.Fatal("allocated without durable reservation")
	}
}
func TestRetainedHomeRestoreCannotAdoptSource(t *testing.T) {
	scope := sandbox.BotHomeScope{OrgID: "org", TeamID: "team", UserID: uuid.NewString(), BotID: uuid.NewString()}
	source := &sandbox.BotHomeBinding{Scope: scope, Backend: sandbox.BackendKindK8s, Ref: scope.Key()}
	f := &retainedRestoreFilesFake{target: source}
	owner := &retainedHomeRestoreOwner{BotRestoreBackend: f, source: source, targets: map[string]*sandbox.BotHomeBinding{}}
	_, err := owner.CreateBotHomeRestoreTargetJournaled(context.Background(), scope, func(*sandbox.BotHomeBinding) error { t.Fatal("reserved source"); return nil })
	if err != store.ErrBotForbidden || len(owner.targets) != 0 {
		t.Fatal("adopted source")
	}
}
func TestRetainedHomePausedSchedulesRejectsFabricatedEvidence(t *testing.T) {
	source := &store.ScheduledJob{ID: uuid.NewString(), Name: "existing", OwnerID: uuid.NewString(), Enabled: true, Payload: store.JobPayload{BotID: uuid.NewString()}}
	restored := *source
	restored.Enabled = false
	if err := retainedHomePausedSchedules([]*store.ScheduledJob{source}, []*store.ScheduledJob{&restored}); err != nil {
		t.Fatal(err)
	}
	for _, pair := range [][2][]*store.ScheduledJob{{nil, nil}, {{source}, nil}, {{source}, {source}}} {
		if retainedHomePausedSchedules(pair[0], pair[1]) == nil {
			t.Fatal("absent or enabled schedules accepted")
		}
	}
	restored.Payload.BotID = uuid.NewString()
	if retainedHomePausedSchedules([]*store.ScheduledJob{source}, []*store.ScheduledJob{&restored}) == nil {
		t.Fatal("foreign schedule binding accepted")
	}
}
func TestRetainedHomeSQLReceiptRejectsRecreatedDatabase(t *testing.T) {
	r := retainedHomeSQLReceipt{name: "owned", oid: 42, comment: "receipt"}
	if validateRetainedHomeSQLIdentity(r, "owned", 42, "receipt", true) != nil {
		t.Fatal("valid receipt rejected")
	}
	for _, v := range []struct {
		name    string
		oid     int64
		comment string
		owned   bool
	}{{"source", 42, "receipt", true}, {"owned", 43, "receipt", true}, {"owned", 42, "foreign", true}, {"owned", 42, "receipt", false}} {
		if validateRetainedHomeSQLIdentity(r, v.name, v.oid, v.comment, v.owned) == nil {
			t.Fatal("changed database identity authorized")
		}
	}
}
func TestRetainedHomePublicRestoreFailureCleanupOnlyTarget(t *testing.T) {
	source := &retainedRestoreImporterFake{}
	target := &retainedRestoreImporterFake{fail: true}
	if retainedHomePublicRestore(context.Background(), target, "archive", entstore.PlatformRestoreOptions{}) == nil {
		t.Fatal("failure hidden")
	}
	if err := retainedHomeCleanupUncommitted(context.Background(), target, "archive", nil); err != nil {
		t.Fatal(err)
	}
	if len(source.calls) != 0 || !reflect.DeepEqual(target.calls, []string{"plan", "restore", "cleanup"}) {
		t.Fatalf("source=%v target=%v", source.calls, target.calls)
	}
}

func TestRetainedHomeColdRestoreHasNoPlanningStub(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "sandbox_k8s_home.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	public := false
	for _, d := range f.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "ColdBackupRestore" {
			continue
		}
		found = true
		for _, s := range fn.Body.List {
			if e, ok := s.(*ast.ExprStmt); ok {
				if c, ok := e.X.(*ast.CallExpr); ok {
					if sel, ok := c.Fun.(*ast.SelectorExpr); ok && sel.Sel.Name == "Fatal" {
						t.Fatal("unconditional Fatal stub remains")
					}
				}
			}
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			if c, ok := n.(*ast.CallExpr); ok {
				if id, ok := c.Fun.(*ast.Ident); ok && id.Name == "retainedHomePublicRestore" {
					public = true
				}
			}
			return true
		})
	}
	if !found || !public {
		t.Fatal("cold acceptance does not execute public importer")
	}
}
