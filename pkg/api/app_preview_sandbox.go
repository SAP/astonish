package api

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/SAP/astonish/web"
)

// sandboxHTMLHead is the opening portion of the sandbox HTML, before scripts.
const sandboxHTMLHead = `<!DOCTYPE html>
<html>
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<!-- Restrictive CSP: only inline scripts (for our runtime) and inline styles
     (for Tailwind). No connect-src, no fetch, no external resources.
     All data flows through postMessage to the parent. -->
<meta http-equiv="Content-Security-Policy" content="default-src 'none'; script-src 'unsafe-inline' 'unsafe-eval'; style-src 'unsafe-inline'; img-src data: blob:; font-src data:;">
<script>
window.__runtimeError = null;
window.onerror = function(msg, src, line, col, err) {
  window.__runtimeError = msg + ' (at ' + (src||'inline') + ':' + line + ':' + col + ')';
};
</script>`

// sandboxHTMLBody is the HTML after all <script> tags have been inserted.
// It contains the styles, DOM, and the application bootstrap script.
const sandboxHTMLBody = `
<style type="text/tailwindcss">
  /* Astonish App Canvas — always dark. Defaults = Nova; parent postMessage
     { type:'theme', pack, tokens } overrides CSS vars when brand packs change.
     Generated apps use: bg-surface, text-app, border-app-border, bg-brand, … */
  @theme {
    --color-app-canvas: #160b1f;
    --color-surface: #1d0f28;
    --color-surface-2: #100816;
    --color-app: #fceef7;
    --color-app-muted: #b49bc3;
    --color-app-border: rgba(240, 220, 255, 0.10);
    --color-brand: #ff6b9d;
    --color-brand-strong: #ffb86b;
    --color-accent3: #b478ff;
    --color-chart-grid: #3d2450;
  }
</style>
<style>
  * { box-sizing: border-box; }
  html, body { margin: 0; padding: 0; font-family: system-ui, -apple-system, sans-serif; }
  body { padding: 16px; color: var(--color-app, #fceef7); }
  /* App Canvas is always dark — Studio light mode does not flip app interior.
     Background color is overridden by applyAppCanvas() when tokens arrive. */
  html { background: var(--color-app-canvas, #160b1f) !important; }
  #root { min-height: 20px; }
  /* Force the component's outermost element to be transparent so the App Canvas
     background shows through. LLMs frequently generate bg-gray-950, bg-black,
     min-h-screen etc. on the root container. CSS !important beats Tailwind
     utilities (which don't use !important), and unlike DOM manipulation this
     survives React re-renders. */
  #root > *:first-child {
    background-color: transparent !important;
    min-height: auto !important;
  }
  #error-display {
    padding: 12px 16px; margin: 8px; border-radius: 8px;
    background: rgba(239, 68, 68, 0.1); border: 1px solid rgba(239, 68, 68, 0.3);
    color: #ef4444; font-size: 13px; font-family: monospace; white-space: pre-wrap;
    display: none;
  }
</style>
</head>
<body class="dark" style="background: transparent;">
<div id="root"></div>
<div id="error-display"></div>
<script>
(function() {
  if (window.__runtimeError) {
    document.getElementById('error-display').style.display = 'block';
    document.getElementById('error-display').textContent = 'Runtime load error: ' + window.__runtimeError;
    window.parent.postMessage({ type: 'render_error', error: 'Runtime load error: ' + window.__runtimeError }, '*');
    return;
  }
  if (!window.Sucrase || !window.React || !window.ReactDOM) {
    var missing = [];
    if (!window.React) missing.push('React');
    if (!window.ReactDOM) missing.push('ReactDOM');
    if (!window.Sucrase) missing.push('Sucrase');
    var msg = 'Runtime globals missing: ' + missing.join(', ') + '. The sandbox runtime bundle may have failed to execute.';
    document.getElementById('error-display').style.display = 'block';
    document.getElementById('error-display').textContent = msg;
    window.parent.postMessage({ type: 'render_error', error: msg }, '*');
    return;
  }

  var RC = window.Recharts || {};
  var rechartsNames = [
    'AreaChart','Area','BarChart','Bar','LineChart','Line','PieChart','Pie','Cell',
    'RadarChart','Radar','PolarGrid','PolarAngleAxis','PolarRadiusAxis',
    'ScatterChart','Scatter','ComposedChart','RadialBarChart','RadialBar',
    'Treemap','Funnel','FunnelChart','Sankey',
    'XAxis','YAxis','ZAxis','CartesianGrid','Tooltip','Legend','ResponsiveContainer',
    'ReferenceLine','ReferenceArea','ReferenceDot','Label','LabelList',
    'Brush','ErrorBar'
  ];
  rechartsNames.forEach(function(name) {
    if (RC[name]) window[name] = RC[name];
  });

  var rootInstance = null;
  var errorEl = document.getElementById('error-display');

  /**
   * Apply brand pack App Canvas tokens (always dark).
   * tokens keys: canvas, surface, surface2, app, appMuted, appBorder,
   *              brand, brandStrong, accent3, chartGrid
   * Maps onto --color-* vars used by Tailwind @theme utilities.
   */
  function applyAppCanvas(pack, tokens) {
    document.body.className = 'dark';
    var root = document.documentElement;
    if (pack) root.setAttribute('data-theme', pack);
    var t = tokens || {};
    var map = {
      canvas: '--color-app-canvas',
      surface: '--color-surface',
      surface2: '--color-surface-2',
      app: '--color-app',
      appMuted: '--color-app-muted',
      appBorder: '--color-app-border',
      brand: '--color-brand',
      brandStrong: '--color-brand-strong',
      accent3: '--color-accent3',
      chartGrid: '--color-chart-grid'
    };
    Object.keys(map).forEach(function(key) {
      if (t[key]) root.style.setProperty(map[key], t[key]);
    });
    // Force paint even if var() resolution lags in some browsers
    if (t.canvas) root.style.background = t.canvas;
    if (t.app) document.body.style.color = t.app;
  }

  function showError(msg, stack) {
    errorEl.style.display = 'block';
    errorEl.textContent = msg + (stack ? '\n\n' + stack : '');
    window.parent.postMessage({ type: 'render_error', error: msg, stack: stack || '' }, '*');
  }

  // ── Data hooks infrastructure ──────────────────────────────────────
  // Pending request callbacks keyed by requestId
  var __dataCallbacks = {};
  // Active data subscriptions keyed by sourceId → array of setState callbacks
  var __dataSubscriptions = {};
  var __requestCounter = 0;
  // App context — set by parent via set_context message
  var __appName = '';

  function __genRequestId() {
    return 'req-' + (++__requestCounter) + '-' + Math.random().toString(36).substr(2, 6);
  }

  // Listen for data responses from parent
  window.addEventListener('message', function(e) {
    var msg = e.data;
    if (!msg || !msg.type) return;

    if (msg.type === 'data_response' || msg.type === 'action_response' || msg.type === 'ai_response' || msg.type === 'state_response') {
      var cb = __dataCallbacks[msg.requestId];
      if (cb) {
        delete __dataCallbacks[msg.requestId];
        cb(msg);
      }
    }

    if (msg.type === 'data_update') {
      var subs = __dataSubscriptions[msg.sourceId];
      if (subs) {
        subs.forEach(function(cb) { cb(msg.data, msg.error || null); });
      }
    }
  });

  // Send a data/action/ai request to parent and return a Promise.
  // timeoutMs defaults to 30s for data/action, callers can override (e.g. 120s for AI).
  function __requestFromParent(type, payload, timeoutMs) {
    var timeout = timeoutMs || 30000;
    return new Promise(function(resolve) {
      var requestId = __genRequestId();
      __dataCallbacks[requestId] = resolve;
      payload.requestId = requestId;
      payload.type = type;
      window.parent.postMessage(payload, '*');
      setTimeout(function() {
        if (__dataCallbacks[requestId]) {
          delete __dataCallbacks[requestId];
          resolve({ error: 'Request timed out after ' + (timeout / 1000) + ' seconds' });
        }
      }, timeout);
    });
  }

  // ── useAppData hook ────────────────────────────────────────────────
  // Usage: const { data, loading, error, refetch } = useAppData('mcp:server/tool', { args: { query: 'SELECT ...' }, interval: 30000 })
  // Pass null/undefined as sourceId to skip fetching (conditional data loading).
  window.useAppData = function useAppData(sourceId, options) {
    options = options || {};
    var generationRef = React.useRef(0);
    var _s1 = React.useState(null);    var data = _s1[0];    var setData = _s1[1];
    var _s2 = React.useState(sourceId ? true : false); var loading = _s2[0]; var setLoading = _s2[1];
    var _s3 = React.useState(null);    var error = _s3[0];   var setError = _s3[1];

    var fetchData = React.useCallback(function() {
      if (!sourceId) return;
      var gen = ++generationRef.current;
      setLoading(true);
      setError(null);
      __requestFromParent('data_request', {
        sourceId: sourceId,
        args: options.args || {}
      }).then(function(resp) {
        // Ignore stale responses from previous sourceId
        if (gen !== generationRef.current) return;
        if (resp.error) {
          setError(resp.error);
          setLoading(false);
        } else {
          setData(resp.data);
          setLoading(false);
        }
      });
    }, [sourceId]);

    React.useEffect(function() {
      if (!sourceId) {
        setData(null);
        setLoading(false);
        setError(null);
        return;
      }
      fetchData();

      // Subscribe for push updates
      if (!__dataSubscriptions[sourceId]) __dataSubscriptions[sourceId] = [];
      var handler = function(newData, newError) {
        if (newError) { setError(newError); } else { setData(newData); }
        setLoading(false);
      };
      __dataSubscriptions[sourceId].push(handler);

      // Request polling if interval specified
      if (options.interval && options.interval > 0) {
        window.parent.postMessage({
          type: 'data_subscribe',
          sourceId: sourceId,
          args: options.args || {},
          interval: options.interval
        }, '*');
      }

      return function() {
        // Bump generation to invalidate any in-flight requests
        generationRef.current++;
        var subs = __dataSubscriptions[sourceId];
        if (subs) {
          var idx = subs.indexOf(handler);
          if (idx >= 0) subs.splice(idx, 1);
        }
        // Unsubscribe polling
        if (options.interval && options.interval > 0) {
          window.parent.postMessage({ type: 'data_unsubscribe', sourceId: sourceId }, '*');
        }
      };
    }, [sourceId, fetchData]);

    return { data: data, loading: loading, error: error, refetch: fetchData };
  };

  // ── useAppAction hook ──────────────────────────────────────────────
  // Usage: const runQuery = useAppAction('mcp:server/tool'); const result = await runQuery({ query: '...' })
  // With timeout: const runQuery = useAppAction('mcp:server/tool', { timeout: 300000 }); // 5 min
  window.useAppAction = function useAppAction(actionId, options) {
    var actionTimeout = (options && options.timeout) || 30000;
    return React.useCallback(function(payload) {
      return __requestFromParent('action_request', {
        actionId: actionId,
        payload: payload || {}
      }, actionTimeout).then(function(resp) {
        if (resp.error) throw new Error(resp.error);
        return resp.data;
      });
    }, [actionId, actionTimeout]);
  };

  // ── useAppAI hook ──────────────────────────────────────────────────
  // Usage: const askAI = useAppAI({ system: 'You are a data analyst.' })
  //        const text = await askAI('Summarize this data', { context: myData })
  window.useAppAI = function useAppAI(options) {
    options = options || {};
    var systemPrompt = options.system || '';
    return React.useCallback(function(prompt, callOptions) {
      callOptions = callOptions || {};
      return __requestFromParent('ai_request', {
        prompt: prompt,
        system: systemPrompt,
        context: callOptions.context || null
      }, 120000).then(function(resp) {
        if (resp.error) throw new Error(resp.error);
        return resp.text;
      });
    }, [systemPrompt]);
  };

  // ── useAppState hook ───────────────────────────────────────────────
  // Provides per-app persistent SQLite database via the backend.
  // Usage:
  //   const db = useAppState();
  //   db.exec('CREATE TABLE IF NOT EXISTS items (id INTEGER PRIMARY KEY, name TEXT)');
  //   const { data, loading, error } = db.query('SELECT * FROM items');
  //   await db.exec('INSERT INTO items (name) VALUES (?)', ['New item']);
  //
  // DESIGN: db.query() is NOT a hook — it's a pure lookup into cached state.
  // This means db.query() can be called anywhere (conditionally, in helpers, etc.)
  // without violating React hooks rules. All reactive state is managed at the
  // useAppState() level via a single useState + useEffect pair.
  //
  // Global mutation counter — shared across all useAppState instances.
  var __stateVersion = { current: 0 };
  var __stateVersionListeners = [];
  var __stateDebounceTimer = null;

  // Circuit breaker: prevent infinite request loops.
  // Tracks ALL outgoing state_query/state_exec requests (initial fetches,
  // re-fetches, and exec calls). If more than 30 requests fire within 2
  // seconds, the breaker trips and blocks all further requests until the
  // page is reloaded. This catches any loop variant — whether from
  // queryFn instability, version-change cascades, or generated app bugs.
  var __requestTimestamps = [];
  var __circuitBroken = false;

  function __shouldAllowRequest() {
    if (__circuitBroken) return false;
    var now = Date.now();
    __requestTimestamps.push(now);
    // Keep only last 2 seconds
    while (__requestTimestamps.length > 0 && now - __requestTimestamps[0] > 2000) {
      __requestTimestamps.shift();
    }
    if (__requestTimestamps.length > 30) {
      __circuitBroken = true;
      console.warn('[useAppState] Circuit breaker tripped: too many requests (>30 in 2s). All state operations paused. Reload to reset.');
      return false;
    }
    return true;
  }

  function __notifyStateChange() {
    // Debounce: coalesce rapid exec() calls into a single version bump
    if (__stateDebounceTimer) return;
    __stateDebounceTimer = setTimeout(function() {
      __stateDebounceTimer = null;
      __stateVersion.current++;
      __stateVersionListeners.forEach(function(fn) { fn(__stateVersion.current); });
    }, 50);
  }

  window.useAppState = function useAppState() {
    // Helper: create a query result that IS an array (so .map/.reduce/.filter work
    // directly) but also has .data/.loading/.error properties for destructuring.
    // This lets both patterns work:
    //   const rows = db.query('...');          rows.map(...)    ← works
    //   const { data, loading } = db.query('...');  data.map(...) ← also works
    function __qr(rows, loading, error) {
      var arr = [].concat(rows || []);
      arr.data = arr;
      arr.loading = !!loading;
      arr.error = error || null;
      return arr;
    }

    // Single piece of state: cache of all query results
    // Shape: { [queryKey]: augmented-array }
    var _cache = React.useState({});
    var queryCache = _cache[0];
    var setQueryCache = _cache[1];

    // Keep a ref to the latest queryCache so queryFn can read it without
    // having queryCache as a dependency (which would recreate queryFn on
    // every cache update and cause infinite re-render loops when consumer
    // code puts db in a useEffect dependency array).
    var queryCacheRef = React.useRef(queryCache);
    queryCacheRef.current = queryCache;

    // Track registered queries: { [queryKey]: { sql, params } }
    var registeredQueries = React.useRef({});
    // Track the version we last fetched at
    var lastFetchedVersion = React.useRef(-1);

    // Subscribe to global version changes
    var _ver = React.useState(__stateVersion.current);
    var version = _ver[0];
    var setVersion = _ver[1];

    React.useEffect(function() {
      __stateVersionListeners.push(setVersion);
      return function() {
        var idx = __stateVersionListeners.indexOf(setVersion);
        if (idx >= 0) __stateVersionListeners.splice(idx, 1);
      };
    }, [setVersion]);

    // Single effect: re-fetch ALL registered queries when version changes
    React.useEffect(function() {
      var queries = registeredQueries.current;
      var keys = Object.keys(queries);
      if (keys.length === 0) return;
      if (lastFetchedVersion.current === version) return;
      lastFetchedVersion.current = version;

      if (!__shouldAllowRequest()) return;

      keys.forEach(function(key) {
        var q = queries[key];
        __requestFromParent('state_query', {
          appName: __appName,
          sql: q.sql,
          params: q.params
        }).then(function(resp) {
          setQueryCache(function(prev) {
            var next = Object.assign({}, prev);
            if (resp.error) {
              next[key] = __qr(prev[key] || [], false, resp.error);
            } else {
              next[key] = __qr(resp.data, false, null);
            }
            return next;
          });
        });
      });
    }, [version]);

    // exec: write/DDL — returns promise, triggers debounced re-fetch
    var execFn = React.useCallback(function(sql, params) {
      if (!__shouldAllowRequest()) {
        return Promise.reject(new Error('Circuit breaker: too many requests'));
      }
      return __requestFromParent('state_exec', {
        appName: __appName,
        sql: sql,
        params: params || []
      }).then(function(resp) {
        if (resp.error) throw new Error(resp.error);
        __notifyStateChange();
        return resp.data;
      });
    }, []);

    // query: pure lookup — registers query, returns result.
    // The return value IS an array (so .map/.reduce/.filter work directly)
    // AND has .data/.loading/.error properties (so destructuring works too).
    // NOT a hook — safe to call anywhere (conditionally, in helpers, loops, etc.)
    //
    // IMPORTANT: queryFn has NO dependencies (stable identity) — it reads the
    // cache via queryCacheRef instead of closing over queryCache state.  This
    // prevents an infinite loop: queryCache change → queryFn recreated → db
    // object recreated → consumer useEffect([db]) fires → exec() → version
    // bump → re-fetch → queryCache change → …
    var queryFn = React.useCallback(function(sql, params) {
      var paramsArr = params || [];
      var key = sql + '|' + JSON.stringify(paramsArr);

      // Register this query if new
      if (!registeredQueries.current[key]) {
        registeredQueries.current[key] = { sql: sql, params: paramsArr };
        // Trigger initial fetch (guarded by circuit breaker)
        if (__shouldAllowRequest()) {
          __requestFromParent('state_query', {
            appName: __appName,
            sql: sql,
            params: paramsArr
          }).then(function(resp) {
            setQueryCache(function(prev) {
              var next = Object.assign({}, prev);
              if (resp.error) {
                next[key] = __qr([], false, resp.error);
              } else {
                next[key] = __qr(resp.data, false, null);
              }
              return next;
            });
          });
        }
      }

      // Return cached result or loading defaults (read via ref, not state)
      return queryCacheRef.current[key] || __qr([], true, null);
    }, []);

    // Memoize the returned object so consumers that put db in a dependency
    // array (e.g. useEffect([db])) do not re-fire on every render.
    return React.useMemo(function() {
      return { exec: execFn, query: queryFn };
    }, [execFn, queryFn]);
  };

  // Make hooks available via require('astonish') too — added to modules below
  // ── End data hooks ─────────────────────────────────────────────────

  function reportHeight() {
    var h = Math.max(document.body.scrollHeight, document.documentElement.scrollHeight);
    window.parent.postMessage({ type: 'render_success', height: h }, '*');
  }

  var modules = {
    'react': window.React,
    'react-dom': window.ReactDOM,
    'react-dom/client': window.ReactDOM,
    'recharts': window.Recharts || {},
    'lucide-react': window.LucideReact || {},
    'astonish': { useAppData: window.useAppData, useAppAction: window.useAppAction, useAppAI: window.useAppAI, useAppState: window.useAppState },
  };

  function sandboxRequire(name) {
    if (modules[name]) return modules[name];
    var baseName = name.split('/')[0];
    if (modules[baseName]) return modules[baseName];
    throw new Error('Module not found: ' + name + '. Available: react, recharts, lucide-react, astonish');
  }

  window.addEventListener('message', function(e) {
    var msg = e.data;
    if (!msg || !msg.type) return;

    if (msg.type === 'render') {
      errorEl.style.display = 'none';
      try {
        var userCode = msg.code;

        // Defense-in-depth: strip optional YAML-style frontmatter that LLMs
        // sometimes emit (e.g. "title: My App\n---\n<JSX>"). The backend
        // normally strips this before sending, but handle it here too so
        // existing sessions with frontmatter-contaminated code still render.
        var fmSep = userCode.indexOf('\n---\n');
        if (fmSep !== -1 && userCode.substring(0, fmSep).indexOf('title:') !== -1) {
          userCode = userCode.substring(fmSep + 5).trim();
        }

        if (!/export\s+default\b/.test(userCode)) {
          var fnMatch = userCode.match(/(?:function|const|let|var)\s+([A-Z][A-Za-z0-9]*)/g);
          if (fnMatch && fnMatch.length > 0) {
            var last = fnMatch[fnMatch.length - 1];
            var name = last.replace(/^(?:function|const|let|var)\s+/, '');
            userCode += '\nexport default ' + name + ';\n';
          }
        }

        var compiled = Sucrase.transform(userCode, {
          transforms: ['jsx', 'typescript', 'imports'],
          jsxPragma: 'React.createElement',
          jsxFragmentPragma: 'React.Fragment',
          production: true,
        }).code;

        var moduleObj = { exports: {} };
        var fn = new Function(
          'React', 'require', 'module', 'exports',
          compiled
        );
        fn(window.React, sandboxRequire, moduleObj, moduleObj.exports);

        var Component = moduleObj.exports.default;
        if (!Component || typeof Component !== 'function') {
          Component = moduleObj.exports;
        }
        if (typeof Component !== 'function' && Component && typeof Component === 'object') {
          var keys = Object.keys(Component).filter(function(k) { return k !== '__esModule'; });
          for (var i = 0; i < keys.length; i++) {
            if (typeof Component[keys[i]] === 'function') {
              Component = Component[keys[i]];
              break;
            }
          }
        }

        if (typeof Component !== 'function') {
          showError('No valid React component found. Make sure to export default a function component.\n\nCompiled output (first 500 chars):\n' + compiled.substring(0, 500));
          return;
        }

        var root = document.getElementById('root');
        if (!rootInstance) {
          rootInstance = ReactDOM.createRoot(root);
        }
        rootInstance.render(React.createElement(Component));

        requestAnimationFrame(function() {
          setTimeout(reportHeight, 50);
        });

      } catch (err) {
        var msg = err.message || String(err);
        if (err instanceof ReferenceError) {
          var undefinedName = msg.match(/^(\w+) is not defined/);
          if (undefinedName) {
            msg += '\n\nHint: "' + undefinedName[1] + '" does not exist in the sandbox. ' +
              'Only native HTML elements (div, button, input, span, etc.) styled with Tailwind CSS are available. ' +
              'There is no component library (no shadcn/ui, no Material UI). ' +
              'Define any custom components as functions in the same file, or use HTML elements with Tailwind classes.';
          }
        }
        showError(msg, err.stack);
      }
    }

    if (msg.type === 'theme') {
      // App Canvas is always dark. Brand pack + tokens come from parent
      // (web/src/themes/appCanvas.ts). Ignore light mode for app interior.
      applyAppCanvas(msg.pack, msg.tokens);
    }

    if (msg.type === 'set_context') {
      if (msg.appName) __appName = msg.appName;
    }
  });

  var resizeObserver = new ResizeObserver(function() {
    reportHeight();
  });
  resizeObserver.observe(document.body);

  window.parent.postMessage({ type: 'sandbox_ready' }, '*');
})();
</script>
</body>
</html>`

