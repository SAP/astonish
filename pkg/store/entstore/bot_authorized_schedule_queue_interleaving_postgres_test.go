//go:build integration

package entstore

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
)

// Pause after ClaimRun has read and authorized the old version, immediately
// before its actual PostgreSQL root CAS. No production hooks or fake CAS result.
type queuedClaimBarrierDB struct {
	botDraftDB
	ready   chan<- struct{}
	release <-chan struct{}
}

type queuedClaimBarrierTx struct {
	botDraftDB
	ready   chan<- struct{}
	release <-chan struct{}
}

func (d queuedClaimBarrierDB) Transaction(ctx context.Context, fn func(botDraftDB) error) error {
	return d.botDraftDB.Transaction(ctx, func(tx botDraftDB) error {
		return fn(queuedClaimBarrierTx{tx, d.ready, d.release})
	})
}

func (d queuedClaimBarrierTx) ClaimLease(ctx context.Context, rec botStoredRecord, run botStoredRun, lease store.BotLease) error {
	close(d.ready)
	select {
	case <-d.release:
		return d.botDraftDB.ClaimLease(ctx, rec, run, lease)
	case <-ctx.Done():
		return ctx.Err()
	}
}

func postgresQueuedClaimInterleaving(t *testing.T, r *botRepository, owner context.Context, id uuid.UUID, apply func() error) error {
	t.Helper()
	runs, err := r.ListRuns(owner, id, store.BotPage{})
	if err != nil {
		return err
	}
	var queued *store.BotRun
	for i := range runs {
		if runs[i].Trigger == store.TriggerCron && runs[i].Status == store.RunQueued {
			if queued != nil {
				t.Fatal("interleaving requires exactly one queued occurrence")
			}
			queued = &runs[i]
		}
	}
	if queued == nil {
		t.Fatal("interleaving missing queued occurrence")
	}
	p, ok := execution.PrincipalFromContext(owner)
	if !ok {
		t.Fatal("interleaving missing owner principal")
	}
	worker := execution.WithBotRunIdentity(owner, execution.BotRunIdentity{BotID: id, RunID: queued.ID, Version: queued.Version, ActorID: p.Subject, OrgSlug: p.OrgSlug, TeamSlug: p.TeamSlug})
	ctx, cancel := context.WithTimeout(worker, 15*time.Second)
	defer cancel()
	ready, release := make(chan struct{}), make(chan struct{})
	claimRepo := *r // Never swap the repository used concurrently by apply.
	claimRepo.db = queuedClaimBarrierDB{r.db, ready, release}
	done := make(chan error, 1)
	go func() {
		_, err := claimRepo.ClaimRun(ctx, store.BotClaimRequest{BotID: id, RunID: queued.ID, WorkerID: "pg-interleaving", ExpiresAt: r.clock().Add(time.Minute)})
		done <- err
	}()
	select {
	case <-ready:
	case err := <-done:
		close(release)
		t.Fatalf("claim failed before root barrier: %v", err)
	case <-ctx.Done():
		close(release)
		<-done
		t.Fatal("claim did not reach root barrier")
	}
	applyErr := apply()
	root, rootErr := r.db.GetBot(owner, id)
	saved, savedErr := r.db.ReadRun(owner, id, queued.ID)
	close(release)
	claimErr := <-done
	if applyErr != nil {
		return applyErr
	}
	if !errors.Is(claimErr, store.ErrBotConflict) {
		t.Fatalf("old-snapshot claim after reconciliation = %v, want root CAS conflict", claimErr)
	}
	if rootErr != nil || savedErr != nil {
		t.Fatalf("read winning reconciliation: root=%v run=%v", rootErr, savedErr)
	}
	afterRoot, err := r.db.GetBot(owner, id)
	if err != nil || !reflect.DeepEqual(root, afterRoot) {
		t.Fatalf("losing claim mutated root: %v", err)
	}
	afterRun, err := r.db.ReadRun(owner, id, queued.ID)
	if err != nil || !reflect.DeepEqual(saved, afterRun) {
		t.Fatalf("losing claim mutated rebound run: %v", err)
	}
	return nil
}
