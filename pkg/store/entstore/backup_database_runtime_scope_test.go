package entstore

import (
	"github.com/SAP/astonish/pkg/backup"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"testing"
)

func TestBackupFileFirstDatabaseRuntimeScopeMapping(t *testing.T) {
	owner, targetOwner, id := uuid.New(), uuid.New(), uuid.New()
	runtime := store.BotContentScope{OwnerID: owner.String(), BotID: id}
	stage := store.BotContentScope{OrgID: "source", TeamID: "team", OwnerID: owner.String(), BotID: id}
	d := fileFirstScopeArchive{Scope: backup.Scope{Kind: "team", OrgSlug: "source", TeamSlug: "team"}, Homes: []fileFirstHomeArchive{{Bot: fileFirstBot{Bot: store.Bot{ID: id, OwnerID: owner, Scope: store.BotScopeTeam}, Stage: stage}}}}
	d.Homes[0].Bot.Selection.Binding.Scope.OrgID = "source"
	d.Homes[0].Bot.Selection.Binding.Scope.TeamID = "team"
	d.Homes[0].Bot.Selection.Binding.Scope.UserID = owner.String()
	m := newFileFirstMapping()
	_, err := mapFileFirstDescriptor(d, PlatformRestoreOptions{MapOrg: map[string]string{"source": "target"}, MapUser: map[string]string{"source/" + owner.String(): "target/" + targetOwner.String()}}, m)
	if err != nil {
		t.Fatal(err)
	}
	next := store.BotContentScope{OwnerID: targetOwner.String(), BotID: id}
	if m.scopes[runtime] != next {
		t.Fatalf("database runtime scope mapping = %+v", m.scopes[runtime])
	}
	record := &botSessionEventRecord{BotID: id, botRuntimeInline: runtimeInline([]byte(`{"private":"retained"}`))}
	record.Scope = runtime
	if err := mapFileFirstInline(record, m); err != nil {
		t.Fatal(err)
	}
	if record.Scope != next {
		t.Fatal("runtime inherited workspace tenant instead of owner/Bot identity")
	}
	record.Scope.OwnerID = uuid.NewString()
	if err := mapFileFirstInline(record, m); err == nil {
		t.Fatal("foreign-owner runtime accepted")
	}
	record.Scope = runtime
	record.BotID = uuid.New()
	if err := mapFileFirstInline(record, m); err == nil {
		t.Fatal("foreign-Bot runtime accepted")
	}
}
