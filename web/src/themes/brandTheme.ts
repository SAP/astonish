/**
 * Brand theme packs (Nova, future Ember/Amethyst/…).
 *
 * Mode (light/dark) is separate: documentElement.classList 'dark'.
 * Brand pack is documentElement.dataset.theme = 'nova' | 'ember' | …
 *
 * Components must use CSS variables / semantic Tailwind classes so swapping
 * packs only requires adding a token dictionary in index.css (or themes/*.css).
 */

export const BRAND_THEMES = ['nova', 'ember', 'amethyst', 'sage', 'aster'] as const

export type BrandTheme = (typeof BRAND_THEMES)[number]

export const DEFAULT_BRAND_THEME: BrandTheme = 'nova'

export const BRAND_THEME_STORAGE_KEY = 'astonish-brand-theme'

/** Human labels for settings / switchers (not all packs may ship yet). */
export const BRAND_THEME_META: Record<BrandTheme, { label: string; tone: string; shipped: boolean }> = {
  nova: { label: 'Nova', tone: 'bold · playful', shipped: true },
  ember: { label: 'Ember', tone: 'safe · warm', shipped: false },
  amethyst: { label: 'Amethyst', tone: 'safe · purple', shipped: false },
  sage: { label: 'Sage', tone: 'mid · fresh', shipped: false },
  aster: { label: 'Aster', tone: 'bold · purple', shipped: true },
}

export function isBrandTheme(value: string | null | undefined): value is BrandTheme {
  return !!value && (BRAND_THEMES as readonly string[]).includes(value)
}

export function getStoredBrandTheme(): BrandTheme {
  if (typeof localStorage === 'undefined') return DEFAULT_BRAND_THEME
  const stored = localStorage.getItem(BRAND_THEME_STORAGE_KEY)
  if (isBrandTheme(stored) && BRAND_THEME_META[stored].shipped) return stored
  return DEFAULT_BRAND_THEME
}

/** Apply brand pack on <html data-theme="…">. Safe to call before React mounts. */
export function applyBrandTheme(brand: BrandTheme = getStoredBrandTheme()): BrandTheme {
  const root = document.documentElement
  const next = BRAND_THEME_META[brand]?.shipped ? brand : DEFAULT_BRAND_THEME
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
