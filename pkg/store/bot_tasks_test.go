package store

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func taskFixture() BotTaskManifest {
	return BotTaskManifest{SchemaVersion: BotTaskSchemaVersion, BotID: uuid.New(), TaskID: uuid.New(), Title: "Research", Assignment: "Research today's releases", OwnerRequirements: []string{"Cite decisive sources"}, ResearchMode: BotTaskResearchOpenWeb, Assets: []BotTaskAsset{{Path: "TASK.md", Kind: BotTaskAssetDefinition, Digest: strings.Repeat("a", 64), SizeBytes: 100}}, Parameters: map[string]json.RawMessage{"sequence": json.RawMessage(`9007199254740993`)}}
}

func TestBotTaskContract(t *testing.T) {
	m := taskFixture()
	if err := m.Validate(BotTaskReady); err != nil {
		t.Fatal(err)
	}
	clone := m.Clone()
	clone.OwnerRequirements[0] = "changed"
	clone.Parameters["sequence"][0] = '1'
	clone.Assets[0].Path = "changed"
	if m.OwnerRequirements[0] != "Cite decisive sources" || string(m.Parameters["sequence"]) != "9007199254740993" || m.Assets[0].Path != "TASK.md" {
		t.Fatal("clone aliases")
	}
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeBotTaskManifest(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(decoded.Parameters["sequence"], m.Parameters["sequence"]) {
		t.Fatal("precision lost")
	}
	for _, tc := range []struct {
		name string
		edit func(*BotTaskManifest)
	}{
		{"schema", func(v *BotTaskManifest) { v.SchemaVersion++ }},
		{"traversal", func(v *BotTaskManifest) { v.Assets[0].Path = "../TASK.md" }},
		{"duplicate", func(v *BotTaskManifest) { v.Assets = append(v.Assets, v.Assets[0]) }},
		{"ancestor-alias", func(v *BotTaskManifest) {
			v.Assets = append(v.Assets, BotTaskAsset{Path: "TASK.md/hidden", Kind: BotTaskAssetText, Digest: strings.Repeat("b", 64), SizeBytes: 1})
		}},
		{"asset-limit", func(v *BotTaskManifest) { v.Assets[0].SizeBytes = BotTaskMaxAssetBytes + 1 }},
		{"foreign-goal", func(v *BotTaskManifest) { v.Goal = &BotTaskGoalRef{BotID: uuid.New(), GoalID: uuid.New(), Revision: 1} }},
		{"foreign-skill", func(v *BotTaskManifest) {
			v.Skills = []BotTaskSkillRef{{BotID: uuid.New(), SkillID: uuid.New(), Revision: 1, Digest: strings.Repeat("b", 64)}}
		}},
		{"bad-parameter", func(v *BotTaskManifest) { v.Parameters["x"] = json.RawMessage(`{"bad":`) }},
		{"no-definition", func(v *BotTaskManifest) { v.Assets = nil }},
		{"wrong-kind", func(v *BotTaskManifest) { v.Assets[0].Kind = "symlink" }},
		{"research-mode", func(v *BotTaskManifest) { v.ResearchMode = "guessed" }},
		{"empty-ready", func(v *BotTaskManifest) { v.Assignment = "" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := m.Clone()
			tc.edit(v)
			if v.Validate(BotTaskReady) == nil {
				t.Fatal("invalid manifest accepted")
			}
		})
	}
	for _, input := range []string{`{}`, `{"schema_version":1,"unknown":true}`, `{"schema_version":"1"}`, `{"schema_version":1} {}`} {
		if _, err := DecodeBotTaskManifest([]byte(input)); err == nil {
			t.Fatalf("accepted %s", input)
		}
	}
	draft := BotTaskManifest{SchemaVersion: 1, BotID: m.BotID, TaskID: m.TaskID, Title: "Incomplete"}
	if err := draft.Validate(BotTaskDraft); err != nil {
		t.Fatal(err)
	}
	if draft.Validate(BotTaskReady) == nil {
		t.Fatal("incomplete ready")
	}
	if (*BotTaskManifest)(nil).Clone() != nil || (*BotTaskRef)(nil).Clone() != nil {
		t.Fatal("nil compatibility")
	}
	if !errors.Is((&BotTaskRef{TaskID: m.TaskID, Revision: 1, Digest: "wrong"}).Validate(), ErrBotInvalid) {
		t.Fatal("digest accepted")
	}
}

func TestBotTaskRevisionIntegrity(t *testing.T) {
	m := taskFixture()
	data := []byte("Task instructions")
	sum := sha256.Sum256(data)
	m.Assets[0].SizeBytes = int64(len(data))
	m.Assets[0].Digest = hex.EncodeToString(sum[:])
	r := BotTaskRevision{SchemaVersion: 1, ID: uuid.New(), BotID: m.BotID, Ref: BotTaskRef{TaskID: m.TaskID, Revision: 1}, Manifest: m, Bundle: map[string][]byte{"TASK.md": data}}
	r.Ref.Digest = r.ContentDigest()
	if err := r.Validate(BotTaskReady); err != nil {
		t.Fatal(err)
	}
	clone := r.Clone()
	clone.Bundle["TASK.md"][0] = 'X'
	if r.Bundle["TASK.md"][0] == 'X' || clone.Validate(BotTaskReady) == nil {
		t.Fatal("bundle clone/integrity")
	}
	clone = r.Clone()
	clone.Bundle["extra"] = []byte("hidden")
	if clone.Validate(BotTaskReady) == nil {
		t.Fatal("undeclared asset")
	}
	if (*BotTaskRevision)(nil).Clone() != nil {
		t.Fatal("nil revision")
	}
}

func TestBotTaskPublicationTransitions(t *testing.T) {
	states := []BotTaskPublicationState{BotTaskPublicationPrepared, BotTaskPublicationFilesReady, BotTaskPublicationCommitted, BotTaskPublicationHeld, BotTaskPublicationAborted, "unknown"}
	for _, from := range states {
		for _, to := range states {
			want := from == BotTaskPublicationPrepared && (to == BotTaskPublicationFilesReady || to == BotTaskPublicationHeld || to == BotTaskPublicationAborted) || from == BotTaskPublicationFilesReady && (to == BotTaskPublicationCommitted || to == BotTaskPublicationHeld || to == BotTaskPublicationAborted)
			if (ValidateBotTaskPublicationTransition(from, to) == nil) != want {
				t.Fatalf("transition %s -> %s", from, to)
			}
		}
	}
	for _, from := range []BotTaskStatus{BotTaskDraft, BotTaskReady, BotTaskArchived, "unknown"} {
		for _, to := range []BotTaskStatus{BotTaskDraft, BotTaskReady, BotTaskArchived, "unknown"} {
			want := from == BotTaskDraft && (to == BotTaskReady || to == BotTaskArchived) || from == BotTaskReady && (to == BotTaskDraft || to == BotTaskArchived) || from == BotTaskArchived && to == BotTaskDraft
			if (ValidateBotTaskStatusTransition(from, to) == nil) != want {
				t.Fatalf("lifecycle %s -> %s", from, to)
			}
		}
	}
}
