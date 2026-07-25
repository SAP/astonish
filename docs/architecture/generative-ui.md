# Generative UI (Visual Apps)

## Overview

Generative UI brings in-chat UI generation to Astonish -- the AI writes React components in response to user requests, renders them live in a sandboxed iframe, and supports iterative refinement through conversation. This is similar to what Claude Artifacts, ChatGPT Canvas, and Vercel v0 provide: the user describes a UI ("build me a sales dashboard"), the AI generates a working React component, and it renders immediately in the chat. The user can then refine it ("add a search bar", "make the chart a bar chart") through follow-up messages.

Beyond ephemeral chat previews, generated UIs can be saved as **Apps** -- a new top-level concept alongside Flows, Fleet, and Drill. Saved apps persist as YAML definitions in `~/.config/astonish/apps/`, appear in a dedicated Apps tab in Studio, and can fetch live data through the Go backend proxy (leveraging existing MCP tools and HTTP APIs). Apps can also make AI calls using the same LLM as the main agent. This creates a natural progression: prototype in chat, refine interactively, save as a reusable app, connect to live data, add AI capabilities.

## Key Design Decisions

### Why LLM-Generated JSX Over a Component Schema

Two approaches were considered:

1. **Structured JSON schema → predefined components**: The AI outputs JSON that maps to a fixed component library (tables, charts, forms). Safer, more predictable, but limited to what the library supports.
2. **LLM generates React JSX**: The AI writes actual React code rendered in a sandboxed iframe. Unlimited flexibility -- any UI the AI can imagine.

JSX generation was chosen because:

- The expressiveness ceiling is dramatically higher. A component schema can handle dashboards and forms but fails for creative UIs (games, visualizations, interactive tutorials, custom layouts).
- Modern LLMs are excellent at writing React code. Claude, GPT-4, and similar models produce working React components reliably.
- The sandboxed iframe provides security isolation -- the generated code runs in a restricted context with communication limited to `postMessage`.
- The same code that renders in the preview can be saved directly as the app source. No translation layer between "preview format" and "storage format."

The tradeoff is that generated code can crash. This is mitigated by an error boundary inside the iframe that catches runtime errors and reports them to the parent, allowing the AI to fix the issue in the refinement loop.

### Why a Sandboxed Iframe with Sucrase

The generated JSX needs a runtime environment. Three options were evaluated:

- **Full Vite dev server per app**: Most compatible but heavyweight. Each preview would spawn a Node.js process, use ~100MB RAM, and take seconds to start.
- **Incus container sandbox**: Maximum isolation but requires the sandbox infrastructure. Too heavy for a UI preview that just needs to render React.
- **Sucrase + iframe sandbox**: Sucrase is a lightweight JSX-to-JS compiler that runs in the browser. Combined with an iframe using `sandbox="allow-scripts allow-same-origin"`, it provides fast compilation (~5ms) with isolation.

Sucrase was chosen because:

- It compiles JSX to `React.createElement` calls without a full Babel toolchain. Combined with the `imports` transform, it also converts ES module `import`/`export` syntax to `require()`/`module.exports`, which the sandbox resolves through a custom module system.
- The iframe's `sandbox` attribute restricts the generated code: no navigation, no popups, no form submission. Communication with the parent page is limited to `postMessage`.
- Cold start is effectively zero -- Sucrase compiles on the main thread in single-digit milliseconds for typical component sizes.

The iframe sandbox uses `sandbox="allow-scripts allow-same-origin"`. The `allow-same-origin` is required so the iframe can:
- Load runtime scripts (`/api/app-preview/runtime.js`, `/api/app-preview/tailwind.js`) from the same origin
- Send authentication cookies with API requests relayed through the parent page

Libraries (React 19, ReactDOM, Sucrase, Recharts, Lucide React) are **pre-bundled into a single IIFE** via Vite during the build step (`vite.config.sandbox.js`). The resulting `sandbox-runtime.js` file is embedded into the Go binary and served at `/api/app-preview/runtime.js`. Tailwind CSS v4's browser runtime is similarly served at `/api/app-preview/tailwind.js`. This avoids any CDN dependency -- the sandbox works fully offline.

### Why a New "Apps" Concept Instead of Extending Flows

Flows are YAML-defined node graphs executed by the AstonishAgent state machine. They model sequential processing: input → LLM → tool → conditional → output. Visual apps are fundamentally different:

- **Flows are backend execution graphs.** Apps are frontend React components.
- **Flows have nodes and edges.** Apps have JSX source code and data sources.
- **Flows run once per invocation.** Apps are persistent, interactive UIs.
- **Flows use `{{variable}}` interpolation.** Apps use React state and hooks.

Forcing apps into the flow schema would require either (a) a special "render JSX" node type that doesn't fit the state machine model, or (b) ignoring most of the flow infrastructure. A separate `~/.config/astonish/apps/` directory with its own YAML schema is cleaner, avoids confusion in the flow listing, and allows app-specific features (data sources, polling intervals, theming) without polluting the flow schema.

