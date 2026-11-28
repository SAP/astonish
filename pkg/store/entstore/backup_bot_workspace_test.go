package entstore

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"

	"github.com/SAP/astonish/pkg/sandbox"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

func TestBotTaskWorkspaceArchiveStrictDecode(t *testing.T) {
	id := uuid.New()
	scope := sandbox.BotHomeScope{OrgID: "archive", TeamID: "ops", UserID: uuid.NewString(), BotID: id.String()}
	sum := sha256.Sum256([]byte("[]"))
	a := BotWorkspaceArchive{SchemaVersion: 1, BotID: id, Home: store.BotHomeBinding{ID: uuid.New(), BotID: id, Scope: store.BotScopeTeam, Backend: "docker", StorageKey: "owned-volume", Revision: 1}, Scope: scope, Snapshot: sandbox.BotHomeWorkspaceSnapshot{Files: []sandbox.BotHomeWorkspaceFile{}, Receipt: sandbox.BotHomeWorkspaceReceipt{BindingKey: scope.Key(), TaskPath: "workspace", Entries: []sandbox.BotHomeWorkspaceEntry{}, Digest: hex.EncodeToString(sum[:])}}}
	raw, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = decodeBotWorkspaceArchive(raw); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"unknown", "trailing", "schema", "bot", "home", "binding", "digest"} {
		t.Run(name, func(t *testing.T) {
			var bad BotWorkspaceArchive
			_ = json.Unmarshal(raw, &bad)
			switch name {
			case "schema":
				bad.SchemaVersion++
			case "bot":
				bad.BotID = uuid.New()
			case "home":
				bad.Home.BotID = uuid.New()
			case "binding":
				bad.Scope.TeamID = "foreign"
			case "digest":
				bad.Snapshot.Receipt.Digest = strings.Repeat("0", 64)
			}
			encoded, _ := json.Marshal(bad)
			if name == "unknown" {
				encoded = append([]byte(`{"unknown":1,`), encoded[1:]...)
			}
			if name == "trailing" {
				encoded = append(encoded, []byte(` {}`)...)
			}
			if _, err := decodeBotWorkspaceArchive(encoded); err == nil {
				t.Fatal("malformed archive accepted")
			}
		})
	}
}
