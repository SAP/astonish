package main

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

// Sanitized structural fixture from employee-offline-restart-tick.json in
// .tmp/astonish-chat-first-sm194wxq. Synthetic IDs; no private envelope metadata,
// instructions, credentials, provider output, or live state is copied. Preserve
// data -> bot_document JSON string -> capitalized Run and the queued state.
const employeeWrappedDiskFixture = `{"runs":[{"id":"11111111-1111-4111-8111-111111111111","bot_id":"22222222-2222-4222-8222-222222222222","status":"queued","data":"{\"bot_document\":\"{\\\"ExternalAdmissionValidated\\\":false,\\\"Run\\\":{\\\"id\\\":\\\"11111111-1111-4111-8111-111111111111\\\",\\\"bot_id\\\":\\\"22222222-2222-4222-8222-222222222222\\\",\\\"session_id\\\":\\\"33333333-3333-4333-8333-333333333333\\\",\\\"version\\\":2,\\\"trigger\\\":\\\"cron\\\",\\\"trigger_id\\\":\\\"44444444-4444-4444-8444-444444444444\\\",\\\"status\\\":\\\"queued\\\",\\\"created_at\\\":\\\"2026-11-09T02:27:33.414315409Z\\\"},\\\"OccurrenceAt\\\":\\\"2026-11-09T02:27:00Z\\\"}\"}"}]}`

func TestEmployeeDiskRunCapturedWrapper(t *testing.T) {
	var disk employeeDisk
	if err := json.Unmarshal([]byte(employeeWrappedDiskFixture), &disk); err != nil {
		t.Fatal(err)
	}
	if len(disk.Runs) != 1 {
		t.Fatal("missing fixture run")
	}
	r := employeeRun(disk.Runs[0])
	if r.ID.String() != disk.Runs[0].ID || r.BotID.String() != "22222222-2222-4222-8222-222222222222" || r.TriggerID.String() != "44444444-4444-4444-8444-444444444444" || r.SessionID.String() != "33333333-3333-4333-8333-333333333333" || r.Status != store.RunQueued || r.Trigger != store.TriggerCron || r.Version != 2 {
		t.Fatal("fixture identities or state changed")
	}
	restarted := time.Date(2026, 11, 9, 2, 27, 3, 0, time.UTC)
	if employeePostRestartRun(r, r.BotID, r.TriggerID, restarted) {
		t.Fatal("queued observation must not become an executed tick")
	}
}

