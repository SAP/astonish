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

  it('ships Nova and Aster; product default is Aster', () => {
    expect(BRAND_THEME_META.nova.shipped).toBe(true)
    expect(BRAND_THEME_META.aster.shipped).toBe(true)
    expect(DEFAULT_BRAND_THEME).toBe('aster')
  })

  it('lists Aster first in select order among shipped packs', () => {
    const shipped = shippedBrandThemes()
    expect(shipped[0]).toBe('aster')
    expect(shipped).toContain('nova')
    expect(BRAND_THEME_SELECT_ORDER[0]).toBe('aster')
  })

  it('resolveBrandTheme cascades user → platform → aster', () => {
    expect(resolveBrandTheme('', '')).toBe('aster')
    expect(resolveBrandTheme('', 'nova')).toBe('nova')
    expect(resolveBrandTheme('aster', 'nova')).toBe('aster')
    expect(resolveBrandTheme('ember', 'nova')).toBe('nova')
  })

  it('applyBrandTheme sets data-theme for Aster', () => {
    const next = applyBrandTheme('aster')
    expect(next).toBe('aster')
    expect(document.documentElement.dataset.theme).toBe('aster')
    expect(localStorage.getItem(BRAND_THEME_STORAGE_KEY)).toBe('aster')
  })

  it('applyBrandTheme falls back for unshipped packs to Aster', () => {
    const next = applyBrandTheme('ember')
    expect(next).toBe(DEFAULT_BRAND_THEME)
    expect(document.documentElement.dataset.theme).toBe('aster')
  })

  it('getStoredBrandTheme returns shipped pack when stored', () => {
    localStorage.setItem(BRAND_THEME_STORAGE_KEY, 'nova')
    expect(getStoredBrandTheme()).toBe('nova')
  })
})
