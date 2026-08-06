// Package embed carries the tree-sitter C sources needed to build the
// astonish tree-sitter shared library at runtime.
//
// The library (libastonish-treesitter.so) is a native shared object compiled
// from the tree-sitter runtime plus the Go/TypeScript/TSX/JavaScript/Python
// grammars. Container images build it at image-build time and install it under
// /usr/lib/astonish. Local code mode, however, runs in the user's own
// environment where that library does not exist, so the binary carries a
// compressed copy of the exact C sources and compiles the library on first use
// (see pkg/codeintel/native/builder.go).
//
// Regenerate the tarball with `make treesitter-embed` whenever the grammar or
// tree-sitter versions in pkg/codeintel/native/Makefile change.
package embed

import _ "embed"

// SourceTarball is a gzip-compressed tar archive of the tree-sitter runtime and
// grammar C sources, laid out so the compile manifest below resolves. Extracted
// under a temporary directory and fed to the C compiler at runtime.
//
//go:embed treesitter-src.tar.gz
var SourceTarball []byte

// CompileManifest describes the exact compiler invocation used to produce the
// shared library. It mirrors pkg/codeintel/native/Makefile — keep the two in
// sync. Paths are relative to the root of the extracted tarball.
type CompileManifest struct {
	// IncludeDirs are added as -I flags, in order.
	IncludeDirs []string
	// Sources are the .c files compiled into the shared object, in order.
	Sources []string
}

// Manifest returns the include directories and source files required to build
// libastonish-treesitter.so from the embedded tarball. It is a function (not a
// var) so callers cannot mutate the shared slices.
func Manifest() CompileManifest {
	return CompileManifest{
		IncludeDirs: []string{
			"tree-sitter/lib/include",
			"tree-sitter/lib/src",
			"tree-sitter-go/src",
			"tree-sitter-typescript/typescript/src",
			"tree-sitter-typescript/tsx/src",
			"tree-sitter-javascript/src",
			"tree-sitter-python/src",
		},
		Sources: []string{
			"tree-sitter/lib/src/lib.c",
			"tree-sitter-go/src/parser.c",
			"tree-sitter-typescript/typescript/src/parser.c",
			"tree-sitter-typescript/typescript/src/scanner.c",
			"tree-sitter-typescript/tsx/src/parser.c",
			"tree-sitter-typescript/tsx/src/scanner.c",
			"tree-sitter-javascript/src/parser.c",
			"tree-sitter-javascript/src/scanner.c",
			"tree-sitter-python/src/parser.c",
			"tree-sitter-python/src/scanner.c",
		},
	}
}

// Version identifies the embedded source set. Bump when the tarball is
// regenerated from new grammar versions; it namespaces the on-disk build cache
// so a binary upgrade rebuilds the library instead of loading a stale one.
const Version = "v0.25.10-g0.23.4-ts0.23.2-js0.23.1-py0.23.6"