func employeeDiskJSON(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func employeeDiskShapes(t *testing.T, r store.BotRun) map[string]string {
	t.Helper()
	flat := employeeDiskJSON(t, r)
	lower := employeeDiskJSON(t, map[string]any{"run": r})
	upper := employeeDiskJSON(t, map[string]any{"Run": r, "ExternalAdmissionValidated": false})
	return map[string]string{
		"flat": flat, "nested-lower": lower, "nested-upper": upper,
		"wrapped-flat":  employeeDiskJSON(t, map[string]string{"bot_document": flat}),
		"wrapped-lower": employeeDiskJSON(t, map[string]string{"bot_document": lower}),
		"wrapped-upper": employeeDiskJSON(t, map[string]string{"bot_document": upper}),
	}
}

func TestEmployeeDiskRunCanonicalShapesPreserveIdentity(t *testing.T) {
	restarted := time.Now().UTC().Truncate(time.Second)
	started := restarted.Add(time.Second)
	r := store.BotRun{ID: uuid.New(), BotID: uuid.New(), SessionID: uuid.New(), TriggerID: uuid.New(), Trigger: store.TriggerCron, Status: store.RunSucceeded, CreatedAt: started, StartedAt: &started, Version: 9007199254740993}
	for name, raw := range employeeDiskShapes(t, r) {
		t.Run(name, func(t *testing.T) {
			got := employeeRun(employeeDiskRow{ID: r.ID.String(), BotID: r.BotID.String(), Data: raw})
			if !reflect.DeepEqual(got, r) || !employeePostRestartRun(got, r.BotID, r.TriggerID, restarted) {
				t.Fatal("canonical run changed or matching execution rejected")
			}
			t.Run("wrong-row-bot", func(t *testing.T) {
				employeeDiskMustPanic(t, employeeDiskRow{ID: r.ID.String(), BotID: uuid.NewString(), Data: raw})
			})
			for _, field := range []string{"bot", "trigger"} {
				bad := r
				if field == "bot" {
					bad.BotID = uuid.New()
				} else {
					bad.TriggerID = uuid.New()
				}
				badRaw := employeeDiskShapes(t, bad)[name]
				decoded := employeeRun(employeeDiskRow{ID: r.ID.String(), Data: badRaw})
				if employeePostRestartRun(decoded, r.BotID, r.TriggerID, restarted) {
					t.Fatal("wrong persisted Bot/trigger accepted")
				}
			}
			if employeePostRestartRun(got, uuid.New(), r.TriggerID, restarted) || employeePostRestartRun(got, r.BotID, uuid.New(), restarted) {
				t.Fatal("wrong Bot or trigger accepted")
			}
		})
	}
}

func TestEmployeeDiskRunRejectsMalformedAmbiguousAndWrongIDs(t *testing.T) {
	r := store.BotRun{ID: uuid.New(), BotID: uuid.New(), TriggerID: uuid.New(), Trigger: store.TriggerCron, Status: store.RunSucceeded}
	flat := employeeDiskJSON(t, r)
	quotedFlat := employeeDiskJSON(t, flat)
	cases := map[string]string{
		"empty": "", "null": "null", "array": "[]", "scalar": `"run"`, "empty-object": `{}`,
		"trailing-document": flat + `{}`, "missing-run": `{"Run":null}`, "array-run": `{"Run":[]}`,
		"wrapper-null": `{"bot_document":null}`, "wrapper-object": `{"bot_document":` + flat + `}`,
		"wrapper-empty": `{"bot_document":""}`, "wrapper-invalid": `{"bot_document":"{"}`,
		"wrapper-nonobject": `{"bot_document":"[]"}`, "wrapper-json-null": `{"bot_document":"null"}`,
		"wrapper-mixed":              `{"bot_document":` + quotedFlat + `,"Run":` + flat + `}`,
		"wrapper-duplicate":          `{"bot_document":` + quotedFlat + `,"bot_document":` + quotedFlat + `}`,
		"wrapper-recursive":          employeeDiskJSON(t, map[string]string{"bot_document": `{"bot_document":` + quotedFlat + `}`}),
		"nested-mixed-flat":          strings.TrimSuffix(flat, "}") + `,"Run":` + flat + `}`,
		"nested-duplicate":           `{"Run":` + flat + `,"Run":` + flat + `}`,
		"nested-case-alias":          `{"Run":` + flat + `,"run":` + flat + `}`,
		"invalid-nested-no-fallback": strings.TrimSuffix(flat, "}") + `,"Run":null}`,
		"flat-duplicate-id":          strings.TrimSuffix(flat, "}") + `,"id":"` + r.ID.String() + `"}`,
		"flat-case-alias":            strings.TrimSuffix(flat, "}") + `,"ID":"` + r.ID.String() + `"}`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) { employeeDiskMustPanic(t, employeeDiskRow{ID: r.ID.String(), Data: raw}) })
	}
	for name, raw := range employeeDiskShapes(t, r) {
		t.Run(name+"/wrong-row", func(t *testing.T) { employeeDiskMustPanic(t, employeeDiskRow{ID: uuid.NewString(), Data: raw}) })
		t.Run(name+"/missing-row", func(t *testing.T) { employeeDiskMustPanic(t, employeeDiskRow{Data: raw}) })
	}
	for _, field := range []string{"id", "bot_id", "trigger_id"} {
		t.Run("invalid-"+field, func(t *testing.T) {
			var fields map[string]any
			if err := json.Unmarshal([]byte(flat), &fields); err != nil {
				t.Fatal(err)
			}
			fields[field] = "not-a-uuid"
			employeeDiskMustPanic(t, employeeDiskRow{ID: r.ID.String(), Data: employeeDiskJSON(t, map[string]string{"bot_document": employeeDiskJSON(t, map[string]any{"Run": fields})})})
		})
	}
	zero := r
	zero.ID = uuid.Nil
	employeeDiskMustPanic(t, employeeDiskRow{ID: uuid.Nil.String(), Data: employeeDiskJSON(t, zero)})
}

func TestEmployeeHomeVolumeWrappedStorage(t *testing.T) {
	bot := uuid.New()
	outer := employeeDiskJSON(t, store.BotHomeBinding{ID: uuid.New(), BotID: bot, Scope: store.BotScopePersonal, Backend: string(sandbox.BackendKindDocker), StorageKey: "owned-home-volume", Revision: 1})
	wrapped := employeeDiskJSON(t, map[string]string{"bot_document": outer})
	got, err := employeeHomeVolume(wrapped, bot.String())
	if err != nil || got != "owned-home-volume" {
		t.Fatalf("wrapped home: %q %v", got, err)
	}
	if _, err := employeeHomeVolume(wrapped, uuid.NewString()); err == nil {
		t.Fatal("wrong Bot Home accepted")
	}
	for _, raw := range []string{`{"bot_document":null}`, `{"bot_document":{}}`, `{"bot_document":"[]"}`, `{"bot_document":"{"}`, strings.TrimSuffix(wrapped, "}") + `,"bot_id":"` + bot.String() + `"}`, employeeDiskJSON(t, map[string]string{"bot_document": wrapped})} {
		if _, err := employeeHomeVolume(raw, bot.String()); err == nil {
			t.Fatal("malformed/ambiguous Home wrapper accepted")
		}
	}
}

func employeeDiskMustPanic(t *testing.T, row employeeDiskRow) {
	t.Helper()
	defer func() {
		if recover() == nil {
			t.Error("malformed, ambiguous, or mismatched disk run accepted")
		}
	}()
	employeeRun(row)
}
