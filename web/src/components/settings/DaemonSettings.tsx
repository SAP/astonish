import { useState, useEffect } from 'react'
import { AlertCircle, AlertTriangle, Check, Loader2, Save } from 'lucide-react'

import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Switch } from '@/components/ui/switch'

import { saveFullConfigSection } from './settingsApi'

export default function DaemonSettings({ config, onSaved }: { config: Record<string, any>; onSaved?: () => void }) {
  const [form, setForm] = useState({
    port: 9393,
    log_dir: '',
    auth: {
      disabled: false,
      session_ttl_days: 90
    }
  })
  const [saving, setSaving] = useState(false)
  const [saveSuccess, setSaveSuccess] = useState(false)
  const [restartRequired, setRestartRequired] = useState(false)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    if (config) {
      setForm({
        port: config.port || 9393,
        log_dir: config.log_dir || '',
        auth: {
          disabled: config.auth?.disabled || false,
          session_ttl_days: config.auth?.session_ttl_days || 90
        }
      })
    }
  }, [config])

  const handleSave = async () => {
    setSaving(true)
    setSaveSuccess(false)
    setRestartRequired(false)
    setError(null)
    try {
      const result = await saveFullConfigSection('daemon', form)
      setSaveSuccess(true)
      if (result.restart_required) {
        setRestartRequired(true)
      }
      if (onSaved) onSaved()
      setTimeout(() => setSaveSuccess(false), 2000)
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setSaving(false)
    }
  }

  return (
    <div className="max-w-2xl space-y-6">
      {restartRequired && (
        <Alert className="border-[color:var(--warning)]/30 bg-[color:var(--warning)]/10 text-foreground">
          <AlertTriangle className="text-[color:var(--warning)]" />
          <AlertDescription>
            Settings saved. Restart the daemon for port or authentication changes to take effect.
          </AlertDescription>
        </Alert>
      )}

      <Card className="border-border bg-card shadow-sm">
        <CardHeader>
          <CardTitle className="text-base">HTTP Server</CardTitle>
          <CardDescription>
            Configure the Studio daemon network port and log location.
          </CardDescription>
        </CardHeader>
        <CardContent className="space-y-4">
          <div className="space-y-2">
            <Label htmlFor="daemon-port">HTTP Port</Label>
            <Input
              id="daemon-port"
              type="number"
              value={form.port}
              onChange={(e) => setForm({ ...form, port: parseInt(e.target.value) || 9393 })}
              min="1024"
              max="65535"
              className="max-w-40 bg-background"
            />
            <p className="text-xs text-muted-foreground">
              Port for the Astonish daemon HTTP server. Requires restart to take effect.
            </p>
          </div>

          <div className="space-y-2">
            <Label htmlFor="daemon-log-directory">Log Directory</Label>
            <Input
              id="daemon-log-directory"
              type="text"
              value={form.log_dir}
              onChange={(e) => setForm({ ...form, log_dir: e.target.value })}
              placeholder="~/.config/astonish/logs/ (default)"
              className="bg-background font-mono"
            />
          </div>
        </CardContent>
      </Card>

      <Card className="border-border bg-card shadow-sm">
        <CardHeader>
          <CardTitle className="text-base">Studio Authentication</CardTitle>
          <CardDescription>
            Manage local Studio authentication and authorized-session lifetime.
          </CardDescription>
        </CardHeader>
        <CardContent className="space-y-4">
          <div className="flex items-start justify-between gap-4 rounded-xl border border-border bg-background p-4">
            <div>
              <Label htmlFor="disable-studio-auth">Disable Authentication</Label>
              <p className="mt-1 text-xs leading-relaxed text-muted-foreground">
                Turn off device-based authentication for the Studio web UI. Not recommended for remote access.
              </p>
            </div>
            <Switch
              id="disable-studio-auth"
              checked={form.auth.disabled}
              onCheckedChange={(checked) => setForm({ ...form, auth: { ...form.auth, disabled: checked } })}
              aria-label="Toggle Studio authentication"
            />
          </div>

          {!form.auth.disabled && (
            <div className="space-y-2">
              <Label htmlFor="session-ttl-days">Session TTL (days)</Label>
              <Input
                id="session-ttl-days"
                type="number"
                value={form.auth.session_ttl_days}
                onChange={(e) => setForm({ ...form, auth: { ...form.auth, session_ttl_days: parseInt(e.target.value) || 90 } })}
                min="1"
                max="365"
                className="max-w-40 bg-background"
              />
              <p className="text-xs text-muted-foreground">
                How many days an authorized session remains valid before re-authorization.
              </p>
            </div>
          )}
        </CardContent>
      </Card>

      <div className="flex flex-wrap items-center gap-3">
        <Button onClick={handleSave} disabled={saving} className="shadow-sm">
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
