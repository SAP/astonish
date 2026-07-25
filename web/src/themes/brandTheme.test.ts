import { describe, it, expect, beforeEach, afterEach } from 'vitest'
import {
  applyBrandTheme,
  BRAND_THEME_META,
  BRAND_THEME_SELECT_ORDER,
  DEFAULT_BRAND_THEME,
  getStoredBrandTheme,
  BRAND_THEME_STORAGE_KEY,
  resolveBrandTheme,
  shippedBrandThemes,
} from './brandTheme'

describe('brandTheme', () => {
  const originalTheme = document.documentElement.dataset.theme

  beforeEach(() => {
    localStorage.removeItem(BRAND_THEME_STORAGE_KEY)
    delete document.documentElement.dataset.theme
  })

  afterEach(() => {
    if (originalTheme) document.documentElement.dataset.theme = originalTheme
    else delete document.documentElement.dataset.theme
    localStorage.removeItem(BRAND_THEME_STORAGE_KEY)
  })

  it('ships bold, classic, and calm packs; product default is Classic', () => {
    expect(BRAND_THEME_META.nova.shipped).toBe(true)
    expect(BRAND_THEME_META.aster.shipped).toBe(true)
    expect(BRAND_THEME_META.classic.shipped).toBe(true)
    expect(BRAND_THEME_META.sage.shipped).toBe(true)
    expect(BRAND_THEME_META.ember.shipped).toBe(true)
    expect(BRAND_THEME_META.amethyst.shipped).toBe(false)
    expect(DEFAULT_BRAND_THEME).toBe('classic')
  })

  it('lists Classic first; then Aster, Nova, Sage, Ember', () => {
    const shipped = shippedBrandThemes()
    expect(shipped[0]).toBe('classic')
    expect(shipped).toEqual(['classic', 'aster', 'nova', 'sage', 'ember'])
    expect(BRAND_THEME_SELECT_ORDER[0]).toBe('classic')
  })

  it('applyBrandTheme accepts classic pre-rebrand pack', () => {
    expect(applyBrandTheme('classic')).toBe('classic')
    expect(document.documentElement.dataset.theme).toBe('classic')
  })

  it('resolveBrandTheme cascades user → platform → classic', () => {
    expect(resolveBrandTheme('', '')).toBe('classic')
    expect(resolveBrandTheme('', 'nova')).toBe('nova')
    expect(resolveBrandTheme('aster', 'nova')).toBe('aster')
    expect(resolveBrandTheme('amethyst', 'nova')).toBe('nova')
    expect(resolveBrandTheme('sage', 'nova')).toBe('sage')
  })

  it('applyBrandTheme sets data-theme for Classic', () => {
    const next = applyBrandTheme('classic')
    expect(next).toBe('classic')
    expect(document.documentElement.dataset.theme).toBe('classic')
    expect(localStorage.getItem(BRAND_THEME_STORAGE_KEY)).toBe('classic')
  })

  it('applyBrandTheme falls back for unshipped packs to Classic', () => {
    const next = applyBrandTheme('amethyst')
    expect(next).toBe(DEFAULT_BRAND_THEME)
    expect(document.documentElement.dataset.theme).toBe('classic')
  })

  it('applyBrandTheme accepts calm Sage and Ember', () => {
    expect(applyBrandTheme('sage')).toBe('sage')
    expect(applyBrandTheme('ember')).toBe('ember')
  })

  it('getStoredBrandTheme returns shipped pack when stored', () => {
    localStorage.setItem(BRAND_THEME_STORAGE_KEY, 'nova')
    expect(getStoredBrandTheme()).toBe('nova')
  })
})
