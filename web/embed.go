package web

import (
	"embed"
	"io/fs"
	"sort"
)

//go:embed all:dist
var distFS embed.FS

// GetDistFS returns the embedded dist filesystem
// Supports versioned builds where output is dist/{version}/
func GetDistFS() fs.FS {
	return resolveDistFS()
}

// GetSandboxRuntime returns the pre-bundled sandbox runtime JS file
// (sandbox-runtime.js) from the embedded dist filesystem, or nil if not found.
func GetSandboxRuntime() []byte {
	distFS := resolveDistFS()
	if distFS == nil {
		return nil
	}
	data, err := fs.ReadFile(distFS, "sandbox-runtime.js")
	if err != nil {
		return nil
	}
	return data
}

// GetSlidesRuntime returns the pre-bundled Slides Web Component runtime JS file
// (slides-runtime.js) from the embedded dist filesystem, or nil if not found.
func GetSlidesRuntime() []byte {
	distFS := resolveDistFS()
	if distFS == nil {
		return nil
	}
	data, err := fs.ReadFile(distFS, "slides-runtime.js")
	if err != nil {
		return nil
	}
	return data
}

// GetTailwindBrowser returns the Tailwind CSS v4 browser runtime script
// (tailwind-browser.js) from the embedded dist filesystem, or nil if not found.
func GetTailwindBrowser() []byte {
	distFS := resolveDistFS()
	if distFS == nil {
		return nil
	}
	data, err := fs.ReadFile(distFS, "tailwind-browser.js")
	if err != nil {
		return nil
	}
	return data
}

func resolveDistFS() fs.FS {
	// First, check if dist exists
	entries, err := distFS.ReadDir("dist")
	if err != nil || len(entries) == 0 {
		return nil
	}

	// Collect directory entries (versioned subfolders like dist/1.5.0/)
	var dirs []string
	for _, e := range entries {
		if e.IsDir() {
			dirs = append(dirs, e.Name())
		}
	}

	// If there are versioned subdirectories, pick the latest (lexicographic sort works for semver)
	if len(dirs) > 0 {
		sort.Strings(dirs)
		versionDir := dirs[len(dirs)-1]
		subFS, err := fs.Sub(distFS, "dist/"+versionDir)
		if err != nil {
			return nil
		}
		return subFS
	}

	// Fallback: check for index.html directly in dist (legacy non-versioned build)
	if _, err := distFS.Open("dist/index.html"); err == nil {
		subFS, err := fs.Sub(distFS, "dist")
		if err != nil {
			return nil
		}
		return subFS
	}

	return nil
}