The Apps tab sits alongside Chat, Flows, Fleet, and Drill in the top navigation, establishing apps as a first-class concept.

### Why Backend-Proxied Data Fetching

Apps need live data -- database queries, API calls, MCP tool invocations. Three options were considered:

1. **Direct fetch from iframe**: The generated code calls `fetch()` directly. Simple but fundamentally broken: the sandbox's restricted context makes cross-origin requests unreliable, CORS blocks most APIs, and there's no way to inject authentication tokens securely.
2. **Proxy through Go backend**: The iframe sends data requests via `postMessage` to the parent page, which calls a Go API endpoint, which executes the actual request. Results flow back through the same channel.
3. **Hybrid**: Backend proxy for MCP/tool data, direct fetch for public APIs.

Backend proxy was chosen because:

- **Security**: API keys and credentials never reach the iframe. The Go backend resolves credentials from the credential store server-side (via `@credential-name` suffix on sourceId URLs).
- **MCP integration**: The proxy can invoke any registered MCP tool, giving apps access to databases, GitHub, Slack, and every other MCP server configured in Astonish -- without the iframe needing to know anything about MCP.
- **CORS elimination**: The Go backend makes server-side HTTP requests, bypassing browser CORS restrictions entirely.
- **Audit trail**: All data requests go through the backend, providing a single point for logging, rate limiting, and access control.
- **SSRF / egress**: `http:` sources are fetched via sandbox `Exec` (`curl`) in the per-user App session (`app-mcp-{userID}`), not dialed from the Studio process. Network Policy PreSeed and OpenShell L7 + `cert_bundles` (corp CA / MITM trust) apply the same way as agent tools. Studio only resolves credentials, builds the request, and rejects non-`http(s)` schemes plus hard-blocked IP literals (loopback / link-local / metadata). Soft-private hosts (e.g. corp GHES on `10.x`) are allowed at the Studio gate; sandbox policy enforces allow/deny. Apps HTTP requires a configured sandbox backend. In-chat network grants do not apply to Apps.

The tradeoff is latency (iframe → postMessage → HTTP → backend → data source → response chain), but for dashboard-style polling this is negligible. Real-time use cases use SSE streaming from the backend, forwarded to the iframe via `postMessage`.

### Why Chat-First Iterative Refinement

The initial implementation prioritizes the chat experience over the Apps infrastructure because:

- The chat preview is the core innovation. Without it, the Apps tab is just a file manager.
- Iterative refinement validates the approach immediately. If the AI can generate, render, and refine a component through conversation, everything else (saving, data sources, management) is incremental.
- The distillation system provides a proven pattern: preview in chat → save to disk. The app save flow follows the same UX as `/distill` for flows.

## Architecture

### Phase 1: In-Chat JSX Preview

The foundation: user requests a UI in chat, the AI generates JSX, and it renders inline as a live preview.

```
User: "Build me a calculator app"
  |
  v
ChatAgent: detects UI generation intent
  |
  v
LLM generates JSX with ```astonish-app fence marker
  |
  v
SSE stream: text events contain the fenced code block
  |
  v
chat_runner.go: detects ```astonish-app, extracts code, emits app_preview SSE event
  |
  v
StudioChat.tsx: receives app_preview event, creates AppPreviewMessage
  |
  v
AppPreviewCard.tsx: renders card with toolbar (fullscreen, code, save)
  |
  v
AppPreview.tsx: renders in sandboxed iframe
  |
  +-- Sends code via postMessage to iframe
  +-- Sucrase compiles JSX → JS (with imports + typescript transforms)
  +-- React 19 renders the component
  +-- Tailwind CSS v4 browser runtime styles it
  +-- Error boundary catches crashes and reports back via postMessage
  |
  v
User sees a working calculator in the chat
```

#### Iframe Sandbox Runtime

The `AppPreview` component creates an iframe pointing to `/api/app-preview/sandbox`, served by `AppPreviewSandboxHandler` in Go. The sandbox HTML loads two pre-bundled scripts from the same origin:

```html
<!DOCTYPE html>
<html>
<head>
<script src="/api/app-preview/runtime.js"></script>
<script src="/api/app-preview/tailwind.js"></script>
<style type="text/tailwindcss">
  /* App Canvas tokens — defaults = Nova dark; parent overrides via theme postMessage */
  @theme {
    --color-surface: #1d0f28;
    --color-surface-2: #100816;
    --color-app: #fceef7;
    --color-app-muted: #b49bc3;
    --color-app-border: rgba(240, 220, 255, 0.10);
    --color-brand: #ff6b9d;
  }
</style>
<style>
  html { background: var(--color-app-canvas, #160b1f) !important; }
  /* Force transparent root so App Canvas shows through */
  #root > *:first-child { background-color: transparent !important; min-height: auto !important; }
</style>
</head>
<body class="dark">
<div id="root"></div>
<div id="error-display"></div>
<script>
  // Bootstrap: validate runtime globals, set up module system, listen for postMessage
  // ...
</script>
</body>
</html>
```

