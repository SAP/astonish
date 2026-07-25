import { useState, useEffect } from 'react'
import { AlertCircle, Check, Loader2, Save } from 'lucide-react'

import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Switch } from '@/components/ui/switch'

import { saveFullConfigSection } from './settingsApi'

export default function SubAgentsSettings({ config, onSaved }: { config: Record<string, any>; onSaved?: () => void }) {
  const [form, setForm] = useState({
    enabled: true,
    max_depth: 2,
    max_concurrent: 5,
    task_timeout_sec: 300
  })
  const [saving, setSaving] = useState(false)
  const [saveSuccess, setSaveSuccess] = useState(false)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    if (config) {
      setForm({
        enabled: config.enabled !== false,
        max_depth: config.max_depth || 2,
        max_concurrent: config.max_concurrent || 5,
        task_timeout_sec: config.task_timeout_sec || 300
      })
    }
  }, [config])

  const handleSave = async () => {
    setSaving(true)
    setSaveSuccess(false)
    setError(null)
    try {
      await saveFullConfigSection('sub_agents', form)
      setSaveSuccess(true)
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
      <Card className="border-border bg-card shadow-sm">
        <CardHeader>
          <CardTitle className="text-base">Sub-Agents</CardTitle>
          <CardDescription>
            Allow the AI to delegate subtasks to specialized sub-agents for parallel execution.
          </CardDescription>
        </CardHeader>
        <CardContent className="space-y-4">
          <div className="flex items-start justify-between gap-4 rounded-xl border border-border bg-background p-4">
            <div>
              <Label htmlFor="sub-agents-enabled">Enable Sub-Agents</Label>
              <p className="mt-1 text-xs text-muted-foreground">Delegation can improve throughput on complex tasks.</p>
            </div>
            <Switch
              id="sub-agents-enabled"
              checked={form.enabled}
              onCheckedChange={(checked) => setForm({ ...form, enabled: checked })}
              aria-label="Enable sub-agents"
            />
          </div>

          {form.enabled && (
            <div className="grid gap-4 border-t pt-4 sm:grid-cols-3">
              <div className="space-y-2">
                <Label htmlFor="sub-agent-depth">Max Delegation Depth</Label>
                <Input
                  id="sub-agent-depth"
                  type="number"
                  value={form.max_depth}
                  onChange={(e) => setForm({ ...form, max_depth: parseInt(e.target.value) || 2 })}
                  min="1"
                  max="10"
                  className="bg-background"
                />
                <p className="text-xs text-muted-foreground">Maximum nesting depth for delegation chains.</p>
              </div>
              <div className="space-y-2">
                <Label htmlFor="sub-agent-concurrent">Max Concurrent</Label>
                <Input
                  id="sub-agent-concurrent"
                  type="number"
                  value={form.max_concurrent}
                  onChange={(e) => setForm({ ...form, max_concurrent: parseInt(e.target.value) || 5 })}
                  min="1"
                  max="20"
                  className="bg-background"
                />
                <p className="text-xs text-muted-foreground">Maximum sub-agents running in parallel.</p>
              </div>
              <div className="space-y-2">
                <Label htmlFor="sub-agent-timeout">Task Timeout (seconds)</Label>
                <Input
                  id="sub-agent-timeout"
                  type="number"
                  value={form.task_timeout_sec}
                  onChange={(e) => setForm({ ...form, task_timeout_sec: parseInt(e.target.value) || 300 })}
                  min="30"
                  max="3600"
                  className="bg-background"
                />
                <p className="text-xs text-muted-foreground">Maximum time before a sub-agent task is cancelled.</p>
              </div>
            </div>
          )}
        </CardContent>
      </Card>

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
