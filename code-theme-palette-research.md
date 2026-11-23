# Astonish Code: ten-theme palette recommendation

## Recommendation

Keep **Blue-gray** as the default and **Dark orange** unchanged. Add eight palettes: **Tokyo Night, Catppuccin Mocha, Nord, Dracula-inspired, Gruvbox, Rosé Pine, Solarized Light-inspired, and GitHub Light-inspired**. This produces eight dark and two light choices without changing components, spacing, typography, permissions, or execution behavior.

These are proposed Astonish adaptations, not claims of official ports or upstream endorsement. Upstream colors were checked against official repositories/published packages. Contrast calculations below were run locally using the WCAG relative-luminance formula; they do not establish full UI accessibility.

## Why these ten

| Theme | Appearance | Primary accent | Reason to include |
|---|---|---|---|
| Blue-gray (existing/default) | Blue-black, pale blue text | Orange | Preserve the current familiar default |
| Dark orange (existing) | Neutral charcoal | Orange | Preserve the existing warmer alternative |
| Tokyo Night | Deep navy, cool lavender-gray text | Blue | Crisp, modern cool alternative |
| Catppuccin Mocha | Dark purple-gray, pastel text | Mauve | Softer pastel alternative with a distinct accent |
| Nord | Slate blue, near-white text | Frost cyan | Restrained, low-saturation look |
| Dracula-inspired | Purple charcoal, off-white text | Purple | More vivid purple personality |
| Gruvbox | Warm charcoal, cream text | Orange | Earthy, retro look distinct from neutral Dark orange |
| Rosé Pine | Deep plum, pale lavender text | Rose | Warm pastel rose rather than another blue/purple accent |
| Solarized Light-inspired | Warm ivory, dark teal text | Deep blue | Paper-like light option |
| GitHub Light-inspired | White/light gray, dark neutral text | Blue | Clean neutral light option |

## Proposed palette tokens

Hex values are implementation candidates. Border/card mappings and semantic role assignments are Astonish choices. Changes from canonical palettes are explained below.

| Theme | Canvas | Card | Border | Text | Muted | Accent |
|---|---|---|---|---|---|---|
| Blue-gray | `#101922` | `#18232e` | `#344552` | `#e2edf5` | `#9cafbf` | `#ff8700` |
| Dark orange | `#111416` | `#1a1e21` | `#444444` | `#d0d0d0` | `#8a8a8a` | `#ff8700` |
| Tokyo Night | `#1a1b26` | `#24283b` | `#414868` | `#c0caf5` | `#9aa5ce` | `#7aa2f7` |
| Catppuccin Mocha | `#1e1e2e` | `#313244` | `#585b70` | `#cdd6f4` | `#a6adc8` | `#cba6f7` |
| Nord | `#2e3440` | `#3b4252` | `#4c566a` | `#eceff4` | `#d8dee9` | `#88c0d0` |
| Dracula-inspired | `#282a36` | `#343746` | `#6272a4` | `#f8f8f2` | `#a0aacb` | `#bd93f9` |
| Gruvbox | `#282828` | `#3c3836` | `#665c54` | `#ebdbb2` | `#bdae93` | `#fe8019` |
| Rosé Pine | `#191724` | `#1f1d2e` | `#524f67` | `#e0def4` | `#908caa` | `#ebbcba` |
| Solarized Light-inspired | `#fdf6e3` | `#eee8d5` | `#93a1a1` | `#073642` | `#586e75` | `#006b9e` |
| GitHub Light-inspired | `#ffffff` | `#f6f8fa` | `#d1d9e0` | `#1f2328` | `#59636e` | `#0969da` |

| Theme | Success | Warning | Danger |
|---|---|---|---|
| Blue-gray | `#38df92` | `#e5b766` | `#ff7f87` |
| Dark orange | `#87d787` | `#af8700` | `#ff5f5f` |
| Tokyo Night | `#9ece6a` | `#e0af68` | `#f7768e` |
| Catppuccin Mocha | `#a6e3a1` | `#f9e2af` | `#f38ba8` |
| Nord | `#a3be8c` | `#ebcb8b` | `#bf616a` |
| Dracula-inspired | `#50fa7b` | `#f1fa8c` | `#ff5555` |
| Gruvbox | `#b8bb26` | `#fabd2f` | `#fb4934` |
| Rosé Pine | `#9ccfd8` | `#f6c177` | `#eb6f92` |
| Solarized Light-inspired | `#526600` | `#805900` | `#b52825` |
| GitHub Light-inspired | `#1a7f37` | `#9a6700` | `#d1242f` |

### Deliberate adaptations