// sandboxFullOnce caches the fully assembled sandbox HTML with inlined JS.
var (
	sandboxFullOnce sync.Once
	sandboxFullHTML string
	sandboxFullETag string // ETag derived from content hash
)

// buildSandboxFullHTML assembles the complete sandbox HTML with the runtime
// and Tailwind JS inlined as <script> blocks. This is used with srcdoc so
// the iframe runs on an opaque ("null") origin with sandbox="allow-scripts"
// — no allow-same-origin — preventing access to the parent's DOM, cookies,
// localStorage, and API endpoints.
func buildSandboxFullHTML() string {
	runtimeJS := web.GetSandboxRuntime()
	tailwindJS := web.GetTailwindBrowser()

	var b strings.Builder
	b.WriteString(sandboxHTMLHead)

	// Inline the runtime JS (React, Sucrase, Recharts, Lucide)
	if runtimeJS != nil {
		b.WriteString("\n<script>")
		b.Write(runtimeJS)
		b.WriteString("</script>")
	}

	// Inline the Tailwind CSS browser runtime
	if tailwindJS != nil {
		b.WriteString("\n<script>")
		b.Write(tailwindJS)
		b.WriteString("</script>")
	}

	b.WriteString(sandboxHTMLBody)
	return b.String()
}

// AppPreviewSandboxFullHandler serves the fully self-contained sandbox HTML
// with all JS inlined. The frontend caches this response and uses it as
// iframe.srcdoc, allowing sandbox="allow-scripts" without allow-same-origin.
//
// Uses ETag-based revalidation so browsers always check for freshness after
// a server rebuild, but get a cheap 304 Not Modified when the content hasn't
// changed. This prevents stale sandbox code from being served from browser
// cache after a binary update.
//
// GET /api/app-preview/sandbox-full
func AppPreviewSandboxFullHandler(w http.ResponseWriter, r *http.Request) {
	sandboxFullOnce.Do(func() {
		sandboxFullHTML = buildSandboxFullHTML()
		hash := sha256.Sum256([]byte(sandboxFullHTML))
		sandboxFullETag = fmt.Sprintf(`"%x"`, hash[:8]) // 16-char hex, quoted per HTTP spec
	})

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// no-cache: browser must revalidate with the server on every use, but can
	// store the response for conditional requests (If-None-Match).
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("ETag", sandboxFullETag)

	// If the client already has this version, return 304 (no body).
	if match := r.Header.Get("If-None-Match"); match == sandboxFullETag {
		w.WriteHeader(http.StatusNotModified)
		return
	}

	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(sandboxFullHTML))
}

