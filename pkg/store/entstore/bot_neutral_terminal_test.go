package entstore

import (
	"context"
	"encoding/json"
	"github.com/SAP/astonish/pkg/store"
	"testing"
)

func TestBotNeutralTerminalPublicationDurable(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			repo, owner, worker, run, lease, now := botRunFixture(t, scope)
			work, err := repo.LoadWork(worker, lease)
			if err != nil {
				t.Fatal(err)
			}
			report := store.BotReport{Work: store.BuildBotWorkProjection(work), Summary: "private blocked diagnostic", Unverified: []string{"unavailable"}}
			report.FinalizeEmployeeMessage(store.RunPartial, work)
			if err := repo.CommitRun(worker, store.BotRunCommit{Lease: lease, Status: store.RunPartial, Report: report}); err != nil {
				t.Fatal(err)
			}
			b, err := repo.Get(owner, run.BotID)
			if err != nil {
				t.Fatal(err)
			}
			var live []store.BotRunState
			owner = store.WithBotRunStateNotifier(owner, func(_ctx context.Context, s store.BotRunState) { live = append(live, s) })
			for i := 0; i < 2; i++ {
				if _, err := repo.PublishReport(owner, store.BotPublicationRequest{BotID: run.BotID, RunID: run.ID, Now: *now}); err != nil {
					t.Fatal(err)
				}
			}
			saved, err := repo.GetRun(owner, run.BotID, run.ID)
			if err != nil || saved.TerminalReason != store.BotTerminalNoAnswer {
				t.Fatalf("not durable: %+v %v", saved, err)
			}
			if len(live) != 1 || live[0].NeutralTerminalLabel() == "" {
				t.Fatalf("live notifications: %+v", live)
			}
			rows, err := messageReviewReader(repo).ReadBotTranscript(owner, b.HomeSessionID, 0, 200)
			if err != nil {
				t.Fatal(err)
			}
			count := 0
			for _, row := range rows {
				raw, _ := json.Marshal(row.Event.Actions.StateDelta[store.BotRunStateKey])
				var s store.BotRunState
				_ = json.Unmarshal(raw, &s)
				if s.TerminalReason == store.BotTerminalNoAnswer {
					count++
					if row.Event.Content != nil {
						t.Fatal("neutral state is speech")
					}
				}
			}
			if count != 1 {
				t.Fatalf("Home terminal outcomes=%d", count)
			}
		})
	}
}
