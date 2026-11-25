package entstore

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

func botContentRunFixture() botStoredRun {
	now := time.Date(2026, 11, 17, 20, 0, 0, 123, time.UTC)
	id, runID, owner, session := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	high := int64(1<<60 + 37)
	v := botStoredRun{RequestHash: strings.Repeat("a", 64), CommitHash: strings.Repeat("b", 64), Instructions: "SENTINEL-title-instructions",
		Attachments:  []store.ChatFile{{Filename: "SENTINEL-filename", MimeType: "text/plain", Data: []byte("SENTINEL-attachment")}},
		OccurrenceAt: now, ExternalAdmissionValidated: true, SkillAdmissionOwner: &owner,
		ContinuationRequests:      map[string]string{"request": strings.Repeat("c", 64)},
		ProgressOwnerContinuation: &botProgressOwnerContinuation{BotRevision: high, CheckpointRevision: high},
		CoalescedOccurrences:      high, ProgressCursor: high, ProgressPending: high, ProgressUncertain: true, ProgressAccountedAt: now,
		ProgressQuota: &mandateRunUsage{Models: map[string]mandateModelCharge{"model-call": {Tokens: high, Settled: true}}, Tokens: high},
		Run: store.BotRun{ID: runID, BotID: id, Version: high, SessionID: session, StateSequence: high, StateCommittedAt: now,
			UserEventID: "event", HistoryCursor: high, RuntimeAccountingRecorded: true, ExecutionMode: execution.BotExecutionMode("interactive"),
			Status: store.RunRunning, Trigger: store.TriggerManual, CreatedAt: now, StartedAt: &now, Provider: "provider", Model: "model",
			Lease:      store.BotLease{BotID: id, RunID: runID, WorkerID: "worker", Fence: high, ExpiresAt: now.Add(time.Hour)},
			Usage:      store.BotUsage{RuntimeNanos: high, InputTokens: high, OutputTokens: high, ToolCalls: high, CostMicros: &high},
			Invocation: "SENTINEL-invocation", Checkpoint: json.RawMessage("{ \"body\": \"SENTINEL-checkpoint\", \"large\": 1152921504606847013 }"),
			Todos: []store.BotTodo{{ID: uuid.New(), Outcome: "SENTINEL-todo", UnblocksWhen: "SENTINEL-blocker"}},
			Report: store.BotReport{Summary: "SENTINEL-report", Unverified: []string{"SENTINEL-unverified"},
				Artifacts: []store.BotArtifact{{ID: uuid.New(), Title: "SENTINEL-artifact-title", RelativePath: "SENTINEL-path"}},
				Failure:   &store.BotReportFailure{TechnicalDetails: "SENTINEL-failure"}},
			Progress:      &store.BotProgressProjection{Summary: "SENTINEL-projection"},
			CallerCeiling: &execution.BotCallerCeiling{ExpiresAt: now, Principal: execution.Principal{Subject: owner.String(), Roles: []string{"owner"}, Scopes: []string{"chat"}}},
			MandateRef:    &store.BotMandateRef{ID: uuid.New(), BotID: id, OrgSlug: "org", TeamSlug: "team", OwnerID: owner.String(), Revision: high}},
	}
	v.Run.TitleConfirmation = &store.BotTitleConfirmation{Version: store.BotTitleConfirmationVersion, State: store.BotTitleConfirmationNeedsQuestion,
		BotID: id, RunID: runID, OwnerID: owner, OriginalTitle: v.Instructions, UserEventID: v.Run.UserEventID}
	v.Work = store.NewBotWorkState(store.BotWorkContract{BotID: id, RunID: runID, Version: high, Instructions: "SENTINEL-work", Charter: "SENTINEL-charter",
		Attachments: []store.BotWorkAttachment{{SourceID: "source", Text: "SENTINEL-source-text"}}})
	v.Work.Candidate = &store.BotWorkCandidate{Text: "SENTINEL-candidate"}
	v.Task = &store.BotTaskRevision{ID: uuid.New(), BotID: id, Ref: store.BotTaskRef{TaskID: uuid.New(), Revision: high, Digest: strings.Repeat("d", 64)},
		Manifest: store.BotTaskManifest{Title: "SENTINEL-task-title", Assignment: "SENTINEL-task-assignment", Parameters: map[string]json.RawMessage{"parameter": json.RawMessage(`{ "text": "SENTINEL-task-param", "large": 1152921504606847013 }`)}},
		Bundle:   map[string][]byte{"task.md": []byte("SENTINEL-bundle")}}
	v.Run.Task = v.Task.Ref.Clone()
	v.Progress = &store.BotProgressState{SchemaVersion: 1, Phase: store.BotProgressPhase("running"), Checkpoint: &store.BotProgressCheckpoint{
		Kind: store.BotProgressCheckpointV1, SchemaVersion: 1, Binding: store.BotProgressBinding{BotID: id, RunID: runID, SessionID: session, Version: high},
		Revision: high, EventCursor: high, TranscriptHighWater: high, DispatchLedgerCursor: high, ContextRef: "SENTINEL-private-context-ref", ContextDigest: strings.Repeat("e", 64),
		LastEvaluation: &store.BotProgressEvaluation{At: now, Summary: "SENTINEL-evaluation"}, NextEligibleAt: now, Usage: v.Run.Usage}}
	v.SkillRequests = map[string]botSkillReplay{"skill-call": {Hash: strings.Repeat("f", 64), Skill: &store.BotSkill{Title: "SENTINEL-skill-replay"}, Work: v.Work.Clone()}}
	return v
}

