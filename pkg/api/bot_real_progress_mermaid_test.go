package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-rod/rod"
	browserlauncher "github.com/go-rod/rod/lib/launcher"
)

// Independent source rendering is test-only. It never alters the report in the
// Studio workspace. The module and configuration are the ones MermaidBlock uses.
func liveProgressMermaidFiles() http.Handler {
	dir, _ := filepath.Abs("../../web/node_modules/mermaid/dist")
	return http.StripPrefix("/__proof_mermaid/", http.FileServer(http.Dir(dir)))
}

const liveProgressPrepareDiagrams = `async html => {
 const root = new DOMParser().parseFromString(html, 'text/html').body;
 const blocks = [...root.querySelectorAll('pre > code.language-mermaid')];
 const diagrams = [];
 if (blocks.length) {
  const mermaid = (await import('/__proof_mermaid/mermaid.esm.min.mjs')).default;
  mermaid.initialize({startOnLoad:false, theme:'neutral', fontFamily:'-apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, "Helvetica Neue", Arial, sans-serif', securityLevel:'strict'});
  for (let i=0;i<blocks.length;i++) {
   const source = blocks[i].textContent.trim();
   const host=document.createElement('div'); host.style.cssText='position:absolute;left:-100000px;top:0;visibility:hidden'; document.body.append(host);
   try {
    const {svg} = await mermaid.render('proof-diagram-' + i, source, host);
    diagrams.push({source, svg});
   } finally { host.remove(); }
  }
 }
 window.__proofExpectedDiagrams = diagrams;
 return diagrams.map(d => ({source:d.source, svg:d.svg}));
}`

func TestLiveProgressRenderedMermaidComparison(t *testing.T) {
	if os.Getenv("ASTONISH_BROWSER_CONTEXT_TEST") != "1" {
		t.Skip("offline browser opt-in; no provider calls")
	}
	mux := http.NewServeMux()
	mux.Handle("/__proof_mermaid/", liveProgressMermaidFiles())
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("<!doctype html><body></body>")) })
	server := httptest.NewServer(mux)
	defer server.Close()
	launch := browserlauncher.New().Bin(os.Getenv("ASTONISH_BOT_BROWSER_PATH")).UserDataDir(t.TempDir()).Headless(true).Leakless(false)
	control, err := launch.Launch()
	if err != nil {
		t.Fatal(err)
	}
	defer launch.Cleanup()
	browser := rod.New().ControlURL(control).MustConnect()
	defer browser.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	page := browser.MustPage(server.URL).Context(ctx)
	html := liveProgressReportHTML(t, "## Design\n\nExact prose [source](https://example.org/design).\n\n```mermaid\nflowchart LR\n A[Agent] -->|request| P[Proxy]\n P --> U[Upstream]\n```\n\n## Decisions\n\nComplete terminal approval explanation.\n\n```mermaid\nsequenceDiagram\n participant H as Human\n participant P as Proxy\n H->>P: Approve request\n P-->>H: Receipt\n```\n\n## Limits\n\nPinned TLS needs explicit policy.")
	page.MustEval(liveProgressPrepareDiagrams, html)
	reset := func() {
		page.MustEval(`async html => {
 const mermaid=(await import('/__proof_mermaid/mermaid.esm.min.mjs')).default;
 document.body.innerHTML='<section aria-label="Bot workspace"><div class="markdown-body"><div class="markdown-body">'+html+'</div></div></section>';
 const blocks=[...document.querySelectorAll('pre > code.language-mermaid')];
 for (let i=0;i<blocks.length;i++) { const code=blocks[i]; const {svg}=await mermaid.render('mermaid-studio-'+i,code.textContent.trim()); const d=document.createElement('div');d.className='mermaid-diagram'; d.innerHTML=svg;code.parentElement.replaceWith(d); }
 }`, html)
	}
	reset()
	if got := page.MustEval(liveProgressExactReport, html).Get("count").Int(); got != 1 {
		t.Fatalf("real rendered Mermaid report rejected: %d", got)
	}
	cases := map[string]string{
		"missing section":     `()=>document.querySelector('.markdown-body p:last-child').remove()`,
		"duplicate section":   `()=>{const p=document.querySelector('.markdown-body p:last-child');p.after(p.cloneNode(true))}`,
		"missing diagram":     `()=>document.querySelector('.mermaid-diagram').remove()`,
		"duplicate diagram":   `()=>{const d=document.querySelector('.mermaid-diagram');d.after(d.cloneNode(true))}`,
		"wrong diagram order": `()=>{const ds=document.querySelectorAll('.mermaid-diagram');const html=ds[0].innerHTML;ds[0].innerHTML=ds[1].innerHTML;ds[1].innerHTML=html}`,
		"wrong label":         `()=>document.querySelector('.mermaid-diagram .nodeLabel').textContent='Wrong Agent'`,
		"missing edge":        `()=>document.querySelector('.mermaid-diagram path.flowchart-link').remove()`,
		"wrong link":          `()=>document.querySelector('.markdown-body a').href='https://example.org/wrong'`,
		"raw source fallback": `()=>document.querySelector('.mermaid-diagram').textContent=window.__proofExpectedDiagrams[0].source`,
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			reset()
			page.MustEval(mutate)
			if page.MustEval(liveProgressExactReport, html).Get("count").Int() != 0 {
				t.Fatal("corrupted report accepted")
			}
		})
	}
	reset()
	page.MustEval(`()=>{const e=document.querySelector('[aria-label="Bot workspace"] > div');e.after(e.cloneNode(true))}`)
	if page.MustEval(liveProgressExactReport, html).Get("count").Int() != 2 {
		t.Fatal("duplicate report cardinality lost")
	}
	if dir := os.Getenv("ASTONISH_SIDECAR_CAPTURE_DIR"); dir != "" {
		var run struct {
			Report struct {
				Summary string `json:"summary"`
			} `json:"report"`
		}
		raw, err := os.ReadFile(filepath.Join(dir, "run.json"))
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, &run); err != nil {
			t.Fatal(err)
		}
		html = liveProgressReportHTML(t, run.Report.Summary)
		page.MustEval(liveProgressPrepareDiagrams, html)
		reset()
		if page.MustEval(liveProgressExactReport, html).Get("count").Int() != 1 {
			t.Fatal("complete captured source with independently transformed diagrams rejected")
		}
		t.Log("captured full design passes offline rendered comparison; not live acceptance")
	}
}
