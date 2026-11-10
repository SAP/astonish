package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/bot"
	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/entstore"
	"github.com/google/uuid"
)

type notificationHTTPTransport struct {
	url    string
	client *http.Client
}

func (n notificationHTTPTransport) Send(ctx context.Context, q bot.NotificationRequest) (bot.NotificationReceipt, error) {
	data, err := json.Marshal(q)
	if err != nil {
		return bot.NotificationReceipt{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.url, bytes.NewReader(data))
	if err != nil {
		return bot.NotificationReceipt{}, err
	}
	response, err := n.client.Do(req)
	if err != nil {
		return bot.NotificationReceipt{}, err
	}
	defer response.Body.Close()
	var receipt bot.NotificationReceipt
	err = json.NewDecoder(response.Body).Decode(&receipt)
	return receipt, err
}

// TestBotNotifications uses production scoped stores, committed run outcomes and
// a real HTTP transport. Every database belongs to t.TempDir; linked identities
// supply live authority. Fixture SQL sets run readiness and immutable snapshots,
// including hostile legacy targets that the live claim gate must reject.
func TestBotNotifications(t *testing.T) {
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		for _, scenario := range []string{"nochange", "threshold-below", "threshold", "defer-once", "completed-goal", "ambiguous", "revoked-binding", "foreign-workspace", "foreign-chat"} {
			t.Run(string(scope)+"/"+scenario, func(t *testing.T) {
				ctx := context.Background()
				dir := t.TempDir()
				_, platform, err := entstore.NewPlatformServices(ctx, entstore.Config{DSN: filepath.Join(dir, "platform.db"), DataDir: dir})
				if err != nil {
					t.Fatal(err)
				}
				defer platform.Close()
				org := &store.Organization{ID: uuid.NewString(), Slug: "notifications", Name: "Notifications", Status: "active"}
				if err := platform.Organizations().Create(ctx, org); err != nil {
					t.Fatal(err)
				}
				if err := platform.ProvisionOrg(ctx, org.ID, org.Slug); err != nil {
					t.Fatal(err)
				}
				ods, err := platform.ForOrg(org.Slug)
				if err != nil {
					t.Fatal(err)
				}
				team := &store.Team{ID: uuid.NewString(), Slug: "team", Name: "Team", SchemaName: "team_team"}
				if err := ods.Teams().CreateTeam(ctx, team); err != nil {
					t.Fatal(err)
				}
				owner := uuid.New()
				if err := platform.Users().Create(ctx, &store.User{ID: owner.String(), Email: owner.String() + "@example.test", DisplayName: "Owner", Status: "active"}); err != nil {
					t.Fatal(err)
				}
				if err := platform.Organizations().AddMember(ctx, owner.String(), org.ID, "member"); err != nil {
					t.Fatal(err)
				}
				if err := ods.Teams().AddMember(ctx, &store.TeamMembership{UserID: owner.String(), TeamID: team.ID, Role: "member"}); err != nil {
					t.Fatal(err)
				}
				ctx, err = execution.WithPrincipal(ctx, execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: owner.String(), OrgSlug: org.Slug, TeamSlug: team.Slug, Authenticated: true})
				if err != nil {
					t.Fatal(err)
				}
				ctx = store.WithTenantContext(ctx, &store.TenantContext{UserID: owner.String(), OrgSlug: org.Slug, TeamSlug: team.Slug})
				repo := ods.ForTeam(team.Slug).Bots()
				dbPath := filepath.Join(dir, "orgs", org.Slug, "teams", team.Slug+".db")
				if scope == store.BotScopePersonal {
					repo = ods.ForUser(owner.String()).Bots()
					dbPath = filepath.Join(dir, "orgs", org.Slug, "personal", owner.String()+".db")
				}
				version := store.DefaultBotVersion()
				version.Charter = "Observe and report"
				b, err := repo.CreateDraft(ctx, store.BotCreateRequest{Name: "Notification acceptance", OwnerID: owner, Version: version, IdempotencyKey: "fixture"})
				if err != nil {
					t.Fatal(err)
				}
				target := store.BotChannelBinding{ID: uuid.New(), Channel: "telegram", ChatID: "123456"}
				linkOwner := owner
				if scenario == "foreign-workspace" || scenario == "foreign-chat" {
					linkOwner = uuid.New()
					otherOrg := &store.Organization{ID: uuid.NewString(), Slug: "other-org", Name: "Other", DBName: "other_org", Status: "active"}
					if err := platform.Organizations().Create(ctx, otherOrg); err != nil {
						t.Fatal(err)
					}
					if err := platform.Users().Create(ctx, &store.User{ID: linkOwner.String(), Email: "other@example.test", DisplayName: "Other", Status: "active"}); err != nil {
						t.Fatal(err)
					}
					if err := platform.Organizations().AddMember(ctx, linkOwner.String(), otherOrg.ID, "member"); err != nil {
						t.Fatal(err)
					}
					if scenario == "foreign-workspace" {
						target.Channel, target.ChatID = "slack", "TOTHER:COTHER"
					}
				}
				externalID := target.ChatID
				if target.Channel == "slack" {
					externalID = "TOTHER:UOTHER"
				}
				if err := platform.UserChannels().Link(ctx, &store.UserChannel{ID: target.ID.String(), UserID: linkOwner.String(), ChannelType: target.Channel, ExternalID: externalID, Enabled: true, Verified: true}); err != nil {
					t.Fatal(err)
				}
				if linkOwner != owner {
					_, err := repo.(store.BotConfigurationStore).ConfigureReviewed(ctx, store.BotConfigurationRequest{BotID: b.ID, ExpectedRevision: b.Revision, ExpectedVersion: b.Version, Reviewed: true, Reason: "reviewed foreign target", Autonomy: version.Autonomy, Budget: version.Budget, Delivery: store.DeliveryPolicy{Notify: store.NotifyAlways, Bindings: []store.BotChannelBinding{target}}})
					if !errors.Is(err, store.ErrBotForbidden) {
						t.Fatalf("foreign configuration: %v", err)
					}
				}
				fixtureDB, err := sql.Open("sqlite", dbPath)
				if err != nil {
					t.Fatal(err)
				}
				defer fixtureDB.Close()
				if _, err := fixtureDB.ExecContext(ctx, "UPDATE bots SET status = 'active' WHERE id = ?", b.ID); err != nil {
					t.Fatal(err)
				}
				run, err := repo.RequestRun(ctx, store.BotRunRequest{BotID: b.ID, Version: 1, Trigger: store.TriggerManual, IdempotencyKey: "run", Instructions: "Observe"})
				if err != nil {
					t.Fatal(err)
				}
				approved, err := repo.GetVersion(ctx, b.ID, 1)
				if err != nil {
					t.Fatal(err)
				}
				approved.Delivery = store.DeliveryPolicy{Notify: store.NotifyOnChange, Bindings: []store.BotChannelBinding{target}}
				report := store.BotReport{Summary: "Observed outcome", Changed: []store.BotEvidence{{Summary: "A"}, {Summary: "B"}}}
				now := time.Now().UTC()
				var goal *store.BotGoal
				switch scenario {
				case "nochange":
					report.Changed = nil
					report.Unchanged = []store.BotEvidence{{Summary: "Stable"}}
				case "threshold-below":
					report.Changed = report.Changed[:1]
					approved.Delivery.Preferences = `{"min_changed":2}`
				case "threshold":
					approved.Delivery.Preferences = `{"min_changed":2}`
				case "defer-once", "completed-goal", "revoked-binding":
					approved.Delivery.Preferences = `{"defer_seconds":60}`
					goal, err = repo.SaveGoal(ctx, store.BotGoalUpdate{BotID: b.ID, Goal: store.BotGoal{BotID: b.ID, Title: "Watch", CompletionCriteria: "Owner confirms", Status: store.BotGoalActive}})
					if err != nil {
						t.Fatal(err)
					}
					approved.Triggers = []store.TriggerSpec{{ID: run.TriggerID, GoalID: &goal.ID}}
				}
				raw, err := json.Marshal(approved)
				if err != nil {
					t.Fatal(err)
				}
				hash := sha256.Sum256(raw)
				if _, err := fixtureDB.ExecContext(ctx, "UPDATE bot_versions SET definition = ?, content_hash = ? WHERE bot_id = ? AND version = 1", string(raw), hex.EncodeToString(hash[:]), b.ID); err != nil {
					t.Fatal(err)
				}
				worker := execution.WithBotRunIdentity(ctx, execution.BotRunIdentity{BotID: b.ID, RunID: run.ID, Version: 1, ActorID: owner.String(), OrgSlug: org.Slug, TeamSlug: team.Slug})
				lease, err := repo.ClaimRun(worker, store.BotClaimRequest{BotID: b.ID, RunID: run.ID, WorkerID: "acceptance", ExpiresAt: now.Add(time.Minute)})
				if err != nil {
					t.Fatal(err)
				}
				if err := repo.CommitRun(worker, store.BotRunCommit{Lease: lease, Status: store.RunSucceeded, Report: notificationReviewedReport(t, worker, repo, lease, report)}); err != nil {
					t.Fatal(err)
				}
				var calls atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var q bot.NotificationRequest
					if err := json.NewDecoder(r.Body).Decode(&q); err != nil {
						t.Error(err)
						w.WriteHeader(400)
						return
					}
					if q.Target != target || q.ID == uuid.Nil || q.Body == "" {
						t.Errorf("incorrect exact-target request %+v", q)
					}
					calls.Add(1)
					receipt := bot.NotificationReceipt{ProviderID: "positive-receipt"}
					if scenario == "ambiguous" {
						receipt = bot.NotificationReceipt{Uncertain: true}
					}
					_ = json.NewEncoder(w).Encode(receipt)
				}))
				defer server.Close()
				publish := func() *store.BotPublication {
					t.Helper()
					// Reconstruct Publisher for each retry: deduplication must be durable.
					publisher := bot.Publisher{Store: repo.(store.BotPublicationStore), Notifier: notificationHTTPTransport{server.URL, server.Client()}, Now: func() time.Time { return now }}
					p, err := publisher.Publish(ctx, b.ID, run.ID)
					if err != nil {
						t.Fatal(err)
					}
					return p
				}
				first := publish()
				if scenario == "defer-once" || scenario == "completed-goal" || scenario == "revoked-binding" {
					if calls.Load() != 0 || first.Notifications[0].State != store.BotNotificationDeferred {
						t.Fatal("not deferred")
					}
					publish()
					now = now.Add(59 * time.Second)
					publish()
					if calls.Load() != 0 {
						t.Fatal("sent before due")
					}
					if scenario == "revoked-binding" {
						if err := platform.UserChannels().Unlink(ctx, target.ID.String()); err != nil {
							t.Fatal(err)
						}
					}
					if scenario == "completed-goal" {
						_, err := repo.CompleteGoal(ctx, store.BotGoalCompletion{BotID: b.ID, GoalID: goal.ID, ExpectedRevision: goal.Revision, ActorID: owner, Evidence: []store.BotEvidence{{Kind: store.BotEvidenceOwnerConfirmation, Summary: "Complete", Source: "owner", ObservedAt: time.Now().Add(-time.Second), ActorID: owner}}})
						if err != nil {
							t.Fatal(err)
						}
					}
					now = now.Add(time.Second)
				}
				final := publish()
				now = now.Add(24 * time.Hour)
				publish()
				wantCalls, wantState := int32(1), store.BotNotificationDelivered
				if scenario == "nochange" || scenario == "threshold-below" || scenario == "completed-goal" || scenario == "revoked-binding" || scenario == "foreign-workspace" || scenario == "foreign-chat" {
					wantCalls, wantState = 0, store.BotNotificationSkipped
				}
				if scenario == "ambiguous" {
					wantState = store.BotNotificationUncertain
				}
				if calls.Load() != wantCalls || final.Notifications[0].State != wantState || final.EventID != first.EventID {
					t.Fatalf("calls=%d want=%d publication=%+v", calls.Load(), wantCalls, final)
				}
				var count int
				if err := fixtureDB.QueryRowContext(ctx, "SELECT COUNT(*) FROM session_events WHERE session_id = ?", b.HomeSessionID.String()).Scan(&count); err != nil {
					t.Fatal(err)
				}
				if count != 1 {
					t.Fatal(fmt.Sprintf("home report count=%d", count))
				}
			})
		}
	}
}
