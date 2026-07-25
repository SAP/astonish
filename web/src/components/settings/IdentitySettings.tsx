import { useState, useEffect } from 'react'
import { AlertCircle, Check, Loader2, Save } from 'lucide-react'

import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Textarea } from '@/components/ui/textarea'

import { saveFullConfigSection } from './settingsApi'

const NOT_SET_VALUE = '__not_set__'

const locales = [
  ['en-US', 'English (US)'],
  ['en-GB', 'English (UK)'],
  ['es-ES', 'Spanish (Spain)'],
  ['fr-FR', 'French (France)'],
  ['de-DE', 'German (Germany)'],
  ['it-IT', 'Italian (Italy)'],
  ['pt-BR', 'Portuguese (Brazil)'],
  ['ja-JP', 'Japanese'],
  ['ko-KR', 'Korean'],
  ['zh-CN', 'Chinese (Simplified)'],
  ['zh-TW', 'Chinese (Traditional)'],
  ['ru-RU', 'Russian'],
  ['ar-SA', 'Arabic'],
  ['hi-IN', 'Hindi'],
  ['nl-NL', 'Dutch'],
  ['sv-SE', 'Swedish'],
  ['pl-PL', 'Polish'],
  ['tr-TR', 'Turkish'],
]

export default function IdentitySettings({ config, onSaved }: { config: Record<string, any>; onSaved?: () => void }) {
  const [form, setForm] = useState({
    name: '',
    username: '',
    email: '',
    bio: '',
    website: '',
    locale: '',
    timezone: ''
  })
  const [saving, setSaving] = useState(false)
  const [saveSuccess, setSaveSuccess] = useState(false)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    if (config) {
      setForm({
        name: config.name || '',
        username: config.username || '',
        email: config.email || '',
        bio: config.bio || '',
        website: config.website || '',
        locale: config.locale || '',
        timezone: config.timezone || ''
      })
    }
  }, [config])

  const handleSave = async () => {
    setSaving(true)
    setSaveSuccess(false)
    setError(null)
    try {
      await saveFullConfigSection('agent_identity', form)
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
          <CardTitle className="text-base">Agent Identity</CardTitle>
          <CardDescription>
            Configure the agent persona used for web portal registrations and profile information.
          </CardDescription>
        </CardHeader>
        <CardContent className="space-y-4">
          <div className="grid gap-4 sm:grid-cols-2">
            <div className="space-y-2">
              <Label htmlFor="identity-display-name">Display Name</Label>
              <Input
                id="identity-display-name"
                type="text"
                value={form.name}
                onChange={(e) => setForm({ ...form, name: e.target.value })}
                placeholder="Agent Name"
                className="bg-background"
              />
            </div>
            <div className="space-y-2">
              <Label htmlFor="identity-username">Username</Label>
              <Input
                id="identity-username"
                type="text"
                value={form.username}
                onChange={(e) => setForm({ ...form, username: e.target.value })}
                placeholder="agentuser"
                className="bg-background font-mono"
              />
              <p className="text-xs text-muted-foreground">Base username for registrations.</p>
            </div>
          </div>

          <div className="space-y-2">
            <Label htmlFor="identity-email">Email</Label>
            <Input
              id="identity-email"
              type="email"
              value={form.email}
              onChange={(e) => setForm({ ...form, email: e.target.value })}
              placeholder="agent@example.com"
              className="bg-background"
            />
            <p className="text-xs text-muted-foreground">Should match the email channel config if using email integration.</p>
          </div>

          <div className="space-y-2">
            <Label htmlFor="identity-bio">Bio</Label>
            <Textarea
              id="identity-bio"
              value={form.bio}
              onChange={(e) => setForm({ ...form, bio: e.target.value })}
              placeholder="A short description for profile fields..."
              rows={3}
              className="bg-background"
            />
          </div>

          <div className="space-y-2">
            <Label htmlFor="identity-website">Website</Label>
            <Input
              id="identity-website"
              type="url"
              value={form.website}
              onChange={(e) => setForm({ ...form, website: e.target.value })}
              placeholder="https://example.com"
              className="bg-background font-mono"
            />
          </div>

          <div className="grid gap-4 sm:grid-cols-2">
            <div className="space-y-2">
              <Label htmlFor="identity-locale">Locale</Label>
              <Select
                value={form.locale || NOT_SET_VALUE}
                onValueChange={(value) => setForm({ ...form, locale: value === NOT_SET_VALUE ? '' : value })}
              >
                <SelectTrigger id="identity-locale" className="bg-background">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value={NOT_SET_VALUE}>Not set</SelectItem>
                  {locales.map(([value, label]) => (
                    <SelectItem key={value} value={value}>{label}</SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
            <div className="space-y-2">
              <Label htmlFor="identity-timezone">Timezone</Label>
              <Input
                id="identity-timezone"
                type="text"
                value={form.timezone}
                onChange={(e) => setForm({ ...form, timezone: e.target.value })}
                placeholder="America/New_York"
                className="bg-background font-mono"
              />
              <p className="text-xs text-muted-foreground">IANA timezone identifier.</p>
            </div>
          </div>
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
