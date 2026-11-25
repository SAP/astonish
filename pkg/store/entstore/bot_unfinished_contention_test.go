package entstore

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

// Observe the genuine filesystem contention without relying on scheduler timing.
type intakeBusyObserver struct {
	intakeDB
	busy chan struct{}
}

func (d intakeBusyObserver) insert(ctx context.Context, b *store.BotUnfinishedIntake) error {
	err := d.intakeDB.insert(ctx, b)
	if errors.Is(err, store.ErrBotBusy) {
		select {
		case d.busy <- struct{}{}:
		default:
		}
	}
	return err
}

func (d intakeBusyObserver) cas(ctx context.Context, b *store.BotUnfinishedIntake, revision int64) error {
	err := d.intakeDB.cas(ctx, b, revision)
	if errors.Is(err, store.ErrBotBusy) {
		select {
		case d.busy <- struct{}{}:
		default:
		}
	}
	return err
}

func TestBotUnfinishedPublicationContentionReplay(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			owner, id := uuid.New(), uuid.New()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			ctx, err := execution.WithPrincipal(ctx, execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: owner.String(), OrgSlug: "org", TeamSlug: "team", Authenticated: true})
			if err != nil {
				t.Fatal(err)
			}
			codec := newScopedBotContentCodec(t.TempDir(), "org", "team")
			var db intakeDB
			if scope == store.BotScopePersonal {
				_, c := setupPersonalStore(t)
				db = personalIntakeDB{client: c, content: codec}
			} else {
				db = teamIntakeDB{client: newTeamClient(t), content: codec}
			}
			opened, err := codec.OpenIntake(ctx, id, owner, scope)
			if err != nil {
				t.Fatal(err)
			}
			defer opened.Close()
			lock, err := opened.(*botDiskContent).lockPublication(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer lock.Close()
			busy := make(chan struct{}, 3)
			repo := &intakeRepository{auth: botRepositoryAuth{scope: scope, orgSlug: "org", teamSlug: "team", userID: owner.String(), check: func(context.Context, string) (string, error) { return "admin", nil }}, db: intakeBusyObserver{intakeDB: db, busy: busy}}
			q := store.BotUnfinishedPut{IdempotencyKey: "same-create", Content: store.BotUnfinishedContent{Mode: store.BotIntakeBlank, Name: "same response"}}
			type result struct {
				row *store.BotUnfinishedIntake
				err error
			}
			done := make(chan result, 1)
			go func() { row, err := repo.Put(ctx, id, q); done <- result{row, err} }()
			// Hold across the old three-attempt CAS budget, deterministically.
			for range 3 {
				select {
				case <-busy:
				case <-ctx.Done():
					t.Fatal("did not encounter held publication lock")
				}
			}
			// The first writer wins SQL while the other request is contending on files.
			if err := lock.Close(); err != nil {
				t.Fatal(err)
			}
			winner, err := store.NewBotUnfinishedIntake(id, owner, scope, q, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			// Both schedules are valid: either this insert wins or the waiting request does.
			err = db.insert(ctx, winner)
			if err != nil && !errors.Is(err, store.ErrBotConflict) && !errors.Is(err, store.ErrBotBusy) {
				t.Fatal(err)
			}
			got := <-done
			if got.err != nil {
				t.Fatalf("exact retry rejected by publication contention: %v", got.err)
			}
			replay, err := repo.Put(ctx, id, q)
			if err != nil {
				t.Fatal(err)
			}
			a, _ := json.Marshal(got.row)
			b, _ := json.Marshal(replay)
			if string(a) != string(b) {
				t.Fatalf("replay changed response: %s != %s", a, b)
			}
			// The same file contention can precede an UPDATE, not just INSERT.
			lock, err = opened.(*botDiskContent).lockPublication(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer lock.Close()
			busy = make(chan struct{}, 3)
			repo.db = intakeBusyObserver{intakeDB: db, busy: busy}
			q.ExpectedRevision, q.IdempotencyKey, q.Content.Name = replay.Revision, "same-update", "updated"
			go func() { row, err := repo.Put(ctx, id, q); done <- result{row, err} }()
			for range 3 {
				select {
				case <-busy:
				case <-ctx.Done():
					t.Fatal("update did not contend")
				}
			}
			if err := lock.Close(); err != nil {
				t.Fatal(err)
			}
			got = <-done
			if got.err != nil || got.row.Revision != replay.Revision+1 {
				t.Fatalf("update contention: %+v", got)
			}
			// Cancellation of a waiting writer must not publish another revision.
			lock, err = opened.(*botDiskContent).lockPublication(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer lock.Close()
			busy = make(chan struct{}, 3)
			repo.db = intakeBusyObserver{intakeDB: db, busy: busy}
			q.ExpectedRevision, q.IdempotencyKey = got.row.Revision, "cancel-update"
			cancelCtx, stop := context.WithCancel(ctx)
			go func() { row, err := repo.Put(cancelCtx, id, q); done <- result{row, err} }()
			select {
			case <-busy:
			case <-ctx.Done():
				t.Fatal("cancelled update did not contend")
			}
			stop()
			if got := <-done; !errors.Is(got.err, context.Canceled) {
				t.Fatalf("waiting writer ignored cancellation: %v", got.err)
			}
		})
	}
}
