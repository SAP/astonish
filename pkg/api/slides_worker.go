package api

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

const astonishRootEnv = "ASTONISH_ROOT"

func slidesWorkerPaths(scriptName string) (workingDir, scriptPath string, err error) {
	var roots []string
	if root := os.Getenv(astonishRootEnv); root != "" {
		roots = append(roots, root)
	}
	if cwd, cwdErr := os.Getwd(); cwdErr == nil {
		roots = append(roots, cwd)
	}
	if _, currentFile, _, ok := runtime.Caller(0); ok {
		roots = append(roots, filepath.Clean(filepath.Join(filepath.Dir(currentFile), "../..")))
	}

	seen := make(map[string]struct{}, len(roots))
	for _, root := range roots {
		root = filepath.Clean(root)
		if _, ok := seen[root]; ok {
			continue
		}
		seen[root] = struct{}{}
		workingDir = filepath.Join(root, "web")
		scriptPath = filepath.Join(root, "pkg", "docs", "slides", "pptxworker", scriptName)
		if directoryExists(workingDir) && fileExists(scriptPath) {
			return workingDir, scriptPath, nil
		}
	}
	return "", "", fmt.Errorf("slides worker %q is not installed; set %s to the Astonish runtime root", scriptName, astonishRootEnv)
}

func directoryExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
