package api_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SAP/astonish/pkg/api"
	"github.com/SAP/astonish/pkg/store"
	"github.com/go-rod/rod"
	browserlauncher "github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
	"github.com/google/uuid"
	"github.com/gorilla/mux"
	"google.golang.org/adk/model"
	adksession "google.golang.org/adk/session"
	"google.golang.org/genai"
)

// Controlled retained filesystem, production HTTP handlers and built Studio. No
// DOM mocking, worker polling, model invocation, daemon restart or live data.
type browserWorkspaceFiles struct {
	store.BotFiles
	root            string
	lists, previews atomic.Int64
}

func (f *browserWorkspaceFiles) Stat(ctx context.Context, _ store.BotID, p string) (*store.BotFileEntry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if filepath.Base(p) != p {
		return nil, store.ErrBotForbidden
	}
	st, err := os.Stat(filepath.Join(f.root, p))
	if err != nil {
		return nil, err
	}
	return &store.BotFileEntry{Path: p, Kind: store.BotFileRegular, SizeBytes: st.Size(), Freshness: fmt.Sprint(st.ModTime().UnixNano())}, nil
}
func (f *browserWorkspaceFiles) List(ctx context.Context, id store.BotID, p, c string, n int) (*store.BotFilePage, error) {
	f.lists.Add(1)
	if p != "" || c != "" {
		return nil, store.ErrBotNotFound
	}
	out := &store.BotFilePage{Entries: []store.BotFileEntry{}}
	for _, name := range []string{"MEMORY.md", "report.md", "large.bin", "page.html", ".notes.md", ".env", ".stage-incomplete"} {
		e, err := f.Stat(ctx, id, name)
		if err != nil {
			return nil, err
		}
		out.Entries = append(out.Entries, *e)
	}
	return out, nil
}

type browserSectionReader struct {
	io.Reader
	io.Closer
}

func (f *browserWorkspaceFiles) Open(ctx context.Context, id store.BotID, p string, r store.BotFileRange) (*store.BotFileStream, error) {
	e, err := f.Stat(ctx, id, p)
	if err != nil {
		return nil, err
	}
	h, err := os.Open(filepath.Join(f.root, p))
	if err != nil {
		return nil, err
	}
	length := e.SizeBytes - r.Offset
	if r.Length > 0 {
		length = r.Length
	}
	return &store.BotFileStream{Entry: *e, ReadCloser: browserSectionReader{io.NewSectionReader(h, r.Offset, length), h}}, nil
}

// The public publication receipt is controlled in this browser tier; worker and
// transaction provenance are proved by separate production publication tests.
type browserPublicationRepo struct {
	store.BotStore
	publication store.BotFilePublication
}