func runRecordEnvelope(t *testing.T, r botRunRecord) map[string]any {
	t.Helper()
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	return map[string]any{"bot_record_v2": string(raw)}
}

func TestBotContentRunCodec(t *testing.T) {
	dir := t.TempDir()
	scope := store.BotContentScope{OrgID: "org", TeamID: "team", OwnerID: uuid.NewString()}
	c := newBotContentCodec(dir, scope)
	v := botContentRunFixture()
	data, err := c.encodeRun(t.Context(), v)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	// Exercise the actual map JSON boundary used by Ent, rather than passing a
	// typed value directly: counters above 2^53 must not go through float64.
	var persisted map[string]any
	if err := json.Unmarshal(raw, &persisted); err != nil {
		t.Fatal(err)
	}
	got, err := newBotContentCodec(dir, scope).decodeRun(t.Context(), v.Run.BotID, persisted)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(v, *got) {
		t.Fatalf("roundtrip differs\nwant: %#v\ngot: %#v", v, *got)
	}
	originalHash, err := botHash(v)
	if err != nil {
		t.Fatal(err)
	}
	restoredHash, err := botHash(*got)
	if err != nil || originalHash != restoredHash {
		t.Fatal("logical hash changed", err)
	}
	if got.RequestHash != v.RequestHash || got.CommitHash != v.CommitHash {
		t.Fatal("idempotency hashes changed")
	}
	var r botRunRecord
	if err := decodeBotRecord(data, &r); err != nil {
		t.Fatal(err)
	}
	if r.LogicalDigest != originalHash {
		t.Fatal("record envelope hashed instead of original logical value")
	}
}

func TestBotContentRunEmpty(t *testing.T) {
	c := newBotContentCodec(t.TempDir(), store.BotContentScope{OwnerID: uuid.NewString()})
	for _, empty := range []bool{false, true} {
		v := botStoredRun{Run: store.BotRun{ID: uuid.New(), BotID: uuid.New(), Version: 1}}
		if empty {
			v.SkillRequests = map[string]botSkillReplay{}
			v.ContinuationRequests = map[string]string{}
		}
		data, err := c.encodeRun(t.Context(), v)
		if err != nil {
			t.Fatal(err)
		}
		got, err := c.decodeRun(t.Context(), v.Run.BotID, data)
		if err != nil || !reflect.DeepEqual(v, *got) {
			t.Fatalf("empty roundtrip: %v", err)
		}
	}
}

func TestBotContentRunRejectsMetadata(t *testing.T) {
	c := newBotContentCodec(t.TempDir(), store.BotContentScope{OrgID: "org", TeamID: "team", OwnerID: uuid.NewString()})
	v := botContentRunFixture()
	data, err := c.encodeRun(t.Context(), v)
	if err != nil {
		t.Fatal(err)
	}
	var original botRunRecord
	if err := decodeBotRecord(data, &original); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*botRunRecord){
		"bot":        func(r *botRunRecord) { r.Control.BotID = uuid.New() },
		"run":        func(r *botRunRecord) { r.Control.ID = uuid.New() },
		"version":    func(r *botRunRecord) { r.Control.Version++ },
		"digest":     func(r *botRunRecord) { r.LogicalDigest = strings.Repeat("0", 64) },
		"size":       func(r *botRunRecord) { r.SizeBytes++ },
		"bodydigest": func(r *botRunRecord) { r.SHA256 = strings.Repeat("0", 64) },
		"execution":  func(r *botRunRecord) { r.Control.ExecutionMode = "changed" },
		"lease":      func(r *botRunRecord) { r.Control.Lease.Fence++ },
		"admission":  func(r *botRunRecord) { r.Control.ExternalAdmissionValidated = false },
	} {
		t.Run(name, func(t *testing.T) {
			r := original
			mutate(&r)
			if _, err := c.decodeRun(t.Context(), v.Run.BotID, runRecordEnvelope(t, r)); err == nil {
				t.Fatal("invalid metadata accepted")
			}
		})
	}
	for name, bad := range map[string]map[string]any{
		"inline":   {"Run": v.Run},
		"mixed":    {"bot_record_v2": data["bot_record_v2"], "body": "SENTINEL"},
		"unknown":  {"bot_record_v2": strings.TrimSuffix(data["bot_record_v2"].(string), "}") + `,"body":"SENTINEL"}`},
		"trailing": {"bot_record_v2": data["bot_record_v2"].(string) + ` {}`},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := c.decodeRun(t.Context(), v.Run.BotID, bad); err == nil {
				t.Fatal("unsupported metadata accepted")
			}
		})
	}
	if _, err := c.decodeRun(t.Context(), uuid.New(), data); err == nil {
		t.Fatal("cross bot accepted")
	}
	other := newBotContentCodec(t.TempDir(), store.BotContentScope{OwnerID: uuid.NewString()})
	if _, err := other.decodeRun(t.Context(), v.Run.BotID, data); err == nil {
		t.Fatal("other scope accepted")
	}
}

