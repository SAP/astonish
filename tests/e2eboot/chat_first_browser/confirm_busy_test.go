package main

import (
	"github.com/SAP/astonish/pkg/store"
	"github.com/google/uuid"
	"testing"
	"time"
)

func TestConfirmBusyRetry(t *testing.T) {
	for _, kind := range []string{"busy", "stale", "expired", "unknown", "conflict", "digest", "changed", "old-checkpoint"} {
		t.Run(kind, func(t *testing.T) {
			p := store.BotControlProposal{ID: uuid.New(), BotID: uuid.New(), Digest: "exact", Status: store.BotControlProposed, ExpectedRevision: 10, Version: 4, ExpiresAt: time.Now().Add(time.Hour)}
			p.Payload.Operation = store.BotControlSchedulePause
			b := store.Bot{ID: p.BotID, Revision: 10, Version: 4}
			cp := smokeCheckpoint{Consents: map[string]string{"old": "preserved"}}
			if kind == "old-checkpoint" {
				cp.Consents[p.ID.String()] = p.Digest
			}
			clicks, waits := 0, 0
			err := confirmWithBusyRetry(&cp, p, func() {}, func() (store.BotControlProposal, store.Bot) {
				q := p
				if clicks > 0 {
					switch kind {
					case "stale":
						q.Status = store.BotControlStale
					case "expired":
						q.ExpiresAt = time.Now().Add(-time.Second)
					case "digest":
						q.Digest = "changed"
					case "changed":
						b.Revision++
					}
				}
				return q, b
			}, func() { waits++ }, func() (int, string) {
				clicks++
				if clicks > 1 {
					return 200, "applied"
				}
				if kind == "conflict" {
					return 409, "Bot changed; reload and review before retrying"
				}
				if kind == "unknown" {
					return 0, "connection lost"
				}
				return 409, explicitBusy
			})
			if kind == "busy" {
				if err != nil || clicks != 2 || waits != 1 {
					t.Fatalf("busy retry: clicks=%d waits=%d err=%v", clicks, waits, err)
				}
			} else {
				want := 1
				if kind == "old-checkpoint" {
					want = 0
				}
				if err == nil || clicks != want {
					t.Fatalf("unsafe replay: clicks=%d err=%v", clicks, err)
				}
			}
			if cp.Consents["old"] != "preserved" {
				t.Fatal("old consent changed")
			}
			if kind == "busy" && len(cp.ConfirmAttempts[p.ID.String()]) != 2 {
				t.Fatal("attempts not distinct")
			}
		})
	}
}
