// Package tmplmeta holds filesystem-persisted metadata shared between the
// top-level sandbox package and backend-specific sub-packages (e.g. incus).
//
// It is a leaf package: it must not import any other astonish sandbox
// package. This breaks what would otherwise be an import cycle between
// pkg/sandbox and backend implementations, which need access to the
// TemplateRegistry.
package tmplmeta
