# Studio UI System

## Overview

Astonish Studio uses a source-owned UI system built on `shadcn/ui`, Tailwind CSS v4, and product CSS tokens. This document owns:

- Dual-axis theming (light/dark **mode** × **brand pack**)
- Shared primitives vs custom product surfaces
- Token usage rules so chrome and CTAs follow the active pack

**Read this before** changing Studio colors, shared controls, shell chrome, settings surfaces, or theme preference plumbing under `web/`.

Related docs:

- `web/src/themes/README.md` — how to add a brand pack / App Canvas tokens (cookbook)
- `docs/architecture/generative-ui.md` — Apps iframe runtime (not shell chrome)
- `docs/architecture/chat-rendering-pipeline.md` — Chat SSE / report gates (structure, not colors)

## Non-negotiable invariants

1. **Dual axis only.** Mode is `html.dark` (or absence). Brand pack is `html[data-theme="…"]`. Never collapse mode + pack into one enum or one storage key.
2. **Product default brand pack is `classic`.** Code: `DEFAULT_BRAND_THEME` in `web/src/themes/brandTheme.ts` and `defaultBrandTheme` in `pkg/api/brand_theme.go`.
3. **Preference cascade:** user `brand_theme` → platform `default_brand_theme` → `classic`. Empty user preference means inherit platform (then product default).
4. **Prefer tokens, not hex.** Semantic Tailwind (`bg-primary`, `text-muted-foreground`, `border-border`) and product CSS vars (`var(--brand)`, `var(--brand-strong)`, `var(--brand-muted)`, `var(--node-*)`) for chrome, CTAs, focus rings, and flow accents.
5. **Do not hard-code brand colors** in product UI. Forbidden for brand chrome / primary actions: legacy purple hex (`#a855f7`, `#7c3aed`, `#805AD5`, `#6B46C1`, `#c084fc`, …), Tailwind `purple-*`, and purple→blue CTA gradients. **Allowed fixed colors:** semantic status (error red, success green, warning amber) and multi-agent *identity* palettes when they distinguish roles, not brand the app shell.
6. **Do not rebuild components per pack.** A new pack is another CSS variable dictionary (`html[data-theme="x"]` + `.dark`) with the same names as `REQUIRED_THEME_TOKENS`.
7. **Do not change Studio Chat SSE handling or the three-signal report gate** as part of visual refresh work. See `chat-rendering-pipeline.md`.
8. **Do not replace React Flow node/edge/canvas behavior** with generic widgets. Tokenize and restyle only.
9. **Server and client allowlists must stay in sync** when shipping a pack: `web/src/themes/brandTheme.ts` ↔ `pkg/api/brand_theme.go`.
10. **Shared logo** uses `AstonishLogo` (brand-token fill/mask). Do not reintroduce hard-coded purple wordmarks in header, home, or chat.

## Layers

1. **Design system**
   - Local shadcn primitives: `web/src/components/ui/*`
   - `cn()` helper: `web/src/lib/utils.ts`
   - Tokens: `web/src/index.css` (pack dictionaries + `@theme inline`)
   - Import alias: `@/*` → `web/src`

2. **Product-specialized**
   - Domain components stay custom React
   - They **consume the same tokens**; they are not rewritten as generic shadcn widgets

## shadcn/ui usage

Use shadcn primitives for standard application UI:

- Buttons and icon buttons
- Inputs, textareas, selects, switches, checkboxes, radio groups, labels
- Dialogs, confirmation modals, sheets, popovers, dropdown menus, tooltips
- Cards, panels, badges, alerts, separators, skeletons
- Tabs, tables, empty states, loading states, error states

Files under `web/src/components/ui/*` are **source-owned**. Edit them intentionally for product-wide behavior or style. Do not treat them as opaque vendored code. Do not let shadcn CLI overwrite `web/src/index.css` without manual review.

## Custom surfaces

Tokenize rather than blindly rewrite with shadcn:

