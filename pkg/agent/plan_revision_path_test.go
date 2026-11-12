package agent

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestCanonicalPlanPath(t *testing.T) {
	for _, tt := range []struct{ name, workspace, path, want string }{
		{"workspace absolute", "/workspace", "/workspace/pkg/../a.go", "a.go"},
		{"workspace relative", "/workspace", "pkg/../a.go", "a.go"},
		{"external absolute", "/workspace", "/outside/sub/../hello.txt", "/outside/hello.txt"},
		{"external relative alias", "/workspace", "../outside/hello.txt", "/outside/hello.txt"},
		{"prefix is not containment", "/workspace", "/workspace-other/hello.txt", "/workspace-other/hello.txt"},
		{"external unknown workspace", "", "/outside/./hello.txt", "/outside/hello.txt"},
		{"external relative workspace", "workspace", "/outside/hello.txt", "/outside/hello.txt"},
		{"relative unknown workspace", "", "../outside/hello.txt", "../outside/hello.txt"},
		{"unknown workspace no false alias", "", "outside/hello.txt", "outside/hello.txt"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := CanonicalPlanPath(tt.workspace, tt.path)
			if err != nil || got != tt.want {
				t.Fatalf("got %q, %v; want %q", got, err, tt.want)
			}
			again, err := CanonicalPlanPath(tt.workspace, got)
			if err != nil || again != got {
				t.Fatalf("not idempotent: %q %v", again, err)
			}
		})
	}
	for _, path := range []string{"", " \t", "a\x00b", "a\nb", "a\rb", ".", "..", "/", "/workspace", "/workspace/.", "a/.."} {
		t.Run("invalid "+path, func(t *testing.T) {
			if _, err := CanonicalPlanPath("/workspace", path); err == nil {
				t.Fatalf("accepted invalid file path %q", path)
			}
		})
	}
}

func TestPlanRevisionExternalPathClassification(t *testing.T) {
	for _, tt := range []struct {
		name string
		edit func(*PlanRevisionRequest)
		want PlanRevisionClassification
	}{
		{"Hello to Hello World details", func(r *PlanRevisionRequest) { r.Steps[0].Details = "Write Hello World without a newline" }, PlanRevisionImmediate},
		{"Hello World verify", func(r *PlanRevisionRequest) { r.Steps[0].Verify = "test Hello = 'Hello World'" }, PlanRevisionImmediate},
		{"outcome", func(r *PlanRevisionRequest) { r.Steps[0].Outcome = "Hello World without a newline" }, PlanRevisionImmediate},
		{"absolute lexical alias", func(r *PlanRevisionRequest) { r.Steps[0].Files[0].Path = "/outside/sub/../hello.txt" }, PlanRevisionNoop},
		{"relative lexical alias", func(r *PlanRevisionRequest) { r.Steps[0].Files[0].Path = "../outside/hello.txt" }, PlanRevisionNoop},
		{"new external target", func(r *PlanRevisionRequest) {
			r.Steps[0].Files = append(r.Steps[0].Files, PlanFileChange{Path: "/elsewhere/new.txt", Kind: "new"})
		}, PlanRevisionImmediate},
		{"changed external target", func(r *PlanRevisionRequest) { r.Steps[0].Files[0].Path = "../elsewhere/new.txt" }, PlanRevisionImmediate},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := revisionFixture()
			s.Steps[0].Files = []PlanFileChange{{Path: "/outside/hello.txt", Kind: "new"}}
			s.Steps[0].Details = "Write Hello without a newline"
			before := s.Clone()
			req := revisionRequest(s)
			tt.edit(&req)
			got, err := BuildPlanRevisionCandidate(s, req, "/workspace", "")
			if err != nil || got.Classification != tt.want {
				t.Fatalf("got %s, %v; want %s", got.Classification, err, tt.want)
			}
			if !reflect.DeepEqual(s, before) {
				t.Fatal("classification mutated accepted definition")
			}
		})
	}
}