// AppPreviewSandboxHandler serves the legacy sandbox HTML (external script refs).
// Kept for backward compatibility but no longer used by the frontend.
func AppPreviewSandboxHandler(w http.ResponseWriter, r *http.Request) {
	// Build the legacy version with <script src> tags
	legacy := sandboxHTMLHead +
		"\n<script src=\"/api/app-preview/runtime.js\"></script>" +
		"\n<script src=\"/api/app-preview/tailwind.js\"></script>" +
		sandboxHTMLBody
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(legacy))
}

// sandboxRuntimeOnce caches the embedded sandbox-runtime.js file.
var (
	sandboxRuntimeOnce sync.Once
	sandboxRuntimeJS   []byte
)

// AppPreviewRuntimeHandler serves the pre-bundled sandbox runtime JS.
func AppPreviewRuntimeHandler(w http.ResponseWriter, r *http.Request) {
	sandboxRuntimeOnce.Do(func() {
		sandboxRuntimeJS = web.GetSandboxRuntime()
	})
	if sandboxRuntimeJS == nil {
		respondError(w, http.StatusNotFound, "sandbox runtime not found — run 'npm run build' in web/")
		return
	}
	w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=86400, immutable")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(sandboxRuntimeJS)
}

// tailwindBrowserOnce caches the Tailwind CSS browser runtime.
var (
	tailwindBrowserOnce sync.Once
	tailwindBrowserJS   []byte
)

// AppPreviewTailwindHandler serves the Tailwind CSS v4 browser runtime.
func AppPreviewTailwindHandler(w http.ResponseWriter, r *http.Request) {
	tailwindBrowserOnce.Do(func() {
		tailwindBrowserJS = web.GetTailwindBrowser()
	})
	if tailwindBrowserJS == nil {
		respondError(w, http.StatusNotFound, "tailwind browser runtime not found — run 'npm run build' in web/")
		return
	}
	w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=86400, immutable")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(tailwindBrowserJS)
}