### App Canvas contract

Generated apps render inside an **App Canvas** that follows Studio **light/dark** and **brand pack** (same dual axis as the shell):

| Concern | Behavior |
|---------|----------|
| Iframe canvas | Light or dark tokens for the active brand pack (`APP_CANVAS_BY_BRAND[pack][mode]`) |
| Studio shell (Apps panel chrome, chat) | Follows Studio tokens / light-dark as usual |
| Theme switch | Parent posts `{ type: 'theme', mode, pack, tokens }` when pack or light/dark changes; sandbox sets `--color-surface`, `--color-brand`, … |
| LLM design system | Prefer tokens: `bg-surface`, `text-app`, `bg-brand`, plus status banners `text-danger` / `bg-danger-soft`, `text-warning` / `bg-warning-soft`, `text-success` / `bg-success-soft` |
| Outermost root | Must stay transparent (no `bg-*`) so canvas shows through |
| App-local theme toggle | **Do not generate** — one token system; parent pushes mode |

| Source of truth | Path |
|-----------------|------|
| Per-pack light + dark token dictionaries | `web/src/themes/appCanvas.ts` (`APP_CANVAS_BY_BRAND`) |
| postMessage builder | `buildAppCanvasThemeMessage()` |
| Sandbox apply | `applyAppCanvas(pack, tokens, mode)` in `pkg/api/app_preview_sandbox.go` |
| LLM skill | `pkg/skills/builtin_content.go` |

Adding a pack: fill shell CSS **and** `APP_CANVAS_BY_BRAND[pack].light` + `.dark`. Token-based apps retheme without regeneration.

The `runtime.js` bundle is a single IIFE built by Vite (`vite.config.sandbox.js`) that contains React 19, ReactDOM, Sucrase, Recharts, and Lucide React icons. The `tailwind.js` file is the `@tailwindcss/browser` runtime (~271KB) that provides Tailwind CSS v4 processing directly in the browser.

When the parent sends a `render` message, the sandbox:

1. Pre-processes the code: if no `export default` is found, appends one for the last PascalCase function.
2. Compiles with Sucrase using `transforms: ['jsx', 'typescript', 'imports']`:
   - `jsx` converts JSX to `React.createElement` calls
   - `typescript` strips type annotations (LLMs sometimes generate TypeScript)
   - `imports` converts `import`/`export` to `require()`/`module.exports`
3. Executes the compiled code via `new Function()`, with a custom `require()` that resolves module names to the pre-loaded globals.
4. Renders the default-exported component into `#root` using `ReactDOM.createRoot`.
5. Reports the content height back to the parent via `postMessage`.

The custom module system maps these module names:
- `'react'` → `window.React`
- `'react-dom'`, `'react-dom/client'` → `window.ReactDOM`
- `'recharts'` → `window.Recharts`
- `'lucide-react'` → `window.LucideReact`
- `'astonish'` → `{ useAppData, useAppAction, useAppAI }`

The iframe uses `sandbox="allow-scripts allow-same-origin"`:
- `allow-scripts`: Permits JavaScript execution.
- `allow-same-origin`: Required for loading sub-resources from the same origin and sending auth cookies. Blocks top-navigation, popups, and form submission.

#### SSE Event: `app_preview`

A new SSE event type carries the generated app to the frontend:

```
event: app_preview
data: {"code":"function Calculator() { ... }","title":"Calculator","description":"","version":1,"appId":"550e8400-e29b-41d4-a716-446655440000"}
```

The event includes an `appId` (UUID) for stable cross-turn matching during iterative refinement:

```go
// Emitted as map[string]any in chat_runner.go
map[string]any{
    "code":    code,
    "title":   title,
    "description": "",
    "version": version,
    "appId":   appID,
}
```

The event is emitted when the backend detects an `astonish-app` fenced code block in the LLM's streaming response. Detection happens in `chat_runner.go`: accumulated text is scanned for the fence markers, and when a complete block is found, the `app_preview` event is sent alongside the text event.

#### Frontend Message Type

```typescript
interface AppPreviewMessage {
    type: 'app_preview'
    code: string
    title: string
    description: string
    version: number
    appId?: string  // Stable UUID for cross-turn matching
}
```

This is added to the `ChatMsg` union in `chatTypes.ts`. In `StudioChat.tsx`, the `app_preview` SSE event handler creates an `AppPreviewMessage` and appends it to the message list. The `AppPreviewCard` component renders the preview with a toolbar: **Fullscreen**, **Code**, **Save**, and version navigation.

#### Code Fence Detection

The LLM is instructed (via the system prompt) to wrap generated UI code in a specific fence:

````
```astonish-app
import React, { useState } from 'react';

export default function MyComponent() {
  const [count, setCount] = useState(0);
  return (
    <div className="p-4">
      <button onClick={() => setCount(c => c + 1)}>Count: {count}</button>
    </div>
  );
}
```
````