| Surface | Primary paths |
|---------|----------------|
| Flow builder / React Flow | `FlowCanvas.tsx`, `nodes/*`, `edges/*`, node/edge editors, flow preview |
| Studio Chat | `StudioChat.tsx`, `chat/*` (SSE, tool grouping, harness) |
| Embedded viewers / media | `chat/EmbeddedFileViewer.tsx`, FilePanel |
| Team container terminal | `TeamContainerTerminal.tsx` |
| Browser handoff / view | browser components |
| App preview iframe | `AppPreview`, `sandbox-runtime.ts` |
| Markdown / Mermaid | chat renderers |

Use shadcn only for standard subpanels, forms, menus, or dialogs **inside** these surfaces.

## Theme tokens

`web/src/index.css` defines shadcn-compatible tokens and Astonish product tokens. Pack selectors:

```css
html[data-theme="classic"] { /* light tokens */ }
html[data-theme="classic"].dark { /* dark tokens */ }
```

`--primary` is aliased to `--brand` so `bg-primary` / `text-primary` follow the pack.

### shadcn semantic (Tailwind via `@theme inline`)

`--background`, `--foreground`, `--card`, `--popover`, `--primary`, `--secondary`, `--muted`, `--accent`, `--destructive`, `--border`, `--input`, `--ring`, `--radius`, and matching `*-foreground` tokens.

Prefer classes: `bg-background`, `text-foreground`, `bg-card`, `border-border`, `bg-primary`, `text-muted-foreground`.

### Product tokens

- Brand: `--brand`, `--brand-strong`, `--brand-muted`, `--brand-foreground`, `--accent2`, `--accent3`, `--accent-glow`
- Shell: `--shell-background`, `--panel-background`, `--panel-border`, `--work-background`, `--work-sidebar`, `--bg-grad`
- Chat: `--chat-user-background`, `--chat-agent-background`, `--chat-thinking-background`, `--tool-background`
- Flow nodes: `--node-input`, `--node-llm`, `--node-tool`, `--node-output`, `--node-start`, `--node-end`
- Status: `--success`, `--warning`, `--danger`, `--info`
- Elevation: `--shadow-soft`, `--shadow-elevated`

### Compatibility tokens

`--bg-primary`, `--text-primary`, `--border-color`, etc. remain for legacy surfaces. Prefer semantic / product tokens for new code.

Full checklist when adding a pack: `REQUIRED_THEME_TOKENS` in `web/src/themes/brandTheme.ts`.

## Brand packs

| Pack | Tone | Notes |
|------|------|--------|
| **classic** | classic · indigo | Pre-rebrand slate navy + indigo; **product default** |
| **aster** | bold · purple | Bold violet |
| **nova** | bold · playful | Hot pink / amber / lilac |
| **sage** | calm · fresh | Soft sage / teal |
| **ember** | calm · warm | Dusty clay / sand |

Reserved (not shipped): **amethyst**. UI combobox order: Classic, Aster, Nova, Sage, Ember (`BRAND_THEME_SELECT_ORDER`).

Settings:

| Layer | Where | Purpose |
|-------|--------|---------|
| Platform default | Platform → General → Default brand theme | Login + fallback when user has no preference |
| User preference | Personal → General → Brand theme | Per-user override (`""` = inherit) |
| Product fallback | code | `classic` |

### Preference + API

| Endpoint / API | Role |
|----------------|------|
| `GET /api/brand-theme` | Auth-exempt; platform (or product) default for pre-auth paint |
| Personal settings `brand_theme` | User preference; empty inherits |
| Platform settings `default_brand_theme` | Instance default |
| `useTheme()` / `refreshBrandTheme()` | Client mode + pack; re-resolves after auth |

Resolution: `resolveBrandTheme(user, platform)` on both client (`brandTheme.ts`) and server (`pkg/api/brand_theme.go`).

### Adding a pack

