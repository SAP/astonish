import { useState, useEffect } from 'react'
import { AlertCircle, Check, ChevronDown, ChevronRight, Loader2, Save } from 'lucide-react'

import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Switch } from '@/components/ui/switch'

import { saveFullConfigSection } from './settingsApi'

function CollapsibleSection({ title, description, enabled, onToggle, expanded, onExpand, children }: { title: string; description: string; enabled: boolean; onToggle: (v: boolean) => void; expanded: boolean; onExpand: () => void; children: React.ReactNode }) {
  return (
    <Card className="border-border bg-card shadow-sm">
      <CardHeader
        className="cursor-pointer gap-3 sm:flex sm:flex-row sm:items-start sm:justify-between"
        onClick={onExpand}
      >
        <div className="flex items-start gap-3">
          {expanded ? <ChevronDown className="mt-0.5 size-4 text-muted-foreground" /> : <ChevronRight className="mt-0.5 size-4 text-muted-foreground" />}
          <div>
            <CardTitle className="text-base">{title}</CardTitle>
            <CardDescription>{description}</CardDescription>
          </div>
        </div>
        <div onClick={(e) => e.stopPropagation()}>
          <Switch checked={enabled} onCheckedChange={onToggle} aria-label={`Enable ${title}`} />
        </div>
      </CardHeader>
      {expanded && (
        <CardContent className="border-t pt-4">
          {children}
        </CardContent>
      )}
    </Card>
  )
}

