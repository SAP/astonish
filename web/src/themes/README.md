# Studio brand themes

## Model

| Axis | Mechanism | Values |
|------|-----------|--------|
| **Mode** | `html` class `dark` | light (default) / dark |
| **Brand pack** | `html[data-theme="…"]` | `nova` (shipped), future `ember`, `amethyst`, `sage`, `aster` |

Do not collapse brand + mode into one enum. They compose:

```html
<html data-theme="nova" class="dark">
```

## API

```ts
import { applyBrandTheme, getStoredBrandTheme, REQUIRED_THEME_TOKENS } from '@/themes/brandTheme'
import { useTheme } from '@/hooks/useTheme'

// Imperative (early boot)
applyBrandTheme('nova')

// In React
const { theme, toggleTheme, brandTheme, setBrandTheme, availableBrandThemes } = useTheme()
```

## Adding a pack (e.g. Ember)

1. Copy the Nova token blocks in `web/src/index.css` (or add `web/src/themes/ember.css` and import it).
2. Define **both**:
   - `html[data-theme="ember"] { … light tokens … }`
   - `html[data-theme="ember"].dark { … dark tokens … }`
3. Use the **same variable names** listed in `REQUIRED_THEME_TOKENS` (`brandTheme.ts`).
4. Set `BRAND_THEME_META.ember.shipped = true` when ready for the UI switcher.
5. Prefer tokens in components; avoid hard-coded hex in chrome.

## Token layers

1. **shadcn semantic** — `--background`, `--primary`, `--border`, … (controls)
2. **Product** — `--brand`, `--accent2`, `--bg-grad`, `--work-sidebar`, chat/node tokens
3. **Compat** — `--bg-primary`, `--text-primary` (legacy maps; prefer semantic)
4. **App Canvas** (generated apps iframe) — always-dark pack dictionary in `appCanvas.ts`

Components that only touch these layers retheme when the pack changes. Hard-coded hex (e.g. hero tile gradients) must be moved into tokens to follow new packs.

## App Canvas (generated apps)

Apps render in a sandboxed iframe with a **fixed dark** canvas. They do **not** follow Studio light mode.

| Piece | Location |
|-------|----------|
| Token dictionaries per pack | `web/src/themes/appCanvas.ts` → `APP_CANVAS_BY_BRAND` |
| Parent → iframe | `buildAppCanvasThemeMessage()` via `AppPreview` postMessage |
| Sandbox apply | `applyAppCanvas(pack, tokens)` in `pkg/api/app_preview_sandbox.go` |
| LLM guidance | `pkg/skills/builtin_content.go` — use `bg-surface`, `bg-brand`, `text-app`, … |

When you add a brand pack:

1. Shell CSS in `index.css` (light + dark).
2. **Also** fill `APP_CANVAS_BY_BRAND.ember` (dark only) if not already stubbed.
3. Flip `shipped: true`.

Token-based apps retheme automatically when `html[data-theme]` changes. Apps with hard-coded `bg-gray-900` do not.
