import { useState, useEffect, useCallback } from 'react'

import {
  applyBrandTheme,
  getStoredBrandTheme,
  type BrandTheme,
  BRAND_THEME_META,
  isBrandTheme,
  isShippedBrandTheme,
  resolveBrandTheme,
  shippedBrandThemes,
  DEFAULT_BRAND_THEME,
} from '../themes/brandTheme'
import {
  fetchPublicBrandTheme,
  fetchUserBrandTheme,
  patchUserBrandTheme,
} from '../api/userSettings'
import { getPlatformBrandTheme, savePlatformBrandTheme } from '../api/platformAdmin'

type Theme = 'dark' | 'light'

export interface UseThemeReturn {
  /** Light / dark mode (independent of brand pack). */
  theme: Theme
  setTheme: (theme: Theme) => void
  toggleTheme: () => void
  /** Effective brand pack currently applied to the DOM. */
  brandTheme: BrandTheme
  /**
   * Set the logged-in user's brand preference (empty string = inherit platform).
   * Applies the effective theme after save.
   */
  setUserBrandTheme: (brand: BrandTheme | '') => Promise<void>
  /**
   * Set platform default brand theme (superadmin). Does not force-overwrite users
   * who already set a personal preference.
   */
  setPlatformBrandTheme: (brand: BrandTheme) => Promise<void>
  /** Stored user preference (empty = inherit). */
  userBrandPreference: string
  /** Platform default pack id. */
  platformBrandDefault: string
  /** Shipped packs in display order. */
  availableBrandThemes: BrandTheme[]
  /** Reload effective theme from server (after login). */
  refreshBrandTheme: () => Promise<void>
  /** @deprecated use setUserBrandTheme */
  setBrandTheme: (brand: BrandTheme) => void
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
  const [userBrandPreference, setUserBrandPreference] = useState('')
  const [platformBrandDefault, setPlatformBrandDefault] = useState('')

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

  const refreshBrandTheme = useCallback(async () => {
    try {
      // Prefer user settings when authenticated (credentials included).
      const user = await fetchUserBrandTheme()
      const effective = resolveBrandTheme(user.brand_theme, user.platform_default)
      setUserBrandPreference(user.brand_theme || '')
      setPlatformBrandDefault(user.platform_default || '')
      setBrandThemeState(effective)
      applyBrandTheme(effective)
      return
    } catch {
      // Not logged in or personal settings unavailable — public platform default.
    }
    try {
      const pub = await fetchPublicBrandTheme()
      const effective = resolveBrandTheme('', pub.brand_theme)
      setUserBrandPreference('')
      setPlatformBrandDefault(pub.source === 'platform' ? pub.brand_theme : '')
      setBrandThemeState(effective)
      applyBrandTheme(effective)
    } catch {
      const fallback = DEFAULT_BRAND_THEME
      setBrandThemeState(fallback)
      applyBrandTheme(fallback)
    }
  }, [])

  // Resolve from server on mount (after localStorage paint).
  useEffect(() => {
    void refreshBrandTheme()
  }, [refreshBrandTheme])

  const toggleTheme = () => {
    setTheme(prev => (prev === 'dark' ? 'light' : 'dark'))
  }

  const setUserBrandTheme = useCallback(async (brand: BrandTheme | '') => {
    if (brand !== '' && !isShippedBrandTheme(brand)) return
    try {
      const res = await patchUserBrandTheme(brand)
      setUserBrandPreference(res.brand_theme || '')
      if (res.platform_default) setPlatformBrandDefault(res.platform_default)
      const effective = resolveBrandTheme(res.brand_theme, res.platform_default || platformBrandDefault)
      setBrandThemeState(effective)
      applyBrandTheme(effective)
    } catch {
      // Fallback: apply locally if API unavailable (personal-mode edge cases).
      if (brand === '') {
        const effective = resolveBrandTheme('', platformBrandDefault)
        setUserBrandPreference('')
        setBrandThemeState(effective)
        applyBrandTheme(effective)
      } else {
        setUserBrandPreference(brand)
        setBrandThemeState(brand)
        applyBrandTheme(brand)
      }
    }
  }, [platformBrandDefault])

  const setPlatformBrandTheme = useCallback(async (brand: BrandTheme) => {
    if (!isShippedBrandTheme(brand)) return
    const res = await savePlatformBrandTheme(brand)
    const next = res.default_brand_theme
    setPlatformBrandDefault(next)
    // If user inherits (no personal preference), re-apply effective.
    if (!userBrandPreference) {
      const effective = resolveBrandTheme('', next)
      setBrandThemeState(effective)
      applyBrandTheme(effective)
    }
  }, [userBrandPreference])

  /** Legacy: set user preference when brand is shipped. */
  const setBrandTheme = useCallback((brand: BrandTheme) => {
    if (!isBrandTheme(brand) || !BRAND_THEME_META[brand].shipped) return
    void setUserBrandTheme(brand)
  }, [setUserBrandTheme])

  const availableBrandThemes = shippedBrandThemes()

  return {
    theme,
    setTheme,
    toggleTheme,
    brandTheme,
    setUserBrandTheme,
    setPlatformBrandTheme,
    userBrandPreference,
    platformBrandDefault,
    availableBrandThemes,
    refreshBrandTheme,
    setBrandTheme,
  }
}

// Re-export for callers that only need a one-shot platform default load.
export async function loadPlatformBrandDefault(): Promise<BrandTheme> {
  try {
    const pub = await fetchPublicBrandTheme()
    return resolveBrandTheme('', pub.brand_theme)
  } catch {
    return DEFAULT_BRAND_THEME
  }
}

export async function loadPlatformBrandDefaultAdmin(): Promise<BrandTheme> {
  try {
    const res = await getPlatformBrandTheme()
    return resolveBrandTheme('', res.default_brand_theme)
  } catch {
    return DEFAULT_BRAND_THEME
  }
}