Follow `web/src/themes/README.md`. Minimum:

1. Light + dark CSS blocks in `index.css` with **all** `REQUIRED_THEME_TOKENS`
2. `BRAND_THEME_META.<id>.shipped = true` and select-order entry if needed
3. Server allowlist in `pkg/api/brand_theme.go`
4. App Canvas light + dark entries in `appCanvas.ts` (`APP_CANVAS_BY_BRAND[pack].light` / `.dark`)

## App Canvas (generated apps)

Generated Apps render in a sandboxed iframe and follow Studio **light/dark** and **brand pack** via the same dual axis as the shell. Parent posts `{ type: 'theme', mode, pack, tokens }`; sandbox sets `--color-*` used by `bg-surface`, `text-app`, `bg-brand`, etc.

| Piece | Location |
|-------|----------|
| Token dictionaries (light + dark per pack) | `web/src/themes/appCanvas.ts` |
| Parent → iframe | `buildAppCanvasThemeMessage()` via AppPreview postMessage |
| Sandbox apply | `applyAppCanvas` in `pkg/api/app_preview_sandbox.go` |
| LLM class guidance | `pkg/skills/builtin_content.go` (`bg-surface`, `bg-brand`, `text-danger` / `bg-danger-soft`, …) |

Token-based apps retheme when `html[data-theme]` or `html.dark` changes. Hard-coded `bg-gray-900` apps do not. Runtime isolation: `generative-ui.md`.

## Shared logo

`web/src/components/AstonishLogo.tsx` applies brand gradients/fills via CSS variables over SVG masks. Use it for header, home hero, and chat empty states instead of static purple assets (except raw SVG sources under `web/public/` used as mask bases).

## Settings surface hierarchy

Settings content sits on `--work-background`. Section cards must read as elevated:

| Role | Tokens / classes |
|------|------------------|
| Page | `--work-background` |
| Section card | `bg-card` / `--panel-background` |
| Nested rows / inputs | `bg-background` or `--bg-secondary` |
| Secondary chrome / chips | `--bg-tertiary` |
| Primary actions | `--brand` / `bg-primary` |

Do **not** use near-black sidebar tokens for settings cards. Shared helpers: `web/src/components/settings/settingsApi.ts` (`settingsCardClass`, `settingsInheritedStyle`).

## File map

| Path | Role |
|------|------|
| `web/src/index.css` | Pack dictionaries, global chrome, node/edge CSS |
| `web/src/themes/brandTheme.ts` | Pack IDs, default, resolve, `REQUIRED_THEME_TOKENS` |
| `web/src/themes/appCanvas.ts` | App Canvas packs (light + dark per brand) |
| `web/src/themes/README.md` | Pack authoring cookbook |
| `web/src/hooks/useTheme.ts` | Light/dark + brand React API |
| `web/src/components/ui/*` | shadcn primitives |
| `web/src/lib/utils.ts` | `cn()` |
| `web/src/components/AstonishLogo.tsx` | Themed logo |
| `pkg/api/brand_theme.go` | Server normalize/resolve + public handler |

## Migration posture

Migration is incremental and opportunistic:

1. Foundation (primitives, tokens, dual-axis packs) is in place.
2. Shell, chat chrome, flow nodes, and high-traffic settings use tokens.
3. Remaining work: replace leftover hard-coded brand literals as files are touched; prefer tokens on every new control.

Do not treat visual work as a license to restructure SSE, report gates, or React Flow interaction models.

## Verification

```bash
cd web
npm run typecheck   # if present; else npm run build (runs tsc --noEmit)
npm run lint
npm test
npm run build
```

- Chat visual changes: keep three-signal report gate tests green; see scenario docs.
- Flow visual changes: Flow Canvas tests + manual check of selection, running state, forms, YAML drawer.
- New brand pack: unit tests in `brandTheme.test.ts` / `appCanvas.test.ts` plus allowlist sync.