export default function ChannelsSettings({ config, onSaved }: { config: Record<string, any>; onSaved?: () => void }) {
  const [form, setForm] = useState({
    enabled: false,
    telegram: {
      enabled: false,
      bot_token: '',
      allow_from: [] as string[]
    },
    email: {
      enabled: false,
      provider: 'imap',
      imap_server: '',
      smtp_server: '',
      address: '',
      username: '',
      password: '',
      poll_interval: 30,
      allow_from: [] as string[],
      folder: 'INBOX',
      mark_read: true,
      max_body_chars: 50000
    }
  })
  const [tgExpanded, setTgExpanded] = useState(false)
  const [emailExpanded, setEmailExpanded] = useState(false)
  const [tgAllowFromText, setTgAllowFromText] = useState('')
  const [emailAllowFromText, setEmailAllowFromText] = useState('')
  const [saving, setSaving] = useState(false)
  const [saveSuccess, setSaveSuccess] = useState(false)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    if (config) {
      const tg = config.telegram || {}
      const em = config.email || {}
      setForm({
        enabled: config.enabled || false,
        telegram: {
          enabled: tg.enabled || false,
          bot_token: tg.bot_token || '',
          allow_from: tg.allow_from || []
        },
        email: {
          enabled: em.enabled || false,
          provider: em.provider || 'imap',
          imap_server: em.imap_server || '',
          smtp_server: em.smtp_server || '',
          address: em.address || '',
          username: em.username || '',
          password: em.password || '',
          poll_interval: em.poll_interval || 30,
          allow_from: em.allow_from || [],
          folder: em.folder || 'INBOX',
          mark_read: em.mark_read !== false,
          max_body_chars: em.max_body_chars || 50000
        }
      })
      setTgAllowFromText((tg.allow_from || []).join(', '))
      setEmailAllowFromText((em.allow_from || []).join(', '))
      if (tg.enabled) setTgExpanded(true)
      if (em.enabled) setEmailExpanded(true)
    }
  }, [config])

  const handleSave = async () => {
    setSaving(true)
    setSaveSuccess(false)
    setError(null)
    try {
      const saveData = {
        ...form,
        telegram: {
          ...form.telegram,
          allow_from: tgAllowFromText.split(',').map(s => s.trim()).filter(Boolean)
        },
        email: {
          ...form.email,
          allow_from: emailAllowFromText.split(',').map(s => s.trim()).filter(Boolean)
        }
      }
      await saveFullConfigSection('channels', saveData)
      setSaveSuccess(true)
      if (onSaved) onSaved()
      setTimeout(() => setSaveSuccess(false), 2000)
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setSaving(false)
    }
  }

  const updateTelegram = (updates: Record<string, any>) => {
    setForm(f => ({ ...f, telegram: { ...f.telegram, ...updates } }))
  }

  const updateEmail = (updates: Record<string, any>) => {
    setForm(f => ({ ...f, email: { ...f.email, ...updates } }))
  }

  return (
    <div className="max-w-2xl space-y-6">
      <Card className="border-border bg-card shadow-sm">
        <CardHeader>
          <CardTitle className="text-base">Channels</CardTitle>
          <CardDescription>Master switch for all communication channels, including Telegram and Email.</CardDescription>
        </CardHeader>
        <CardContent>
          <div className="flex items-start justify-between gap-4 rounded-xl border border-border bg-background p-4">
            <div>
              <Label htmlFor="channels-enabled">Enable Channels</Label>
              <p className="mt-1 text-xs text-muted-foreground">Disable this to pause all external channel listeners.</p>
            </div>
            <Switch
              id="channels-enabled"
              checked={form.enabled}
              onCheckedChange={(checked) => setForm({ ...form, enabled: checked })}
              aria-label="Enable channels"
            />
          </div>
        </CardContent>
      </Card>

      <CollapsibleSection
        title="Telegram"
        description="Receive and respond to messages via Telegram bot."
        enabled={form.telegram.enabled}
        onToggle={(v) => updateTelegram({ enabled: v })}
        expanded={tgExpanded}
        onExpand={() => setTgExpanded(!tgExpanded)}
      >
        <div className="space-y-4">
          <div className="space-y-2">
            <Label htmlFor="telegram-bot-token">Bot Token</Label>
            <Input
              id="telegram-bot-token"
              type="password"
              value={form.telegram.bot_token}
              onChange={(e) => updateTelegram({ bot_token: e.target.value })}
              placeholder="Paste your BotFather token"
              className="bg-background font-mono"
            />
            <p className="text-xs text-muted-foreground">
              Get a token from <a href="https://t.me/BotFather" target="_blank" rel="noreferrer" className="text-primary underline">@BotFather</a> on Telegram.
            </p>
          </div>
          <div className="space-y-2">
            <Label htmlFor="telegram-allowed-users">Allowed User IDs</Label>
            <Input
              id="telegram-allowed-users"
              type="text"
              value={tgAllowFromText}
              onChange={(e) => setTgAllowFromText(e.target.value)}
              placeholder="123456789, 987654321"
              className="bg-background font-mono"
            />
            <p className="text-xs text-muted-foreground">Comma-separated Telegram user IDs allowed to interact with the bot. Required for security.</p>
          </div>
        </div>
      </CollapsibleSection>

      <CollapsibleSection
        title="Email"
        description="Monitor an inbox and respond to emails."
        enabled={form.email.enabled}
        onToggle={(v) => updateEmail({ enabled: v })}
        expanded={emailExpanded}
        onExpand={() => setEmailExpanded(!emailExpanded)}
      >
        <div className="space-y-4">
          <div className="grid gap-4 sm:grid-cols-2">
            <div className="space-y-2">
              <Label htmlFor="email-imap-server">IMAP Server</Label>
              <Input id="email-imap-server" type="text" value={form.email.imap_server} onChange={(e) => updateEmail({ imap_server: e.target.value })} placeholder="imap.gmail.com:993" className="bg-background font-mono" />
            </div>
            <div className="space-y-2">
              <Label htmlFor="email-smtp-server">SMTP Server</Label>
              <Input id="email-smtp-server" type="text" value={form.email.smtp_server} onChange={(e) => updateEmail({ smtp_server: e.target.value })} placeholder="smtp.gmail.com:587" className="bg-background font-mono" />
            </div>
          </div>

          <div className="space-y-2">
            <Label htmlFor="channel-email-address">Email Address</Label>
            <Input id="channel-email-address" type="email" value={form.email.address} onChange={(e) => updateEmail({ address: e.target.value })} placeholder="agent@example.com" className="bg-background" />
          </div>

          <div className="grid gap-4 sm:grid-cols-2">
            <div className="space-y-2">
              <Label htmlFor="channel-email-username">Username</Label>
              <Input id="channel-email-username" type="text" value={form.email.username} onChange={(e) => updateEmail({ username: e.target.value })} placeholder="Same as email (default)" className="bg-background" />
            </div>
            <div className="space-y-2">
              <Label htmlFor="channel-email-password">Password</Label>
              <Input id="channel-email-password" type="password" value={form.email.password} onChange={(e) => updateEmail({ password: e.target.value })} placeholder="App password" className="bg-background" />
            </div>
          </div>

          <div className="space-y-2">
            <Label htmlFor="email-allowed-senders">Allowed Senders</Label>
            <Input id="email-allowed-senders" type="text" value={emailAllowFromText} onChange={(e) => setEmailAllowFromText(e.target.value)} placeholder="user@example.com, * for anyone" className="bg-background" />
            <p className="text-xs text-muted-foreground">Comma-separated email addresses. Use * to allow anyone.</p>
          </div>

          <div className="space-y-4 border-t pt-4">
            <div className="grid gap-4 sm:grid-cols-3">
              <div className="space-y-2">
                <Label htmlFor="email-poll-interval">Poll Interval (sec)</Label>
                <Input id="email-poll-interval" type="number" value={form.email.poll_interval} onChange={(e) => updateEmail({ poll_interval: parseInt(e.target.value) || 30 })} min="5" className="bg-background" />
              </div>
              <div className="space-y-2">
                <Label htmlFor="email-folder">Folder</Label>
                <Input id="email-folder" type="text" value={form.email.folder} onChange={(e) => updateEmail({ folder: e.target.value })} placeholder="INBOX" className="bg-background" />
              </div>
              <div className="space-y-2">
                <Label htmlFor="email-max-body">Max Body (chars)</Label>
                <Input id="email-max-body" type="number" value={form.email.max_body_chars} onChange={(e) => updateEmail({ max_body_chars: parseInt(e.target.value) || 50000 })} min="1000" className="bg-background" />
              </div>
            </div>
            <div className="flex items-start justify-between gap-4 rounded-xl border border-border bg-background p-4">
              <div>
                <Label htmlFor="email-mark-read">Mark as Read</Label>
                <p className="mt-1 text-xs text-muted-foreground">Mark processed emails as read.</p>
              </div>
              <Switch id="email-mark-read" checked={form.email.mark_read} onCheckedChange={(checked) => updateEmail({ mark_read: checked })} aria-label="Mark as read" />
            </div>
          </div>
        </div>
      </CollapsibleSection>

      <div className="flex flex-wrap items-center gap-3">
        <Button onClick={handleSave} disabled={saving}>
          {saving ? <Loader2 className="animate-spin" /> : <Save />}
          {saving ? 'Saving...' : 'Save Changes'}
        </Button>
        {saveSuccess && (
          <span className="flex items-center gap-1 text-sm text-[color:var(--success)]"><Check className="size-4" /> Saved</span>
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
