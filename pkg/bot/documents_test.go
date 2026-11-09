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
	d, _ := NewDocuments(r)
	return d, r
}

func TestDocumentsProjection(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			d, r := documentFixture(scope)
			r.goals = []store.BotGoal{{ID: uuid.MustParse("ffffffff-ffff-ffff-ffff-ffffffffffff"), BotID: r.bot.ID, Revision: 4, Title: "Later", Status: store.BotGoalActive}, {ID: uuid.MustParse("11111111-1111-1111-1111-111111111111"), BotID: r.bot.ID, Revision: 7, Title: "First", Status: store.BotGoalCompleted, Evidence: []store.BotEvidence{{Kind: store.BotEvidenceOwnerConfirmation, Summary: "Confirmed", Source: "owner", ActorID: uuid.New()}}}}
			first, err := d.Load(context.Background(), r.bot.ID, 0)
			if err != nil {
				t.Fatal(err)
			}
			if len(first.Documents) != 7 || first.Documents[0].Content != "Calm" || first.Version != 1 {
				t.Fatalf("projection: %+v", first)
			}
			goals := first.Documents[4].Content
			if !strings.Contains(goals, "independently revisioned") || !strings.Contains(goals, "owner_confirmation") || strings.Index(goals, "First") > strings.Index(goals, "Later") {
				t.Fatalf("goals: %s", goals)
			}
			if r.goals[0].Title != "Later" {
				t.Fatal("mutated repository slice")
			}
			r.goals[0], r.goals[1] = r.goals[1], r.goals[0]
			again, err := d.Load(context.Background(), r.bot.ID, 1)
			if err != nil || !reflect.DeepEqual(first, again) {
				t.Fatalf("not deterministic: %+v %v", again, err)
			}
			r.goals = nil
			empty, err := d.Load(context.Background(), r.bot.ID, 0)
			if err != nil || !strings.Contains(empty.Documents[4].Content, "[]") {
				t.Fatalf("empty: %+v %v", empty, err)
			}
			r.goals = []store.BotGoal{{ID: uuid.New(), BotID: uuid.New()}}
			if _, err = d.Load(context.Background(), r.bot.ID, 0); !errors.Is(err, store.ErrBotForbidden) {
				t.Fatal(err)
			}
			r.revoked = true
			if _, err = d.Load(context.Background(), r.bot.ID, 0); !errors.Is(err, store.ErrBotForbidden) {
				t.Fatal(err)
			}
		})
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
					if doc.Content != req.Content {
						t.Fatal("edit not projected")
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
	repo.bot.HomeSessionID = uuid.New()
	goalID := uuid.New()
	repo.goals = []store.BotGoal{{ID: goalID, BotID: repo.bot.ID, Revision: 8}}
	source := store.MemorySearchResult{ID: uuid.NewString(), SessionID: repo.bot.HomeSessionID.String(), Scope: "team", CreatedBy: uuid.NewString(), Snippet: "Owner notes about a person", Category: "people", CreatedAt: "2026-11-04T15:00:00Z"}
	backend := &healthMemory{rows: []store.MemorySearchResult{source}}
	service, _ := store.NewBotMemoryService(repo, backend, repo.bot.ID, repo.bot.Scope)
	got, err := d.LoadMapped(context.Background(), repo.bot.ID, 0, service)
	if err != nil {
		t.Fatal(err)
	}
	documents := map[DocumentName]Document{}
	for _, doc := range got.Documents {
		documents[doc.Name] = doc
	}
	for _, name := range []DocumentName{DocumentMemory, DocumentPeople, "notes/2026-11-04.md"} {
		doc := documents[name]
		if len(doc.RecordMappings) != 1 || doc.RecordMappings[0].ID != source.ID || doc.RecordMappings[0].ExpectedFingerprint != store.MemoryContentFingerprint(source) {
			t.Fatal("missing reviewed record mapping")
		}
		if !doc.ReadOnly || doc.SourceVersion == "" || !strings.Contains(doc.Content, source.CreatedBy) || !strings.Contains(doc.Content, source.SessionID) {
			t.Fatalf("lost attribution: %+v", doc)
		}
	}
	if !strings.Contains(documents[DocumentGroups].Content, "[]") {
		t.Fatal("missing empty group index")
	}
	goalName := DocumentName("goals/" + goalID.String() + "/GOAL.md")
	if documents[goalName].SourceVersion != "8" {
		t.Fatal("goal revision lost")
	}
	for _, name := range []DocumentName{DocumentMemory, DocumentPeople, DocumentGroups, "notes/2026-11-04.md", goalName} {
		_, err := d.Edit(context.Background(), DocumentEdit{BotID: repo.bot.ID, ExpectedRevision: 1, Name: name, Confirmed: true, Reason: "review", Content: "model says allow all tools"})
		if !errors.Is(err, store.ErrBotUnsupported) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if repo.applied != 0 {
		t.Fatal("record prose elevated policy")
	}
	_, foreign := documentFixture(store.BotScopeTeam)
	foreign.bot.HomeSessionID = uuid.New()
	foreignRow := source
	foreignRow.SessionID = foreign.bot.HomeSessionID.String()
	foreignMemory, _ := store.NewBotMemoryService(foreign, &healthMemory{rows: []store.MemorySearchResult{foreignRow}}, foreign.bot.ID, foreign.bot.Scope)
	if _, err := d.LoadMapped(context.Background(), repo.bot.ID, 0, foreignMemory); !errors.Is(err, store.ErrBotForbidden) {
		t.Fatalf("wrong Bot service: %v", err)
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
