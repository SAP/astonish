# Studio UI System

## Overview

Astonish Studio uses a source-owned UI system built on `shadcn/ui`, Tailwind CSS v4, and Astonish product tokens. The goal is to make common UI development predictable while preserving custom surfaces that encode Astonish-specific interactions.

This document owns the rules for Studio theme tokens, shared primitives, and the boundary between standard application UI and custom product UI.

## Layers

Studio UI has two layers:

1. **Design system layer**
   - Local shadcn primitives in `web/src/components/ui/*`.
   - Shared utility `cn()` in `web/src/lib/utils.ts`.
   - Semantic theme tokens in `web/src/index.css`.
   - The `@/*` import alias points to `web/src`.

2. **Product-specialized layer**
   - Domain-specific components remain custom React components.
   - These components consume the same tokens but are not rewritten as generic shadcn widgets.

This keeps common controls consistent without flattening product-specific workflows.

## shadcn/ui usage

Use shadcn primitives for standard application UI:

- Buttons and icon buttons.
- Inputs, textareas, selects, switches, checkboxes, radio groups, labels.
- Dialogs, confirmation modals, sheets, popovers, dropdown menus, tooltips.
- Cards, panels, badges, alerts, separators, skeletons.
- Tabs, tables, empty states, loading states, and error states.

The files under `web/src/components/ui/*` are source-owned. They may be edited intentionally when Astonish needs a product-wide behavior or style adjustment. Do not treat them as opaque vendored code.

## Custom surfaces

The following surfaces remain custom and should be tokenized rather than blindly rewritten with shadcn components:

- Flow builder and React Flow canvas:
  - `web/src/components/FlowCanvas.tsx`
  - `web/src/components/nodes/*`
  - `web/src/components/edges/*`
  - node editor, edge editor, YAML drawer, and flow preview interactions
- Studio Chat rendering pipeline:
  - `web/src/components/StudioChat.tsx`
  - `web/src/components/chat/*`
  - SSE handling, tool grouping, artifact/report/video harness behavior
- Embedded file viewer and media player.
- Team container terminal.
- Browser handoff and browser view.
- App preview iframe sandbox.
- Markdown and Mermaid rendering.

For these surfaces, use shadcn only inside standard subpanels, forms, menus, or dialogs. Preserve the custom interaction model.

## Theme tokens

`web/src/index.css` defines both shadcn-compatible tokens and Astonish product tokens.

### shadcn tokens

These are consumed by Tailwind v4 through `@theme inline`:

- `--background`, `--foreground`
- `--card`, `--card-foreground`
- `--popover`, `--popover-foreground`
- `--primary`, `--primary-foreground`
- `--secondary`, `--secondary-foreground`
- `--muted`, `--muted-foreground`
- `--accent`, `--accent-foreground`
- `--destructive`, `--destructive-foreground`
- `--border`, `--input`, `--ring`
- `--radius`

Use Tailwind semantic classes such as `bg-background`, `text-foreground`, `bg-card`, `border-border`, `bg-primary`, and `text-muted-foreground` for new shared UI.

### Astonish product tokens

These preserve product-specific styling needs:

- `--brand`, `--brand-strong`, `--brand-muted`, `--brand-foreground`
- `--shell-background`, `--panel-background`, `--panel-border`
- `--canvas-background`
- `--chat-user-background`, `--chat-agent-background`, `--chat-thinking-background`
- `--tool-background`
- `--node-input`, `--node-llm`, `--node-tool`, `--node-output`, `--node-start`, `--node-end`
- `--success`, `--warning`, `--danger`, `--info`
- `--shadow-soft`, `--shadow-elevated`

Existing compatibility tokens such as `--bg-primary`, `--text-primary`, and `--border-color` remain during migration. Prefer semantic shadcn tokens for new common UI and product tokens for custom surfaces.

## Theme posture

Studio ships the **Nova** rebrand direction (from the design handoff):

- **Dark:** rich plum base (`#160B1F`) with pink→amber→lilac ambient gradients (`--bg-grad`).
- **Light:** soft rose paper (`#FDF4F7`) with matching ambient glows.
- **Accents:** `--brand` / `--accent2` / `--accent3` (hot pink, warm amber, electric lilac).
- **Type:** Geist (UI) + Fraunces (display / wordmark).
- **Controls:** shadcn primitives remain the standard control system; brand personality lives in tokens + shell/home/composer skins.
- Dense work surfaces (agent cards, harness, settings) stay more opaque than the atmospheric page background for readability.

