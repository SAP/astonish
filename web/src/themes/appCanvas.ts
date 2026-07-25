/**
 * App Canvas — generated apps iframe surface (light + dark × brand pack).
 *
 * Studio shell has light + dark and brand packs. Apps receive the same dual
 * axis via postMessage so token classes (bg-surface, text-app, bg-brand, …)
 * retheme without dual-coding inside the LLM app.
 *
 * Adding a brand pack:
 * 1. Fill shell tokens in index.css (light + dark).
 * 2. Fill APP_CANVAS_BY_BRAND[pack].light and .dark here (brand surfaces).
 * 3. Flip BRAND_THEME_META[pack].shipped = true.
 * Semantic danger/warning/success tokens are shared across packs (mode-only).
 *
 * The sandbox applies these CSS variable values via postMessage.
 */

import {
  DEFAULT_BRAND_THEME,
  getStoredBrandTheme,
  isBrandTheme,
  type BrandTheme,
} from './brandTheme'

export type AppCanvasMode = 'light' | 'dark'

/** Brand / surface tokens that vary by pack. */
export type AppCanvasBrandTokens = {
  /** Page / html background */
  canvas: string
  /** Cards, calculator body */
  surface: string
  /** Inputs, keys, nested */
  surface2: string
  /** Primary text */
  app: string
  /** Muted / secondary text */
  appMuted: string
  /** Borders */
  appBorder: string
  /** Primary brand (buttons, accents) */
  brand: string
  /** Warm secondary accent */
  brandStrong: string
  /** Tertiary accent (lilac etc.) */
  accent3: string
  /** Chart grid lines */
  chartGrid: string
}

/**
 * Semantic status tokens (shared across brand packs; light vs dark only).
 * Use for error/warning/success banners so text stays readable in both modes.
 */
export type AppCanvasSemanticTokens = {
  /** Error text */
  danger: string
  /** Error banner background */
  dangerSoft: string
  /** Error banner border */
  dangerBorder: string
  /** Warning text */
  warning: string
  /** Warning banner background */
  warningSoft: string
  /** Warning banner border */
  warningBorder: string
  /** Success text */
  success: string
  /** Success banner / chip background */
  successSoft: string
  /** Success border */
  successBorder: string
}

/** Full token set applied to the sandbox. */
export type AppCanvasTokens = AppCanvasBrandTokens & AppCanvasSemanticTokens

export type AppCanvasPack = {
  light: AppCanvasBrandTokens
  dark: AppCanvasBrandTokens
}

/** Maps AppCanvasTokens → CSS vars used by sandbox @theme utilities. */
export const APP_CANVAS_CSS_VARS = {
  canvas: '--color-app-canvas',
  surface: '--color-surface',
  surface2: '--color-surface-2',
  app: '--color-app',
  appMuted: '--color-app-muted',
  appBorder: '--color-app-border',
  brand: '--color-brand',
  brandStrong: '--color-brand-strong',
  accent3: '--color-accent3',
  chartGrid: '--color-chart-grid',
  danger: '--color-danger',
  dangerSoft: '--color-danger-soft',
  dangerBorder: '--color-danger-border',
  warning: '--color-warning',
  warningSoft: '--color-warning-soft',
  warningBorder: '--color-warning-border',
  success: '--color-success',
  successSoft: '--color-success-soft',
  successBorder: '--color-success-border',
} as const satisfies Record<keyof AppCanvasTokens, string>

/**
 * Shared semantic status colors — high contrast on soft tinted backgrounds.
 * Do not use pastel Tailwind red-400 / yellow-300 for body text on light canvas.
 */
export const APP_CANVAS_SEMANTIC: Record<AppCanvasMode, AppCanvasSemanticTokens> = {
  light: {
    danger: '#b91c1c',
    dangerSoft: 'rgba(185, 28, 28, 0.10)',
    dangerBorder: 'rgba(185, 28, 28, 0.28)',
    warning: '#a16207',
    warningSoft: 'rgba(161, 98, 7, 0.12)',
    warningBorder: 'rgba(161, 98, 7, 0.28)',
    success: '#047857',
    successSoft: 'rgba(4, 120, 87, 0.10)',
    successBorder: 'rgba(4, 120, 87, 0.28)',
  },
  dark: {
    danger: '#fca5a5',
    dangerSoft: 'rgba(239, 68, 68, 0.15)',
    dangerBorder: 'rgba(248, 113, 113, 0.35)',
    warning: '#fcd34d',
    warningSoft: 'rgba(251, 191, 36, 0.15)',
    warningBorder: 'rgba(252, 211, 77, 0.35)',
    success: '#6ee7b7',
    successSoft: 'rgba(16, 185, 129, 0.15)',
    successBorder: 'rgba(52, 211, 153, 0.35)',
  },
}

/**
 * App Canvas brand dictionaries per pack (light + dark).
 * Align with shell tokens in index.css. Unshipped packs stay defined for enablement.
 */