- **Tokyo Night:** combines Night canvas with Storm card. Uses brighter official foreground/secondary colors rather than dim comments for important terminal text.
- **Mocha:** uses Mauve instead of Blue as the main accent to diversify the catalog. Other roles map to Base, Surface0, Surface2, Text, Subtext0, Green, Yellow, and Red.
- **Nord:** secondary text uses Snow Storm rather than the very faint comment shade. This sacrifices some visual separation for readability.
- **Dracula-inspired:** custom card `#343746` replaces the brighter selection background; custom muted `#a0aacb` replaces dim comment text. This gives roughly 5.1:1 muted and 4.9:1 accent contrast on the card.
- **Gruvbox:** uses official `light3` (`#bdae93`) for secondary text rather than comment gray. The body/secondary text are readable; canonical bright-red danger is weaker on cards and needs separate testing.
- **Rosé Pine:** uses Rose as primary accent, Subtle as secondary text, and Foam for success (upstream uses Foam for additions). Success need not always be green because icons and labels retain meaning.
- **Solarized Light-inspired:** darkens text and functional blue/green/gold/red colors. The standard Solarized colors are attractive but too weak for small colored text on the darker ivory card. The proposed muted shade still falls slightly short of 4.5:1 on cards; promote essential secondary information to body text or darken muted during implementation.
- **GitHub Light-inspired:** maps Primer muted background to cards; palette values use the published `@primer/primitives@10.0.0` light tokens.

## Contrast findings

Minimum contrast across canvas **and card** for the initially researched body/secondary/accent colors:

| Palette | Body text | Secondary text | Accent |
|---|---:|---:|---:|
| Tokyo Night | 9.02:1 | 6.00:1 | 5.78:1 |
| Catppuccin Mocha (mauve accent) | 8.69:1 | 5.65:1 | 6.19:1 |
| Nord | 8.73:1 | 7.45:1 | 5.03:1 |
| Rosé Pine (rose accent) | 12.50:1 | 5.12:1 | 9.76:1 |
| GitHub Light | 14.84:1 | 5.74:1 | 4.88:1 |

Additional checks support the Dracula card adaptation, Gruvbox muted adjustment (5.32:1 on card), and Solarized functional-color adjustments (about 4.77–5.26:1 on card). **Not every status color meets 4.5:1**: Dracula/Gruvbox red and Nord red need review on every actual surface. Do not advertise all ten as accessibility-certified. Keep textual statuses and glyphs; never convey state solely by color.

## Fit with the existing implementation

`pkg/tui/theme.go` already separates nine palette roles from shared component construction. Most dark additions are straightforward data additions. Keep the current IDs/default and preference file format unchanged.

Before implementing:

1. Make diff backgrounds palette-specific. `buildCodeTheme` currently hard-codes dark green/red backgrounds (`#1a3320`, `#3d1f1f`) for both themes. Light themes cannot safely reuse these behind dark text.
2. Suggested light diff bands: GitHub-inspired `#dafbe1` / `#ffebe9`; Solarized-inspired custom ivory-green/ivory-red bands, selected after contrast checks.
3. Preserve the existing picker interaction. Ten rows plus markers/help/errors must remain usable in short terminals through bounded scrolling rather than changing the menu pattern.
4. Test transcript text, syntax, diffs, tables, selected rows, permission warnings, plan cards, footer hints, both light palettes, NO_COLOR, resize, and saved-theme reopen.
5. Retain shared geometry; do not turn palette work into a component redesign.

## Sources and attribution

- Tokyo Night: https://github.com/tokyo-night/tokyo-night-vscode-theme
- Catppuccin palettes: https://github.com/catppuccin/catppuccin#-palette
- Catppuccin style guide: https://github.com/catppuccin/catppuccin/blob/main/docs/style-guide.md
- Nord definitions: https://github.com/nordtheme/nord/blob/develop/src/nord.css
- Dracula OSS palette: https://github.com/dracula/dracula-theme#color-palette-oss
- Gruvbox definitions: https://github.com/morhetz/gruvbox/blob/master/colors/gruvbox.vim
- Rosé Pine definitions: https://github.com/rose-pine/rose-pine-palette/blob/main/dist/css/rose-pine.css
- Solarized canonical design: https://ethanschoonover.com/solarized/
- Primer repository: https://github.com/primer/primitives
- Versioned Primer light tokens: https://cdn.jsdelivr.net/npm/@primer/primitives@10.0.0/dist/css/functional/themes/light.css

Upstream projects publish MIT/MIT-X11 licenses. When copying distributed theme definitions/code or substantial upstream material, retain the applicable copyright and permission notices. Label modified palettes as adaptations; do not reuse brand logos, imply endorsement, or treat paid Dracula PRO assets as OSS.

## Scope of this research

Official-source research and local color contrast calculations completed. No production theme changes were made. These combinations have not yet been visually exercised in Astonish terminals; implementation acceptance remains necessary.
