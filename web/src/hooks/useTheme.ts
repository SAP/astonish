import { useState, useEffect, useCallback } from 'react'

import {
  applyBrandTheme,
  getStoredBrandTheme,
  type BrandTheme,
  BRAND_THEME_META,
  isBrandTheme,
} from '../themes/brandTheme'

type Theme = 'dark' | 'light'

export interface UseThemeReturn {
  /** Light / dark mode (independent of brand pack). */
  theme: Theme
  setTheme: (theme: Theme) => void
  toggleTheme: () => void
  /** Brand pack (nova, future ember/amethyst/…). */
  brandTheme: BrandTheme
  setBrandTheme: (brand: BrandTheme) => void
  /** Shipped packs only — for switcher UIs later. */
  availableBrandThemes: BrandTheme[]
}

export function useTheme(): UseThemeReturn {
  const [theme, setTheme] = useState<Theme>(() => {
    const stored = localStorage.getItem('astonish-theme')
    if (stored === 'dark' || stored === 'light') return stored

    if (window.matchMedia('(prefers-color-scheme: dark)').matches) {
      return 'dark'
    }
    return 'light'
  })

  const [brandTheme, setBrandThemeState] = useState<BrandTheme>(() => getStoredBrandTheme())

  useEffect(() => {
    const root = window.document.documentElement

    if (theme === 'dark') {
      root.classList.add('dark')
    } else {
      root.classList.remove('dark')
    }

    localStorage.setItem('astonish-theme', theme)
  }, [theme])

  useEffect(() => {
    applyBrandTheme(brandTheme)
  }, [brandTheme])

  useEffect(() => {
    const mediaQuery = window.matchMedia('(prefers-color-scheme: dark)')

    const handleChange = (e: MediaQueryListEvent) => {
      const stored = localStorage.getItem('astonish-theme')
      if (!stored) {
        setTheme(e.matches ? 'dark' : 'light')
      }
    }

    mediaQuery.addEventListener('change', handleChange)
    return () => mediaQuery.removeEventListener('change', handleChange)
  }, [])

  const toggleTheme = () => {
    setTheme(prev => (prev === 'dark' ? 'light' : 'dark'))
  }

  const setBrandTheme = useCallback((brand: BrandTheme) => {
    if (!isBrandTheme(brand) || !BRAND_THEME_META[brand].shipped) return
    setBrandThemeState(brand)
  }, [])

  const availableBrandThemes = (Object.keys(BRAND_THEME_META) as BrandTheme[]).filter(
    (id) => BRAND_THEME_META[id].shipped
  )

  return { theme, setTheme, toggleTheme, brandTheme, setBrandTheme, availableBrandThemes }
}