func TestPlanRevisionExternalPathConflicts(t *testing.T) {
	for _, tt := range []struct {
		name, want string
		parallel   bool
	}{
		{"duplicate aliases", "duplicate file path", false},
		{"parallel aliases", "parallel phases share file", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s := revisionFixture()
			s.Steps[0].Files = []PlanFileChange{{Path: "/outside/hello.txt", Kind: "new"}}
			req := revisionRequest(s)
			if tt.parallel {
				req.Steps[0].ParallelGroup, req.Steps[1].ParallelGroup = "wave", "wave"
				req.Steps[1].Files = []PlanFileChange{{Path: "../outside/sub/../hello.txt", Kind: "new"}}
			} else {
				req.Steps[0].Files = append(req.Steps[0].Files, PlanFileChange{Path: "../outside/hello.txt", Kind: "new"})
			}
			_, err := BuildPlanRevisionCandidate(s, req, "/workspace", "")
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("want %q, got %v", tt.want, err)
			}
		})
	}
}

func TestPlanRevisionExternalPathImmediateRestore(t *testing.T) {
	for _, changeTarget := range []bool{false, true} {
		t.Run(map[bool]string{false: "Hello World outcome", true: "new external target"}[changeTarget], func(t *testing.T) {
			parent := t.TempDir()
			workspace := filepath.Join(parent, "workspace")
			outside := filepath.Join(parent, "outside")
			for _, dir := range []string{workspace, outside} {
				if err := os.Mkdir(dir, 0700); err != nil {
					t.Fatal(err)
				}
			}
			target := filepath.Join(outside, "hello.txt")
			extra := filepath.Join(outside, "extra.txt")
			planPath := filepath.Join(workspace, "PLAN.md")
			s := runtimeFixture()
			s.Steps[0].Files = []PlanFileChange{{Path: target, Kind: "new"}}
			s.Steps[0].Outcome = "Hello without a newline"
			calls := 0
			options := PlanRuntimeOptions{Workspace: workspace, CommitSnapshot: AtomicPlanSnapshotFile(planPath), Verify: func(string) (int, string, error) { calls++; return 0, "", nil }}
			r := NewPlanRuntimeFromSnapshot(s, options)
			req := revisionRequest(s)
			req.Steps[0].Details = "Write Hello World without a newline"
			if changeTarget {
				req.Steps[0].Files = append(req.Steps[0].Files, PlanFileChange{Path: extra, Kind: "new"})
			} else {
				req.Steps[0].Outcome = "Hello World without a newline"
			}
			got, err := r.Revise(req)
			if err != nil {
				t.Fatal(err)
			}
			if got.Kind != PlanRevisionResultApplied || got.Snapshot.PendingRevision != nil || got.Snapshot.Revision != s.Revision+1 {
				t.Fatalf("scope revision not applied: %+v", got)
			}
			restore := func() *PlanRuntime {
				raw, err := os.ReadFile(planPath)
				if err != nil {
					t.Fatal(err)
				}
				doc, goal, steps, err := ParsePlanDocument(string(raw))
				if err != nil {
					t.Fatal(err)
				}
				return NewPlanRuntime(NewPlanState(goal, doc, steps), options)
			}
			restored := restore().Snapshot()
			if restored.Revision != s.Revision+1 || restored.PendingRevision != nil || !reflect.DeepEqual(restored.Definition(), got.Snapshot.Definition()) || restored.Steps[0].Details != req.Steps[0].Details || restored.Steps[0].Outcome != req.Steps[0].Outcome {
				t.Fatalf("accepted definition lost: %+v", restored)
			}
			wantFiles := map[string]bool{target: true}
			if changeTarget {
				wantFiles[extra] = true
			}
			if len(restored.Steps[0].Files) != len(wantFiles) {
				t.Fatal("restoration changed external target count")
			}
			for _, file := range restored.Steps[0].Files {
				if !wantFiles[file.Path] || file.Kind != "new" {
					t.Fatalf("restoration changed external target: %+v", file)
				}
			}
			if calls != 0 {
				t.Fatal("revision executed verification")
			}
			for _, path := range []string{target, extra} {
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatalf("revision created target %s: %v", path, err)
				}
			}
		})
	}
}