### Multi-theme (brand packs)

Studio separates **mode** and **brand pack**:

| Axis | DOM | API |
|------|-----|-----|
| Light / dark | `html.dark` | `useTheme().theme` / `toggleTheme()` |
| Brand pack | `html[data-theme="nova"]` | `useTheme().brandTheme` / `setBrandTheme()` |

Shipped packs: **Aster** (bold purple, **product default**) and **Nova** (pink/amber). Packs live as CSS variable dictionaries (`html[data-theme="aster"|"nova"]` and matching `.dark` selectors).

| Layer | Where | Purpose |
|-------|--------|---------|
| Platform default | Platform → General → Default brand theme | Login screen + fallback for users without a preference |
| User preference | Personal → General → Brand theme | Per-user override (`""` = inherit platform) |
| Product fallback | code / `DEFAULT_BRAND_THEME` | **aster** when neither is set |

Adding Ember/Amethyst later means another dictionary with the **same token names** — see `web/src/themes/README.md` and `REQUIRED_THEME_TOKENS` in `web/src/themes/brandTheme.ts`.

Do not rebuild components per theme. Keep using semantic Tailwind (`bg-primary`) and product tokens (`var(--work-sidebar)`).

## Migration rules

1. New standard UI should use `web/src/components/ui/*` primitives.
2. New feature-specific layout should compose primitives through small local components rather than ad hoc Tailwind copies.
3. Existing custom surfaces should be migrated by token alignment first, not wholesale replacement.
4. Do not change Studio Chat SSE handling or report/artifact gates as part of visual refresh work.
5. Do not replace React Flow node/edge/canvas behavior with generic components.
6. Do not let shadcn CLI output overwrite `web/src/index.css` without manual review.

## Migration status

The UI system migration is incremental:

1. **Foundation** — local shadcn primitives, `@/*` alias, semantic + product tokens, architecture guidance.
2. **App shell + dialogs** — TopBar, Sidebar, Upgrade/Confirm/Install modals.
3. **Settings/Admin** — high-traffic full-config panels and shared settings style constants now use semantic tokens/primitives. Large stateful admin surfaces (credentials, sandbox, skills, scheduler, MCP server cards) retain custom workflows while inheriting tokenized shared styles.
4. **MCP secondary surfaces** — ProviderModelSelector, MCP Store, and MCP Inspector use Dialog + shared form primitives.
5. **Chat visual refresh** — session sidebar, harness chrome, artifact cards, model pickers, chat composer (attach/input/send), and chat bubble CSS use product tokens/primitives without changing SSE/report gates. Dark shell retuned to charcoal-neutral to remove blue banding between header and content. User bubbles use soft brand-tinted surfaces (not solid candy purple); floating AI FAB uses solid primary (no purple→blue gradient).
6. **Flow token refresh** — canvas, minimap, OverflowNode, and node type colors consume `--node-*` / canvas tokens without replacing React Flow behavior.
7. **Product workspace sweep** — Home, Apps/Fleet/Drill empty states, and Knowledge shell styling use semantic classes.

Remaining work is opportunistic: convert leftover native form controls inside large custom admin panels and continue replacing hard-coded color literals as those files are touched.

### Settings surface hierarchy

Settings content sits on `--work-background`. Section cards and list panels use **elevated** surfaces so they never disappear into the page:

| Role | Tokens / classes |
|------|------------------|
| Page | `--work-background` |
| Section card | `bg-card` / `--panel-background` (aliased to card in dark Nova) |
| Nested rows / inputs | `bg-background` (inset) or `--bg-secondary` list tiles |
| Secondary chrome / chips | `--bg-tertiary` (above page, not pure black) |
| Primary actions | `--brand` |

Do **not** use near-black `--sidebar-background` / pure black for settings cards or inherited MCP/provider rows. Shared helpers live in `web/src/components/settings/settingsApi.ts` (`settingsCardClass`, `settingsInheritedStyle`).

## Verification

For UI-system changes, run:

```bash
cd web
npm run typecheck
npm run lint
npm test
npm run build
```

For chat visual changes, also run the relevant Studio Chat scenario tests and verify the three-signal report gate remains unchanged.

For flow visual changes, run the Flow Canvas tests and manually inspect node selection, running states, node forms, the YAML drawer, and canvas interactions.