The backend detects this fence in the streaming text and:
1. Extracts the code content.
2. Emits an `app_preview` SSE event with the code, auto-detected title, and version.
3. The frontend shows a compact `AppCodeIndicator` in place of the raw code block in the markdown rendering.

This dual-path approach (code fence in text + explicit SSE event) ensures the preview works whether the AI uses a tool or outputs inline code.

#### System Prompt for UI Generation

The system prompt uses a three-tier architecture:

- **Tier 1 (always present)**: Critical rules embedded in the static system prompt (`system_prompt_builder.go`). Includes the `astonish-app` fence format, available libraries, import patterns, `useAppData`/`useAppAction`/`useAppAI` hook syntax, and the "no fetch()" rule.
- **Tier 2 (vector search)**: Comprehensive guidance stored as a guidance document (`guidance_content.go`) and retrieved via memory search for "generative-ui". Includes full hook documentation, code examples, design system patterns, and refinement instructions.

Key differences from the original design:
- LLMs use standard ES `import` statements (`import React, { useState } from 'react'`), not global variables. Sucrase's `imports` transform converts these to `require()` calls.
- Lucide icons are imported from `'lucide-react'`: `import { Search, Plus } from 'lucide-react'`
- Recharts components are imported from `'recharts'`: `import { BarChart, Bar, XAxis, YAxis } from 'recharts'`
- Data hooks are imported from `'astonish'`: `import { useAppData, useAppAI } from 'astonish'`

#### Preview UX in Chat

The app preview renders in the chat as an elevated card (`AppPreviewCard`):

```
┌─────────────────────────────────────────────┐
│  ◆ Calculator                [⛶] [</>] [Save] │
│─────────────────────────────────────────────│
│                                             │
│        ┌──────────────────┐                │
│        │           7 8 9  │                │
│        │  Display   4 5 6 │                │
│        │           1 2 3  │                │
│        │             0 .  │                │
│        └──────────────────┘                │
│                                             │
└─────────────────────────────────────────────┘
```

Toolbar actions:
- **⛶ Fullscreen**: Opens the iframe in a fixed overlay filling the viewport.
- **</> Code**: Toggles an inline code panel showing the JSX source.
- **Save**: Opens an inline name input bar (pre-filled with auto-detected title), then triggers the save flow.

The preview card auto-sizes to the iframe's content height (communicated via `postMessage` from the iframe after render). Maximum height in the chat is 500px; fullscreen removes this limit.

### Phase 2: Iterative Refinement

The user can modify the generated app through conversation, with each iteration updating the live preview.

```
User: "Make the header blue and add a search bar"
  |
  v
ChatAgent: ClassifyAppIntent() determines this is a refinement request (via LLM classification)
  |
  v
System prompt includes current JSX source as context:
  "Active App Refinement — current source code: [code]
   Apply the requested changes and output the updated component."
  |
  v
LLM generates updated JSX
  |
  v
chat_runner.go: detects updated code, emits app_preview with incremented version and same appId
  |
  v
Frontend: AppPreviewCard shows new version, version navigation allows browsing history
```

#### Active App Tracking

The chat session maintains state for the "active app" being refined:

```go
// pkg/agent/chat_app_refine.go
type ActiveApp struct {
    AppID         string   `json:"appId"`         // Stable UUID for cross-turn matching
    Code          string   `json:"code"`          // Current version's source code
    Title         string   `json:"title"`         // Auto-detected component name
    Versions      []string `json:"versions"`      // History of previous code versions
    Version       int      `json:"version"`       // Current version number
    Modifications []string `json:"modifications"` // History of user change requests
}
```

This is stored in the ChatAgent's per-session state (keyed by session ID, protected by a mutex). When the user sends a follow-up message while an app is active, the current source code is injected into the LLM context so it can apply incremental changes.

On the first turn of an "Improve with AI" session (coming from the Apps tab), the backend **seeds** the `ActiveApp` and emits an `app_preview` SSE event immediately -- before the LLM responds -- so the user sees the app card right away. A duplicate detection guard prevents the LLM from re-emitting the same code.

#### Version History

Each refinement increments the version counter and stores the previous code in the versions array. The preview card shows version navigation:

```
┌─────────────────────────────────────────────┐
│  ◆ Dashboard  v3          [◀ v2] [v4 ▶]    │
│─────────────────────────────────────────────│
│  (current version of the app)               │
└─────────────────────────────────────────────┘
```

Users can browse previous versions. Clicking a previous version restores that code in the preview.

#### Session Persistence

App previews are persisted to the session JSONL transcript using the same prefix-marker pattern as distill previews:

```
[app_preview]{"code":"...","title":"...","description":"...","version":3,"appId":"..."}
```

On session reload, `tryParseAppPreviewMessage()` detects these prefixes and reconstructs `AppPreviewMessage` objects. The version history is reconstructed by collecting all `app_preview` entries with the same `appId`.

