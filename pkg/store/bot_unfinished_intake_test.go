package store

import (
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestBotUnfinishedIntakeTransitions(t *testing.T) {
	for _, scope := range []BotScope{BotScopePersonal, BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			owner := uuid.New()
			now := time.Now()
			q := BotUnfinishedPut{IdempotencyKey: "create", Content: BotUnfinishedContent{Mode: BotIntakeBlank}}
			b, err := NewBotUnfinishedIntake(uuid.New(), owner, scope, q, now)
			if err != nil || b.Revision != 1 {
				t.Fatalf("create: %+v %v", b, err)
			}
			replay, err := b.Replace(owner, scope, q, now.Add(time.Hour))
			if err != nil || !reflect.DeepEqual(b, replay) {
				t.Fatalf("create retry: %+v %v", replay, err)
			}
			q.Content.Name = "Changed"
			if _, err := b.Replace(owner, scope, q, now); !errors.Is(err, ErrBotConflict) {
				t.Fatalf("changed retry: %v", err)
			}
			q.ExpectedRevision = 1
			q.IdempotencyKey = "replace"
			q.Content.Carryover.Goals = []BotCarryoverGoal{{Title: "Partial"}}
			saved, err := b.Replace(owner, scope, q, now)
			if err != nil || saved.Revision != 2 || b.Revision != 1 || b.Content.Name != "" {
				t.Fatalf("replace: %+v %v", saved, err)
			}
			q.Content.Carryover.Goals[0].Title = "Caller mutation"
			if saved.Content.Carryover.Goals[0].Title != "Partial" {
				t.Fatal("caller aliases saved content")
			}
			q.Content.Carryover.Goals[0].Title = "Partial"
			replay, err = saved.Replace(owner, scope, q, now.Add(time.Hour))
			if err != nil || !reflect.DeepEqual(saved, replay) {
				t.Fatalf("replace retry: %+v %v", replay, err)
			}
			replay.Content.Carryover.Goals[0].Title = "Mutated replay"
			if saved.Content.Carryover.Goals[0].Title != "Partial" {
				t.Fatal("replay aliases saved content")
			}
			q.IdempotencyKey = "losing-writer"
			if _, err := saved.Replace(owner, scope, q, now); !errors.Is(err, ErrBotConflict) {
				t.Fatalf("stale writer: %v", err)
			}
			del := BotUnfinishedDelete{ExpectedRevision: 1, IdempotencyKey: "discard"}
			if _, err := saved.Discard(owner, scope, del, now); !errors.Is(err, ErrBotConflict) {
				t.Fatalf("stale delete: %v", err)
			}
			del.ExpectedRevision = 2
			dead, err := saved.Discard(owner, scope, del, now)
			if err != nil || !dead.Discarded || dead.Content != nil || dead.Revision != 3 {
				t.Fatalf("discard: %+v %v", dead, err)
			}
			replay, err = dead.Discard(owner, scope, del, now.Add(time.Hour))
			if err != nil || !reflect.DeepEqual(dead, replay) {
				t.Fatalf("discard retry: %+v %v", replay, err)
			}
			q.ExpectedRevision = 3
			if _, err := dead.Replace(owner, scope, q, now); !errors.Is(err, ErrBotConflict) {
				t.Fatalf("resurrection: %v", err)
			}
		})
	}
}

func TestBotUnfinishedIntakeOwnership(t *testing.T) {
	owner := uuid.New()
	q := BotUnfinishedPut{IdempotencyKey: "one", Content: BotUnfinishedContent{Mode: BotIntakeChat}}
	for _, scope := range []BotScope{BotScopePersonal, BotScopeTeam} {
		b, err := NewBotUnfinishedIntake(uuid.New(), owner, scope, q, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		otherScope := BotScopePersonal
		if scope == otherScope {
			otherScope = BotScopeTeam
		}
		for _, actor := range []struct {
			owner uuid.UUID
			scope BotScope
		}{{uuid.New(), scope}, {owner, otherScope}, {uuid.Nil, scope}} {
			if err := b.Authorize(actor.owner, actor.scope); !errors.Is(err, ErrBotForbidden) {
				t.Fatalf("read owner guard: %v", err)
			}
			if _, err := b.Replace(actor.owner, actor.scope, q, time.Now()); !errors.Is(err, ErrBotForbidden) {
				t.Fatalf("write owner guard: %v", err)
			}
			if _, err := b.Discard(actor.owner, actor.scope, BotUnfinishedDelete{ExpectedRevision: 1, IdempotencyKey: "del"}, time.Now()); !errors.Is(err, ErrBotForbidden) {
				t.Fatalf("delete owner guard: %v", err)
			}
		}
	}
}

func TestBotUnfinishedIntakeValidation(t *testing.T) {
	base := BotUnfinishedPut{IdempotencyKey: "one", Content: BotUnfinishedContent{Mode: BotIntakeBlank}}
	for name, modify := range map[string]func(*BotUnfinishedPut){
		"mode":     func(q *BotUnfinishedPut) { q.Content.Mode = "active" },
		"key":      func(q *BotUnfinishedPut) { q.IdempotencyKey = " " },
		"revision": func(q *BotUnfinishedPut) { q.ExpectedRevision = -1 },
		"overflow": func(q *BotUnfinishedPut) { q.ExpectedRevision = math.MaxInt64 },
		"bytes":    func(q *BotUnfinishedPut) { q.Content.Charter = strings.Repeat("x", BotIntakeMaxBytes) },
		"name":     func(q *BotUnfinishedPut) { q.Content.Name = strings.Repeat("x", 201) },
		"source":   func(q *BotUnfinishedPut) { id := uuid.Nil; q.Content.SourceID = &id },
		"goals":    func(q *BotUnfinishedPut) { q.Content.Carryover.Goals = make([]BotCarryoverGoal, 101) },
	} {
		t.Run(name, func(t *testing.T) {
			q := base
			modify(&q)
			if _, err := NewBotUnfinishedIntake(uuid.New(), uuid.New(), BotScopePersonal, q, time.Now()); !errors.Is(err, ErrBotInvalid) {
				t.Fatalf("validation: %v", err)
			}
		})
	}
	for _, raw := range []string{
		`{"content":{"mode":"blank","credentials":{"token":"secret"}}}`,
		`{"content":{"mode":"blank","enabled":true}}`,
		`{"owner_id":"forged","content":{"mode":"blank"}}`,
	} {
		var q BotUnfinishedPut
		d := json.NewDecoder(strings.NewReader(raw))
		d.DisallowUnknownFields()
		if err := d.Decode(&q); err == nil {
			t.Fatal("authority field accepted")
		}
	}
}
