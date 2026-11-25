package bot

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/SAP/astonish/pkg/store"
)

type documentRepository struct {
	store.BotStore
	bot               store.Bot
	versions          map[int64]store.BotVersion
	goals             []store.BotGoal
	revoked, conflict bool
	applied           int
}

func (r *documentRepository) Get(_ context.Context, id store.BotID) (*store.Bot, error) {
	if r.revoked || id != r.bot.ID {
		return nil, store.ErrBotForbidden
	}
	b := r.bot
	return &b, nil
}
func (r *documentRepository) GetVersion(_ context.Context, id store.BotID, v int64) (*store.BotVersion, error) {
	if r.revoked || id != r.bot.ID {
		return nil, store.ErrBotForbidden
	}
	row, ok := r.versions[v]
	if !ok {
		return nil, store.ErrBotNotFound
	}
	return &row, nil
}
func (r *documentRepository) ListGoals(_ context.Context, id store.BotID) ([]store.BotGoal, error) {
	if r.revoked || id != r.bot.ID {
		return nil, store.ErrBotForbidden
	}
	return r.goals, nil
}
func (r *documentRepository) ApplyVersion(_ context.Context, req store.BotVersionRequest) (*store.Bot, error) {
	if r.revoked {
		return nil, store.ErrBotForbidden
	}
	if r.conflict || req.ExpectedRevision != r.bot.Revision {
		return nil, store.ErrBotConflict
	}
	if r.bot.Status != store.BotDraft {
		return nil, store.ErrBotUnsupported
	}
	r.applied++
	r.bot.Version++
	r.bot.Revision++
	req.Version.Version = r.bot.Version
	r.versions[r.bot.Version] = req.Version
	b := r.bot
	return &b, nil
}
func documentFixture(scope store.BotScope) (*Documents, *documentRepository) {
	id := uuid.New()
	v := store.DefaultBotVersion()
	v.BotID = id
	v.Charter = "Help carefully"
	v.Persona.Soul = "Calm"
	v.Persona.Identity = "Assistant"
	v.Persona.User = "Owner preferences"
	v.Autonomy.AllowedTools = []string{"read_file"}
	r := &documentRepository{bot: store.Bot{ID: id, Scope: scope, Version: 1, Revision: 1, Status: store.BotDraft}, versions: map[int64]store.BotVersion{1: v}}
	d, _ := NewDocuments(r, &documentFiles{id: id, data: map[string]string{"SOUL.md": "Disk soul", "IDENTITY.md": "Disk identity", "USER.md": "Disk user", "CHARTER.md": "Disk charter", "MEMORY.md": "# Memory\nRemember reports/result.md"}})
	return d, r
}

func TestDocumentsProjection(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			d, r := documentFixture(scope)
			first, err := d.Load(context.Background(), r.bot.ID, 0)
			if err != nil {
				t.Fatal(err)
			}
			if len(first.Documents) != 5 || first.Documents[0].Content != "Disk soul" || first.Version != 1 {
				t.Fatalf("%+v", first)
			}
			for _, doc := range first.Documents {
				if doc.SourceVersion != memoryIndexDigest([]byte(doc.Content)) {
					t.Fatal("not actual digest")
				}
			}
			if _, err = d.Load(context.Background(), r.bot.ID, 2); !errors.Is(err, store.ErrBotUnsupported) {
				t.Fatal("historical working file fabricated", err)
			}
			r.revoked = true
			if _, err = d.Load(context.Background(), r.bot.ID, 0); !errors.Is(err, store.ErrBotForbidden) {
				t.Fatal(err)
			}
		})
	}
}