#### Refinement Intent Detection

When an active app exists and the user sends a message, the system determines the intent using **LLM-first classification** (`ClassifyAppIntent` in `chat_app_refine.go`). The only early returns are for magic string markers sent by the UI:

- `__app_save__` or `__app_save__:<name>` → save intent (from the Save button)
- `__app_done__` → done intent

Everything else goes to the LLM, which classifies the message as one of:
1. **Refine**: "Make the header blue" → update the app
2. **Save**: "Save this app as Sales Dashboard" → trigger save flow
3. **Unrelated**: "What's the weather?" → normal chat, deactivate app context

### Phase 3: Apps Persistence & Management

Users can save generated UIs as Apps and manage them from a dedicated tab.

#### App YAML Schema

```yaml
name: sales_dashboard
description: Real-time sales metrics dashboard with filtering
version: 3
created_at: 2026-04-20T10:00:00Z
updated_at: 2026-04-20T10:30:00Z
session_id: abc-123-def-456  # originating chat session

code: |
  import React, { useState } from 'react';
  import { useAppData } from 'astonish';
  import { BarChart, Bar, XAxis, YAxis, ResponsiveContainer } from 'recharts';

  export default function SalesDashboard() {
    const { data, loading } = useAppData('mcp:postgres-mcp/query', {
      args: { query: "SELECT * FROM sales ORDER BY date DESC LIMIT 100" }
    });
    // ... full JSX component source
  }

data_sources:
  - id: sales_data
    type: mcp_tool
    config:
      server: postgres-mcp
      tool: query
      args:
        query: "SELECT * FROM sales ORDER BY date DESC LIMIT 100"
    interval: 30s

  - id: exchange_rates
    type: http_api
    config:
      url: "https://api.exchangerate-api.com/v4/latest/USD"
      method: GET
    interval: 60s
```

#### Go Structs

```go
// pkg/apps/types.go
type VisualApp struct {
    Name        string       `json:"name" yaml:"name"`
    Description string       `json:"description" yaml:"description"`
    Code        string       `json:"code" yaml:"code"`
    Version     int          `json:"version" yaml:"version"`
    DataSources []DataSource `json:"dataSources,omitempty" yaml:"data_sources,omitempty"`
    CreatedAt   time.Time    `json:"createdAt" yaml:"created_at"`
    UpdatedAt   time.Time    `json:"updatedAt" yaml:"updated_at"`
    SessionID   string       `json:"sessionId,omitempty" yaml:"session_id,omitempty"`
}

type DataSource struct {
    ID       string         `json:"id" yaml:"id"`
    Type     string         `json:"type" yaml:"type"`     // "mcp_tool", "http_api", "static"
    Config   map[string]any `json:"config" yaml:"config"`
    Interval string         `json:"interval,omitempty" yaml:"interval,omitempty"`
}
```

Storage location: `~/.config/astonish/apps/{app_name}.yaml`

All CRUD operations (`SaveApp`, `LoadApp`, `DeleteApp`, `ListApps`, `Slugify`) are in `pkg/apps/types.go`.

#### API Endpoints

| Method | Endpoint | Purpose |
|---|---|---|
| `GET` | `/api/apps` | List all saved apps (name, description, updatedAt) |
| `GET` | `/api/apps/{name}` | Get full app definition including code |
| `PUT` | `/api/apps/{name}` | Create or update an app |
| `DELETE` | `/api/apps/{name}` | Delete an app |
| `GET` | `/api/apps/{name}/stream` | SSE streaming for data source polling |
| `POST` | `/api/apps/data` | Proxy a data request (sourceId + args in body) |
| `POST` | `/api/apps/action` | Proxy an action/mutation request |
| `POST` | `/api/apps/ai` | Proxy an AI/LLM request |
| `GET` | `/api/app-preview/sandbox` | Serve the sandbox HTML page |
| `GET` | `/api/app-preview/runtime.js` | Serve the pre-bundled sandbox runtime |
| `GET` | `/api/app-preview/tailwind.js` | Serve the Tailwind CSS browser runtime |

The `/api/apps/data`, `/api/apps/action`, and `/api/apps/ai` routes are registered **before** `/api/apps/{name}` to avoid the Gorilla Mux wildcard capturing them. All routes are registered in `pkg/api/handlers.go`.

#### Save Flow (Chat → App)

When the user clicks the "Save" button on the `AppPreviewCard`:

1. An inline name input bar appears (pre-filled with the auto-detected component title).
2. The user confirms (Enter or click Save), which sends `__app_save__:<name>` as a chat message.
3. The backend's `ClassifyAppIntent` detects the save intent, extracting the custom name.
4. The backend calls `apps.SaveApp()` to write the YAML to `~/.config/astonish/apps/{name}.yaml`.
5. An `app_saved` SSE event confirms the save (with `name` and `path`).
6. A DOM event (`astonish:apps-updated`) refreshes the Apps tab listing.

