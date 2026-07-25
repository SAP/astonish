/**
 * Brand theme packs (Nova, future Ember/Amethyst/…).
 *
 * Mode (light/dark) is separate: documentElement.classList 'dark'.
 * Brand pack is documentElement.dataset.theme = 'nova' | 'ember' | …
 *
 * Components must use CSS variables / semantic Tailwind classes so swapping
 * packs only requires adding a token dictionary in index.css (or themes/*.css).
 */

export const BRAND_THEMES = ['nova', 'ember', 'amethyst', 'sage', 'aster', 'classic'] as const

export type BrandTheme = (typeof BRAND_THEMES)[number]

/** Product default for fresh installs and unresolved cascade. */
export const DEFAULT_BRAND_THEME: BrandTheme = 'classic'

/**
 * Combobox / display order — Classic first (default), then bold, then calm.
 * Filter with shipped meta before rendering.
 */
export const BRAND_THEME_SELECT_ORDER: BrandTheme[] = [
  'classic',
  'aster',
  'nova',
  'sage',
  'ember',
  'amethyst',
]

export const BRAND_THEME_STORAGE_KEY = 'astonish-brand-theme'
export const PLATFORM_BRAND_THEME_STORAGE_KEY = 'astonish-platform-brand-theme'

/** Human labels for settings / switchers (not all packs may ship yet). */
export const BRAND_THEME_META: Record<BrandTheme, { label: string; tone: string; shipped: boolean }> = {
  nova: { label: 'Nova', tone: 'bold · playful', shipped: true },
  ember: { label: 'Ember', tone: 'calm · warm', shipped: true },
  amethyst: { label: 'Amethyst', tone: 'safe · purple', shipped: false },
  sage: { label: 'Sage', tone: 'calm · fresh', shipped: true },
  aster: { label: 'Aster', tone: 'bold · purple', shipped: true },
  classic: { label: 'Classic', tone: 'classic · indigo', shipped: true },
}

export function isBrandTheme(value: string | null | undefined): value is BrandTheme {
  return !!value && (BRAND_THEMES as readonly string[]).includes(value)
}

/** True if value is a shipped brand pack. */
export function isShippedBrandTheme(value: string | null | undefined): value is BrandTheme {
  return isBrandTheme(value) && BRAND_THEME_META[value].shipped
}

/** Shipped packs in select order. */
export function shippedBrandThemes(): BrandTheme[] {
  return BRAND_THEME_SELECT_ORDER.filter((id) => BRAND_THEME_META[id].shipped)
}

/**
 * Cascade: user preference → platform default → product default (aster).
 * Empty / unshipped values are skipped.
 */
export function resolveBrandTheme(
  userTheme?: string | null,
  platformDefault?: string | null,
): BrandTheme {
  if (isShippedBrandTheme(userTheme)) return userTheme
  if (isShippedBrandTheme(platformDefault)) return platformDefault
  return DEFAULT_BRAND_THEME
}

export function getStoredBrandTheme(): BrandTheme {
  if (typeof localStorage === 'undefined') return DEFAULT_BRAND_THEME
  const stored = localStorage.getItem(BRAND_THEME_STORAGE_KEY)
  if (isShippedBrandTheme(stored)) return stored
  return DEFAULT_BRAND_THEME
}

/** Apply brand pack on <html data-theme="…">. Safe to call before React mounts. */
export function applyBrandTheme(brand: BrandTheme = getStoredBrandTheme()): BrandTheme {
  const root = document.documentElement
  const next = isShippedBrandTheme(brand) ? brand : DEFAULT_BRAND_THEME
  root.dataset.theme = next
  localStorage.setItem(BRAND_THEME_STORAGE_KEY, next)
  return next
}

/**
 * Required CSS custom properties every brand pack must define (light + dark).
 * Used as a checklist when adding Ember/Amethyst/etc.
 */
export const REQUIRED_THEME_TOKENS = [
  // Brand accents
  '--brand',
  '--brand-strong',
  '--brand-muted',
  '--brand-foreground',
  '--accent2',
  '--accent3',
  '--accent-glow',
  // shadcn semantic
  '--background',
  '--foreground',
  '--card',
  '--card-foreground',
  '--popover',
  '--popover-foreground',
  '--primary',
  '--primary-foreground',
  '--secondary',
  '--secondary-foreground',
  '--muted',
  '--muted-foreground',
  '--accent',
  '--accent-foreground',
  '--destructive',
  '--destructive-foreground',
  '--border',
  '--input',
  '--ring',
  // Product / shell
  '--bg-grad',
  '--sidebar-background',
  '--work-background',
  '--work-sidebar',
  '--work-panel',
  '--shell-background',
  '--panel-background',
  '--item-active',
  '--item-hover',
  '--search-bg',
  '--input-bg',
  '--input-shadow',
  '--card-surface',
  '--card-border',
  '--chat-user-background',
  '--chat-agent-background',
  '--font-ui',
  '--font-display',
] as const

/**
 * App Canvas (generated apps) is a separate always-dark dictionary per pack.
 * See `appCanvas.ts` / `APP_CANVAS_BY_BRAND` — required when shipping a new brand.
 */
