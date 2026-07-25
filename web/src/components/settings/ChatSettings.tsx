import { useState, useEffect } from 'react'
import { AlertCircle, Check, Loader2, Save } from 'lucide-react'

import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Switch } from '@/components/ui/switch'
import { Textarea } from '@/components/ui/textarea'

import { saveFullConfigSection } from './settingsApi'

export default function ChatSettings({ config, onSaved }: { config: Record<string, any>; onSaved?: () => void }) {
  const [form, setForm] = useState({
    system_prompt: '',
    max_tool_calls: 0,
    max_tools: 0,
    auto_approve: false,
    workspace_dir: '',
    flow_save_dir: ''
  })
  const [saving, setSaving] = useState(false)
  const [saveSuccess, setSaveSuccess] = useState(false)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    if (config) {
      setForm({
        system_prompt: config.system_prompt || '',
        max_tool_calls: config.max_tool_calls || 0,
        max_tools: config.max_tools || 0,
        auto_approve: config.auto_approve || false,
        workspace_dir: config.workspace_dir || '',
        flow_save_dir: config.flow_save_dir || ''
      })
    }
  }, [config])

  const handleSave = async () => {
    setSaving(true)
    setSaveSuccess(false)
    setError(null)
    try {
      await saveFullConfigSection('chat', form)
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
          <CardTitle className="text-base">System Prompt</CardTitle>
          <CardDescription>
            Customize the instructions appended to Astonish&apos;s built-in system prompt.
          </CardDescription>
        </CardHeader>
        <CardContent className="space-y-2">
          <Label htmlFor="system-prompt">System Prompt</Label>
          <Textarea
            id="system-prompt"
            value={form.system_prompt}
            onChange={(e) => setForm({ ...form, system_prompt: e.target.value })}
            placeholder="You are Astonish, an AI assistant with access to tools..."
            rows={5}
            className="min-h-28 resize-y bg-background"
          />
          <p className="text-xs leading-relaxed text-muted-foreground">
            Leave empty to use only the default prompt: “You are Astonish, an AI assistant with access to tools. You help users accomplish tasks by calling tools and reasoning through problems.”
          </p>
        </CardContent>
      </Card>

      <Card className="border-border bg-card shadow-sm">
        <CardHeader>
          <CardTitle className="text-base">Tool Limits</CardTitle>
          <CardDescription>
            Control how many tool calls and tool definitions are available during chat.
          </CardDescription>
        </CardHeader>
        <CardContent className="grid gap-4 sm:grid-cols-2">
          <div className="space-y-2">
            <Label htmlFor="max-tool-calls">Max Tool Calls</Label>
            <Input
              id="max-tool-calls"
              type="number"
              value={form.max_tool_calls || ''}
              onChange={(e) => setForm({ ...form, max_tool_calls: parseInt(e.target.value) || 0 })}
              placeholder="100 (default)"
              min="0"
              className="bg-background"
            />
            <p className="text-xs text-muted-foreground">
              Maximum tool calls per conversation turn. Set to 0 to use the default.
            </p>
          </div>
          <div className="space-y-2">
            <Label htmlFor="max-tools">Max Tools</Label>
            <Input
              id="max-tools"
              type="number"
              value={form.max_tools || ''}
              onChange={(e) => setForm({ ...form, max_tools: parseInt(e.target.value) || 0 })}
              placeholder="128 (default)"
              min="0"
              className="bg-background"
            />
            <p className="text-xs text-muted-foreground">
              Maximum tools exposed to the LLM. Set to 0 to use the default.
            </p>
          </div>
        </CardContent>
      </Card>

      <Card className="border-border bg-card shadow-sm">
        <CardHeader>
          <CardTitle className="text-base">Approvals</CardTitle>
          <CardDescription>
            Configure whether tool executions require interactive approval.
          </CardDescription>
        </CardHeader>
        <CardContent>
          <div className="flex items-start justify-between gap-4 rounded-xl border border-border bg-background p-4">
            <div>
              <Label htmlFor="auto-approve-tool-calls">Auto-Approve Tool Calls</Label>
              <p className="mt-1 text-xs leading-relaxed text-muted-foreground">
                Automatically approve all tool executions without prompting. Default: off.
              </p>
            </div>
            <Switch
              id="auto-approve-tool-calls"
              checked={form.auto_approve}
              onCheckedChange={(checked) => setForm({ ...form, auto_approve: checked })}
              aria-label="Toggle auto-approve tool calls"
            />
          </div>
        </CardContent>
      </Card>

      <Card className="border-border bg-card shadow-sm">
        <CardHeader>
          <CardTitle className="text-base">Directories</CardTitle>
          <CardDescription>
            Set optional filesystem locations used by chat tools and flow recording.
          </CardDescription>
        </CardHeader>
        <CardContent className="space-y-4">
          <div className="space-y-2">
            <Label htmlFor="workspace-directory">Workspace Directory</Label>
            <Input
              id="workspace-directory"
              type="text"
              value={form.workspace_dir}
              onChange={(e) => setForm({ ...form, workspace_dir: e.target.value })}
              placeholder="Current working directory (default)"
              className="bg-background font-mono"
            />
            <p className="text-xs text-muted-foreground">
              Working directory for tool execution. Default: the directory where Astonish was started.
            </p>
          </div>
          <div className="space-y-2">
            <Label htmlFor="flow-save-directory">Flow Save Directory</Label>
            <Input
              id="flow-save-directory"
              type="text"
              value={form.flow_save_dir}
              onChange={(e) => setForm({ ...form, flow_save_dir: e.target.value })}
              placeholder="~/.config/astonish/flows/ (default)"
              className="bg-background font-mono"
            />
            <p className="text-xs text-muted-foreground">
              Directory where recorded flows are saved. Default: ~/.config/astonish/flows/.
            </p>
          </div>
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
