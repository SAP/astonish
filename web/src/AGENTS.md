# web/src — AGENTS.md

React 19 SPA for Astonish Studio. Mixed **TypeScript (`.ts`/`.tsx`)** and **legacy JSX (`.jsx`)** — new files should be `.tsx`.

## Scope
- Entry: `main.tsx` (React root) and `App.tsx`.
- `components/` — React components (mix of `.tsx` and `.jsx`).
- `api/` — SPA fetch/SSE client for the Go `/api/*` surface.
- `sandbox-runtime.ts` — sandboxed iframe runtime for Apps (generative UI).

## Conventions
- **Language**: `.tsx` for new UI files unless mirroring an existing `.jsx` neighbor. `npm run build` runs `tsc --noEmit` — do not commit code that fails it.
- **Components**: functional + hooks, single per file, `export default` for the main component.
- **State**: React hooks only. **No Redux/Zustand/Jotai/etc.** Cross-cutting state uses React Context sparingly.
- **Styling**: Tailwind CSS v4 with semantic tokens in `index.css`. Prefer local shadcn primitives in `components/ui/*` and CSS variables (`bg-primary`, `var(--brand)`). Custom product surfaces must be tokenized — never hard-code purple/indigo brand hex or `purple-*` for chrome/CTAs. Full rules: `docs/architecture/studio-ui-system.md`.
- **Imports**: external first, local second. Named exports preferred for utilities. The `@/*` alias points to `web/src` and is preferred for shared UI-system imports.
- **Handlers**: CamelCase, prevent default on forms, cleanup in `useEffect`.
- **Lint**: `npm run lint` bootstraps an isolated ESLint toolchain in `web/lint-tool/` so TypeScript ESLint can use the TS 6 compatibility shim while the app uses TypeScript 7. The active flat config lives in `web/lint-tool/eslint.config.js`; `web/eslint.config.js` re-exports it for editor discovery. Separate blocks for `{js,jsx}` and `{ts,tsx}` — do not merge them.
- **Testing**: Vitest (`npm test`).

## Non-negotiable invariants

### StudioChat.tsx (report vs. artifact rendering)
`StudioChat.tsx` implements the client side of the **three-signal report gate**. A markdown artifact is promoted into the right-hand **`HarnessPanel`** (via `EmbeddedFileViewer`) **only** when all three signals hold:

1. Emitted in the last turn (after the most recent user message).
2. `fileType === 'Markdown'`.
3. `isReport === true` (set by the backend when an `` ```astonish-report `` fence's `path:` matched the artifact path).

Anything failing any one of these renders as the compact `ArtifactCard`. **Do not widen the markdown report gate in the SPA.** If the backend regresses the marker, fix it in `pkg/api/chat_runner.go`, not by weakening the client check.

The chat stream shows a compact `HarnessPlaceholder` for gated reports, Apps, Flow draft, Tutorial blueprint/slideshow, browser handoff, and last-turn videos. The full UI mounts in `HarnessPanel` (~1080px preferred, user-resizable; chat keeps a ~380px floor), which auto-opens on the latest harness emission and auto-collapses the session sidebar.

Last-turn **Video** artifacts (e.g. `browser_stop_recording`) open in the harness via `EmbeddedFileViewer` with `fillHeight` — separate from the markdown report contract. Slideshow-owned tutorial MP4s stay inside `TutorialSceneSlideshowCard` (also harnessed). FilePanel / EmbeddedFileViewer must not fetch video as text; use `fetchArtifactBlob` + `<video>`.

Authoritative reference: `docs/architecture/chat-rendering-pipeline.md`, "The Report Pipeline" section.

### Studio UI system (themes, tokens, shadcn)

Authoritative reference: **`docs/architecture/studio-ui-system.md`**.  
Pack cookbook (add brand / App Canvas tokens): **`web/src/themes/README.md`**.

Agents doing any UI styling or shared-component work under `web/` must follow:

1. **Dual axis:** light/dark via `html.dark`; brand pack via `html[data-theme]`. Do not collapse into one enum.
2. **Default pack is `classic`.** Cascade: user `brand_theme` → platform `default_brand_theme` → classic. Empty user = inherit.
3. **Primitives:** standard controls/dialogs/menus/forms → `components/ui/*` + `cn()` in `lib/utils.ts` (source-owned; editable).
4. **Custom surfaces stay custom** (tokenize only): Flow Canvas/nodes/edges, Studio Chat SSE/rendering, embedded viewers, terminal, browser handoff, Apps iframe. Use shadcn only for standard subpanels inside them.
5. **No hard-coded brand colors** for chrome/CTAs (`#a855f7`, `#805AD5`, `purple-*`, purple→blue gradients, etc.). Use `bg-primary` / `var(--brand*)` / pack tokens. Fixed status or multi-agent *identity* colors may stay non-brand when they distinguish roles.
6. **New shipped pack:** light+dark CSS dictionaries (`REQUIRED_THEME_TOKENS`), `BRAND_THEME_META.shipped`, server allowlist in `pkg/api/brand_theme.go`, App Canvas **light + dark** entries in `themes/appCanvas.ts`. Client and server allowlists must match.
7. **Do not mix** visual refresh with SSE/report-gate changes or React Flow behavior replacement.
8. **Logo:** use `AstonishLogo` (tokenized), not hard-coded purple assets in chrome.

### Generative UI (Apps) sandbox
The Apps runtime runs user-described React apps in a sandboxed iframe with an opaque origin, communicating with the parent via `postMessage` and a SSRF-protected server-side proxy. Do not remove the iframe boundary or the origin isolation.

Authoritative reference: `docs/architecture/generative-ui.md`.

### SSE consumer contract
Every SSE event type produced by `pkg/api/chat_runner.go` has a matching handler in `StudioChat.tsx` (and related components). If you add an event type on the backend, add its handler here and add a scenario fixture. If you rename an event type, update both sides in the same commit.

Fleet SSE (`fleet_*` events from `FleetSessionStreamHandler`) follows the same same-commit rule. New events such as `fleet_agent_started` / `fleet_agent_finished` / `fleet_task_*` / `fleet_mailbox_delivered` must land with their Studio handlers together.

### FleetExecutionPanel parallel lanes
When `maxParallelAgents ≤ 1` (or no `lane_index >= 0` events), keep the single-column Perplexity timeline. Multi-column lanes are only for parallel sessions — do not force a multi-column layout onto serial fleets.

## When editing
1. Adding a new component? `.tsx`, functional + hooks, keep it under 300 lines. Extract if it grows.
2. Changing colors, chrome, or shared controls? Read `docs/architecture/studio-ui-system.md` and use tokens / shadcn — no hard-coded brand hex.
3. Adding or shipping a brand pack? Follow `web/src/themes/README.md` and keep Go allowlist in sync.
4. Adding a new SSE event? See the "SSE consumer contract" above.
5. Adding a new page? Register it in the router, wire up any needed API call in `api/`, and cover it with a Vitest test.
