import { useState, useEffect } from 'react'
import { AlertCircle, Check, Info, Loader2, Save } from 'lucide-react'

import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Switch } from '@/components/ui/switch'

import { saveFullConfigSection } from './settingsApi'

interface BrowserForm {
  headless: boolean | null
  viewport_width: number
  viewport_height: number
  no_sandbox: boolean | null
  chrome_path: string
  user_data_dir: string
  navigation_timeout: number
  proxy: string
  remote_cdp_url: string
  fingerprint_seed: string
  fingerprint_platform: string
  handoff_bind_address: string
  handoff_port: number
}

interface BrowserSettingsProps {
  config: Record<string, any> | null
  onSaved?: () => void
}

export default function BrowserSettings({ config, onSaved }: BrowserSettingsProps) {
  const [form, setForm] = useState<BrowserForm>({
    headless: null,
    viewport_width: 1920,
    viewport_height: 1080,
    no_sandbox: null,
    chrome_path: '',
    user_data_dir: '',
    navigation_timeout: 30,
    proxy: '',
    remote_cdp_url: '',
    fingerprint_seed: '',
    fingerprint_platform: '',
    handoff_bind_address: '',
    handoff_port: 9222
  })
  const [saving, setSaving] = useState(false)
  const [saveSuccess, setSaveSuccess] = useState(false)
  const [error, setError] = useState<string | null>(null)

  // Derive engine type from config values — mirrors detectCurrentEngine() in Go
  const getEngineType = (cfg: BrowserForm): string => {
    if (cfg.remote_cdp_url) return 'remote'
    if (!cfg.chrome_path) return 'default'
    if (cfg.chrome_path.includes('cloakbrowser')) return 'cloakbrowser'
    return 'custom'
  }

  const [engineType, setEngineType] = useState('default')

  useEffect(() => {
    if (config) {
      const f: BrowserForm = {
        headless: config.headless ?? null,
        viewport_width: config.viewport_width || 1920,
        viewport_height: config.viewport_height || 1080,
        no_sandbox: config.no_sandbox ?? null,
        chrome_path: config.chrome_path || '',
        user_data_dir: config.user_data_dir || '',
        navigation_timeout: config.navigation_timeout || 30,
        proxy: config.proxy || '',
        remote_cdp_url: config.remote_cdp_url || '',
        fingerprint_seed: config.fingerprint_seed || '',
        fingerprint_platform: config.fingerprint_platform || '',
        handoff_bind_address: config.handoff_bind_address || '',
        handoff_port: config.handoff_port || 9222
      }
      setForm(f)
      setEngineType(getEngineType(f))
    }
  }, [config])

  const handleEngineChange = (type: string) => {
    setEngineType(type)
    // Clear engine-specific fields when switching
    if (type === 'default') {
      setForm(f => ({ ...f, chrome_path: '', remote_cdp_url: '', fingerprint_seed: '', fingerprint_platform: '' }))
    } else if (type === 'cloakbrowser') {
      setForm(f => ({ ...f, remote_cdp_url: '', fingerprint_platform: f.fingerprint_platform || 'windows' }))
    } else if (type === 'custom') {
      setForm(f => ({ ...f, remote_cdp_url: '', fingerprint_seed: '', fingerprint_platform: '' }))
    } else if (type === 'remote') {
      setForm(f => ({ ...f, chrome_path: '', fingerprint_seed: '', fingerprint_platform: '' }))
    }
  }

  const handleSave = async () => {
    setSaving(true)
    setSaveSuccess(false)
    setError(null)
    try {
      await saveFullConfigSection('browser', form as unknown as Record<string, unknown>)
      setSaveSuccess(true)
      if (onSaved) onSaved()
      setTimeout(() => setSaveSuccess(false), 2000)
    } catch (err: any) {
      setError(err.message)
    } finally {
      setSaving(false)
    }
  }

  const isHeadless = form.headless === true
  const isNoSandbox = form.no_sandbox === true

  return (
    <div className="max-w-2xl space-y-6">
      <Card className="border-border bg-card shadow-sm">
        <CardHeader>
          <CardTitle className="text-base">Browser Engine</CardTitle>
          <CardDescription>
            Choose the Chromium runtime Astonish uses for browser automation and human handoff.
          </CardDescription>
        </CardHeader>
        <CardContent className="space-y-4">
          <div className="space-y-2">
            <Label htmlFor="browser-engine">Browser Engine</Label>
            <Select value={engineType} onValueChange={handleEngineChange}>
              <SelectTrigger id="browser-engine" className="w-full bg-background">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value="default">Default (Chromium, auto-downloaded by Astonish)</SelectItem>
                <SelectItem value="cloakbrowser">CloakBrowser (anti-detect Chromium with stealth patches)</SelectItem>
                <SelectItem value="custom">Custom Chrome/Chromium path</SelectItem>
                <SelectItem value="remote">Remote browser (connect via CDP)</SelectItem>
              </SelectContent>
            </Select>
            <p className="text-xs leading-relaxed text-muted-foreground">
              {engineType === 'default' && 'Astonish will automatically download and manage a Chromium binary.'}
              {engineType === 'cloakbrowser' && 'CloakBrowser provides advanced fingerprint spoofing at the binary level. Install via CLI: astonish config browser.'}
              {engineType === 'custom' && 'Point to an existing Chrome or Chromium installation on your system.'}
              {engineType === 'remote' && 'Connect to a remote browser instance via Chrome DevTools Protocol.'}
            </p>
          </div>

          {engineType === 'custom' && (
            <div className="space-y-2">
              <Label htmlFor="chrome-binary-path">Chrome Binary Path</Label>
              <Input
                id="chrome-binary-path"
                type="text"
                value={form.chrome_path}
                onChange={(e) => setForm({ ...form, chrome_path: e.target.value })}
                placeholder="/usr/bin/google-chrome"
                className="bg-background font-mono"
              />
            </div>
          )}

          {engineType === 'cloakbrowser' && (
            <>
              <div className="space-y-2">
                <Label htmlFor="cloakbrowser-binary-path">Chrome Binary Path</Label>
                <Input
                  id="cloakbrowser-binary-path"
                  type="text"
                  value={form.chrome_path}
                  onChange={(e) => setForm({ ...form, chrome_path: e.target.value })}
                  placeholder="~/.cloakbrowser/chromium-.../chrome"
                  className="bg-background font-mono"
                />
                <p className="text-xs text-muted-foreground">
                  Path to the CloakBrowser binary. Use the CLI to auto-install: <code>astonish config browser</code>.
                </p>
              </div>
              <div className="grid gap-4 sm:grid-cols-2">
                <div className="space-y-2">
                  <Label htmlFor="fingerprint-platform">Fingerprint Platform</Label>
                  <Select
                    value={form.fingerprint_platform || 'windows'}
                    onValueChange={(value) => setForm({ ...form, fingerprint_platform: value })}
                  >
                    <SelectTrigger id="fingerprint-platform" className="bg-background">
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value="windows">Windows (recommended)</SelectItem>
                      <SelectItem value="macos">macOS</SelectItem>
                      <SelectItem value="linux">Linux</SelectItem>
                    </SelectContent>
                  </Select>
                </div>
                <div className="space-y-2">
                  <Label htmlFor="fingerprint-seed">Fingerprint Seed</Label>
                  <Input
                    id="fingerprint-seed"
                    type="text"
                    value={form.fingerprint_seed}
                    onChange={(e) => setForm({ ...form, fingerprint_seed: e.target.value })}
                    placeholder="e.g. 42000"
                    className="bg-background font-mono"
                  />
                  <p className="text-xs text-muted-foreground">Unique seed for consistent fingerprint generation.</p>
                </div>
              </div>
            </>
          )}

          {engineType === 'remote' && (
            <div className="space-y-2">
              <Label htmlFor="remote-cdp-url">Remote CDP URL</Label>
              <Input
                id="remote-cdp-url"
                type="text"
                value={form.remote_cdp_url}
                onChange={(e) => setForm({ ...form, remote_cdp_url: e.target.value })}
                placeholder="ws://192.168.1.100:9222/devtools/browser/..."
                className="bg-background font-mono"
              />
              <p className="text-xs text-muted-foreground">
                WebSocket URL of the Chrome DevTools Protocol endpoint. Use the CLI for auto-discovery: <code>astonish config browser</code>.
              </p>
            </div>
          )}
        </CardContent>
      </Card>

      <Card className="border-border bg-card shadow-sm">
        <CardHeader>
          <CardTitle className="text-base">Display</CardTitle>
          <CardDescription>Configure viewport size and whether the browser runs visibly.</CardDescription>
        </CardHeader>
        <CardContent className="space-y-4">
          <div className="flex items-start justify-between gap-4 rounded-xl border border-border bg-background p-4">
            <div>
              <Label htmlFor="browser-headless">Headless Mode</Label>
              <p className="mt-1 text-xs leading-relaxed text-muted-foreground">
                Run browser without a visible window. Headed mode with Xvfb produces more realistic fingerprints.
              </p>
            </div>
            <Switch
              id="browser-headless"
              checked={isHeadless}
              onCheckedChange={(checked) => setForm({ ...form, headless: checked ? true : null })}
              aria-label="Headless mode"
            />
          </div>

          <div className="grid gap-4 sm:grid-cols-2">
            <div className="space-y-2">
              <Label htmlFor="viewport-width">Viewport Width</Label>
              <Input
                id="viewport-width"
                type="number"
                value={form.viewport_width}
                onChange={(e) => setForm({ ...form, viewport_width: parseInt(e.target.value) || 1920 })}
                min="320"
                max="3840"
                className="bg-background"
              />
            </div>
            <div className="space-y-2">
              <Label htmlFor="viewport-height">Viewport Height</Label>
              <Input
                id="viewport-height"
                type="number"
                value={form.viewport_height}
                onChange={(e) => setForm({ ...form, viewport_height: parseInt(e.target.value) || 1080 })}
                min="240"
                max="2160"
                className="bg-background"
              />
            </div>
          </div>
        </CardContent>
      </Card>

      <Card className="border-border bg-card shadow-sm">
        <CardHeader>
          <CardTitle className="text-base">Network</CardTitle>
          <CardDescription>Route browser traffic and tune navigation waits.</CardDescription>
        </CardHeader>
        <CardContent className="space-y-4">
          <div className="space-y-2">
            <Label htmlFor="browser-proxy">Proxy</Label>
            <Input
              id="browser-proxy"
              type="text"
              value={form.proxy}
              onChange={(e) => setForm({ ...form, proxy: e.target.value })}
              placeholder="http://user:pass@host:port or socks5://host:port"
              className="bg-background font-mono"
            />
            <p className="text-xs text-muted-foreground">Route browser traffic through an HTTP or SOCKS proxy.</p>
          </div>
          <div className="space-y-2">
            <Label htmlFor="navigation-timeout">Navigation Timeout (seconds)</Label>
            <Input
              id="navigation-timeout"
              type="number"
              value={form.navigation_timeout}
              onChange={(e) => setForm({ ...form, navigation_timeout: parseInt(e.target.value) || 30 })}
              min="5"
              max="300"
              className="max-w-40 bg-background"
            />
          </div>
        </CardContent>
      </Card>

      <Card className="border-border bg-card shadow-sm">
        <CardHeader>
          <CardTitle className="text-base">Advanced</CardTitle>
          <CardDescription>Configure persistent profiles, Chrome sandbox behavior, and CDP handoff.</CardDescription>
        </CardHeader>
        <CardContent className="space-y-4">
          <div className="space-y-2">
            <Label htmlFor="user-data-directory">User Data Directory</Label>
            <Input
              id="user-data-directory"
              type="text"
              value={form.user_data_dir}
              onChange={(e) => setForm({ ...form, user_data_dir: e.target.value })}
              placeholder="~/.config/astonish/browser/ (default)"
              className="bg-background font-mono"
            />
            <p className="text-xs text-muted-foreground">Persistent browser profile directory. Stores cookies, localStorage, etc.</p>
          </div>

          <div className="flex items-start justify-between gap-4 rounded-xl border border-border bg-background p-4">
            <div>
              <Label htmlFor="browser-no-sandbox">No Sandbox</Label>
              <p className="mt-1 text-xs text-muted-foreground">Disable Chrome sandbox. Auto-enabled when running as root.</p>
            </div>
            <Switch
              id="browser-no-sandbox"
              checked={isNoSandbox}
              onCheckedChange={(checked) => setForm({ ...form, no_sandbox: checked ? true : null })}
              aria-label="No sandbox"
            />
          </div>

          <div className="grid gap-4 sm:grid-cols-2">
            <div className="space-y-2">
              <Label htmlFor="handoff-bind-address">Handoff Bind Address</Label>
              <Input
                id="handoff-bind-address"
                type="text"
                value={form.handoff_bind_address}
                onChange={(e) => setForm({ ...form, handoff_bind_address: e.target.value })}
                placeholder="0.0.0.0 (default)"
                className="bg-background font-mono"
              />
              <p className="text-xs text-muted-foreground">CDP handoff proxy bind address for human-in-the-loop.</p>
            </div>
            <div className="space-y-2">
              <Label htmlFor="handoff-port">Handoff Port</Label>
              <Input
                id="handoff-port"
                type="number"
                value={form.handoff_port}
                onChange={(e) => setForm({ ...form, handoff_port: parseInt(e.target.value) || 9222 })}
                min="1024"
                max="65535"
                className="bg-background"
              />
            </div>
          </div>
        </CardContent>
      </Card>

      {engineType === 'cloakbrowser' && (
        <Alert className="border-primary/30 bg-primary/10 text-foreground">
          <Info className="text-primary" />
          <AlertDescription>
            CloakBrowser dependency installation (Python, pip, Xvfb) is only available through the CLI. Run{' '}
            <code className="rounded bg-background px-1 py-0.5">astonish config browser</code> for guided setup.
          </AlertDescription>
        </Alert>
      )}

      <div className="flex flex-wrap items-center gap-3">
        <Button onClick={handleSave} disabled={saving}>
          {saving ? <Loader2 className="animate-spin" /> : <Save />}
          {saving ? 'Saving...' : 'Save Changes'}
        </Button>
        {saveSuccess && (
          <span className="flex items-center gap-1 text-sm text-[color:var(--success)]">
            <Check className="size-4" /> Saved
          </span>
        )}
        {error && (
          <Alert variant="destructive" className="w-auto py-2">
            <AlertCircle />
            <AlertDescription>{error}</AlertDescription>
          </Alert>
        )}
      </div>
    </div>
  )
}
