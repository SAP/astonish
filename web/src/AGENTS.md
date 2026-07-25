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
- **Styling**: Tailwind CSS v4 with semantic tokens in `index.css`. Standard UI should prefer local shadcn primitives in `components/ui/*`; custom product surfaces should consume tokens rather than duplicate hard-coded styles.
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

### Studio UI system
Shared application UI uses source-owned shadcn/ui primitives under `components/ui/*` plus the `cn()` helper in `lib/utils.ts`. These files are local source and may be edited intentionally for product-wide behavior or styling. Prefer shadcn primitives for standard controls, dialogs, menus, sheets, cards, badges, alerts, tabs, tables, and form elements.

Do **not** blindly rewrite specialized surfaces as shadcn-only UI. Flow Canvas/node/edge rendering, Studio Chat SSE/rendering logic, embedded viewers, terminal, browser handoff, and the Apps iframe sandbox remain custom surfaces. Tokenize and restyle those surfaces through `index.css` tokens and use shadcn only for standard subpanels/forms/menus inside them.

Authoritative reference: `docs/architecture/studio-ui-system.md`.

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
2. Adding a new SSE event? See the "SSE consumer contract" above.
3. Adding a new page? Register it in the router, wire up any needed API call in `api/`, and cover it with a Vitest test.
