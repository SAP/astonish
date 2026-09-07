package sandbox

import (
	"context"
	"encoding/base64"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/SAP/astonish/pkg/sandbox/tmplmeta"
	"github.com/SAP/astonish/pkg/store"
)

// MaterializeBootstrapFiles writes template bootstrap files into a sandbox
// via Backend.PushFile. Files are mounted only — never executed.
func MaterializeBootstrapFiles(ctx context.Context, backend Backend, sessionID string, files []store.BootstrapFile) error {
	if backend == nil || sessionID == "" || len(files) == 0 {
		return nil
	}
	for _, f := range files {
		if err := validateBootstrapFile(f); err != nil {
			return err
		}
		mode := bootstrapFileMode(f)
		if err := backend.PushFile(ctx, sessionID, f.Path, strings.NewReader(f.Content), mode); err != nil {
			return fmt.Errorf("bootstrap_files[%s]: %w", f.Path, err)
		}
		slog.Info("injected template bootstrap file", "component", "sandbox-bootstrap", "session_id", sessionID, "path", f.Path)
	}
	return nil
}

// MaterializeBootstrapFilesIncus writes bootstrap files via shell exec
// (used when PushFile is unavailable on the Incus fleet path).
func MaterializeBootstrapFilesIncus(ctx context.Context, execFn func(command []string, env map[string]string) (stdout, stderr []byte, exitCode int, err error), files []store.BootstrapFile) error {
	if execFn == nil || len(files) == 0 {
		return nil
	}
	_ = ctx
	for _, f := range files {
		if err := validateBootstrapFile(f); err != nil {
			return err
		}
		mode := bootstrapFileMode(f)
		dir := filepath.Dir(f.Path)
		encoded := base64.StdEncoding.EncodeToString([]byte(f.Content))
		script := fmt.Sprintf("mkdir -p %q && echo %q | base64 -d > %q && chmod %o %q", dir, encoded, f.Path, mode, f.Path)
		_, stderr, exitCode, err := execFn([]string{"bash", "-lc", script}, nil)
		if err != nil {
			return fmt.Errorf("bootstrap_files[%s]: %w", f.Path, err)
		}
		if exitCode != 0 {
			return fmt.Errorf("bootstrap_files[%s]: exit %d: %s", f.Path, exitCode, strings.TrimSpace(string(stderr)))
		}
		slog.Info("injected template bootstrap file (incus)", "component", "sandbox-bootstrap", "path", f.Path)
	}
	return nil
}

func validateBootstrapFile(f store.BootstrapFile) error {
	if strings.TrimSpace(f.Path) == "" {
		return fmt.Errorf("bootstrap_files: path is required")
	}
	if !filepath.IsAbs(f.Path) {
		return fmt.Errorf("bootstrap_files: path %q must be absolute", f.Path)
	}
	if strings.Contains(f.Path, "..") {
		return fmt.Errorf("bootstrap_files: path %q must not contain ..", f.Path)
	}
	if f.Content == "" {
		return fmt.Errorf("bootstrap_files[%s]: content is required", f.Path)
	}
	return nil
}

func bootstrapFileMode(f store.BootstrapFile) os.FileMode {
	mode := os.FileMode(0o755)
	if f.Mode != "" {
		var parsed os.FileMode
		if _, err := fmt.Sscanf(f.Mode, "%o", &parsed); err == nil {
			mode = parsed
		}
	}
	return mode
}

// InjectBootstrapFilesAfterSwitch materializes template bootstrap_files into the
// session container after ReplaceSession. Mount only — never auto-executes.
// No-ops when registry/pool/container is unavailable or the template has no files.
func InjectBootstrapFilesAfterSwitch(pool *NodeClientPool, registry *TemplateRegistry, sessionID, templateName string) {
	if pool == nil || sessionID == "" || templateName == "" {
		return
	}
	files := LookupBootstrapFiles(context.Background(), registry, nil, templateName)
	if len(files) == 0 {
		return
	}
	backend := pool.GetBackend()
	if backend == nil {
		slog.Warn("cannot inject bootstrap_files: no sandbox backend", "component", "sandbox-bootstrap", "template", templateName)
		return
	}
	err := MaterializeBootstrapFiles(context.Background(), backend, sessionID, files)
	if err != nil {
		slog.Warn("failed to inject template bootstrap_files", "component", "sandbox-bootstrap", "template", templateName, "error", err)
	}
}

