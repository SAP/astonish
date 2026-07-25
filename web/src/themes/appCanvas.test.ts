import { describe, it, expect, afterEach } from 'vitest'
import {
  APP_CANVAS_BY_BRAND,
  APP_CANVAS_CSS_VARS,
  APP_CANVAS_SEMANTIC,
  buildAppCanvasThemeMessage,
  getAppCanvasTokens,
  getActiveAppCanvasMode,
} from './appCanvas'
import { BRAND_THEMES, DEFAULT_BRAND_THEME, type BrandTheme } from './brandTheme'

describe('appCanvas', () => {
  const originalTheme = document.documentElement.dataset.theme
  const hadDark = document.documentElement.classList.contains('dark')

  afterEach(() => {
    if (originalTheme) document.documentElement.dataset.theme = originalTheme
    else delete document.documentElement.dataset.theme
    if (hadDark) document.documentElement.classList.add('dark')
    else document.documentElement.classList.remove('dark')
  })

  it('defines complete light + dark brand tokens for every pack', () => {
    const brandKeys = [
      'canvas',
      'surface',
      'surface2',
      'app',
      'appMuted',
      'appBorder',
      'brand',
      'brandStrong',
      'accent3',
      'chartGrid',
    ] as const
    for (const brand of BRAND_THEMES) {
      const pack = APP_CANVAS_BY_BRAND[brand]
      expect(pack, brand).toBeDefined()
      for (const mode of ['light', 'dark'] as const) {
        const tokens = pack[mode]
        expect(tokens, `${brand}.${mode}`).toBeDefined()
        for (const key of brandKeys) {
          expect(tokens[key], `${brand}.${mode}.${key}`).toMatch(/^(#|rgb|rgba|hsl|oklch)/)
        }
      }
    }
  })

  it('merges semantic danger/warning/success into getAppCanvasTokens', () => {
    for (const mode of ['light', 'dark'] as const) {
      const tokens = getAppCanvasTokens('nova', mode)
      expect(tokens.danger).toBe(APP_CANVAS_SEMANTIC[mode].danger)
      expect(tokens.warning).toBe(APP_CANVAS_SEMANTIC[mode].warning)
      expect(tokens.success).toBe(APP_CANVAS_SEMANTIC[mode].success)
      expect(tokens.dangerSoft).toMatch(/rgba|rgb|#/)
      expect(tokens.warningBorder).toMatch(/rgba|rgb|#/)
    }
  })

  it('light semantic text is darker (higher contrast) than dark-mode pastels', () => {
    // Rough check: light danger is a deep red (#b91c1c), not a pastel like #f87171
    expect(APP_CANVAS_SEMANTIC.light.danger.toLowerCase()).toBe('#b91c1c')
    expect(APP_CANVAS_SEMANTIC.light.warning.toLowerCase()).toBe('#a16207')
    expect(APP_CANVAS_SEMANTIC.dark.danger.toLowerCase()).toBe('#fca5a5')
  })

  it('maps every token key to a CSS variable', () => {
    const tokens = getAppCanvasTokens('nova', 'dark')
    for (const key of Object.keys(tokens) as (keyof typeof tokens)[]) {
      expect(APP_CANVAS_CSS_VARS[key]).toMatch(/^--color-/)
    }
  })

  it('light and dark canvases differ for each pack', () => {
    for (const brand of BRAND_THEMES) {
      const light = getAppCanvasTokens(brand, 'light')
      const dark = getAppCanvasTokens(brand, 'dark')
      expect(light.canvas, brand).not.toBe(dark.canvas)
      expect(light.app, brand).not.toBe(dark.app)
      expect(light.danger, brand).not.toBe(dark.danger)
    }
  })

  it('buildAppCanvasThemeMessage includes pack + mode + tokens for active theme', () => {
    document.documentElement.dataset.theme = 'nova'
    document.documentElement.classList.add('dark')
    const msg = buildAppCanvasThemeMessage()
    expect(msg.type).toBe('theme')
    expect(msg.mode).toBe('dark')
    expect(msg.pack).toBe('nova')
    expect(msg.tokens.brand).toBe(APP_CANVAS_BY_BRAND.nova.dark.brand)
    expect(msg.tokens.canvas).toBe(APP_CANVAS_BY_BRAND.nova.dark.canvas)
    expect(msg.tokens.danger).toBe(APP_CANVAS_SEMANTIC.dark.danger)
  })

  it('buildAppCanvasThemeMessage reflects light mode from document', () => {
    document.documentElement.dataset.theme = 'classic'
    document.documentElement.classList.remove('dark')
    expect(getActiveAppCanvasMode()).toBe('light')
    const msg = buildAppCanvasThemeMessage()
    expect(msg.mode).toBe('light')
    expect(msg.pack).toBe('classic')
    expect(msg.tokens.canvas).toBe(APP_CANVAS_BY_BRAND.classic.light.canvas)
    expect(msg.tokens.brand).toBe(APP_CANVAS_BY_BRAND.classic.light.brand)
    expect(msg.tokens.warning).toBe(APP_CANVAS_SEMANTIC.light.warning)
  })

  it('buildAppCanvasThemeMessage accepts explicit pack and mode', () => {
    const msg = buildAppCanvasThemeMessage('ember', 'light')
    expect(msg.pack).toBe('ember')
    expect(msg.mode).toBe('light')
    expect(msg.tokens.brand).toBe(APP_CANVAS_BY_BRAND.ember.light.brand)
    expect(msg.tokens.danger).toBe(APP_CANVAS_SEMANTIC.light.danger)
    expect(msg.tokens.brand).not.toBe(APP_CANVAS_BY_BRAND.nova.light.brand)
  })

  it('every pack has a distinct dark brand accent', () => {
    const brands = new Set(BRAND_THEMES.map((b: BrandTheme) => APP_CANVAS_BY_BRAND[b].dark.brand))
    expect(brands.size).toBe(BRAND_THEMES.length)
  })

  it('defaults to product default (classic) canvas for DEFAULT_BRAND_THEME', () => {
    document.documentElement.classList.add('dark')
    expect(getAppCanvasTokens(DEFAULT_BRAND_THEME, 'dark').canvas).toBe(
      APP_CANVAS_BY_BRAND.classic.dark.canvas,
    )
  })
})