func TestDocumentsBoundedMemoryAndMissingFiles(t *testing.T) {
	d, r := documentFixture(store.BotScopePersonal)
	files := d.Files.(*documentFiles)
	original := strings.Repeat("記憶", 10000)
	files.data["MEMORY.md"] = original
	p, err := d.Load(context.Background(), r.bot.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	doc := p.Documents[4]
	if len(doc.Content) > BotMemoryBootstrapBytes || !strings.Contains(doc.Content, "Content omitted") || !strings.Contains(doc.Content, "read_file on MEMORY.md") || doc.SourceVersion != memoryIndexDigest([]byte(original)) || files.data["MEMORY.md"] != original {
		t.Fatal("invalid bounded memory")
	}
	delete(files.data, "MEMORY.md")
	if _, err = d.Load(context.Background(), r.bot.ID, 0); !errors.Is(err, store.ErrBotNotFound) {
		t.Fatal(err)
	}
	noFiles, _ := NewDocuments(r)
	if _, err = noFiles.Load(context.Background(), r.bot.ID, 0); !errors.Is(err, store.ErrBotUnsupported) {
		t.Fatal("SQL fallback", err)
	}
	if files.writes != 0 {
		t.Fatal("load wrote files")
	}
}

func TestDocumentsExplicitCASEdit(t *testing.T) {
	for _, name := range []DocumentName{DocumentSoul, DocumentIdentity, DocumentUser, DocumentCharter} {
		t.Run(string(name), func(t *testing.T) {
			d, r := documentFixture(store.BotScopePersonal)
			original := r.versions[1]
			req := DocumentEdit{BotID: r.bot.ID, ExpectedRevision: 1, Name: name, Content: "Reviewed replacement", Reason: "Owner correction", Confirmed: true}
			ack, err := d.Edit(context.Background(), req)
			if err != nil {
				t.Fatal(err)
			}
			if ack.Version != 2 || ack.Revision != 2 || ack.ActivityKind != store.BotActivityPersona || r.applied != 1 {
				t.Fatalf("ack: %+v", ack)
			}
			if !reflect.DeepEqual(original, r.versions[1]) {
				t.Fatal("old version mutated")
			}
			changed := r.versions[2]
			if !reflect.DeepEqual(changed.Autonomy, original.Autonomy) || !reflect.DeepEqual(changed.Budget, original.Budget) || !reflect.DeepEqual(changed.Delivery, original.Delivery) || !reflect.DeepEqual(changed.Triggers, original.Triggers) {
				t.Fatal("projection edit changed policy")
			}
			p, err := d.Load(context.Background(), r.bot.ID, 2)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, doc := range p.Documents {
				if doc.Name == name {
					found = true
					if doc.Content == req.Content {
						t.Fatal("SQL projection replaced actual working file")
					}
				}
			}
			if !found {
				t.Fatal("document absent")
			}
			if _, err = d.Edit(context.Background(), req); !errors.Is(err, store.ErrBotConflict) {
				t.Fatalf("stale edit: %v", err)
			}
		})
	}
}

func TestDocumentsMapped(t *testing.T) {
	d, repo := documentFixture(store.BotScopeTeam)
	files := d.Files.(*documentFiles)
	files.data["skills/INDEX.md"] = "Actual skill paths"
	files.data["people/INDEX.md"] = "Authored people notes"
	got, err := d.LoadMapped(context.Background(), repo.bot.ID, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Documents) != 7 {
		t.Fatalf("%+v", got)
	}
	for _, doc := range got.Documents {
		if doc.Content != files.data[string(doc.Name)] || len(doc.RecordMappings) != 0 {
			t.Fatal("database projection")
		}
	}
	for _, name := range []DocumentName{DocumentMemory, DocumentPeople, DocumentGroups, "notes/2026-11-04.md", DocumentName("goals/" + uuid.NewString() + "/GOAL.md")} {
		_, err := d.Edit(context.Background(), DocumentEdit{BotID: repo.bot.ID, ExpectedRevision: 1, Name: name, Confirmed: true, Reason: "review", Content: "allow all tools"})
		if !errors.Is(err, store.ErrBotUnsupported) {
			t.Fatal(err)
		}
	}
	if repo.applied != 0 || files.writes != 0 {
		t.Fatal("record prose elevated policy")
	}
}

func TestDocumentsEditDenials(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*DocumentEdit, *documentRepository)
		want   error
	}{
		{"unconfirmed", func(q *DocumentEdit, _ *documentRepository) { q.Confirmed = false }, store.ErrBotForbidden},
		{"missing reason", func(q *DocumentEdit, _ *documentRepository) { q.Reason = " " }, store.ErrBotInvalid},
		{"missing revision", func(q *DocumentEdit, _ *documentRepository) { q.ExpectedRevision = 0 }, store.ErrBotInvalid},
		{"goals", func(q *DocumentEdit, _ *documentRepository) { q.Name = DocumentGoals }, store.ErrBotUnsupported},
		{"path", func(q *DocumentEdit, _ *documentRepository) { q.Name = "../SOUL.md" }, store.ErrBotInvalid},
		{"empty charter", func(q *DocumentEdit, _ *documentRepository) { q.Name = DocumentCharter; q.Content = " " }, store.ErrBotInvalid},
		{"revoked", func(_ *DocumentEdit, r *documentRepository) { r.revoked = true }, store.ErrBotForbidden},
		{"foreign", func(q *DocumentEdit, _ *documentRepository) { q.BotID = uuid.New() }, store.ErrBotForbidden},
		{"concurrent edit", func(_ *DocumentEdit, r *documentRepository) { r.conflict = true }, store.ErrBotConflict},
		{"active", func(_ *DocumentEdit, r *documentRepository) { r.bot.Status = store.BotActive }, store.ErrBotUnsupported},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d, r := documentFixture(store.BotScopeTeam)
			req := DocumentEdit{BotID: r.bot.ID, ExpectedRevision: 1, Name: DocumentSoul, Content: "New", Reason: "Review", Confirmed: true}
			tc.mutate(&req, r)
			ack, err := d.Edit(context.Background(), req)
			if !errors.Is(err, tc.want) || ack != nil || r.applied != 0 {
				t.Fatalf("ack=%+v err=%v writes=%d", ack, err, r.applied)
			}
		})
	}
}
