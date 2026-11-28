package entstore

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

func knowledgeContentFixture() knowledgeEnvelope {
	const sentinel = "KNOWLEDGE_DISK_SENTINEL"
	input := store.BotKnowledgeExtractionInput{SchemaVersion: 1, Complete: true, OwnerMessage: sentinel + " owner", Conversation: sentinel + " conversation", Outcome: store.BotKnowledgeOutcome{Status: "partial", Answer: sentinel + " answer", Failure: sentinel + " failure", Review: sentinel + " review", Tools: []store.BotKnowledgeToolEvidence{{ID: "tool-1", Name: "read_file", Arguments: sentinel + " args", Result: sentinel + " result", Failure: sentinel + " tool failure"}}}, PriorFiles: []store.BotKnowledgeFileSummary{{Path: "knowledge/prior.md", Digest: skillSHA("prior"), Summary: sentinel + " prior"}}}
	input.Source = store.BotKnowledgeExtractionSource{BotID: uuid.New(), RunID: uuid.New(), Version: 1, Sequence: 9007199254740993, OutcomeDigest: store.BotKnowledgeDigest(input.Outcome)}
	decision := &store.BotKnowledgeExtractionDecision{SchemaVersion: 1, Kind: store.BotKnowledgeDecisionChanges, Reason: sentinel + " reason", Proposals: []store.BotKnowledgeProposal{{Kind: store.BotKnowledgeProposalKnowledge, Path: "knowledge/new.md", Content: sentinel + " bundle", EvidenceIDs: []string{"tool-1"}}}}
	r := store.BotKnowledgeExtraction{ID: uuid.New(), Input: input, State: store.BotKnowledgeExtractionPrepared, Revision: 3, AttemptID: uuid.New(), Decision: decision, DecisionDigest: store.BotKnowledgeDigest(*decision), HoldReason: sentinel + " hold"}
	return knowledgeEnvelope{Record: r, UsageBase: &store.BotUsage{InputTokens: 9007199254740993}, Lease: store.BotKnowledgeExtractionLease{BotID: input.Source.BotID, ExtractionID: r.ID, WorkerID: "worker", Fence: 9007199254740993, ExpiresAt: time.Now().UTC().Truncate(time.Microsecond)}, ActorID: uuid.New(), RootFence: 9007199254740993}
}

func assertKnowledgeContentAbsent(t *testing.T, data map[string]any) {
	t.Helper()
	raw, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"KNOWLEDGE_DISK_SENTINEL", base64.StdEncoding.EncodeToString([]byte("KNOWLEDGE_DISK_SENTINEL")), `\u004b\u004e\u004f\u0057`} {
		if strings.Contains(string(raw), text) {
			t.Fatalf("knowledge content leaked into SQL: %s", text)
		}
	}
}