export const APP_CANVAS_BY_BRAND: Record<BrandTheme, AppCanvasPack> = {
  nova: {
    light: {
      canvas: '#fdf4f7',
      surface: '#ffffff',
      surface2: '#f7e9f0',
      app: '#2a0f1f',
      appMuted: '#6e4a5d',
      appBorder: 'rgba(80, 30, 60, 0.10)',
      brand: '#e14c82',
      brandStrong: '#f09144',
      accent3: '#9457e6',
      chartGrid: '#e8d0dc',
    },
    dark: {
      canvas: '#160b1f',
      surface: '#1d0f28',
      surface2: '#100816',
      app: '#fceef7',
      appMuted: '#b49bc3',
      appBorder: 'rgba(240, 220, 255, 0.10)',
      brand: '#ff6b9d',
      brandStrong: '#ffb86b',
      accent3: '#b478ff',
      chartGrid: '#3d2450',
    },
  },
  ember: {
    light: {
      canvas: '#faf6f3',
      surface: '#ffffff',
      surface2: '#f3ebe4',
      app: '#2a2018',
      appMuted: '#7a6a5c',
      appBorder: 'rgba(80, 50, 35, 0.10)',
      brand: '#c4785a',
      brandStrong: '#e0a84a',
      accent3: '#a87898',
      chartGrid: '#e5d8cc',
    },
    dark: {
      canvas: '#14100e',
      surface: '#1c1714',
      surface2: '#100c0a',
      app: '#f5ebe4',
      appMuted: '#b0a090',
      appBorder: 'rgba(230, 210, 190, 0.10)',
      brand: '#d49278',
      brandStrong: '#e8c070',
      accent3: '#d4a0b8',
      chartGrid: '#4a3020',
    },
  },
  amethyst: {
    light: {
      canvas: '#f6f2fc',
      surface: '#ffffff',
      surface2: '#efe8fa',
      app: '#1a0a2e',
      appMuted: '#6a5a80',
      appBorder: 'rgba(80, 50, 120, 0.12)',
      brand: '#7c4dff',
      brandStrong: '#9b6dff',
      accent3: '#6a7cff',
      chartGrid: '#ddd0f0',
    },
    dark: {
      canvas: '#120a1c',
      surface: '#1a1028',
      surface2: '#0c0814',
      app: '#f4eefc',
      appMuted: '#a898c0',
      appBorder: 'rgba(220, 200, 255, 0.12)',
      brand: '#9b6dff',
      brandStrong: '#c4a0ff',
      accent3: '#7b8cff',
      chartGrid: '#352450',
    },
  },
  sage: {
    light: {
      canvas: '#f4f7f5',
      surface: '#ffffff',
      surface2: '#e8f0ec',
      app: '#1a2822',
      appMuted: '#5a7068',
      appBorder: 'rgba(40, 70, 55, 0.10)',
      brand: '#4a8f7a',
      brandStrong: '#8faf5a',
      accent3: '#3d8fa8',
      chartGrid: '#d0e0d8',
    },
    dark: {
      canvas: '#0c1210',
      surface: '#141c18',
      surface2: '#0a100e',
      app: '#e8f2ed',
      appMuted: '#8aa098',
      appBorder: 'rgba(180, 210, 195, 0.10)',
      brand: '#6aaf96',
      brandStrong: '#b5d46a',
      accent3: '#5ec8e0',
      chartGrid: '#1e4034',
    },
  },
  aster: {
    light: {
      canvas: '#f5f0fc',
      surface: '#ffffff',
      surface2: '#efe6fa',
      app: '#1a0a2e',
      appMuted: '#6a5a80',
      appBorder: 'rgba(80, 40, 120, 0.10)',
      brand: '#9b4dff',
      brandStrong: '#b070ff',
      accent3: '#6a5cff',
      chartGrid: '#ddd0f0',
    },
    dark: {
      canvas: '#10081a',
      surface: '#1a1028',
      surface2: '#0c0614',
      app: '#f6eefc',
      appMuted: '#b0a0c8',
      appBorder: 'rgba(230, 200, 255, 0.12)',
      brand: '#c44dff',
      brandStrong: '#e080ff',
      accent3: '#8866ff',
      chartGrid: '#3a2060',
    },
  },
  classic: {
    light: {
      canvas: '#fafbfe',
      surface: '#ffffff',
      surface2: '#f1f3f9',
      app: '#0b1222',
      appMuted: '#6b7280',
      appBorder: '#e5e8f0',
      brand: '#5f4fb2',
      brandStrong: '#3d3580',
      accent3: '#818cf8',
      chartGrid: '#d7ddff',
    },
    dark: {
      canvas: '#0b1222',
      surface: '#0f172a',
      surface2: '#111b30',
      app: '#f6f7fb',
      appMuted: '#9ca3af',
      appBorder: 'rgba(255, 255, 255, 0.10)',
      brand: '#8d7ae0',
      brandStrong: '#a5b4fc',
      accent3: '#c4b5fd',
      chartGrid: '#35448f',
    },
  },
}

/** Active brand from DOM (preferred) or storage. */
export function getActiveBrandTheme(): BrandTheme {
  if (typeof document !== 'undefined') {
    const fromDom = document.documentElement.dataset.theme
    if (isBrandTheme(fromDom)) return fromDom
  }
  return getStoredBrandTheme()
}

/** Active Studio light/dark from document class. */
export function getActiveAppCanvasMode(): AppCanvasMode {
  if (typeof document !== 'undefined' && document.documentElement.classList.contains('dark')) {
    return 'dark'
  }
  return 'light'
}

export function getAppCanvasTokens(
  brand: BrandTheme = getActiveBrandTheme(),
  mode: AppCanvasMode = getActiveAppCanvasMode(),
): AppCanvasTokens {
  const pack = APP_CANVAS_BY_BRAND[brand] ?? APP_CANVAS_BY_BRAND[DEFAULT_BRAND_THEME]
  const brandTokens = pack[mode] ?? pack.dark
  return {
    ...brandTokens,
    ...APP_CANVAS_SEMANTIC[mode],
  }
}

/** Payload for iframe postMessage { type: 'theme', … }. */
export function buildAppCanvasThemeMessage(
  brand: BrandTheme = getActiveBrandTheme(),
  mode: AppCanvasMode = getActiveAppCanvasMode(),
) {
  return {
    type: 'theme' as const,
    mode,
    pack: brand,
    tokens: getAppCanvasTokens(brand, mode),
  }
}