// LookupBootstrapFiles resolves bootstrap files for a template slug.
// Prefers the Incus JSON template registry, then the platform SandboxTemplateStore.
func LookupBootstrapFiles(ctx context.Context, registry *TemplateRegistry, tplStore store.SandboxTemplateStore, templateSlug string) []store.BootstrapFile {
	templateSlug = strings.TrimSpace(strings.TrimPrefix(templateSlug, "@"))
	if templateSlug == "" || templateSlug == "base" {
		return nil
	}

	if registry != nil {
		_ = registry.Load()
		if meta := registry.Get(templateSlug); meta != nil && len(meta.BootstrapFiles) > 0 {
			return registryBootstrapToStore(meta.BootstrapFiles)
		}
	}

	if tplStore != nil {
		if files := lookupBootstrapInStore(ctx, tplStore, templateSlug); len(files) > 0 {
			return files
		}
	}
	return nil
}

func lookupBootstrapInStore(ctx context.Context, tplStore store.SandboxTemplateStore, slug string) []store.BootstrapFile {
	if strings.HasPrefix(slug, "team-") {
		owner := strings.TrimPrefix(slug, "team-")
		if tpl, err := tplStore.GetBySlug(ctx, store.SandboxTemplateScopeTeam, owner, slug); err == nil && tpl != nil {
			return tpl.BootstrapFiles
		}
	}

	list, err := tplStore.List(ctx, store.SandboxTemplateFilter{})
	if err != nil {
		return nil
	}
	for _, tpl := range list {
		if tpl == nil {
			continue
		}
		if tpl.Slug == slug || tpl.Name == slug {
			return tpl.BootstrapFiles
		}
	}
	return nil
}

func registryBootstrapToStore(files []tmplmeta.BootstrapFileMeta) []store.BootstrapFile {
	out := make([]store.BootstrapFile, 0, len(files))
	for _, f := range files {
		out = append(out, store.BootstrapFile{Path: f.Path, Content: f.Content, Mode: f.Mode})
	}
	return out
}

// StoreBootstrapToRegistry converts store files for TemplateMeta persistence.
func StoreBootstrapToRegistry(files []store.BootstrapFile) []tmplmeta.BootstrapFileMeta {
	out := make([]tmplmeta.BootstrapFileMeta, 0, len(files))
	for _, f := range files {
		out = append(out, tmplmeta.BootstrapFileMeta{Path: f.Path, Content: f.Content, Mode: f.Mode})
	}
	return out
}

var sandboxTemplateStoreForBootstrap store.SandboxTemplateStore

// SetSandboxTemplateStoreForBootstrap wires the platform template store so
// save_sandbox_template can persist bootstrap_files beyond the local registry.
func SetSandboxTemplateStoreForBootstrap(s store.SandboxTemplateStore) {
	sandboxTemplateStoreForBootstrap = s
}

// PersistBootstrapOnRegistry updates TemplateMeta.BootstrapFiles for name.
func PersistBootstrapOnRegistry(registry *TemplateRegistry, name string, files []store.BootstrapFile) error {
	if registry == nil || name == "" {
		return nil
	}
	if err := registry.Load(); err != nil && !os.IsNotExist(err) {
		// Keep going with in-memory state if disk reload fails.
	}
	meta := registry.Get(name)
	if meta == nil {
		return fmt.Errorf("template %q not found in registry", name)
	}
	meta.BootstrapFiles = StoreBootstrapToRegistry(files)
	return registry.Update(meta)
}

// PersistBootstrapFiles stores bootstrap files on the local template registry
// and, when available, on the matching SandboxTemplateStore row.
func PersistBootstrapFiles(registry *TemplateRegistry, name string, files []store.BootstrapFile) error {
	if err := PersistBootstrapOnRegistry(registry, name, files); err != nil {
		return err
	}
	return PersistBootstrapOnStore(context.Background(), sandboxTemplateStoreForBootstrap, name, files)
}

// PersistBootstrapOnStore updates BootstrapFiles on a store template matching slug/name.
func PersistBootstrapOnStore(ctx context.Context, tplStore store.SandboxTemplateStore, slug string, files []store.BootstrapFile) error {
	if tplStore == nil || slug == "" {
		return nil
	}
	list, err := tplStore.List(ctx, store.SandboxTemplateFilter{})
	if err != nil {
		return err
	}
	for _, tpl := range list {
		if tpl == nil {
			continue
		}
		if tpl.Slug == slug || tpl.Name == slug {
			tpl.BootstrapFiles = files
			return tplStore.Update(ctx, tpl)
		}
	}
	return nil
}