func TestBotContentRunRejectsCorruptMissing(t *testing.T) {
	c := newBotContentCodec(t.TempDir(), store.BotContentScope{OwnerID: uuid.NewString()})
	v := botContentRunFixture()
	data, err := c.encodeRun(t.Context(), v)
	if err != nil {
		t.Fatal(err)
	}
	var r botRunRecord
	if err := decodeBotRecord(data, &r); err != nil {
		t.Fatal(err)
	}
	var snapshot botRunContent
	if err := json.Unmarshal(r.Inline, &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.Stored.Run.CallerCeiling != nil || snapshot.Stored.Run.MandateRef != nil || snapshot.Stored.SkillAdmissionOwner != nil || snapshot.Stored.ExternalAdmissionValidated || snapshot.Stored.ProgressQuota != nil || snapshot.Stored.ProgressOwnerContinuation != nil || snapshot.Stored.Run.RuntimeAccountingRecorded {
		t.Fatal("private admission authority copied into body")
	}
	// Corrupt the inline body: integrity check must reject it.
	corrupt := r
	corrupt.Inline = bytes.Replace(append([]byte(nil), r.Inline...), []byte("SENTINEL"), []byte("CORRUPT!"), 1)
	if _, err := c.decodeRun(t.Context(), v.Run.BotID, runRecordEnvelope(t, corrupt)); !errors.Is(err, store.ErrBotConflict) {
		t.Fatal("corrupt body accepted", err)
	}
	// A different run's valid body cannot replace this record under the same Bot.
	other := v
	other.Run.ID = uuid.New()
	other.Run.TitleConfirmation = nil
	otherData, err := c.encodeRun(t.Context(), other)
	if err != nil {
		t.Fatal(err)
	}
	var otherRecord botRunRecord
	if err := decodeBotRecord(otherData, &otherRecord); err != nil {
		t.Fatal(err)
	}
	substituted := r
	substituted.botRuntimeInline = otherRecord.botRuntimeInline
	if _, err := c.decodeRun(t.Context(), v.Run.BotID, runRecordEnvelope(t, substituted)); err == nil {
		t.Fatal("cross run substitution accepted")
	}
}

// A review guard against future accidental embedding of hydrated models or an
// arbitrary JSON body in SQL. Typed identifier/hash and accounting maps are OK.
func TestBotContentRunSQLAllowlist(t *testing.T) {
	forbidden := map[reflect.Type]bool{
		reflect.TypeOf(store.BotRun{}): true, reflect.TypeOf(store.BotWorkState{}): true,
		reflect.TypeOf(store.BotTaskRevision{}): true, reflect.TypeOf(store.BotProgressState{}): true,
		reflect.TypeOf(store.BotReport{}): true, reflect.TypeOf(json.RawMessage{}): true,
	}
	seen := map[reflect.Type]bool{}
	var visit func(reflect.Type)
	visit = func(typ reflect.Type) {
		if forbidden[typ] || typ.Kind() == reflect.Interface {
			t.Fatalf("unallowlisted SQL type: %v", typ)
		}
		if seen[typ] {
			return
		}
		seen[typ] = true
		switch typ.Kind() {
		case reflect.Pointer, reflect.Slice, reflect.Array:
			visit(typ.Elem())
		case reflect.Map:
			visit(typ.Key())
			visit(typ.Elem())
		case reflect.Struct:
			for i := 0; i < typ.NumField(); i++ {
				f := typ.Field(i)
				if f.IsExported() && f.Tag.Get("json") != "-" {
					visit(f.Type)
				}
			}
		}
	}
	visit(reflect.TypeOf(botRunRecord{}))
}
