import { useState, useEffect } from 'react'
import { AlertCircle, Check, Loader2, Save, Search, Settings2, X } from 'lucide-react'

import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Label } from '@/components/ui/label'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'

import { fetchUserDefaultModel, patchUserDefaultModel, type UserDefaultModel } from '../../api/userSettings'
import { fetchSettings, fetchProviderModels, type SettingsData, type ProviderInfo } from './settingsApi'
import ProviderModelSelector from '../ProviderModelSelector'

const NOT_SET_VALUE = '__not_set__'

export default function UserDefaultModelSettings() {
  const [form, setForm] = useState<UserDefaultModel>({ defaultProvider: '', defaultModel: '' })
  const [settings, setSettings] = useState<SettingsData | null>(null)
  const [loading, setLoading] = useState(true)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [saveSuccess, setSaveSuccess] = useState(false)

  const [showModelSelector, setShowModelSelector] = useState(false)
  const [availableModels, setAvailableModels] = useState<string[]>([])
  const [loadingModels, setLoadingModels] = useState(false)

  const enhancedModelTypes = ['openrouter', 'anthropic', 'gemini', 'groq', 'litellm', 'openai', 'poe', 'sap_ai_core', 'xai', 'lm_studio', 'ollama', 'openai_compat']

  useEffect(() => {
    const loadData = async () => {
      try {
        const [userDefault, settingsData] = await Promise.all([
          fetchUserDefaultModel(),
          fetchSettings(),
        ])
        setForm(userDefault)
        setSettings(settingsData)
      } catch (err: any) {
        setError(err.message)
      } finally {
        setLoading(false)
      }
    }
    void loadData()
  }, [])

  const allEffectiveProviders: ProviderInfo[] = settings?.providers || []
  const selectedDefaultType = allEffectiveProviders.find(p => p.name === form.defaultProvider)?.type || ''
  const inheritedDefaults = {
    provider: settings?.general.default_provider || '',
    model: settings?.general.default_model || '',
    source: 'Team'
  }

  const handleDefaultProviderChange = (providerName: string) => {
    setForm({ ...form, defaultProvider: providerName === NOT_SET_VALUE ? '' : providerName, defaultModel: '' })
    setAvailableModels([])
    setError(null)
  }

  const loadModelsForDefaultProvider = async (providerId: string) => {
    if (!providerId) {
      setAvailableModels([])
      return
    }
    setLoadingModels(true)
    setError(null)
    try {
      const data = await fetchProviderModels(providerId)
      setAvailableModels(data.models || [])
    } catch (err: any) {
      setError(err.message)
      setAvailableModels([])
    } finally {
      setLoadingModels(false)
    }
  }

  const handleSave = async () => {
    setSaving(true)
    setError(null)
    try {
      await patchUserDefaultModel(form.defaultProvider, form.defaultModel)
      setSaveSuccess(true)
      setTimeout(() => setSaveSuccess(false), 2000)
    } catch (err: any) {
      setError(err.message)
    } finally {
      setSaving(false)
    }
  }

  const handleClear = async () => {
    setForm({ defaultProvider: '', defaultModel: '' })
    setSaving(true)
    setError(null)
    try {
      await patchUserDefaultModel('', '')
      setSaveSuccess(true)
      setTimeout(() => setSaveSuccess(false), 2000)
    } catch (err: any) {
      setError(err.message)
    } finally {
      setSaving(false)
    }
  }

  if (loading) {
    return (
      <div className="flex items-center justify-center rounded-xl border bg-card p-8 text-primary">
        <Loader2 className="size-5 animate-spin" />
      </div>
    )
  }

  return (
    <>
      <Card className="max-w-2xl border-border bg-card shadow-sm">
        <CardHeader>
          <div className="flex items-center gap-2">
            <Settings2 className="size-4 text-primary" />
            <CardTitle className="text-base">Default Model</CardTitle>
          </div>
          <CardDescription>
            Set a personal provider/model override, or inherit the default configured by your team.
          </CardDescription>
        </CardHeader>
        <CardContent className="space-y-4">
          <div className="space-y-2">
            <Label htmlFor="user-default-provider">Default Provider</Label>
            {allEffectiveProviders.length > 0 ? (
              <Select
                value={form.defaultProvider || NOT_SET_VALUE}
                onValueChange={handleDefaultProviderChange}
              >
                <SelectTrigger id="user-default-provider" className="w-full bg-background">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value={NOT_SET_VALUE}>Not Set</SelectItem>
                  {allEffectiveProviders.map(provider => (
                    <SelectItem key={provider.name} value={provider.name}>
                      {provider.name} ({provider.type})
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            ) : (
              <p className="text-xs text-muted-foreground">
                No providers configured at the team level.
              </p>
            )}
          </div>

          <div className="space-y-2">
            <Label htmlFor="user-default-model">Default Model</Label>
            {form.defaultProvider ? (
              enhancedModelTypes.includes(selectedDefaultType) ? (
                <Button
                  id="user-default-model"
                  type="button"
                  variant="outline"
                  className="w-full justify-between bg-background font-normal"
                  onClick={() => setShowModelSelector(true)}
                >
                  <span className="truncate text-left">
                    {form.defaultModel || 'Click to select a model...'}
                  </span>
                  <Search className="text-muted-foreground" />
                </Button>
              ) : (
                <div className="relative">
                  <Select
                    value={form.defaultModel || NOT_SET_VALUE}
                    onOpenChange={(open) => {
                      if (open && form.defaultProvider && availableModels.length === 0 && !loadingModels) {
                        void loadModelsForDefaultProvider(form.defaultProvider)
                      }
                    }}
                    onValueChange={(value) => setForm({ ...form, defaultModel: value === NOT_SET_VALUE ? '' : value })}
                  >
                    <SelectTrigger id="user-default-model" className="w-full bg-background">
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      {availableModels.length === 0 && !loadingModels && (
                        <SelectItem value={form.defaultModel || NOT_SET_VALUE}>
                          {form.defaultModel || 'Click to load models...'}
                        </SelectItem>
                      )}
                      {loadingModels && <SelectItem value="__loading__" disabled>Loading models...</SelectItem>}
                      {availableModels.length > 0 && (
                        <>
                          <SelectItem value={NOT_SET_VALUE}>Select a model...</SelectItem>
                          {availableModels.map(model => (
                            <SelectItem key={model} value={model}>{model}</SelectItem>
                          ))}
                        </>
                      )}
                    </SelectContent>
                  </Select>
                  {loadingModels && (
                    <Loader2 className="absolute right-9 top-1/2 size-4 -translate-y-1/2 animate-spin text-primary" />
                  )}
                </div>
              )
            ) : (
              <div className="rounded-md border bg-background px-3 py-2 text-sm text-muted-foreground">
                Not Set
              </div>
            )}
          </div>

          {!form.defaultProvider && (
            <div className="flex items-start gap-2 rounded-lg border border-dashed bg-background p-3">
              <Settings2 className="mt-0.5 size-3 shrink-0 text-muted-foreground" />
              <div className="text-xs text-muted-foreground">
                {inheritedDefaults.provider ? (
                  <>
                    <span className="font-medium">Inheriting from {inheritedDefaults.source}:</span>{' '}
                    <span>{inheritedDefaults.provider}</span>
                    {inheritedDefaults.model && <span> / </span>}
                    {inheritedDefaults.model && <span className="font-mono">{inheritedDefaults.model}</span>}
                  </>
                ) : (
                  'No default configured at any level'
                )}
              </div>
            </div>
          )}

          <div className="flex flex-wrap items-center gap-3">
            <Button onClick={handleSave} disabled={saving} size="sm">
              {saving ? <Loader2 className="animate-spin" /> : <Save />}
              {saving ? 'Saving...' : 'Save Default'}
            </Button>
            <Button onClick={handleClear} disabled={saving} variant="outline" size="sm">
              <X />
              Clear
            </Button>
          </div>

          {saveSuccess && (
            <span className="flex items-center gap-1 text-sm text-[color:var(--success)]">
              <Check className="size-4" /> Default model saved.
            </span>
          )}

          {error && (
            <Alert variant="destructive">
              <AlertCircle />
              <AlertDescription>{error}</AlertDescription>
            </Alert>
          )}
        </CardContent>
      </Card>
      <ProviderModelSelector
        isOpen={showModelSelector}
        onClose={() => setShowModelSelector(false)}
        onSelect={(modelId) => {
          setForm({ ...form, defaultModel: modelId })
          setShowModelSelector(false)
        }}
        currentModel={form.defaultModel}
        provider={form.defaultProvider}
      />
    </>
  )
}
