import { describe, it, expect, beforeEach, afterEach } from 'vitest'
import {
  applyBrandTheme,
  BRAND_THEME_META,
  DEFAULT_BRAND_THEME,
  getStoredBrandTheme,
  BRAND_THEME_STORAGE_KEY,
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

  it('ships Nova and Aster for theme framework testing', () => {
    expect(BRAND_THEME_META.nova.shipped).toBe(true)
    expect(BRAND_THEME_META.aster.shipped).toBe(true)
  })

  it('applyBrandTheme sets data-theme for Aster', () => {
    const next = applyBrandTheme('aster')
    expect(next).toBe('aster')
    expect(document.documentElement.dataset.theme).toBe('aster')
    expect(localStorage.getItem(BRAND_THEME_STORAGE_KEY)).toBe('aster')
  })

  it('applyBrandTheme falls back for unshipped packs', () => {
    const next = applyBrandTheme('ember')
    expect(next).toBe(DEFAULT_BRAND_THEME)
    expect(document.documentElement.dataset.theme).toBe(DEFAULT_BRAND_THEME)
  })

  it('getStoredBrandTheme returns shipped Aster when stored', () => {
    localStorage.setItem(BRAND_THEME_STORAGE_KEY, 'aster')
    expect(getStoredBrandTheme()).toBe('aster')
  })
})
