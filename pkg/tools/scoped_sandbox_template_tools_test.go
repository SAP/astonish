package tools

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
	"google.golang.org/adk/tool"
	"google.golang.org/adk/tool/toolconfirmation"
)

// Embedded interfaces deliberately panic for any unexpected host/backend call.
// Only the real template handler's expected scoped operations are implemented.
type templateScopeContext struct {
	tool.Context
	base           context.Context
	session, owner string
}

func (c templateScopeContext) ToolConfirmation() *toolconfirmation.ToolConfirmation { return nil }

func (c templateScopeContext) Deadline() (time.Time, bool) { return c.base.Deadline() }
func (c templateScopeContext) Done() <-chan struct{}       { return c.base.Done() }
func (c templateScopeContext) Err() error                  { return c.base.Err() }
func (c templateScopeContext) Value(k any) any             { return c.base.Value(k) }
func (c templateScopeContext) SessionID() string           { return c.session }
func (c templateScopeContext) UserID() string              { return c.owner }

type templateScopeBackend struct {
	sandbox.Backend
	t               *testing.T
	session, owner  string
	saves, destroys int
	destroyErr      error
}

func (b *templateScopeBackend) check(ctx context.Context, sid string) {
	b.t.Helper()
	if sid != b.session || store.UserIDFromContext(ctx) != b.owner {
		b.t.Errorf("retargeted backend: session=%q owner=%q, want %q %q", sid, store.UserIDFromContext(ctx), b.session, b.owner)
	}
	if _, ok := ctx.Deadline(); !ok {
		b.t.Error("backend call has no deadline")
	}
}
func (b *templateScopeBackend) SaveSessionAsTemplate(ctx context.Context, sid string) (*sandbox.TemplateArtifact, error) {
	b.check(ctx, sid)
	b.saves++
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &sandbox.TemplateArtifact{LayerID: "layer-" + b.owner, SizeBytes: 42}, nil
}
func (b *templateScopeBackend) DestroySession(ctx context.Context, sid string) error {
	b.check(ctx, sid)
	b.destroys++
	if err := ctx.Err(); err != nil {
		return err
	}
	return b.destroyErr
}

type templateScopePool struct {
	sandbox.ToolNodePool
	t                                       *testing.T
	session, org, team                      string
	removed, scoped, selected, bound, ready int
}

func (p *templateScopePool) check(sid string) {
	p.t.Helper()
	if sid != p.session {
		p.t.Errorf("retargeted pool session=%q want %q", sid, p.session)
	}
}
func (p *templateScopePool) Remove(sid string) { p.check(sid); p.removed++ }
func (p *templateScopePool) SetSessionScope(sid, org, team string) {
	p.check(sid)
	if org != p.org || team != p.team {
		p.t.Errorf("pool scope=%q/%q want %q/%q", org, team, p.org, p.team)
	}
	p.scoped++
}
func (p *templateScopePool) GetOrCreateWithTemplate(sid, name string) sandbox.ToolNodeClient {
	p.check(sid)
	if name != "project" {
		p.t.Errorf("template=%q", name)
	}
	if p.scoped != p.removed {
		p.t.Error("scope not set before provisioning")
	}
	p.selected++
	return &templateScopeClient{pool: p}
}

type templateScopeClient struct {
	sandbox.ToolNodeClient
	pool *templateScopePool
}

func (c *templateScopeClient) BindSession(sid string) { c.pool.check(sid); c.pool.bound++ }
func (c *templateScopeClient) EnsureReady(sid string) error {
	c.pool.check(sid)
	c.pool.ready++
	return nil
}

type templateScopeFixture struct {
	ctx       templateScopeContext
	backend   *templateScopeBackend
	pool      *templateScopePool
	registry  *sandbox.TemplateRegistry
	save, use tool.Tool
}

func newTemplateScopeFixture(t *testing.T, owner string) *templateScopeFixture {
	t.Helper()
	base := store.WithUserID(context.Background(), owner)
	base = store.WithOrgSlug(base, "org-"+owner)
	base = store.WithTeamSlug(base, "team-"+owner)
	f := &templateScopeFixture{
		ctx:      templateScopeContext{base: base, session: "session-" + owner, owner: owner},
		backend:  &templateScopeBackend{t: t, session: "session-" + owner, owner: owner},
		pool:     &templateScopePool{t: t, session: "session-" + owner, org: "org-" + owner, team: "team-" + owner},
		registry: sandbox.NewInMemoryRegistry(t.TempDir()),
	}
	var err error
	f.save, err = NewSaveSandboxTemplateToolFromBackend(f.backend, f.registry, nil)
	if err != nil {
		t.Fatal(err)
	}
	f.use, err = NewUseSandboxTemplateToolFromBackend(f.pool, f.backend, f.registry)
	if err != nil {
		t.Fatal(err)
	}
	return f
}
func runTemplateScopeTool(t *testing.T, executable tool.Tool, ctx tool.Context, args map[string]any, status string) {
	t.Helper()
	runnable, ok := executable.(interface {
		Run(tool.Context, any) (map[string]any, error)
	})
	if !ok {
		t.Fatalf("%s is not runnable", executable.Name())
	}
	got, err := runnable.Run(ctx, args)
	if err != nil || got["status"] != status {
		t.Errorf("%s result=%v error=%v want %q", executable.Name(), got, err, status)
	}
}

