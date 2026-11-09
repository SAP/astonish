package main

import (
	"fmt"
	"sort"
	"strings"

	"github.com/SAP/astonish/pkg/store"
)

// employeeFinalStoppedEvidence uses the same physical ownership predicate as the
// employee idle barrier, on the retained pre-delete snapshot (never deleted rows).
// The legacy terminal-evidence gate name means stopped execution, not all-success
// history. Interrupted remains resumable; no row or document is rewritten.
func employeeFinalStoppedEvidence(d employeeDisk, bot string) (string, error) {
	if !employeeExecutionIdle(d, bot) || len(d.Runs) == 0 {
		return "", fmt.Errorf("final stopped-run evidence requires same-Bot history and explicitly cleared active run and all leases")
	}
	counts := map[store.RunStatus]int{}
	for _, row := range d.Runs {
		// Retain the strict document/identity validation and its error behavior.
		// Indexed status is authoritative after fencing, not historical Run JSON.
		employeeRun(row)
		counts[store.RunStatus(row.Status)]++
	}
	statuses := make([]string, 0, len(counts))
	for status, count := range counts {
		statuses = append(statuses, fmt.Sprintf("%s=%d", status, count))
	}
	sort.Strings(statuses)
	return fmt.Sprintf("Pre-delete same-employee execution stopped: active_run_id and Bot/run leases explicitly NULL; %d recorded runs (%s). Interrupted/failed history is not success; operation receipts are separate from model prose", len(d.Runs), strings.Join(statuses, ", ")), nil
}
