package entstore

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

func TestBotContentSourceIntakeSQL(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			ctx := context.Background()
			codec := newScopedBotContentCodec(t.TempDir(), "org", "team")
			p := &store.BotSourceIntakeProposal{ID: uuid.New(), OwnerID: uuid.New(), Scope: scope, Digest: strings.Repeat("a", 64), Status: store.BotSourceIntakeProposed, CreatedAt: time.Now().UTC(), Request: store.BotSourceIntakeRequest{Mode: store.BotIntakeChat, Charter: "source-prose-sentinel", Carryover: store.BotCarryover{Summary: "source-carryover-sentinel"}}}
			var db botDB
			var raw func() []byte
			if scope == store.BotScopePersonal {
				_, c := setupPersonalStore(t)
				db = personalBotDB{client: c, content: codec}
				raw = func() []byte {
					r, e := c.BotIntake.Get(ctx, p.ID)
					if e != nil {
						t.Fatal(e)
					}
					return r.Content
				}
			} else {
				c := newTeamClient(t)
				db = teamBotDB{client: c, content: codec}
				raw = func() []byte {
					r, e := c.BotIntake.Get(ctx, p.ID)
					if e != nil {
						t.Fatal(e)
					}
					return r.Content
				}
			}
			if err := sourceProposalWrite(ctx, db, p, 0); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(raw()), "sentinel") {
				t.Fatal("source body in SQL")
			}
			got, revision, err := sourceProposalRead(ctx, db, p.ID, p.OwnerID)
			if err != nil || revision != 1 || got.Request.Charter != p.Request.Charter {
				t.Fatalf("read: %+v %v", got, err)
			}
			p.Status = store.BotSourceIntakeRejected
			if err := sourceProposalWrite(ctx, db, p, 1); err != nil {
				t.Fatal(err)
			}
			got, revision, err = sourceProposalRead(ctx, db, p.ID, p.OwnerID)
			if err != nil || revision != 2 || got.Status != p.Status {
				t.Fatalf("decision: %+v %v", got, err)
			}
		})
	}
}

func TestBotContentTemplateIndependentOwnership(t *testing.T) {
	ctx, _ := execution.WithPrincipal(context.Background(), execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: uuid.NewString(), OrgSlug: "org", TeamSlug: "team", Authenticated: true})
	client := newTeamClient(t)
	dir := t.TempDir()
	content := newBotTemplateContent(dir, "org", "team")
	s := &teamBotTemplateStore{client: client, content: content, auth: botRepositoryAuth{orgSlug: "org", teamSlug: "team", scope: store.BotScopeTeam, check: func(context.Context, string) (string, error) { return "admin", nil }}}
	sentinel := "template-private-prose-sentinel"
	def := store.BotTemplateDefinition{SchemaVersion: 1, Name: "Reviewed template", Charter: sentinel}
	published, err := s.Publish(ctx, def, 0)
	if err != nil {
		t.Fatal(err)
	}
	row, err := client.BotTemplate.Get(ctx, published.ID)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(row.Definition)
	if strings.Contains(string(raw), sentinel) {
		t.Fatal("template prose in SQL")
	}
	audits, err := client.TeamAuditLog.Query().All(ctx)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ = json.Marshal(audits)
	if strings.Contains(string(raw), sentinel) {
		t.Fatal("template prose in audit SQL")
	}
	var record botIndependentRecord
	if err := decodeBotRecord(row.Definition, &record); err != nil {
		t.Fatal(err)
	}
	if record.Content.Scope.BotID != uuid.Nil || record.Content.Scope.OwnerID != "" || record.Content.Kind != store.BotContentTemplate {
		t.Fatal("not independently team owned")
	}
	// Removing a source Bot namespace cannot remove a team-owned template object.
	source, err := newBotDiskContent(dir, store.BotContentScope{OrgID: "org", TeamID: "team", OwnerID: uuid.NewString(), BotID: uuid.New()})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(dir, source.namespace())); err != nil {
		t.Fatal(err)
	}
	source.Close()
	s.content = newBotTemplateContent(dir, "org", "team")
	got, err := s.Get(ctx, published.ID)
	if err != nil || got.Definition.Charter != sentinel {
		t.Fatalf("reopen: %+v %v", got, err)
	}
	foreign := newBotTemplateContent(dir, "org", "other")
	if err := foreign.decode(ctx, published.ID, store.BotContentTemplate, 1, row.Definition, new(store.BotTemplateDefinition)); !errors.Is(err, store.ErrBotInvalid) {
		t.Fatalf("cross scope: %v", err)
	}
	disk, _ := content.Open(ctx)
	d := disk.(*botDiskContent)
	name, _ := diskContentName(record.Content)
	path := filepath.Join(dir, d.namespace(), name)
	disk.Close()
	if err := os.WriteFile(path, []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, published.ID); err == nil {
		t.Fatal("corruption accepted")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, published.ID); err == nil {
		t.Fatal("missing object accepted")
	}
	if err := s.content.decode(ctx, published.ID, store.BotContentTemplate, 1, map[string]any{"charter": sentinel}, new(store.BotTemplateDefinition)); !errors.Is(err, store.ErrBotUnsupported) {
		t.Fatalf("legacy: %v", err)
	}
}

