package api

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/config"
	"github.com/SAP/astonish/pkg/store"
	"github.com/SAP/astonish/pkg/store/entstore"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
)

// Exercise the actual auth -> RegisterRoutes -> TenantMiddleware -> scoped
// repository chain. No test middleware injects a principal or services.
func TestBotFileDeliveryHTTPNativeAuthorization(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	base, db, err := entstore.NewPlatformServices(ctx, entstore.Config{DSN: filepath.Join(dir, "platform.db"), DataDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	org := &store.Organization{ID: uuid.NewString(), Slug: "download-test", Name: "Download", Status: "active"}
	must(db.Organizations().Create(ctx, org))
	must(db.ProvisionOrg(ctx, org.ID, org.Slug))
	ods, err := db.ForOrg(org.Slug)
	must(err)
	team := &store.Team{ID: uuid.NewString(), Slug: "downloads", Name: "Downloads", SchemaName: "team_downloads"}
	must(ods.Teams().CreateTeam(ctx, team))
	owner := uuid.NewString()
	user := &store.User{ID: owner, Email: owner + "@example.test", DisplayName: "Owner", Status: "active"}
	must(db.Users().Create(ctx, user))
	must(db.Organizations().AddMember(ctx, owner, org.ID, "member"))
	must(ods.Teams().AddMember(ctx, &store.TeamMembership{UserID: owner, TeamID: team.ID, Role: "member"}))
	files := &fileDeliveryFiles{data: map[string][]byte{"report.md": []byte("# Download bytes\n")}}
	base.BotFilesFactory = func(context.Context, *store.Services, store.BotScope) (store.BotFiles, error) { return files, nil }
	pa := &PlatformAuth{jwt: NewJWTIssuer("isolated-download-test", time.Minute, time.Hour), pgStore: db, orgResolver: db, authCfg: config.PlatformAuthConfig{LoopbackBypass: "never"}}
	token, err := pa.jwt.IssueAccessToken(owner, user.Email, user.DisplayName, org.Slug, team.Slug, "member", "")
	must(err)
	router := mux.NewRouter()
	RegisterRoutes(router, base, db, entstore.TenantMiddleware(db))
	server := httptest.NewServer(PlatformAuthMiddleware(pa, router))
	defer server.Close()
	request := func(method, target, bearer string, cookie *http.Cookie, headers map[string]string, body string, status int) (*http.Response, []byte) {
		t.Helper()
		req, err := http.NewRequest(method, server.URL+target, strings.NewReader(body))
		must(err)
		req.Header.Set("Content-Type", "application/json")
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		if cookie != nil {
			req.AddCookie(cookie)
		}
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		res, err := server.Client().Do(req)
		must(err)
		data, err := io.ReadAll(res.Body)
		res.Body.Close()
		must(err)
		if res.StatusCode != status {
			t.Fatalf("%s %s: %d want %d: %s", method, target, res.StatusCode, status, data)
		}
		return res, data
	}
	_, raw := request("POST", "/api/studio/bots", token, nil, nil, `{"name":"Download bot","definition":{"charter":"Save files"},"idempotency_key":"download-bot"}`, 201)
	var b store.Bot
	must(json.Unmarshal(raw, &b))
	if b.ID == uuid.Nil {
		t.Fatalf("create returned %s", raw)
	}
	root := "/api/studio/bots/" + b.ID.String() + "/files"
	request("GET", root+"/download?path=report.md", "", nil, nil, "", 401)
	res, raw := request("POST", root+"/download-ticket?path=report.md&token=never-return-this", token, nil, map[string]string{"X-Astonish-Team": team.Slug}, "", 200)
	var ticket struct {
		URL string `json:"url"`
	}
	must(json.Unmarshal(raw, &ticket))
	if strings.Contains(ticket.URL, "token") || strings.Contains(ticket.URL, token) {
		t.Fatal("credential URL")
	}
	cookies := res.Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || cookies[0].Path != root+"/download" || cookies[0].SameSite != http.SameSiteStrictMode {
		t.Fatal("unsafe cookie")
	}
	cookie := cookies[0]
	nav := map[string]string{"Sec-Fetch-Mode": "navigate"}
	_, raw = request("GET", ticket.URL, "", cookie, nav, "", 200)
	if string(raw) != "# Download bytes\n" {
		t.Fatal("native download bytes")
	}
	request("HEAD", ticket.URL, "", cookie, nav, "", 200)
	request("GET", ticket.URL, "", cookie, map[string]string{"Range": "bytes=2-5"}, "", 206)
	request("GET", ticket.URL, "invalid", cookie, nav, "", 401)
	request("GET", ticket.URL+"&scope=team", "", cookie, nav, "", 401)
	request("GET", strings.Replace(ticket.URL, "report.md", "other.md", 1), "", cookie, nav, "", 401)
	request("GET", root+"/stat?path=report.md", "", cookie, nil, "", 401)
	request("POST", ticket.URL, "", cookie, nil, "", 401)
	request("POST", root+"/download-ticket?path=report.md", "", cookie, nil, "", 401)
	request("GET", ticket.URL, "", cookie, map[string]string{"Sec-Fetch-Site": "cross-site"}, "", 401)
	request("GET", ticket.URL, "", nil, nav, "", 401)
	otherToken, err := pa.jwt.IssueAccessToken(uuid.NewString(), "", "", org.Slug, team.Slug, "member", "")
	must(err)
	request("GET", ticket.URL, otherToken, cookie, nav, "", 401)
	user.Status = "suspended"
	must(db.Users().Update(ctx, user))
	request("GET", ticket.URL, "", cookie, nav, "", 403)
	user.Status = "active"
	must(db.Users().Update(ctx, user))
	org.Status = "suspended"
	must(db.Organizations().Update(ctx, org))
	request("GET", ticket.URL, "", cookie, nav, "", 403)
	org.Status = "active"
	must(db.Organizations().Update(ctx, org))
	// A previously issued admin/member cookie cannot survive directory revocation,
	// even if the original access token is also still valid.
	must(ods.Teams().RemoveMember(ctx, owner, team.ID))
	request("GET", ticket.URL, "", cookie, nav, "", 403)
	request("GET", ticket.URL, token, cookie, nav, "", 403)
	must(ods.Teams().AddMember(ctx, &store.TeamMembership{UserID: owner, TeamID: team.ID, Role: "member"}))
	request("GET", ticket.URL, "", cookie, nav, "", 200)
	must(db.Organizations().RemoveMember(ctx, owner, org.ID))
	request("GET", ticket.URL, "", cookie, nav, "", 403)
	must(db.Organizations().AddMember(ctx, owner, org.ID, "member"))
	key := sha256.Sum256([]byte(cookie.Value))
	botFileTickets.Lock()
	expired := botFileTickets.items[key]
	expired.expires = time.Now().Add(-time.Second)
	botFileTickets.items[key] = expired
	botFileTickets.Unlock()
	request("GET", ticket.URL, "", cookie, nav, "", 401)
	if files.opens.Load() != files.closed.Load() {
		t.Fatal("stream leaked")
	}
	t.Log("Production platform auth and tenant route chain: bearer-issued HttpOnly ticket streams GET/HEAD/range without bearer; exact object/method/expiry and live membership revocation enforced")
}
