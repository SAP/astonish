package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/execution"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/entstore"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
)

// Real HTTP and routed SQLite stores; authentication and trusted observation
// ingestion are controlled fixtures, not mocks of the mutation implementation.
func TestBotMemoryAttributedUpdateHTTP(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	base, db, err := entstore.NewPlatformServices(ctx, entstore.Config{DSN: filepath.Join(dir, "platform.db"), DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	org := &store.Organization{ID: uuid.NewString(), Slug: "update-guard", Name: "Guard", Status: "active"}
	if err := db.Organizations().Create(ctx, org); err != nil {
		t.Fatal(err)
	}
	if err := db.ProvisionOrg(ctx, org.ID, org.Slug); err != nil {
		t.Fatal(err)
	}
	ods, err := db.ForOrg(org.Slug)
	if err != nil {
		t.Fatal(err)
	}
	team := &store.Team{ID: uuid.NewString(), Slug: "team-a", Name: "Team", SchemaName: "team_team-a"}
	if err := ods.Teams().CreateTeam(ctx, team); err != nil {
		t.Fatal(err)
	}
	owner := uuid.NewString()
	if err := db.Users().Create(ctx, &store.User{ID: owner, Email: owner + "@example.test", DisplayName: "Owner", Status: "active"}); err != nil {
		t.Fatal(err)
	}
	if err := db.Organizations().AddMember(ctx, owner, org.ID, "member"); err != nil {
		t.Fatal(err)
	}
	if err := ods.Teams().AddMember(ctx, &store.TeamMembership{UserID: owner, TeamID: team.ID, Role: "member"}); err != nil {
		t.Fatal(err)
	}
	router := mux.NewRouter()
	RegisterRoutes(router, base, db, entstore.TenantMiddleware(db))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := execution.WithPrincipal(r.Context(), execution.Principal{Kind: execution.PrincipalKindUser, Authentication: execution.AuthMethodPlatformJWT, Surface: execution.SurfaceStudio, Subject: owner, OrgSlug: org.Slug, TeamSlug: team.Slug, Authenticated: true})
		if err != nil {
			http.Error(w, "fixture", 500)
			return
		}
		c = context.WithValue(c, platformUserKey{}, &PlatformUser{ID: owner, OrgSlug: org.Slug, TeamSlug: team.Slug, Role: "member"})
		c = store.WithTenantContext(c, &store.TenantContext{UserID: owner, OrgSlug: org.Slug, TeamSlug: team.Slug})
		router.ServeHTTP(w, r.WithContext(c))
	}))
	defer server.Close()
	request := func(t *testing.T, method, path, body string, status int) []byte {
		t.Helper()
		req, err := http.NewRequest(method, server.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		res, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		data, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatal(err)
		}
		if res.StatusCode != status {
			t.Fatalf("%s %s: %d want %d: %s", method, path, res.StatusCode, status, data)
		}
		return data
	}
	for _, scope := range []string{"personal", "team"} {
		t.Run(scope, func(t *testing.T) {
			suffix := "?scope=" + scope
			var b store.Bot
			if err := json.Unmarshal(request(t, "POST", "/api/studio/bots"+suffix, fmt.Sprintf(`{"name":"Guard","idempotency_key":%q,"definition":{"charter":"Observe"}}`, uuid.NewString()), 201), &b); err != nil {
				t.Fatal(err)
			}
			path := "/api/studio/bots/" + b.ID.String()
			var memories store.MemoryStore
			if scope == "personal" {
				memories = ods.ForUser(owner).Memories()
			} else {
				memories = ods.ForTeam(team.Slug).Memories()
			}
			now := time.Now().UTC().Truncate(time.Second)
			attr := &store.MemoryAttribution{BotID: b.ID, Source: "receipt:observed", ObservedAt: now, Status: store.MemoryKnowledgeVerified, Evidence: &store.BotEvidence{Kind: store.BotEvidenceObservation, Source: "receipt:observed", Summary: "Observed original", ObservedAt: now, ActorID: b.OwnerID}}
			if err := memories.Add(ctx, store.MemoryEntry{SessionID: b.HomeSessionID.String(), CreatedBy: owner, Content: "observed original", Category: "fact", Attribution: attr}); err != nil {
				t.Fatal(err)
			}
			var listed struct {
				Memories []store.MemorySearchResult `json:"memories"`
			}
			if err := json.Unmarshal(request(t, "GET", path+"/memory"+suffix, "", 200), &listed); err != nil {
				t.Fatal(err)
			}
			if len(listed.Memories) != 1 {
				t.Fatal("missing observation")
			}
			before := listed.Memories[0]
			entry := path + "/memory/" + before.ID + suffix
			request(t, "PUT", entry, `{"content":"unobserved replacement","category":"changed"}`, 409)
			get := func() store.MemorySearchResult {
				var row store.MemorySearchResult
				if err := json.Unmarshal(request(t, "GET", entry, "", 200), &row); err != nil {
					t.Fatal(err)
				}
				return row
			}
			if after := get(); !reflect.DeepEqual(after, before) {
				t.Fatalf("rejected update changed observation: %+v", after)
			}
			for _, field := range []string{`"attribution":null`, `"metadata":{}`, `"session_id":"forged"`} {
				request(t, "PUT", entry, `{"content":"observed original",`+field+`}`, 400)
			}
			request(t, "PUT", entry, `{"content":"observed original","category":"reviewed"}`, 204)
			categorized := get()
			expected := before
			expected.Category = "reviewed"
			if !reflect.DeepEqual(categorized, expected) {
				t.Fatalf("category edit changed evidence/scope: %+v", categorized)
			}
			q := store.BotMemoryReview{ID: uuid.New(), ExpectedRevision: b.Revision, Reason: "review replacement", Confirmed: true, Edits: []store.BotMemoryReviewEdit{{SessionID: b.HomeSessionID.String(), Content: "reviewed replacement", Sources: []store.BotMemoryReviewSource{{ID: categorized.ID, SessionID: categorized.SessionID, ExpectedFingerprint: store.MemoryContentFingerprint(categorized)}}}}}
			body, err := json.Marshal(q)
			if err != nil {
				t.Fatal(err)
			}
			var receipt store.BotMemoryReviewReceipt
			if err := json.Unmarshal(request(t, "POST", path+"/documents/MEMORY.md/ingest"+suffix, string(body), 200), &receipt); err != nil {
				t.Fatal(err)
			}
			saved := get()
			if saved.Snippet != before.Snippet || saved.Attribution.Status != store.MemoryKnowledgeVerified || !reflect.DeepEqual(saved.Attribution.Evidence, attr.Evidence) || saved.Attribution.SupersededBy == "" {
				t.Fatalf("review lost original: %+v", saved)
			}
			var replacement store.MemorySearchResult
			if err := json.Unmarshal(request(t, "GET", path+"/memory/"+receipt.ReplacementIDs[0]+suffix, "", 200), &replacement); err != nil {
				t.Fatal(err)
			}
			if replacement.Snippet != "reviewed replacement" || replacement.Attribution.Status != store.MemoryKnowledgeInferred || replacement.Attribution.Evidence != nil {
				t.Fatalf("replacement inherited evidence: %+v", replacement)
			}
			request(t, "POST", path+"/memory"+suffix, fmt.Sprintf(`{"session_id":%q,"content":"ordinary"}`, b.HomeSessionID.String()), 201)
			if err := json.Unmarshal(request(t, "GET", path+"/memory"+suffix, "", 200), &listed); err != nil {
				t.Fatal(err)
			}
			for _, row := range listed.Memories {
				if row.Snippet == "ordinary" {
					request(t, "PUT", path+"/memory/"+row.ID+suffix, `{"content":"ordinary changed"}`, 204)
					return
				}
			}
			t.Fatal("ordinary memory missing")
		})
	}
}
