import { useState, useEffect, useCallback } from 'react'
import { AlertTriangle, Loader2, Save } from 'lucide-react'

import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Switch } from '@/components/ui/switch'
import { cn } from '@/lib/utils'

import { useTheme } from '../../hooks/useTheme'
import { BRAND_THEME_META } from '../../themes/brandTheme'
import type { BrandTheme } from '../../themes/brandTheme'
import type { SettingsData, WebCapableTools, StandardServer } from './settingsApi'
import * as adminApi from '../../api/platformAdmin'

const NONE_VALUE = '__none__'

interface GeneralSettingsProps {
  settings: SettingsData | null
  generalForm: {
    default_provider: string
    default_model: string
    web_search_tool: string
    web_extract_tool: string
    timezone: string
  }
  setGeneralForm: (form: GeneralSettingsProps['generalForm']) => void
  webCapableTools: WebCapableTools
  standardServers: StandardServer[]
  saving: boolean
  onSave: () => void
  onSectionChange?: (section: string) => void
  isPlatform?: boolean
}

export default function GeneralSettings({
  generalForm,
  setGeneralForm,
  webCapableTools,
  standardServers,
  saving,
  onSave,
  onSectionChange,
  isPlatform = false
}: GeneralSettingsProps) {
  const hasUninstalledWebServers = standardServers.some(s => !s.installed)
  const hasNoWebTools = !generalForm.web_search_tool && !generalForm.web_extract_tool

  return (
    <div className="max-w-2xl space-y-6">
      {isPlatform && <EnvironmentSection />}

      <BrandThemeSection />

      <Card className="border-border bg-card shadow-sm">
        <CardHeader>
          <CardTitle className="text-base">Web Tools</CardTitle>
          <CardDescription>
            Choose which configured MCP tools Astonish should use for web search and URL extraction.
          </CardDescription>
        </CardHeader>
        <CardContent className="space-y-5">
          <div className="space-y-2">
            <Label htmlFor="web-search-tool">Web Search Tool</Label>
            <Select
              value={generalForm.web_search_tool || NONE_VALUE}
              onValueChange={(value) => setGeneralForm({
                ...generalForm,
                web_search_tool: value === NONE_VALUE ? '' : value
              })}
            >
              <SelectTrigger id="web-search-tool" className="w-full bg-background">
                <SelectValue placeholder="None (disabled)" />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value={NONE_VALUE}>None (disabled)</SelectItem>
                {webCapableTools.webSearch.map(tool => (
                  <SelectItem key={`${tool.source}:${tool.name}`} value={`${tool.source}:${tool.name}`}>
                    {tool.source} ({tool.name})
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
            <p className="text-xs text-muted-foreground">
              Used for internet search when finding MCP servers online.
            </p>
          </div>

          <div className="space-y-2">
            <Label htmlFor="web-extract-tool">Web Extract Tool</Label>
            <Select
              value={generalForm.web_extract_tool || NONE_VALUE}
              onValueChange={(value) => setGeneralForm({
                ...generalForm,
                web_extract_tool: value === NONE_VALUE ? '' : value
              })}
            >
              <SelectTrigger id="web-extract-tool" className="w-full bg-background">
                <SelectValue placeholder="None (disabled)" />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value={NONE_VALUE}>None (disabled)</SelectItem>
                {webCapableTools.webExtract.map(tool => (
                  <SelectItem key={`${tool.source}:${tool.name}`} value={`${tool.source}:${tool.name}`}>
                    {tool.source} ({tool.name})
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
            <p className="text-xs text-muted-foreground">
              Used to extract content from URLs when a user provides a link.
            </p>
          </div>

          {hasNoWebTools && hasUninstalledWebServers && (
            <Alert className="border-primary/20 bg-primary/5 text-foreground">
              <AlertDescription>
                No web tools are configured. Go to the{' '}
                <Button
                  type="button"
                  variant="link"
                  className="h-auto p-0 text-primary"
                  onClick={() => onSectionChange?.('mcp')}
                >
                  MCP Servers
                </Button>{' '}
                section to quick-install a web search provider.
              </AlertDescription>
            </Alert>
          )}
        </CardContent>
      </Card>

      <Card className="border-border bg-card shadow-sm">
        <CardHeader>
          <CardTitle className="text-base">Timezone</CardTitle>
          <CardDescription>
            Configure the default timezone used for scheduling and time display.
          </CardDescription>
        </CardHeader>
        <CardContent className="space-y-2">
          <Label htmlFor="timezone">IANA Timezone</Label>
          <Input
            id="timezone"
            type="text"
            value={generalForm.timezone}
            onChange={(e) => setGeneralForm({ ...generalForm, timezone: e.target.value })}
            placeholder="e.g. America/Sao_Paulo (leave empty for system default)"
            className="bg-background"
          />
          <p className="text-xs text-muted-foreground">
            Must be a valid IANA timezone identifier.
          </p>
        </CardContent>
      </Card>

      <Button onClick={onSave} disabled={saving} className="shadow-sm">
        {saving ? <Loader2 className="animate-spin" /> : <Save />}
        {saving ? 'Saving...' : 'Save Changes'}
      </Button>
    </div>
  )
}

function EnvironmentSection() {
  const [devEnvironment, setDevEnvironment] = useState(false)
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState('')
  const [success, setSuccess] = useState('')

  const load = useCallback(async () => {
    setLoading(true)
    try {
      const data = await adminApi.getPlatformAuthSettings()
      setDevEnvironment(data.dev_environment)
    } catch (e) {
      setError((e as Error).message)
    } finally {
      setLoading(false)
    }
  }, [])

  useEffect(() => { void load() }, [load])

  useEffect(() => {
    if (!success) return undefined
    const timer = setTimeout(() => setSuccess(''), 3000)
    return () => clearTimeout(timer)
  }, [success])

  useEffect(() => {
    if (!error) return undefined
    const timer = setTimeout(() => setError(''), 5000)
    return () => clearTimeout(timer)
  }, [error])

  const handleToggle = async () => {
    setSaving(true)
    try {
      const newValue = !devEnvironment
      const updated = await adminApi.savePlatformAuthSettings({ dev_environment: newValue })
      setDevEnvironment(updated.dev_environment)
      setSuccess(`Development environment ${newValue ? 'enabled' : 'disabled'}`)
    } catch (e) {
      setError((e as Error).message)
    } finally {
      setSaving(false)
    }
  }

  return (
    <Card className="border-border bg-card shadow-sm">
      <CardHeader>
        <CardTitle className="text-base">Environment</CardTitle>
        <CardDescription>
          Platform-wide environment flags for superadmins.
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-4">
        {error && (
          <Alert variant="destructive">
            <AlertTriangle />
            <AlertDescription>{error}</AlertDescription>
          </Alert>
        )}
        {success && (
          <Alert className="border-[color:var(--success)]/30 bg-[color:var(--success)]/10 text-foreground">
            <AlertDescription>{success}</AlertDescription>
          </Alert>
        )}

        {loading ? (
          <div className="flex items-center gap-2 py-2 text-sm text-muted-foreground">
            <Loader2 className="size-4 animate-spin" />
            Loading...
          </div>
        ) : (
          <div className="flex items-start justify-between gap-4 rounded-xl border border-border bg-background p-4">
            <div className="flex items-start gap-3">
              <div className={cn(
                'rounded-lg p-2',
                devEnvironment ? 'bg-[color:var(--warning)]/15 text-[color:var(--warning)]' : 'bg-muted text-muted-foreground'
              )}>
                <AlertTriangle className="size-4" />
              </div>
              <div>
                <Label htmlFor="development-environment" className="text-sm font-medium">
                  Development environment
                </Label>
                <p className="mt-1 text-xs leading-relaxed text-muted-foreground">
                  When enabled, all outbound emails include a warning banner indicating this is a development instance that may be unstable.
                </p>
              </div>
            </div>
            <Switch
              id="development-environment"
              checked={devEnvironment}
              disabled={saving}
              onCheckedChange={handleToggle}
              aria-label="Toggle development environment banner"
              className="mt-1"
            />
          </div>
        )}
      </CardContent>
    </Card>
  )
}

/**
 * Platform default brand pack (superadmin / Platform → General).
 * Used at login and when users choose “Use platform default”.
 */
function BrandThemeSection() {
  const {
    platformBrandDefault,
    setPlatformBrandTheme,
    availableBrandThemes,
    brandTheme,
  } = useTheme()
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState('')
  const value = (platformBrandDefault || brandTheme) as BrandTheme

  const onChange = async (next: string) => {
    if (!next || next === value) return
    setSaving(true)
    setError('')
    try {
      await setPlatformBrandTheme(next as BrandTheme)
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Failed to save')
    } finally {
      setSaving(false)
    }
  }

  return (
    <Card className="border-border bg-card shadow-sm">
      <CardHeader>
        <CardTitle className="text-base">Default brand theme</CardTitle>
        <CardDescription>
          Instance-wide color pack for the login screen and for users who have not chosen a personal theme.
          Each user can override this under Personal → General. Light/dark mode is separate (header toggle).
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-2">
        {error && (
          <Alert variant="destructive">
            <AlertTriangle />
            <AlertDescription>{error}</AlertDescription>
          </Alert>
        )}
        <Label htmlFor="platform-brand-theme">Pack</Label>
        <Select value={value} onValueChange={onChange} disabled={saving}>
          <SelectTrigger id="platform-brand-theme" className="w-full bg-background" data-testid="platform-brand-theme-select">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {availableBrandThemes.map((id) => (
              <SelectItem key={id} value={id}>
                {BRAND_THEME_META[id].label}
                <span className="ml-2 text-muted-foreground">· {BRAND_THEME_META[id].tone}</span>
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <p className="text-xs text-muted-foreground">
          Default: <span className="font-medium text-foreground">{BRAND_THEME_META[value]?.label ?? value}</span>
          {saving ? ' · Saving…' : ''}
        </p>
      </CardContent>
    </Card>
  )
}
