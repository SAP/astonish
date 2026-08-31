package tools

import (
	"fmt"
	"runtime/debug"
	"time"

	"google.golang.org/adk/tool"
)

// browserToolTimeout is the maximum time any single browser tool call can take.
// It exceeds the Kubernetes sandbox readiness window plus browser startup so a
// browser-first turn waits for provisioning instead of timing out while the pod
// continues starting. Individual operations retain their shorter timeouts.
const browserToolTimeout = 4 * time.Minute

// safeBrowserFunc wraps a browser tool handler with panic recovery and a
// per-tool timeout. This prevents two critical issues:
//
//  1. Panic in a tool goroutine: The ADK dispatches parallel tool calls in
//     goroutines with sync.WaitGroup. If a goroutine panics, wg.Done() never
//     fires and wg.Wait() blocks forever, hanging the entire sub-agent.
//
//  2. Indefinite blocking: If a browser tool blocks forever (dead CDP connection,
//     unresponsive page), the only backstop is the 10-minute task timeout. The
//     per-tool timeout ensures individual operations fail fast.
//
// Usage: wrap the handler function before passing to functiontool.New:
//
//	functiontool.New(cfg, safeBrowserFunc(BrowserNavigate(mgr, guard)))
func safeBrowserFunc[TArgs, TResults any](fn func(tool.Context, TArgs) (TResults, error)) func(tool.Context, TArgs) (TResults, error) {
	return func(ctx tool.Context, args TArgs) (result TResults, err error) {
		type outcome struct {
			result TResults
			err    error
		}

		ch := make(chan outcome, 1)

		go func() {
			// Panic recovery: convert panics to errors so the goroutine
			// completes normally and wg.Done() executes in the ADK.
			defer func() {
				if r := recover(); r != nil {
					stack := string(debug.Stack())
					ch <- outcome{err: fmt.Errorf("browser tool panic: %v\n%s", r, stack)}
				}
			}()

			res, fnErr := fn(ctx, args)
			ch <- outcome{result: res, err: fnErr}
		}()

		select {
		case out := <-ch:
			return out.result, out.err
		case <-time.After(browserToolTimeout):
			var zero TResults
			return zero, fmt.Errorf("browser tool timed out after %s", browserToolTimeout)
		}
	}
}
