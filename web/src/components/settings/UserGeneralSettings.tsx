import { useState } from 'react'
import { AlertTriangle } from 'lucide-react'

import { Alert, AlertDescription } from '@/components/ui/alert'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Label } from '@/components/ui/label'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'

import { useTheme } from '../../hooks/useTheme'
import { BRAND_THEME_META, DEFAULT_BRAND_THEME, type BrandTheme } from '../../themes/brandTheme'

const INHERIT_VALUE = '__platform_default__'

/**
 * Personal → General: per-user preferences (brand theme first; expand later).
 */
export default function UserGeneralSettings() {
  const {
    brandTheme,
    userBrandPreference,
    platformBrandDefault,
    setUserBrandTheme,
    availableBrandThemes,
  } = useTheme()
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState('')

  const selectValue = userBrandPreference ? userBrandPreference : INHERIT_VALUE
  const platformLabel = platformBrandDefault
    ? BRAND_THEME_META[platformBrandDefault as BrandTheme]?.label || platformBrandDefault
    : BRAND_THEME_META[DEFAULT_BRAND_THEME].label

  const onChange = async (value: string) => {
    setSaving(true)
    setError('')
    try {
      if (value === INHERIT_VALUE) {
        await setUserBrandTheme('')
      } else {
        await setUserBrandTheme(value as BrandTheme)
      }
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Failed to save')
    } finally {
      setSaving(false)
    }
  }

  return (
    <div className="max-w-2xl space-y-6">
      <Card className="border-border bg-card shadow-sm">
        <CardHeader>
          <CardTitle className="text-base">Brand theme</CardTitle>
          <CardDescription>
            Your color pack for Studio chrome and generated apps. Layout stays the same — only colors change.
            Light/dark mode is separate (header toggle).
          </CardDescription>
        </CardHeader>
        <CardContent className="space-y-2">
          {error && (
            <Alert variant="destructive">
              <AlertTriangle />
              <AlertDescription>{error}</AlertDescription>
            </Alert>
          )}
          <Label htmlFor="user-brand-theme">Pack</Label>
          <Select value={selectValue} onValueChange={onChange} disabled={saving}>
            <SelectTrigger id="user-brand-theme" className="w-full bg-background" data-testid="user-brand-theme-select">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value={INHERIT_VALUE}>
                Use platform default ({platformLabel})
              </SelectItem>
              {availableBrandThemes.map((id) => (
                <SelectItem key={id} value={id}>
                  {BRAND_THEME_META[id].label}
                  <span className="ml-2 text-muted-foreground">· {BRAND_THEME_META[id].tone}</span>
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          <p className="text-xs text-muted-foreground">
            Active:{' '}
            <span className="font-medium text-foreground">
              {BRAND_THEME_META[brandTheme]?.label ?? brandTheme}
            </span>
            {userBrandPreference
              ? ' · personal preference'
              : ' · inheriting platform default'}
            {saving ? ' · Saving…' : ''}
          </p>
        </CardContent>
      </Card>
    </div>
  )
}