func (r browserPublicationRepo) ReadFilePublication(ctx context.Context, b store.BotID, run store.RunID, seq int64) (*store.BotFilePublication, error) {
	if _, err := r.BotStore.Get(ctx, b); err != nil {
		return nil, err
	}
	if b != r.publication.BotID || run != r.publication.RunID || seq != r.publication.ReportSequence {
		return nil, store.ErrBotNotFound
	}
	p := r.publication
	return &p, nil
}
func TestBotFileWorkspaceBrowserAcceptance(t *testing.T) {
	if os.Getenv("ASTONISH_MEMORY_BROWSER_ACCEPTANCE") != "1" {
		t.Skip("set ASTONISH_MEMORY_BROWSER_ACCEPTANCE=1 after building Studio")
	}
	bin := os.Getenv("ASTONISH_BOT_BROWSER_PATH")
	if bin == "" {
		t.Fatal("explicit browser executable required")
	}
	var pkg struct{ Version string }
	raw, err := os.ReadFile("../../web/package.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &pkg); err != nil {
		t.Fatal(err)
	}
	dist, err := filepath.Abs(filepath.Join("../../web/dist", pkg.Version))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(dist, "index.html")); err != nil {
		t.Fatal("build Studio first", err)
	}
	for _, scope := range []store.BotScope{store.BotScopePersonal, store.BotScopeTeam} {
		t.Run(string(scope), func(t *testing.T) {
			f := newInteractiveHTTPFixture(t, "", nil, []string{"chat", "tool:execute"}, scope)
			files := &browserWorkspaceFiles{root: t.TempDir()}
			large := bytes.Repeat([]byte{0, 255, 19, 42}, (17<<20)/4)
			for name, data := range map[string][]byte{".notes.md": []byte("# Hidden notes"), ".env": []byte("SECRET=never-public"), ".stage-incomplete": []byte("partial"), "MEMORY.md": []byte("# Compact memory\nSee report.md."), "report.md": []byte("# Retained report\nSource on disk."), "page.html": []byte("<script>window.executedFile=true</script>"), "large.bin": large} {
				if err := os.WriteFile(filepath.Join(files.root, name), data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			runID := uuid.New()
			envelope := store.BotFilePublication{SchemaVersion: 1, BotID: f.b.ID, RunID: runID, HomeSessionID: f.b.HomeSessionID, HomeID: f.b.Home.ID, ReportSequence: 1, EventID: store.BotPublicationEventID(runID, 1), Summary: "Saved file delivery.", Artifacts: []store.BotPublishedArtifact{{ID: uuid.New(), HomeID: f.b.Home.ID, RelativePath: "large.bin", SHA256: fmt.Sprintf("%x", sha256.Sum256(large)), SizeBytes: int64(len(large)), MediaType: "application/octet-stream", Preview: store.BotFilePreviewDownload}}}
			server := generationHealthBrowserServer(t, f, dist, func(routes *mux.Router, svc *store.Services) {
				if scope == store.BotScopeTeam {
					svc.Bots = browserPublicationRepo{svc.Bots, envelope}
				} else {
					svc.PersonalBots = browserPublicationRepo{svc.PersonalBots, envelope}
				}
				routes.HandleFunc("/api/studio/bots/{botID}/sessions/{sessionID}/events", api.BotEventsHandler).Methods("GET")
				ss := svc.PersonalSessions
				if scope == store.BotScopeTeam {
					ss = svc.Sessions
				}
				event := &adksession.Event{ID: envelope.EventID, InvocationID: "bot-report:" + envelope.RunID.String(), Author: "assistant", Timestamp: time.Now(), LLMResponse: model.LLMResponse{Content: genai.NewContentFromText(envelope.Summary, "model")}}
				event.Actions.StateDelta = map[string]any{store.BotFilePublicationKey: envelope}
				if err := ss.AppendFleetEvent(f.ctx, f.b.HomeSessionID.String(), event); err != nil {
					t.Fatal(err)
				}
				svc.BotFilesFactory = func(context.Context, *store.Services, store.BotScope) (store.BotFiles, error) { return files, nil }
				root := "/api/studio/bots/{botID}/files"
				routes.HandleFunc(root, api.BotFilesHandler).Methods("GET")
				routes.HandleFunc(root+"/stat", api.BotFileStatHandler).Methods("GET")
				routes.HandleFunc(root+"/preview", func(w http.ResponseWriter, r *http.Request) { files.previews.Add(1); api.BotFilePreviewHandler(w, r) }).Methods("GET")
				routes.HandleFunc(root+"/download-ticket", api.BotFileDownloadTicketHandler).Methods("POST")
				routes.HandleFunc(root+"/download", api.BotFileDownloadHandler).Methods("GET", "HEAD")
			})
			launch := browserlauncher.New().Bin(bin).UserDataDir(t.TempDir()).Headless(true).Leakless(false)
			control, err := launch.Launch()
			if err != nil {
				t.Fatal(err)
			}
			defer launch.Cleanup()
			browser := rod.New().ControlURL(control).MustConnect()
			defer browser.Close()
			downloads := t.TempDir()
			if err := (proto.BrowserSetDownloadBehavior{Behavior: proto.BrowserSetDownloadBehaviorBehaviorAllow, DownloadPath: downloads}).Call(browser); err != nil {
				t.Fatal(err)
			}
			page := browser.MustPage(server.URL + "/#/chat/bots/" + string(scope) + "/" + f.b.ID.String()).Timeout(40 * time.Second)
			page = page.WithPanic(func(v any) {
				text, _ := page.CancelTimeout().Eval(`()=>document.body.innerText`)
				t.Fatalf("browser: %v\n%v", v, text)
			})
			page.MustElement(`[aria-label="Bot workspace"]`).MustVisible()
			page.MustElement(`[data-publication-id="` + envelope.EventID + `"]`).MustVisible()
			if files.lists.Load() != 0 {
				t.Fatal("closed Details eagerly listed files")
			}
			open := func() {
				page.MustElementR("button", "^Details$").MustClick()
				page.MustElement(`[aria-label="Bot files"]`).MustVisible()
				page.MustElementR("button", "^report.md$").MustVisible()
			}
			open()
			if page.MustEval(`()=>[...document.querySelectorAll('[aria-label="Bot files"] button')].some(b=>b.textContent==='.notes.md')`).Bool() {
				t.Fatal("hidden by default")
			}
			page.MustElement(`[aria-label="Bot files"] input[type="checkbox"]`).MustClick()
			page.MustElementR("button", "^\\.notes.md$").MustClick()
			page.MustElementR("h1", "^Hidden notes$").MustVisible()
			if page.MustEval(`()=>[...document.querySelectorAll('[aria-label="Bot files"] button')].some(b=>['.env','.stage-incomplete'].includes(b.textContent))`).Bool() {
				t.Fatal("private hidden files leaked")
			}
			page.MustElement(`[aria-label="Bot files"] input[type="checkbox"]`).MustClick()
			page.MustElementR("button", "^report.md$").MustClick()
			page.MustElementR("h1", "^Retained report$").MustVisible()
			page.MustElementR("button", "^Close preview$").MustClick()
			page.MustElementR("button", "^page.html$").MustClick()
			page.MustElementR("pre", "window.executedFile").MustVisible()
			if page.MustEval(`()=>window.executedFile===true`).Bool() {
				t.Fatal("executed file HTML")
			}
			page.MustElementR("button", "^Close preview$").MustClick()
			before := files.previews.Load()
			page.MustElementR("button", "^large.bin$").MustClick()
			page.MustElementR("div", "^Preview unavailable. Download this file.$").MustVisible()
			if files.previews.Load() != before {
				t.Fatal("binary preview request")
			}
			page.MustElement(`[aria-label="File preview"]`).MustElementR("button", "^Download$").MustClick()
			page.MustElementR("button", "^Download File$").MustClick()
			deadline := time.Now().Add(15 * time.Second)
			var downloaded []byte
			for time.Now().Before(deadline) {
				downloaded, err = os.ReadFile(filepath.Join(downloads, "large.bin"))
				if err == nil && len(downloaded) == len(large) {
					break
				}
				time.Sleep(50 * time.Millisecond)
			}
			if !bytes.Equal(downloaded, large) {
				t.Fatalf("native binary mismatch: %d bytes %v", len(downloaded), err)
			}
			page.MustElementR("button", "^Close preview$").MustClick()
			page.MustElementR("button", "^Close$").MustClick()
			page.MustReload()
			page.MustElement(`[data-publication-id="` + envelope.EventID + `"]`).MustVisible()
			if len(page.MustElements(`[data-artifact-id="`+envelope.Artifacts[0].ID.String()+`"]`)) != 1 {
				t.Fatal("duplicate restored delivery")
			}
			page.MustElement(`[aria-label="Bot workspace"]`).MustVisible()
			open()
			page.MustElementR("button", "^MEMORY.md$").MustClick()
			page.MustElementR("h1", "^Compact memory$").MustVisible()
			if strings.Contains(page.MustElement("body").MustText(), ".astonish/") {
				t.Fatal("private storage leaked")
			}
			page.MustElementR("button", "^Close preview$").MustClick()
			page.MustElementR("button", "^Close$").MustClick()
			for _, width := range []int{390, 1280} {
				for _, theme := range []string{"light", "dark"} {
					page.MustSetViewport(width, 844, 1, false)
					page.MustEval(`theme=>localStorage.setItem('astonish-theme',theme)`, theme)
					page.MustReload()
					page.MustElement(`[aria-label="Bot workspace"]`).MustVisible()
					open()
					if page.MustEval(`()=>document.documentElement.classList.contains('dark')`).Bool() != (theme == "dark") {
						t.Fatal("theme not applied")
					}
					if !page.MustEval(`()=>{const r=document.querySelector('[role="dialog"]').getBoundingClientRect();return r.left>=0&&r.right<=innerWidth&&r.top>=0&&r.bottom<=innerHeight}`).Bool() {
						t.Fatalf("Details outside viewport width=%d theme=%s", width, theme)
					}
					page.MustElement(`[aria-label="Database-backed Configuration"]`).MustVisible()
					page.MustElement(`[aria-label="Activity"]`).MustVisible()
					page.MustElementR("button", "^MEMORY.md$").MustClick()
					page.MustElementR("h1", "^Compact memory$").MustVisible()
					page.MustElementR("button", "^Close preview$").MustClick()
					page.MustElementR("button", "^Close$").MustClick()
				}
			}
			if f.model.productions != 0 || f.pool.calls != 0 {
				t.Fatal("file reads ran the Bot")
			}
			t.Logf("scope=%s built Studio lazy Details, disk Markdown, escaped HTML, native 17MiB download sha256=%x, reload, no model", scope, sha256.Sum256(downloaded))
		})
	}
}