func TestBotContentKnowledgeCodec(t *testing.T) {
	dir := t.TempDir()
	scope := store.BotContentScope{OrgID: "org", TeamID: "team", OwnerID: uuid.NewString()}
	codec := newBotContentCodec(dir, scope)
	v := knowledgeContentFixture()
	data, err := codec.encodeKnowledge(t.Context(), v)
	if err != nil {
		t.Fatal(err)
	}
	assertKnowledgeContentAbsent(t, data)
	got, err := newBotContentCodec(dir, scope).decodeKnowledge(t.Context(), v.Record.Input.Source.BotID, v.Record.ID, data)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(v, *got) {
		t.Fatalf("exact hydrated roundtrip mismatch: %+v", got)
	}
	old, _ := botJSONMap(v)
	if _, err = codec.decodeKnowledge(t.Context(), v.Record.Input.Source.BotID, v.Record.ID, old); !errors.Is(err, errBotDevelopmentContentFormat) {
		t.Fatalf("inline accepted: %v", err)
	}
	var record botKnowledgeRecord
	if err := decodeBotRecord(data, &record); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*botKnowledgeRecord){
		"format":          func(r *botKnowledgeRecord) { r.Format = 1 },
		"kind":            func(r *botKnowledgeRecord) { r.Content.Kind = store.BotContentDocument },
		"owner":           func(r *botKnowledgeRecord) { r.Content.Scope.OwnerID = uuid.NewString() },
		"bot":             func(r *botKnowledgeRecord) { r.Content.Scope.BotID = uuid.New() },
		"revision":        func(r *botKnowledgeRecord) { r.Content.Revision++ },
		"digest":          func(r *botKnowledgeRecord) { r.Content.SHA256 = skillSHA("bad") },
		"size":            func(r *botKnowledgeRecord) { r.Content.SizeBytes++ },
		"missing":         func(r *botKnowledgeRecord) { r.Content.Key = "missing" },
		"input_digest":    func(r *botKnowledgeRecord) { r.InputDigest = skillSHA("bad") },
		"decision_digest": func(r *botKnowledgeRecord) { r.DecisionDigest = skillSHA("bad") },
	} {
		t.Run(name, func(t *testing.T) {
			bad := record
			change(&bad)
			raw, _ := json.Marshal(bad)
			if _, err := codec.decodeKnowledge(t.Context(), v.Record.Input.Source.BotID, v.Record.ID, map[string]any{"bot_record_v2": string(raw)}); err == nil {
				t.Fatal("corrupt reference accepted")
			}
		})
	}
}

func TestBotContentKnowledgeDecisionShape(t *testing.T) {
	codec := newBotContentCodec(t.TempDir(), store.BotContentScope{OwnerID: uuid.NewString()})
	for _, proposals := range [][]store.BotKnowledgeProposal{nil, {}} {
		v := knowledgeContentFixture()
		v.Record.State = store.BotKnowledgeExtractionNoChange
		v.Record.Decision = &store.BotKnowledgeExtractionDecision{SchemaVersion: 1, Kind: store.BotKnowledgeDecisionNoChange, Reason: "KNOWLEDGE_DISK_SENTINEL unchanged", Proposals: proposals}
		v.Record.DecisionDigest = store.BotKnowledgeDigest(*v.Record.Decision)
		data, err := codec.encodeKnowledge(t.Context(), v)
		if err != nil {
			t.Fatal(err)
		}
		got, err := codec.decodeKnowledge(t.Context(), v.Record.Input.Source.BotID, v.Record.ID, data)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(v, *got) {
			t.Fatal("nil/empty proposal shape or exact digest changed")
		}
	}
}

func TestBotContentKnowledgeSQLite(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			r, ctx, input, _ := knowledgeStorageFixture(t, scope)
			v := knowledgeContentFixture()
			v.Record.Input.Source = input.Source
			v.Record.Input.Source.OutcomeDigest = store.BotKnowledgeDigest(v.Record.Input.Outcome)
			v.Lease.BotID = input.Source.BotID
			db := r.db.(botKnowledgeDB)
			if err := db.saveKnowledge(ctx, v, 0); err != nil {
				t.Fatal(err)
			}
			var data map[string]any
			switch d := r.db.(type) {
			case personalBotDB:
				row, err := d.client.BotKnowledgeExtraction.Get(ctx, v.Record.ID)
				if err != nil {
					t.Fatal(err)
				}
				data = row.Data
			case teamBotDB:
				row, err := d.client.BotKnowledgeExtraction.Get(ctx, v.Record.ID)
				if err != nil {
					t.Fatal(err)
				}
				data = row.Data
			default:
				t.Fatal("missing scoped SQLite adapter")
			}
			assertKnowledgeContentAbsent(t, data)
			got, err := db.readKnowledge(ctx, input.Source.BotID, v.Record.ID)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(v, *got) {
				t.Fatal("SQL hydration changed knowledge or authority")
			}
			if _, err = db.findKnowledge(ctx, v.Record.Input.Source); err != nil {
				t.Fatal(err)
			}
			v.Record.Revision++
			if err = db.saveKnowledge(ctx, v, v.Record.Revision); !errors.Is(err, store.ErrBotConflict) {
				t.Fatalf("stale CAS accepted: %v", err)
			}
		})
	}
}
