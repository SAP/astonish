/**
 * App Canvas — always-dark surface for generated apps (iframe sandbox).
 *
 * Studio shell has light + dark. Apps only use the dark "night" variant of the
 * active brand pack so the LLM never dual-themes content.
 *
 * Adding a brand pack (Ember, …):
 * 1. Fill shell tokens in index.css (light + dark).
 * 2. Fill APP_CANVAS_BY_BRAND[pack] here (dark only).
 * 3. Flip BRAND_THEME_META[pack].shipped = true.
 *
 * Generated apps use Tailwind tokens (bg-surface, bg-brand, text-app, …).
 * The sandbox receives these CSS variable values via postMessage.
 */

import {
  DEFAULT_BRAND_THEME,
  getStoredBrandTheme,
  isBrandTheme,
  type BrandTheme,
} from './brandTheme'

/** CSS custom properties the sandbox applies (Tailwind @theme / utilities). */
export type AppCanvasTokens = {
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
} as const satisfies Record<keyof AppCanvasTokens, string>

/**
 * Dark App Canvas dictionary per brand pack.
 * Unshipped packs are still defined so enabling them is dictionary-only.
 */
export const APP_CANVAS_BY_BRAND: Record<BrandTheme, AppCanvasTokens> = {
  nova: {
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
  // Calm warm night (Ember)
  ember: {
    canvas: '#14100e',
    surface: '#1c1714',
    surface2: '#100c0a',
    app: '#f5ebe4',
    appMuted: '#b0a090',
    appBorder: 'rgba(230, 210, 190, 0.10)',
    brand: '#d49278',
    brandStrong: '#e0b888',
    accent3: '#c4a898',
    chartGrid: '#4a3020',
  },
  // Cool purple night (reserved — not shipped yet)
  amethyst: {
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
  // Calm fresh night (Sage)
  sage: {
    canvas: '#0c1210',
    surface: '#141c18',
    surface2: '#0a100e',
    app: '#e8f2ed',
    appMuted: '#8aa098',
    appBorder: 'rgba(180, 210, 195, 0.10)',
    brand: '#6aaf96',
    brandStrong: '#8bc4ad',
    accent3: '#7aafbc',
    chartGrid: '#1e4034',
  },
  // Bold violet night
  aster: {
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
  // Pre-rebrand slate navy + indigo (matches classic dark shell)
  classic: {
    canvas: '#0b1222',
    surface: '#0f172a',
    surface2: '#111b30',
    app: '#f6f7fb',
    appMuted: '#9ca3af',
    appBorder: 'rgba(255, 255, 255, 0.10)',
    brand: '#8d7ae0',
    brandStrong: '#7262ce',
    accent3: '#a998ef',
    chartGrid: '#35448f',
  },
}

export function getAppCanvasTokens(brand: BrandTheme = getActiveBrandTheme()): AppCanvasTokens {
  return APP_CANVAS_BY_BRAND[brand] ?? APP_CANVAS_BY_BRAND[DEFAULT_BRAND_THEME]
}

/** Active brand from DOM (preferred) or storage. */
export function getActiveBrandTheme(): BrandTheme {
  if (typeof document !== 'undefined') {
    const fromDom = document.documentElement.dataset.theme
    if (isBrandTheme(fromDom)) return fromDom
  }
  return getStoredBrandTheme()
}

/** Payload for iframe postMessage { type: 'theme', … }. */
export function buildAppCanvasThemeMessage(brand: BrandTheme = getActiveBrandTheme()) {
  return {
    type: 'theme' as const,
    mode: 'dark' as const,
    pack: brand,
    tokens: getAppCanvasTokens(brand),
  }
}
