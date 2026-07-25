import { useState, useEffect } from 'react'
import { AlertCircle, Check, Loader2, Save } from 'lucide-react'

import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Switch } from '@/components/ui/switch'

import { saveFullConfigSection } from './settingsApi'

const DEFAULT_STORAGE_VALUE = '__default_file__'

export default function SessionsSettings({ config, onSaved }: { config: Record<string, any>; onSaved?: () => void }) {
  const [form, setForm] = useState({
    storage: '',
    base_dir: '',
    compaction: {
      enabled: true,
      threshold: 0.8,
      preserve_recent: 4
    },
    cleanup: {
      max_age_days: 5
    }
  })
  const [saving, setSaving] = useState(false)
  const [saveSuccess, setSaveSuccess] = useState(false)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    if (config) {
      setForm({
        storage: config.storage || '',
        base_dir: config.base_dir || '',
        compaction: {
          enabled: config.compaction?.enabled !== false,
          threshold: config.compaction?.threshold || 0.8,
          preserve_recent: config.compaction?.preserve_recent || 4
        },
        cleanup: {
          max_age_days: config.cleanup?.max_age_days ?? 5
        }
      })
    }
  }, [config])

  const handleSave = async () => {
    setSaving(true)
    setSaveSuccess(false)
    setError(null)
    try {
      await saveFullConfigSection('sessions', form)
      setSaveSuccess(true)
      if (onSaved) onSaved()
      setTimeout(() => setSaveSuccess(false), 2000)
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setSaving(false)
    }
  }

  const effectiveStorage = form.storage || 'file'

  return (
    <div className="max-w-2xl space-y-6">
      <Card className="border-border bg-card shadow-sm">
        <CardHeader>
          <CardTitle className="text-base">Storage</CardTitle>
          <CardDescription>
            Choose where session transcripts are stored and whether they survive restarts.
          </CardDescription>
        </CardHeader>
        <CardContent className="space-y-4">
          <div className="space-y-2">
            <Label htmlFor="session-storage">Storage Type</Label>
            <Select
              value={form.storage || DEFAULT_STORAGE_VALUE}
              onValueChange={(value) => setForm({ ...form, storage: value === DEFAULT_STORAGE_VALUE ? '' : value })}
            >
              <SelectTrigger id="session-storage" className="w-full bg-background">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value={DEFAULT_STORAGE_VALUE}>File (default)</SelectItem>
                <SelectItem value="file">File</SelectItem>
                <SelectItem value="memory">Memory</SelectItem>
              </SelectContent>
            </Select>
            <p className="text-xs leading-relaxed text-muted-foreground">
              File sessions are persisted to disk at ~/.config/astonish/sessions/. Memory sessions are stored in RAM only and are lost on restart.
            </p>
          </div>

          {effectiveStorage === 'file' && (
            <div className="space-y-2">
              <Label htmlFor="sessions-directory">Sessions Directory</Label>
              <Input
                id="sessions-directory"
                type="text"
                value={form.base_dir}
                onChange={(e) => setForm({ ...form, base_dir: e.target.value })}
                placeholder="~/.config/astonish/sessions/ (default)"
                className="bg-background font-mono"
              />
              <p className="text-xs text-muted-foreground">
                Directory where session files are stored.
              </p>
            </div>
          )}
        </CardContent>
      </Card>

      <Card className="border-border bg-card shadow-sm">
        <CardHeader>
          <CardTitle className="text-base">Context Compaction</CardTitle>
          <CardDescription>
            Automatically summarize older messages when the context window fills up.
          </CardDescription>
        </CardHeader>
        <CardContent className="space-y-4">
          <div className="flex items-start justify-between gap-4 rounded-xl border border-border bg-background p-4">
            <div>
              <Label htmlFor="context-compaction">Enable compaction</Label>
              <p className="mt-1 text-xs text-muted-foreground">Default: enabled.</p>
            </div>
            <Switch
              id="context-compaction"
              checked={form.compaction.enabled}
              onCheckedChange={(checked) => setForm({ ...form, compaction: { ...form.compaction, enabled: checked } })}
              aria-label="Toggle context compaction"
            />
          </div>

          {form.compaction.enabled && (
            <div className="grid gap-4 sm:grid-cols-2">
              <div className="space-y-2">
                <Label htmlFor="compaction-threshold">Threshold</Label>
                <Input
                  id="compaction-threshold"
                  type="number"
                  value={form.compaction.threshold}
                  onChange={(e) => setForm({ ...form, compaction: { ...form.compaction, threshold: parseFloat(e.target.value) || 0.8 } })}
                  min="0.1"
                  max="1.0"
                  step="0.05"
                  className="bg-background"
                />
                <p className="text-xs text-muted-foreground">Fraction of context window that triggers compaction.</p>
              </div>
              <div className="space-y-2">
                <Label htmlFor="preserve-recent">Preserve Recent</Label>
                <Input
                  id="preserve-recent"
                  type="number"
                  value={form.compaction.preserve_recent}
                  onChange={(e) => setForm({ ...form, compaction: { ...form.compaction, preserve_recent: parseInt(e.target.value) || 4 } })}
                  min="1"
                  max="20"
                  className="bg-background"
                />
                <p className="text-xs text-muted-foreground">Recent messages to keep intact.</p>
              </div>
            </div>
          )}
        </CardContent>
      </Card>

      <Card className="border-border bg-card shadow-sm">
        <CardHeader>
          <CardTitle className="text-base">Session Cleanup</CardTitle>
          <CardDescription>
            Automatically delete inactive sessions and destroy their associated containers.
          </CardDescription>
        </CardHeader>
        <CardContent className="space-y-2">
          <Label htmlFor="session-cleanup-days">Auto-delete after (days)</Label>
          <Input
            id="session-cleanup-days"
            type="number"
            value={form.cleanup.max_age_days}
            onChange={(e) => setForm({ ...form, cleanup: { ...form.cleanup, max_age_days: parseInt(e.target.value) || 0 } })}
            min="0"
            max="365"
            className="max-w-32 bg-background"
          />
          <p className="text-xs text-muted-foreground">
            Set to 0 to disable automatic cleanup. Default: 5 days.
          </p>
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
