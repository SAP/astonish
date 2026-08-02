// Command astonish-sandbox-entrypoint-script emits the canonical
// astonish-sandbox-entrypoint POSIX-shell script to stdout.
//
// The script is the PID-1 entrypoint for sandbox pods in the K8s+Sysbox
// backend. Its source of truth lives in pkg/sandbox/k8s.EntrypointScript;
// this command exists so image builders can generate the script
// deterministically without invoking the main astonish binary, which
// would pull in the full server + UI.
//
// Usage (host / Makefile — preferred; keeps Docker layers cacheable):
//
//	make sandbox-entrypoint
//	# writes ./astonish-sandbox-entrypoint for COPY into docker/sandbox-base
//
//	go run ./cmd/astonish-sandbox-entrypoint-script > astonish-sandbox-entrypoint
//
// The emitted script is parameterised ONLY by the defaults baked into
// EntrypointScriptOptions.applyDefaults; re-running with a different
// deployment layout requires changing those defaults (they are tied to
// the pod manifest produced by buildPodManifest, so a divergence here
// would break the runtime contract).
//
// See docs/architecture/sandbox-backends.md §5.3 step 3.
package main

import (
	"fmt"
	"io"
	"os"

	"github.com/SAP/astonish/pkg/sandbox/k8s"
)

func main() {
	if err := run(os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "astonish-sandbox-entrypoint-script: %v\n", err)
		os.Exit(1)
	}
}

func run(w io.Writer) error {
	script := k8s.EntrypointScript(k8s.EntrypointScriptOptions{})
	if _, err := io.WriteString(w, script); err != nil {
		return fmt.Errorf("write script: %w", err)
	}
	return nil
}
