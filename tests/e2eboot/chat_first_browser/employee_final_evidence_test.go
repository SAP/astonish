package main

import (
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

func employeeFinalFixture(t *testing.T) (employeeDisk, string) {
	t.Helper()
	d, bot := employeeIdleFixture(t, store.RunInterrupted)
	d.Bots[0].Status = string(store.BotActive)
	for i := 0; i < 13; i++ {
		r := d.Runs[0]
		r.ID = uuid.NewString()
		r.Status = string(store.RunSucceeded)
		d.Runs = append(d.Runs, r)
	}
	for i := range d.Runs {
		r := &d.Runs[i]
		run := store.BotRun{ID: uuid.MustParse(r.ID), BotID: uuid.MustParse(bot), Status: store.RunStatus(r.Status)}
		// Match the owned SQLite bot_document / capitalized Run envelope.
		inner := mustEmployeeJSON(t, map[string]any{"Run": run})
		r.Data = string(mustEmployeeJSON(t, map[string]string{"bot_document": string(inner)}))
	}
	return d, bot
}

func TestEmployeeFinalStoppedEvidence(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*employeeDisk)
		want   bool
	}{
		{"interrupted-cleared-and-thirteen-succeeded", func(*employeeDisk) {}, true},
		{"interrupted-live-active-run", func(d *employeeDisk) { d.Bots[0].ActiveRunID = json.RawMessage(`"still-tearing-down"`) }, false},
		{"interrupted-live-run-lease", func(d *employeeDisk) { d.Runs[0].LeaseExpiresAt = json.RawMessage(`"2099-01-01T00:00:00Z"`) }, false},
		{"bot-lease", func(d *employeeDisk) { d.Bots[0].LeaseExpiresAt = json.RawMessage(`"2099-01-01T00:00:00Z"`) }, false},
		{"expired-run-lease", func(d *employeeDisk) { d.Runs[0].LeaseExpiresAt = json.RawMessage(`"2020-01-01T00:00:00Z"`) }, false},
		{"running-sql-despite-success-document", func(d *employeeDisk) { d.Runs[1].Status = string(store.RunRunning) }, false},
		{"queued", func(d *employeeDisk) { d.Runs[1].Status = string(store.RunQueued) }, false},
		{"waiting-approval", func(d *employeeDisk) { d.Runs[1].Status = string(store.RunWaitingApproval) }, false},
		{"missing-active-run", func(d *employeeDisk) { d.Bots[0].ActiveRunID = nil }, false},
		{"missing-bot-lease", func(d *employeeDisk) { d.Bots[0].LeaseExpiresAt = nil }, false},
		{"missing-run-lease", func(d *employeeDisk) { d.Runs[1].LeaseExpiresAt = nil }, false},
		{"malformed-lease", func(d *employeeDisk) { d.Runs[1].LeaseExpiresAt = json.RawMessage(`{"bad":true}`) }, false},
		{"quoted-null-lease", func(d *employeeDisk) { d.Runs[1].LeaseExpiresAt = json.RawMessage(`"null"`) }, false},
		{"missing-status", func(d *employeeDisk) { d.Runs[1].Status = "" }, false},
		{"unknown-status", func(d *employeeDisk) { d.Runs[1].Status = "unknown" }, false},
		{"wrong-bot", func(d *employeeDisk) { d.Bots[0].ID = uuid.NewString() }, false},
		{"peer-run", func(d *employeeDisk) { d.Runs[1].BotID = uuid.NewString() }, false},
		{"deleted-rows-cannot-prove-stopped", func(d *employeeDisk) { *d = employeeDisk{} }, false},
		{"missing-run-history", func(d *employeeDisk) { d.Runs = nil }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, bot := employeeFinalFixture(t)
			tc.change(&d)
			before := string(mustEmployeeJSON(t, d))
			detail, err := employeeFinalStoppedEvidence(d, bot)
			if (err == nil) != tc.want {
				t.Fatalf("detail=%q err=%v want pass=%v", detail, err, tc.want)
			}
			if !tc.want && detail != "" {
				t.Fatal("failure produced receipt detail")
			}
			if tc.want && (!strings.Contains(detail, "interrupted=1") || !strings.Contains(detail, "succeeded=13")) {
				t.Fatalf("lost actual history: %s", detail)
			}
			if before != string(mustEmployeeJSON(t, d)) {
				t.Fatal("rewrote durable run history")
			}
		})
	}
	if store.RunInterrupted.Terminal() {
		t.Fatal("production interrupted must remain resumable")
	}
}

func TestEmployeeFinalStoppedEvidenceWiring(t *testing.T) {
	raw, err := os.ReadFile("newemployee.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	capture := strings.Index(source, `deletion := readOwned("delete-before", "read")`)
	confirm := strings.Index(source, `actions["hard-delete"] = true`)
	final := strings.Index(source, `stage("employee-terminal-evidence")`)
	if capture < 0 || confirm <= capture || final <= confirm {
		t.Fatal("missing ordered deletion checkpoints")
	}
	for _, region := range []string{source[capture:confirm], source[final:]} {
		if !strings.Contains(region, "employeeFinalStoppedEvidence(deletion, id)") {
			t.Fatal("gate must validate exact pre-delete capture")
		}
	}
	if strings.Contains(source[final:], "Status.Terminal()") || strings.Contains(source[final:], "readOwned(") {
		t.Fatal("final gate must not classify resumability or query deleted rows")
	}
	if !strings.Contains(source[final:], `receipt("terminal-evidence", detail, "employee-delete-before.json")`) {
		t.Fatal("receipt lost validated detail/source")
	}
}

func TestEmployeeFinalStoppedEvidenceMalformedDocumentStillFails(t *testing.T) {
	d, bot := employeeFinalFixture(t)
	d.Runs[0].Data = `{"bot_document":"malformed"}`
	defer func() {
		if recover() == nil {
			t.Fatal("malformed persisted document was hidden")
		}
	}()
	_, _ = employeeFinalStoppedEvidence(d, bot)
}

func TestEmployeeFinalStoppedEvidenceReportsIndexedStatuses(t *testing.T) {
	d, bot := employeeFinalFixture(t)
	for i, status := range []store.RunStatus{store.RunPartial, store.RunFailed, store.RunSkipped, store.RunCancelled} {
		d.Runs[i+1].Status = string(status)
	}
	// SQL fencing can leave historical Run JSON unchanged. Do not relabel SQL
	// interrupted as success, nor fail execution-idle because of historical JSON.
	var historical = map[string]any{"Run": store.BotRun{ID: uuid.MustParse(d.Runs[0].ID), BotID: uuid.MustParse(bot), Status: store.RunRunning}}
	d.Runs[0].Data = string(mustEmployeeJSON(t, historical))
	before := append([]employeeDiskRow(nil), d.Runs...)
	detail, err := employeeFinalStoppedEvidence(d, bot)
	if err != nil {
		t.Fatal(err)
	}
	for _, count := range []string{"interrupted=1", "succeeded=9", "partial=1", "failed=1", "skipped=1", "cancelled=1"} {
		if !strings.Contains(detail, count) {
			t.Fatalf("missing %s: %s", count, detail)
		}
	}
	if !reflect.DeepEqual(before, d.Runs) {
		t.Fatal("rewrote rows or historical documents")
	}
}
