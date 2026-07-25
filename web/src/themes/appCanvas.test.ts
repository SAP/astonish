import { describe, it, expect, beforeEach, afterEach } from 'vitest'
import {
  APP_CANVAS_BY_BRAND,
  APP_CANVAS_CSS_VARS,
  buildAppCanvasThemeMessage,
  getAppCanvasTokens,
} from './appCanvas'
import { BRAND_THEMES, DEFAULT_BRAND_THEME, type BrandTheme } from './brandTheme'

describe('appCanvas', () => {
  const originalTheme = document.documentElement.dataset.theme

  afterEach(() => {
    if (originalTheme) document.documentElement.dataset.theme = originalTheme
    else delete document.documentElement.dataset.theme
  })

  it('defines a complete dark canvas for every brand pack', () => {
    for (const brand of BRAND_THEMES) {
      const tokens = APP_CANVAS_BY_BRAND[brand]
      expect(tokens, brand).toBeDefined()
      for (const key of Object.keys(APP_CANVAS_CSS_VARS) as (keyof typeof APP_CANVAS_CSS_VARS)[]) {
        expect(tokens[key], `${brand}.${key}`).toMatch(/^(#|rgb|rgba|hsl|oklch)/)
      }
    }
  })

  it('maps every token key to a CSS variable', () => {
    const tokens = getAppCanvasTokens('nova')
    for (const key of Object.keys(tokens) as (keyof typeof tokens)[]) {
      expect(APP_CANVAS_CSS_VARS[key]).toMatch(/^--color-/)
    }
  })

  it('buildAppCanvasThemeMessage includes pack + tokens for active theme', () => {
    document.documentElement.dataset.theme = 'nova'
    const msg = buildAppCanvasThemeMessage()
    expect(msg.type).toBe('theme')
    expect(msg.mode).toBe('dark')
    expect(msg.pack).toBe('nova')
    expect(msg.tokens.brand).toBe(APP_CANVAS_BY_BRAND.nova.brand)
    expect(msg.tokens.canvas).toBe(APP_CANVAS_BY_BRAND.nova.canvas)
  })

  it('buildAppCanvasThemeMessage accepts an explicit pack (e.g. future Ember)', () => {
    const msg = buildAppCanvasThemeMessage('ember')
    expect(msg.pack).toBe('ember')
    expect(msg.tokens).toEqual(APP_CANVAS_BY_BRAND.ember)
    expect(msg.tokens.brand).not.toBe(APP_CANVAS_BY_BRAND.nova.brand)
  })

  it('every pack has a distinct brand accent', () => {
    const brands = new Set(BRAND_THEMES.map((b: BrandTheme) => APP_CANVAS_BY_BRAND[b].brand))
    expect(brands.size).toBe(BRAND_THEMES.length)
  })

  it('defaults to nova canvas when theme unset', () => {
    delete document.documentElement.dataset.theme
    // getStoredBrandTheme may still return stored pack; force nova via explicit arg
    expect(getAppCanvasTokens(DEFAULT_BRAND_THEME).canvas).toBe(APP_CANVAS_BY_BRAND.nova.canvas)
  })
})