func TestBotContentUnfinishedIndependentOwnership(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			ctx := context.Background()
			codec := newScopedBotContentCodec(t.TempDir(), "org", "team")
			b := &store.BotUnfinishedIntake{ID: uuid.New(), OwnerID: uuid.New(), Scope: scope, Revision: 1, Content: &store.BotUnfinishedContent{Mode: store.BotIntakeBlank, Charter: "intake-prose-sentinel", Carryover: store.BotCarryover{Summary: "carryover-prose-sentinel"}}}
			var db intakeDB
			var raw func() []byte
			if scope == store.BotScopePersonal {
				_, c := setupPersonalStore(t)
				db = personalIntakeDB{client: c, content: codec}
				raw = func() []byte {
					r, e := c.BotIntake.Get(ctx, b.ID)
					if e != nil {
						t.Fatal(e)
					}
					return r.Content
				}
			} else {
				c := newTeamClient(t)
				db = teamIntakeDB{client: c, content: codec}
				raw = func() []byte {
					r, e := c.BotIntake.Get(ctx, b.ID)
					if e != nil {
						t.Fatal(e)
					}
					return r.Content
				}
			}
			if err := db.insert(ctx, b); err != nil {
				t.Fatal(err)
			}
			data := raw()
			if strings.Contains(string(data), "prose-sentinel") {
				t.Fatal("intake prose in SQL")
			}
			got, err := db.read(ctx, b.ID, b.OwnerID)
			if err != nil || got.Content.Charter != b.Content.Charter || got.Content.Carryover.Summary != b.Content.Carryover.Summary {
				t.Fatalf("hydrate: %+v %v", got, err)
			}
			var envelope map[string]any
			json.Unmarshal(data, &envelope)
			var rec botIndependentRecord
			if err := decodeBotRecord(envelope, &rec); err != nil {
				t.Fatal(err)
			}
			if rec.Content.Scope.BotID != uuid.Nil || rec.Content.Scope.IntakeID != b.ID || rec.Content.Kind != store.BotContentIntake {
				t.Fatal("draft borrowed Bot ownership")
			}
			foreign := *b
			foreign.ID = uuid.New()
			if err := codec.decodeIntake(ctx, &foreign, data); err == nil {
				t.Fatal("cross intake read")
			}
			foreign = *b
			foreign.OwnerID = uuid.New()
			if err := codec.decodeIntake(ctx, &foreign, data); err == nil {
				t.Fatal("cross owner read")
			}
			if err := codec.decodeIntake(ctx, b, []byte(`{"charter":"legacy"}`)); !errors.Is(err, store.ErrBotUnsupported) {
				t.Fatalf("legacy: %v", err)
			}
		})
	}
}