Users can also save via natural language ("save this as Sales Dashboard"), which the LLM intent classifier routes to the save flow.

#### Apps Tab (Frontend)

A new top-level tab in `TopBar.tsx`:

```
[Chat] [Flows] [Apps] [Fleet] [Drill]
```

The Apps view (`AppsView.tsx`) has two sub-views:

**List View:**
- Grid of saved apps with name, description, last updated
- Click to run or delete

**Runner View:**
- Full-viewport iframe rendering of the app (with deep-linking: `#/apps/AppName`)
- Toolbar: "Improve with AI" (opens a new chat session with the app's code as context), "View Code" (toggles a bottom drawer with CodeMirror JSX editor), "Delete"
- Data sources are active (polling via the parent-side relay)
- Page refresh preserves the selected app via URL hash

### Phase 4: Live Data & Backend Proxy

Apps fetch data through the Go backend, with support for MCP tools, HTTP APIs, and real-time updates.

#### Convention-Based Routing

The primary mechanism for data access is **convention-based sourceId routing**. The `sourceId` string passed to `useAppData` or `useAppAction` determines how the backend resolves the request:

| Prefix | Format | Example | Backend Action |
|---|---|---|---|
| `mcp:` | `mcp:<server>/<tool>` | `mcp:postgres-mcp/query` | Invoke MCP tool via `mcp.InvokeTool()` |
| `http:` | `http:<METHOD>:<url>` | `http:GET:https://api.example.com/data` | Server-side HTTP request (SSRF-protected; private IPs need Network Policy Allow or config `extra_endpoints`) |
| `http:` | `http:<METHOD>:<url>@<cred>` | `http:GET:https://api.example.com/data@my-key` | HTTP request with credential auth |
| `static:` | `static:<key>` | `static:config` | Return static data from app's DataSource config |

When a `sourceId` doesn't match any convention prefix and an `appName` is provided, the backend falls back to looking up the app's saved `DataSources` YAML config for a matching `id`.

#### Credential Resolution

Credentials are resolved server-side using the `@credential-name` suffix convention. A regex (`@([a-zA-Z][a-zA-Z0-9_-]*)$`) extracts the credential name from the end of the URL, ensuring it doesn't conflict with `@` symbols in HTTP basic auth URLs.

The credential store (`pkg/credentials/store.go`) resolves the name to a header key/value pair via `store.Resolve(name)`. This supports API keys, Bearer tokens, Basic auth, and OAuth (client_credentials and authorization_code with auto-refresh). Credentials never reach the iframe.

#### Data Request Flow

```
App (iframe)
  |
  | useAppData('mcp:postgres-mcp/query', { args: { query: 'SELECT ...' } })
  |   → postMessage({ type: 'data_request', sourceId: 'mcp:postgres-mcp/query', requestId: 'req-1', args: {...} })
  |
  v
Parent page (AppPreview.tsx)
  |
  | POST /api/apps/data { sourceId: 'mcp:postgres-mcp/query', args: {...}, requestId: 'req-1' }
  |
  v
Go backend (app_data_handler.go) → resolveDataSource()
  |
  +-- "mcp:" prefix → resolveMCPSource() → mcp.InvokeTool()
  +-- "http:" prefix → resolveHTTPSource() → server-side HTTP (with optional @credential auth)
  +-- "static:" prefix → resolveStaticSource() → return from app YAML config
  +-- fallback → resolveAppDataSource() → look up in saved app's DataSources
  |
  v
Response flows back: backend → JSON → parent → postMessage({ type: 'data_response' }) → iframe
```

#### postMessage Protocol

Messages between the iframe and parent page use a typed protocol:

**Iframe → Parent:**

| Message Type | Fields | Purpose |
|---|---|---|
| `sandbox_ready` | (none) | Sandbox initialized, ready to receive code |
| `render_success` | `height` | Component rendered successfully, report content height |
| `render_error` | `error`, `stack` | Compilation or runtime error |
| `data_request` | `sourceId`, `requestId`, `args` | Request data from a source |
| `action_request` | `actionId`, `requestId`, `payload` | Trigger a backend action (mutation) |
| `ai_request` | `prompt`, `system`, `context`, `requestId` | Request an AI/LLM call |
| `data_subscribe` | `sourceId`, `args`, `interval` | Start polling a data source |
| `data_unsubscribe` | `sourceId` | Stop polling a data source |

**Parent → Iframe:**

| Message Type | Fields | Purpose |
|---|---|---|
| `render` | `code` | Send JSX source to compile and render |
| `theme` | `mode`, `pack`, `tokens` | App Canvas: `mode` is `'light'` or `'dark'`; `pack` + `tokens` from `appCanvas.ts` retheme `--color-*` |
| `data_response` | `requestId`, `data`, `error` | Response to a data request |
| `action_response` | `requestId`, `data`, `error` | Response to an action request |
| `ai_response` | `requestId`, `text`, `error` | Response to an AI request |
| `data_update` | `sourceId`, `data`, `error` | Pushed data update (from polling) |

#### Pre-Injected Hooks

The iframe runtime includes three pre-built hooks that abstract the postMessage protocol. They are available both as globals (`window.useAppData`) and via ES import (`import { useAppData } from 'astonish'`):

**`useAppData(sourceId, options?)`** — Fetch data from a source.

```javascript
// Returns { data, loading, error, refetch }
const { data, loading, error } = useAppData('http:GET:https://api.example.com/data');

// With args (for MCP tools)
const { data } = useAppData('mcp:postgres-mcp/query', {
  args: { query: 'SELECT * FROM sales' }
});

// With polling interval (milliseconds)
const { data } = useAppData('http:GET:https://api.example.com/metrics', {
  interval: 30000  // refresh every 30s
});

// With credential auth
const { data } = useAppData('http:GET:https://api.example.com/data@my-api-key');
```

**`useAppAction(actionId, options?)`** — Trigger a mutation/action.

```javascript
// Returns an async function
const runQuery = useAppAction('mcp:postgres-mcp/query');
const result = await runQuery({ query: 'INSERT INTO ...' });

// Optional timeout in ms (default 30000). Raise only for long-running MCP ops.
const runBuild = useAppAction('mcp:server/build', { timeout: 300000 }); // 5 min
```

**`useAppAI(options?)`** — Make a one-shot LLM call.

```javascript
// Returns an async function
const askAI = useAppAI({ system: 'You are a concise data analyst.' });
const summary = await askAI('Summarize this data', { context: tableData });
// summary is a string
```

The AI hook uses a 2-minute timeout (vs 30s for data hooks, and 30s by default for action hooks — configurable via `{ timeout }`) since LLM calls can take longer. The backend handler (`POST /api/apps/ai`) uses the same LLM model configured for the main Astonish agent via `ChatManager`.

#### Polling and Streaming

Data sources with an `interval` option are automatically polled:

- The iframe sends a `data_subscribe` message to the parent with the interval.
- The parent page (`AppPreview.tsx`) sets up a `setInterval` (minimum 5s) that calls the data proxy endpoint.
- Results are forwarded to the iframe via `data_update` postMessages.
- The `useAppData` hook receives these updates and triggers React re-renders.
- When the component unmounts, a `data_unsubscribe` message stops polling.

For saved apps with server-side polling, the backend provides an SSE endpoint at `GET /api/apps/{name}/stream?sourceId=X` that polls the data source and streams `data_update` events.

### Phase 5: Polish & Advanced Features

#### App Templates

*Not yet implemented.* Planned: pre-built starting points for common patterns (Dashboard, CRUD Table, Form Builder, Data Viewer, Monitoring) stored as YAML files loadable via the `/app` command.

#### App Export

*Not yet implemented.* Planned: export as standalone HTML (single file with embedded runtime) or YAML definition (for sharing between Astonish instances).

#### Multi-View Apps

Supported naturally by the JSX approach -- the AI generates a component with internal routing. No framework changes needed:

```jsx
export default function App() {
  const [view, setView] = useState('dashboard');
  return (
    <div>
      <nav>
        <button onClick={() => setView('dashboard')}>Dashboard</button>
        <button onClick={() => setView('settings')}>Settings</button>
      </nav>
      {view === 'dashboard' && <Dashboard />}
      {view === 'settings' && <Settings />}
    </div>
  );
}
```

#### Code Editor

The Apps tab includes a built-in CodeMirror editor for viewing and editing app source code:

- **Bottom drawer layout**: The preview fills the top portion, the code editor fills the bottom (toggled via the "Code" button in the toolbar).
- Uses CodeMirror with `@codemirror/lang-javascript` for JSX syntax highlighting.
- Implemented in `CodeDrawer.tsx`.

## Implementation Phases

| Phase | Status | Description | Key Files |
|---|---|---|---|
| **1** | Complete | In-chat JSX preview: iframe sandbox, Sucrase compilation, code fence detection, `app_preview` SSE event | `AppPreview.tsx`, `AppPreviewCard.tsx`, `app_preview_sandbox.go`, `chat_runner.go` |
| **2** | Complete | Iterative refinement: active app tracking, LLM intent classification, version history, session persistence | `chat_app_refine.go`, `chat_handlers.go`, `StudioChat.tsx` |
| **3** | Complete | Apps persistence: YAML storage, CRUD API, Apps tab, save-from-chat, deep-linking | `pkg/apps/types.go`, `app_handlers.go`, `AppsView.tsx`, `apps.ts` |
| **4** | Complete | Live data: backend proxy, convention-based routing, `useAppData`/`useAppAction`/`useAppAI` hooks, credential support, polling/streaming | `app_data_handler.go`, `app_ai_handler.go`, `mcp/invoke.go` |
| **5** | Partial | Code editor (bottom drawer with CodeMirror). Templates and export not yet implemented. | `CodeDrawer.tsx` |

## Key Files

| File | Purpose |
|---|---|
| `pkg/api/app_preview_sandbox.go` | Sandbox HTML template with Sucrase compilation, postMessage protocol, `useAppData`/`useAppAction`/`useAppAI` hooks, custom module system |
| `pkg/api/app_data_handler.go` | Data/action proxy: convention-based routing (`mcp:`, `http:`, `static:`), credential resolution, SSE streaming |
| `pkg/api/app_ai_handler.go` | AI proxy: one-shot LLM calls for in-app AI features (2-minute timeout) |
| `pkg/api/app_handlers.go` | REST CRUD handlers for apps (`GET`/`PUT`/`DELETE /api/apps/`) |
| `pkg/api/chat_runner.go` | App preview SSE event emission, code fence detection in streaming text, `extractComponentTitle()` |
| `pkg/api/chat_handlers.go` | Save flow orchestration, app seeding, intent classification wiring |
| `pkg/agent/chat_app_refine.go` | `ActiveApp` struct, `ClassifyAppIntent()` LLM-first classification, `AppIntentResult` |
| `pkg/agent/guidance_content.go` | Tier 2 guidance document: full hook documentation, code examples, design system, refinement instructions |
| `pkg/agent/system_prompt_builder.go` | Tier 1 critical rules: fence format, available hooks, "no fetch()" rule, style hints |
| `pkg/apps/types.go` | `VisualApp`, `DataSource` structs, CRUD functions (`SaveApp`, `LoadApp`, `DeleteApp`, `ListApps`, `Slugify`) |
| `pkg/mcp/invoke.go` | MCP tool invocation helper used by the data proxy |
| `web/src/components/chat/AppPreview.tsx` | Sandboxed iframe component: postMessage relay for data/action/AI requests, polling management, theme sync |
| `web/src/components/chat/AppPreviewCard.tsx` | Chat message card: toolbar (fullscreen, code, save with name dialog), version navigation |
| `web/src/components/chat/AppCodeIndicator.tsx` | Compact collapsed indicator for `astonish-app` code blocks in chat |
| `web/src/components/AppsView.tsx` | Apps tab: list view, runner view with deep-linking (`#/apps/AppName`) |
| `web/src/components/CodeDrawer.tsx` | Bottom panel code editor with CodeMirror JSX syntax highlighting |
| `web/src/api/apps.ts` | API client: `fetchApps`, `fetchApp`, `saveApp`, `deleteApp`, `fetchAppData`, `fetchAppAction`, `fetchAppAI`, `connectAppStream` |
| `web/src/components/chat/chatTypes.ts` | `AppPreviewMessage`, `AppSavedMessage` types in the `ChatMsg` union |

## Interactions

- **Agent Engine**: The ChatAgent detects UI generation intent via the system prompt and guidance documents. The active app context is tracked per-session for refinement. When an active app exists, `ClassifyAppIntent()` uses an LLM call to determine whether the user wants to refine, save, or do something unrelated. The current source code is injected into the system prompt for refinement turns.
- **Sessions**: App previews are persisted in the session JSONL transcript using prefix markers (`[app_preview]`), following the same pattern as distill previews. Session reload reconstructs app preview messages and version history. Session titles get a provisional value from the first user message immediately (before the agent turn), then a best-effort LLM refine may emit a second `session_title` before the SSE stream closes.
- **MCP**: The data proxy endpoint invokes MCP tools via `mcp.InvokeTool()`. Any MCP server configured in Astonish is available as a data source for apps via the `mcp:<server>/<tool>` sourceId convention, providing access to databases, APIs, and external services.
- **Credentials**: Data sources use the `@credential-name` suffix convention on sourceId URLs. The backend resolves credentials from the Astonish credential store (`pkg/credentials/store.go`) via `store.Resolve(name)`, which returns an auth header key/value pair. Credentials support API keys, Bearer tokens, Basic auth, and OAuth (client_credentials and authorization_code with auto-refresh). Credentials never reach the iframe.
- **AI**: Apps can make one-shot LLM calls via the `useAppAI` hook. The backend handler (`POST /api/apps/ai`) uses the same model as the main Astonish agent (resolved via `ChatManager`). Calls are non-streaming with a 2-minute timeout. The LLM has no tool access -- apps should fetch data separately via `useAppData` and pass it as context.
- **API & Studio**: REST endpoints for app CRUD and data/action/AI proxy. The Apps tab is a new top-level view in the Studio UI. The `TopBar` component has an "Apps" navigation item. Deep-linking via URL hash (`#/apps/AppName`) preserves app selection across page refreshes.
- **Flows**: Apps and flows are independent concepts with separate storage and schemas. However, a flow could invoke an app (display it to the user) via a future `show_app` output node type, and an app could trigger a flow via the action system.
- **Sandbox**: The iframe sandbox is browser-native (not Incus). However, data sources that invoke MCP tools may execute within sandbox containers if the MCP server runs in a container (via the existing `ContainerMCPTransport`).