func TestSandboxTemplateToolsConcurrentFactoryIsolation(t *testing.T) {
	// Ordinary factory and two scoped catalogs use these same public constructors.
	// Constructing subsequent tools must never change the first tool's dependencies.
	fixtures := []*templateScopeFixture{newTemplateScopeFixture(t, "ordinary"), newTemplateScopeFixture(t, "alice"), newTemplateScopeFixture(t, "bob")}
	var wg sync.WaitGroup
	for _, f := range fixtures {
		wg.Go(func() {
			for i := 0; i < 8; i++ {
				// Concurrent catalog construction used to overwrite package-global deps.
				if _, err := NewSaveSandboxTemplateToolFromBackend(nil, nil, nil); err != nil {
					t.Error(err)
				}
				if _, err := NewUseSandboxTemplateToolFromBackend(nil, nil, nil); err != nil {
					t.Error(err)
				}
				runTemplateScopeTool(t, f.save, f.ctx, map[string]any{"template_name": "project", "description": f.ctx.owner, "overwrite": true}, "saved")
				runTemplateScopeTool(t, f.use, f.ctx, map[string]any{"template_name": "project"}, "ok")
			}
		})
	}
	wg.Wait()
	for _, f := range fixtures {
		if f.backend.saves != 8 || f.backend.destroys != 8 || f.pool.selected != 8 || f.pool.bound != 8 || f.pool.ready != 8 {
			t.Errorf("%s operations: backend=%+v pool=%+v", f.ctx.owner, f.backend, f.pool)
		}
		meta := f.registry.Get("project")
		if meta == nil || meta.Description != f.ctx.owner {
			t.Errorf("%s registry retargeted: %+v", f.ctx.owner, meta)
		}
	}
}

func TestSandboxTemplateToolsMissingDependenciesFailClosed(t *testing.T) {
	f := newTemplateScopeFixture(t, "configured")
	constructors := []struct {
		name   string
		create func() (tool.Tool, error)
	}{
		{"save/backend", func() (tool.Tool, error) { return NewSaveSandboxTemplateToolFromBackend(nil, f.registry, nil) }},
		{"save/registry", func() (tool.Tool, error) { return NewSaveSandboxTemplateToolFromBackend(f.backend, nil, nil) }},
		{"use/backend", func() (tool.Tool, error) { return NewUseSandboxTemplateToolFromBackend(f.pool, nil, f.registry) }},
		{"use/pool", func() (tool.Tool, error) { return NewUseSandboxTemplateToolFromBackend(nil, f.backend, f.registry) }},
		{"use/registry", func() (tool.Tool, error) { return NewUseSandboxTemplateToolFromBackend(f.pool, f.backend, nil) }},
		{"legacy/save", func() (tool.Tool, error) { return NewSaveSandboxTemplateTool(nil, f.registry, nil) }},
		{"legacy/use", func() (tool.Tool, error) { return NewUseSandboxTemplateTool(nil, f.registry) }},
	}
	for _, tc := range constructors {
		t.Run(tc.name, func(t *testing.T) {
			executable, err := tc.create()
			if err != nil {
				t.Fatal(err)
			}
			runTemplateScopeTool(t, executable, f.ctx, map[string]any{"template_name": "project"}, "error")
		})
	}
	if f.backend.saves != 0 || f.backend.destroys != 0 || f.pool.selected != 0 {
		t.Fatal("missing dependency reached configured backend/pool")
	}
	// The ordinary configured tools still execute after constructing unavailable tools.
	runTemplateScopeTool(t, f.save, f.ctx, map[string]any{"template_name": "project"}, "saved")
	runTemplateScopeTool(t, f.use, f.ctx, map[string]any{"template_name": "project"}, "ok")
}

func TestSandboxTemplateToolsPreserveCancellationAndStopOnDestroyFailure(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		t.Run(fmt.Sprint(cancelled), func(t *testing.T) {
			f := newTemplateScopeFixture(t, "owner")
			if err := f.registry.Add(&sandbox.TemplateMeta{Name: "project"}); err != nil {
				t.Fatal(err)
			}
			if cancelled {
				ctx, cancel := context.WithCancel(f.ctx.base)
				cancel()
				f.ctx.base = ctx
				runTemplateScopeTool(t, f.save, f.ctx, map[string]any{"template_name": "project"}, "error")
			} else {
				f.backend.destroyErr = errors.New("destroy denied")
			}
			runTemplateScopeTool(t, f.use, f.ctx, map[string]any{"template_name": "project"}, "error")
			if f.pool.removed != 0 || f.pool.selected != 0 {
				t.Fatal("failed destroy must not remove or provision a client")
			}
		})
	}
}
