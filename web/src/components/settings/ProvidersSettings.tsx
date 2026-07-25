import { useState } from 'react'
import { Key, ChevronRight, Save, Plus, Trash2, X, AlertCircle, Loader2, Search, Settings2, Zap } from 'lucide-react'

import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'

import { saveSettings, replaceAllProviders, savePlatformProviders, saveOrgProviders, deleteProviderAtLevel, fetchProviderModels, testProviderConnection } from './settingsApi'
import type { SettingsData, ProviderFieldDef, ProviderTestResult } from './settingsApi'
import ProviderModelSelector from '../ProviderModelSelector'

export interface InheritedProvider {
  name: string
  type: string
  level: string
}

export interface InheritedDefaults {
  provider: string
  model: string
  source: string // 'Platform' or 'Org'
}

interface ProvidersSettingsProps {
  settings: SettingsData | null
  providerForms: Record<string, Record<string, string>>
  setProviderForms: (forms: Record<string, Record<string, string>>) => void
  generalForm: {
    default_provider: string
    default_model: string
  }
  setGeneralForm: (form: any) => void
  saving: boolean
  setSaving: (saving: boolean) => void
  setSaveSuccess: (success: boolean) => void
  error: string | null
  setError: (error: string | null) => void
  loadData: () => void
  onSettingsSaved?: () => void
  /** When set, routes save/delete to level-specific APIs instead of the generic settings endpoint */
  level?: 'platform' | 'org' | 'team'
  /** Providers inherited from higher levels (platform/org) — shown in the default provider dropdown */
  inheritedProviders?: InheritedProvider[]
  /** Callback to save default provider + model at the appropriate level */
  onSaveDefault?: (provider: string, model: string) => Promise<void>
  /** Inherited defaults from higher levels — shown as informational when not explicitly set */
  inheritedDefaults?: InheritedDefaults
}

export default function ProvidersSettings({
  settings,
  providerForms,
  setProviderForms,
  generalForm,
  setGeneralForm,
  saving,
  setSaving,
  setSaveSuccess,
  error,
  setError,
  loadData,
  onSettingsSaved,
  level,
  inheritedProviders = [],
  onSaveDefault,
  inheritedDefaults
}: ProvidersSettingsProps) {
  const [expandedProvider, setExpandedProvider] = useState<string | null>(null)
  const [showAddProvider, setShowAddProvider] = useState(false)
  const [newProviderName, setNewProviderName] = useState('')
  const [newProviderType, setNewProviderType] = useState('openai')
  const [deletingProvider, setDeletingProvider] = useState<string | null>(null)

  // Test Connection state
  const [testingProvider, setTestingProvider] = useState<string | null>(null)
  const [testResult, setTestResult] = useState<Record<string, ProviderTestResult | null>>({})

  // Default Configuration section state
  const [showModelSelector, setShowModelSelector] = useState(false)
  const [availableModels, setAvailableModels] = useState<string[]>([])
  const [loadingModels, setLoadingModels] = useState(false)
  const [savingDefault, setSavingDefault] = useState(false)
  const [defaultError, setDefaultError] = useState<string | null>(null)

  // Build the effective list of all providers for the default dropdown
  // (own providers at this level + inherited from higher levels)
  const allEffectiveProviders: { name: string; type: string; level?: string }[] = [
    ...inheritedProviders.map(p => ({ name: p.name, type: p.type, level: p.level })),
    ...(settings?.providers || []).map(p => ({ name: p.name, type: p.type, level: level }))
  ]

  // Resolve the type of the currently selected default provider
  const selectedDefaultType = allEffectiveProviders.find(p => p.name === generalForm.default_provider)?.type || ''

  // Provider types that support the enhanced model browser
  const enhancedModelTypes = ['openrouter', 'anthropic', 'gemini', 'groq', 'litellm', 'openai', 'poe', 'sap_ai_core', 'xai', 'lm_studio', 'ollama', 'openai_compat']

  const handleDefaultProviderChange = (providerName: string) => {
    setGeneralForm({ ...generalForm, default_provider: providerName, default_model: '' })
    setAvailableModels([])
    setDefaultError(null)
  }

  const loadModelsForDefaultProvider = async (providerId: string) => {
    if (!providerId) {
      setAvailableModels([])
      return
    }
    setLoadingModels(true)
    setDefaultError(null)
    try {
      const data = await fetchProviderModels(providerId)
      setAvailableModels(data.models || [])
    } catch (err: any) {
      setDefaultError(err.message)
      setAvailableModels([])
    } finally {
      setLoadingModels(false)
    }
  }

  const handleSaveDefault = async () => {
    if (!onSaveDefault) return
    setSavingDefault(true)
    setDefaultError(null)
    try {
      await onSaveDefault(generalForm.default_provider, generalForm.default_model)
      setSavingDefault(false)
    } catch (err: any) {
      setDefaultError(err.message)
      setSavingDefault(false)
    }
  }

  const providerTypeOptions = [
    { value: 'anthropic', label: 'Anthropic' },
    { value: 'gemini', label: 'Google GenAI' },
    { value: 'groq', label: 'Groq' },
    { value: 'litellm', label: 'LiteLLM' },
    { value: 'lm_studio', label: 'LM Studio' },
    { value: 'ollama', label: 'Ollama' },
    { value: 'openai', label: 'OpenAI' },
    { value: 'openai_compat', label: 'OpenAI Compatible' },
    { value: 'openrouter', label: 'OpenRouter' },
    { value: 'poe', label: 'Poe' },
    { value: 'sap_ai_core', label: 'SAP AI Core' },
    { value: 'xai', label: 'xAI' },
  ]

  const getProviderFields = (type: string): ProviderFieldDef[] => {
    const fieldMappings: Record<string, ProviderFieldDef[]> = {
      anthropic: [{ key: 'api_key', label: 'API Key', type: 'password' }],
      gemini: [{ key: 'api_key', label: 'API Key', type: 'password' }],
      openai: [{ key: 'api_key', label: 'API Key', type: 'password' }],
      groq: [{ key: 'api_key', label: 'API Key', type: 'password' }],
      litellm: [
        { key: 'api_key', label: 'API Key', type: 'password' },
        { key: 'base_url', label: 'Base URL', type: 'text' }
      ],
      lm_studio: [{ key: 'base_url', label: 'Base URL', type: 'text' }],
      ollama: [{ key: 'base_url', label: 'Base URL', type: 'text' }],
      openai_compat: [
        { key: 'api_key', label: 'API Key', type: 'password' },
        { key: 'base_url', label: 'Base URL', type: 'text' }
      ],
      openrouter: [{ key: 'api_key', label: 'API Key', type: 'password' }],
      poe: [{ key: 'api_key', label: 'API Key', type: 'password' }],
      sap_ai_core: [
        { key: 'client_id', label: 'Client ID', type: 'text' },
        { key: 'client_secret', label: 'Client Secret', type: 'password' },
        { key: 'auth_url', label: 'Auth URL', type: 'text' },
        { key: 'base_url', label: 'Base URL', type: 'text' },
        { key: 'resource_group', label: 'Resource Group', type: 'text' }
      ],
      xai: [{ key: 'api_key', label: 'API Key', type: 'password' }],
    }
    return fieldMappings[type] || []
  }

  const handleSaveProvider = async (providerName: string) => {
    setSaving(true)
    try {
      if (level === 'platform' || level === 'org') {
        // Level-specific save: build a providers map with this provider's fields
        const fields = providerForms[providerName] || {}
        const existingProvider = settings?.providers?.find(p => p.name === providerName)
        const provType = existingProvider?.type || providerName
        const providerConfig: Record<string, string> = { type: provType }
        for (const [key, value] of Object.entries(fields)) {
          if (value && key !== 'type') providerConfig[key] = value
        }
        // Build full providers map from current settings
        const allProviders: Record<string, Record<string, string>> = {}
        for (const p of (settings?.providers || [])) {
          if (p.name === providerName) {
            allProviders[p.name] = providerConfig
          } else {
            const cfg: Record<string, string> = { type: p.type }
            if (p.fields) for (const [k, v] of Object.entries(p.fields)) cfg[k] = v
            allProviders[p.name] = cfg
          }
        }
        // If it's a new provider not in the list yet, add it
        if (!allProviders[providerName]) {
          allProviders[providerName] = providerConfig
        }
        const saveFn = level === 'platform' ? savePlatformProviders : saveOrgProviders
        await saveFn({ providers: allProviders })
      } else {
        // Default: use the existing replace-all-providers approach (team/personal mode)
        const currentProviders = (settings?.providers || []).map(p => {
          const provider: Record<string, unknown> = { name: p.name, type: p.type }
          if (p.fields) {
            for (const [key, val] of Object.entries(p.fields)) {
              provider[key] = val
            }
          }
          return provider
        })

        const existingProvider = settings?.providers?.find(p => p.name === providerName)
        const existingType = existingProvider?.type || providerName
        const newProviderConfig: Record<string, unknown> = { name: providerName, type: existingType }
        for (const [key, value] of Object.entries(providerForms[providerName] || {})) {
          if (value && key !== 'type') {
            newProviderConfig[key] = value
          }
        }

        let updatedProviders: Record<string, unknown>[]
        const existingIndex = currentProviders.findIndex(p => p.name === providerName)
        if (existingIndex >= 0) {
          updatedProviders = [...currentProviders]
          updatedProviders[existingIndex] = newProviderConfig
        } else {
          updatedProviders = [...currentProviders, newProviderConfig]
        }

        await replaceAllProviders(updatedProviders)
      }

      setExpandedProvider(providerName)
      setSaving(false)
      setSaveSuccess(true)
      setTimeout(() => setSaveSuccess(false), 2000)
      loadData()
      if (onSettingsSaved) onSettingsSaved()
    } catch (err: any) {
      setSaving(false)
      setError(err.message)
    }
  }

  const handleAddProvider = async () => {
    if (!newProviderName.trim()) {
      setError('Provider instance name is required')
      return
    }
    if (settings?.providers?.some(p => p.name === newProviderName.trim())) {
      setError('A provider with this name already exists')
      return
    }

    setSaving(true)
    try {
      if (level === 'platform' || level === 'org') {
        // Level-specific add: rebuild full providers map including new one
        const allProviders: Record<string, Record<string, string>> = {}
        for (const p of (settings?.providers || [])) {
          const cfg: Record<string, string> = { type: p.type }
          if (p.fields) for (const [k, v] of Object.entries(p.fields)) cfg[k] = v
          allProviders[p.name] = cfg
        }
        allProviders[newProviderName.trim()] = { type: newProviderType }
        const saveFn = level === 'platform' ? savePlatformProviders : saveOrgProviders
        await saveFn({ providers: allProviders })
      } else {
        await saveSettings({
          providers: {
            [newProviderName.trim()]: { type: newProviderType }
          }
        })
      }
      setShowAddProvider(false)
      setNewProviderName('')
      setNewProviderType('openai')
      loadData()
      if (onSettingsSaved) onSettingsSaved()
    } catch (err: any) {
      setError(err.message)
    } finally {
      setSaving(false)
    }
  }

  const handleDeleteProvider = async (providerName: string) => {
    setDeletingProvider(providerName)
    try {
      if (level === 'platform' || level === 'org' || level === 'team') {
        await deleteProviderAtLevel(level, providerName)
      } else {
        const currentProviders = (settings?.providers || []).map(p => {
          const provider: Record<string, unknown> = { name: p.name, type: p.type }
          if (p.fields) {
            for (const [key, val] of Object.entries(p.fields)) {
              provider[key] = val
            }
          }
          return provider
        })
        const updatedProviders = currentProviders.filter(p => p.name !== providerName)
        await replaceAllProviders(updatedProviders)
      }

      if (generalForm.default_provider === providerName) {
        setGeneralForm({ ...generalForm, default_provider: '', default_model: '' })
      }

      loadData()
      if (onSettingsSaved) onSettingsSaved()
    } catch (err: any) {
      setError(err.message)
    } finally {
      setDeletingProvider(null)
    }
  }

  const handleTestConnection = async (providerName: string, providerType: string) => {
    setTestingProvider(providerName)
    setTestResult(prev => ({ ...prev, [providerName]: null }))
    try {
      // Build params from form fields (only non-empty, non-masked values)
      const formFields = providerForms[providerName] || {}
      const existingProvider = settings?.providers?.find(p => p.name === providerName)
      const params: Record<string, string> = {}
      for (const [key, value] of Object.entries(formFields)) {
        if (value && key !== 'type' && !value.startsWith('****')) {
          params[key] = value
        }
      }
      // Include existing saved fields that weren't overridden (non-masked)
      if (existingProvider?.fields) {
        for (const [key, value] of Object.entries(existingProvider.fields)) {
          if (!(key in params) && value && !value.startsWith('****')) {
            params[key] = value
          }
        }
      }
      const result = await testProviderConnection(providerType, params)
      setTestResult(prev => ({ ...prev, [providerName]: result }))
    } catch (err: any) {
      setTestResult(prev => ({ ...prev, [providerName]: { success: false, error: err.message } }))
    } finally {
      setTestingProvider(null)
    }
  }

  return (
    <>
      <div className="space-y-6">
        {/* Default Configuration Section */}
        {onSaveDefault && (
          <div className="rounded-lg border p-4" style={{ borderColor: 'var(--border-color)', background: 'var(--bg-secondary)' }}>
            <div className="flex items-center gap-2 mb-4">
              <Settings2 size={16} style={{ color: 'var(--brand)' }} />
              <h3 className="text-sm font-semibold" style={{ color: 'var(--text-primary)' }}>
                Default Configuration
              </h3>
            </div>

            <div className="space-y-4">
              {/* Default Provider Dropdown */}
              <div>
                <label className="block text-xs font-medium mb-1.5" style={{ color: 'var(--text-secondary)' }}>
                  Default Provider
                </label>
                {allEffectiveProviders.length > 0 ? (
                  <div className="flex items-center gap-2">
                    <select
                      value={generalForm.default_provider}
                      onChange={(e) => handleDefaultProviderChange(e.target.value)}
                      className="flex-1 px-3 py-2 rounded-lg border text-sm"
                      style={{ background: 'var(--bg-primary)', borderColor: 'var(--border-color)', color: 'var(--text-primary)' }}
                    >
                      {level === 'platform' ? (
                        <option value="">Select a provider...</option>
                      ) : (
                        <option value="">Not Set</option>
                      )}
                      {allEffectiveProviders.map(p => (
                        <option key={p.name} value={p.name}>
                          {p.name} ({p.type}){p.level && p.level !== level ? ` — ${p.level}` : ''}
                        </option>
                      ))}
                    </select>
                    {/* Clear button for org/team when explicitly set */}
                    {level !== 'platform' && generalForm.default_provider && (
                      <button
                        onClick={() => setGeneralForm({ ...generalForm, default_provider: '', default_model: '' })}
                        className="p-1.5 rounded-md border transition-colors hover:border-red-400"
                        style={{ borderColor: 'var(--border-color)', color: 'var(--text-muted)' }}
                        title="Clear override (revert to inherited)"
                      >
                        <X size={14} />
                      </button>
                    )}
                  </div>
                ) : (
                  <p className="text-xs" style={{ color: 'var(--text-muted)' }}>
                    Configure a provider below to set defaults
                  </p>
                )}
              </div>

              {/* Default Model Selector */}
              <div>
                <label className="block text-xs font-medium mb-1.5" style={{ color: 'var(--text-secondary)' }}>
                  Default Model
                </label>
                {generalForm.default_provider ? (
                  <>
                    {enhancedModelTypes.includes(selectedDefaultType) ? (
                      <div className="flex items-center gap-2">
                        <button
                          onClick={() => setShowModelSelector(true)}
                          className="flex-1 px-3 py-2 rounded-lg border text-sm text-left flex items-center justify-between"
                          style={{
                            background: 'var(--bg-primary)',
                            borderColor: 'var(--border-color)',
                            color: generalForm.default_model ? 'var(--text-primary)' : 'var(--text-muted)'
                          }}
                        >
                          <span className="truncate">
                            {generalForm.default_model || (level === 'platform' ? 'Click to select a model...' : 'Not Set')}
                          </span>
                          <Search size={14} style={{ color: 'var(--text-muted)' }} />
                        </button>
                        {/* Clear button for model when explicitly set at org/team */}
                        {level !== 'platform' && generalForm.default_model && (
                          <button
                            onClick={() => setGeneralForm({ ...generalForm, default_model: '' })}
                            className="p-1.5 rounded-md border transition-colors hover:border-red-400"
                            style={{ borderColor: 'var(--border-color)', color: 'var(--text-muted)' }}
                            title="Clear override (revert to inherited)"
                          >
                            <X size={14} />
                          </button>
                        )}
                      </div>
                    ) : (
                      <div className="flex items-center gap-2">
                        <div className="relative flex-1">
                          <select
                            value={generalForm.default_model}
                            onChange={(e) => setGeneralForm({ ...generalForm, default_model: e.target.value })}
                            onFocus={() => {
                              if (generalForm.default_provider && availableModels.length === 0 && !loadingModels) {
                                loadModelsForDefaultProvider(generalForm.default_provider)
                              }
                            }}
                            className="w-full px-3 py-2 rounded-lg border text-sm"
                            style={{ background: 'var(--bg-primary)', borderColor: 'var(--border-color)', color: 'var(--text-primary)' }}
                          >
                            {availableModels.length === 0 && !loadingModels && (
                              <option value={generalForm.default_model || ''}>
                                {generalForm.default_model || (level === 'platform' ? 'Click to load models...' : 'Not Set')}
                              </option>
                            )}
                            {loadingModels && <option value="">Loading models...</option>}
                            {availableModels.length > 0 && (
                              <>
                                <option value="">{level === 'platform' ? 'Select a model...' : 'Not Set'}</option>
                                {availableModels.map(model => (
                                  <option key={model} value={model}>{model}</option>
                                ))}
                              </>
                            )}
                          </select>
                          {loadingModels && (
                            <div className="absolute right-8 top-1/2 -translate-y-1/2">
                              <Loader2 size={14} className="animate-spin" style={{ color: 'var(--accent)' }} />
                            </div>
                          )}
                        </div>
                        {/* Clear button for model when explicitly set at org/team */}
                        {level !== 'platform' && generalForm.default_model && (
                          <button
                            onClick={() => setGeneralForm({ ...generalForm, default_model: '' })}
                            className="p-1.5 rounded-md border transition-colors hover:border-red-400"
                            style={{ borderColor: 'var(--border-color)', color: 'var(--text-muted)' }}
                            title="Clear override (revert to inherited)"
                          >
                            <X size={14} />
                          </button>
                        )}
                      </div>
                    )}
                  </>
                ) : (
                  <div className="px-3 py-2 rounded-lg border text-sm" style={{ background: 'var(--bg-primary)', borderColor: 'var(--border-color)', color: 'var(--text-muted)' }}>
                    {level === 'platform' ? 'Select a provider first' : 'Not Set'}
                  </div>
                )}
              </div>

              {/* Inheritance info (org/team only — shown when at least one field is not explicitly set) */}
              {level !== 'platform' && inheritedDefaults && (inheritedDefaults.provider || inheritedDefaults.model) && (!generalForm.default_provider || !generalForm.default_model) && (
                <div className="flex items-start gap-2 p-2.5 rounded-lg" style={{ background: 'var(--bg-primary)', border: '1px dashed var(--border-color)' }}>
                  <Settings2 size={12} className="mt-0.5 flex-shrink-0" style={{ color: 'var(--text-muted)' }} />
                  <div className="text-xs" style={{ color: 'var(--text-muted)' }}>
                    <span className="font-medium">Inheriting from {inheritedDefaults.source}:</span>{' '}
                    {inheritedDefaults.provider && <span>{inheritedDefaults.provider}</span>}
                    {inheritedDefaults.provider && inheritedDefaults.model && <span> / </span>}
                    {inheritedDefaults.model && <span className="font-mono">{inheritedDefaults.model}</span>}
                  </div>
                </div>
              )}

              {/* Save Default Button */}
              <div className="flex items-center gap-3">
                <button
                  onClick={handleSaveDefault}
                  disabled={savingDefault}
                  className="flex items-center gap-2 px-3 py-1.5 rounded-lg text-white text-sm font-medium transition-all shadow-sm hover:shadow-md hover:scale-[1.02] active:scale-95 disabled:opacity-50"
                  style={{ background: 'var(--brand)' }}
                >
                  <Save size={14} />
                  {savingDefault ? 'Saving...' : 'Save Default'}
                </button>
              </div>

              {/* Error */}
              {defaultError && (
                <div className="flex items-center gap-2 p-2 rounded text-xs" style={{ background: 'rgba(239, 68, 68, 0.1)', color: '#f87171' }}>
                  <AlertCircle size={12} />
                  {defaultError}
                </div>
              )}
            </div>
          </div>
        )}

        {/* Add Provider Button */}
        <div className="flex items-center justify-between">
          <button
            onClick={() => setShowAddProvider(true)}
            className="flex items-center gap-2 px-4 py-2 rounded-lg font-medium transition-all shadow-md hover:shadow-lg hover:scale-[1.02] active:scale-95"
            style={{ background: 'var(--brand)', color: '#fff' }}
          >
            <Plus size={16} />
            Add Provider Instance
          </button>
          <span className="text-sm" style={{ color: 'var(--text-muted)' }}>
            {settings?.providers?.length || 0} provider(s) configured
          </span>
        </div>

        {/* Provider Instances */}
        {settings?.providers?.map(provider => {
          const isExpanded = expandedProvider === provider.name
          const isDeleting = deletingProvider === provider.name
          const providerFields = getProviderFields(provider.type)

          return (
            <div
              key={provider.name}
              className={`rounded-lg border transition-all ${
                isExpanded ? 'border-purple-500 ring-1 ring-purple-500/30' : 
                'hover:border-purple-500/50'
              }`}
              style={{ background: 'var(--bg-secondary)', borderColor: isExpanded ? undefined : 'var(--border-color)' }}
            >
              {/* Card Header */}
              <div 
                className="p-4 cursor-pointer"
                onClick={() => setExpandedProvider(isExpanded ? null : provider.name)}
              >
                <div className="flex items-start justify-between gap-3">
                  <div className="flex items-center gap-3">
                    <div 
                      className="w-10 h-10 rounded-lg flex items-center justify-center"
                      style={{ 
                        background: 'linear-gradient(135deg, var(--brand-muted) 0%, rgba(124, 58, 237, 0.2) 100%)',
                        border: '1px solid rgba(168, 85, 247, 0.3)'
                      }}
                    >
                      <Key size={18} style={{ color: 'var(--brand)' }} />
                    </div>
                    <div>
                      <div className="flex items-center gap-2">
                        <span className="text-lg font-medium" style={{ color: 'var(--text-primary)' }}>
                          {provider.name}
                        </span>
                        {provider.configured ? (
                          <span className="px-2 py-0.5 text-xs rounded" 
                            style={{ background: 'rgba(20,150,71,0.12)', color: '#149647' }}>
                            Configured
                          </span>
                        ) : (
                          <span className="px-2 py-0.5 text-xs rounded" 
                            style={{ background: 'rgba(107,114,128,0.15)', color: '#6b7280' }}>
                            Not configured
                          </span>
                        )}
                      </div>
                      <div className="text-sm" style={{ color: 'var(--text-muted)' }}>
                        {provider.display_name} {provider.name !== provider.display_name && `(${provider.name})`}
                      </div>
                    </div>
                  </div>
                  <div className="flex items-center gap-2">
                    {generalForm.default_provider === provider.name && (
                      <span className="px-2 py-0.5 text-xs rounded"
                        style={{ background: 'var(--brand-muted)', color: 'var(--brand)' }}>
                        Default
                      </span>
                    )}
                    <button
                      onClick={(e) => {
                        e.stopPropagation()
                        handleDeleteProvider(provider.name)
                      }}
                      disabled={isDeleting}
                      className="p-2 rounded-lg text-red-400 hover:bg-red-500/20 transition-colors disabled:opacity-50"
                      title="Delete provider"
                    >
                      {isDeleting ? <Loader2 size={16} className="animate-spin" /> : <Trash2 size={16} />}
                    </button>
                    <ChevronRight 
                      size={20} 
                      className={`transition-transform ${isExpanded ? 'rotate-90' : ''}`}
                      style={{ color: 'var(--text-muted)' }}
                    />
                  </div>
                </div>
              </div>

              {/* Expanded Form */}
              {isExpanded && (
                <div className="px-4 pb-4 pt-0 border-t" style={{ borderColor: 'var(--border-color)' }}>
                  <div className="pt-4 space-y-4">
                    {/* Instance Info */}
                    <div className="grid grid-cols-2 gap-4 p-3 rounded-lg" 
                      style={{ background: 'var(--bg-primary)' }}>
                      <div>
                        <div className="text-xs" style={{ color: 'var(--text-muted)' }}>Instance Name</div>
                        <div className="font-mono text-sm" style={{ color: 'var(--text-primary)' }}>{provider.name}</div>
                      </div>
                      <div>
                        <div className="text-xs" style={{ color: 'var(--text-muted)' }}>Provider Type</div>
                        <div className="text-sm" style={{ color: 'var(--text-primary)' }}>{provider.display_name}</div>
                      </div>
                    </div>

                    {/* Configuration Fields */}
                    <div className="space-y-3">
                      {providerFields.length === 0 ? (
                        <p className="text-sm" style={{ color: 'var(--text-muted)' }}>
                          No configuration fields for this provider type.
                        </p>
                      ) : (
                        providerFields.map(field => (
                          <div key={field.key}>
                            <label className="block text-sm mb-1 capitalize" style={{ color: 'var(--text-secondary)' }}>
                              {field.label}
                            </label>
                            <input
                              type={field.type || 'text'}
                              value={providerForms[provider.name]?.[field.key] || ''}
                              onChange={(e) => setProviderForms({
                                ...providerForms,
                                [provider.name]: { ...providerForms[provider.name], [field.key]: e.target.value }
                              })}
                              placeholder={`Enter ${field.label.toLowerCase()}...`}
                              className="w-full px-3 py-2 rounded border text-sm font-mono"
                              style={{ background: 'var(--bg-primary)', borderColor: 'var(--border-color)', color: 'var(--text-primary)' }}
                            />
                          </div>
                        ))
                      )}
                    </div>

                    {/* Save & Test Buttons */}
                    <div className="flex items-center gap-3">
                      <button
                        onClick={() => handleSaveProvider(provider.name)}
                        disabled={saving}
                        className="flex items-center gap-2 px-4 py-2 rounded-lg text-white font-medium transition-all shadow-md hover:shadow-lg hover:scale-[1.02] active:scale-95 disabled:opacity-50"
                        style={{ background: 'var(--brand)' }}
                      >
                        <Save size={16} />
                        {saving ? 'Saving...' : 'Save'}
                      </button>
                      <button
                        onClick={() => handleTestConnection(provider.name, provider.type)}
                        disabled={testingProvider === provider.name}
                        className="flex items-center gap-2 px-4 py-2 rounded-lg font-medium transition-all border hover:scale-[1.02] active:scale-95 disabled:opacity-50"
                        style={{ borderColor: 'var(--border-color)', color: 'var(--text-secondary)', background: 'var(--bg-primary)' }}
                      >
                        {testingProvider === provider.name ? (
                          <Loader2 size={16} className="animate-spin" />
                        ) : (
                          <Zap size={16} />
                        )}
                        {testingProvider === provider.name ? 'Testing...' : 'Test Connection'}
                      </button>
                    </div>

                    {/* Test Result */}
                    {testResult[provider.name] && (
                      <div className={`flex items-start gap-2 p-3 rounded-lg text-sm ${
                        testResult[provider.name]!.success ? '' : ''
                      }`} style={{
                        background: testResult[provider.name]!.success
                          ? 'rgba(20, 150, 71, 0.1)'
                          : 'rgba(239, 68, 68, 0.1)',
                        color: testResult[provider.name]!.success ? '#149647' : '#f87171'
                      }}>
                        {testResult[provider.name]!.success ? (
                          <Zap size={16} className="flex-shrink-0 mt-0.5" />
                        ) : (
                          <AlertCircle size={16} className="flex-shrink-0 mt-0.5" />
                        )}
                        <div>
                          {testResult[provider.name]!.success ? (
                            <span>Connection successful — {testResult[provider.name]!.model_count} model(s) available</span>
                          ) : (
                            <span>{testResult[provider.name]!.error}</span>
                          )}
                        </div>
                      </div>
                    )}
                  </div>
                </div>
              )}
            </div>
          )
        })}

        {(!settings?.providers || settings.providers.length === 0) && (
          <div className="rounded-lg border border-dashed py-12 text-center text-muted-foreground">
            <Key className="mx-auto mb-3 size-12 opacity-30" />
            <p className="mb-2 text-lg font-medium text-foreground">No providers configured</p>
            <p className="mb-4 text-sm">Add your first provider instance to get started</p>
            <Button onClick={() => setShowAddProvider(true)} className="mx-auto">
              <Plus />
              Add Provider
            </Button>
          </div>
        )}
      </div>

      <Dialog open={showAddProvider} onOpenChange={(open) => {
        setShowAddProvider(open)
        if (!open) {
          setNewProviderName('')
          setNewProviderType('openai')
          setError(null)
        }
      }}>
        <DialogContent className="max-w-md border-border bg-card shadow-[var(--shadow-elevated)]">
          <DialogHeader>
            <DialogTitle>Add Provider Instance</DialogTitle>
            <DialogDescription>
              Create a named provider configuration for this scope.
            </DialogDescription>
          </DialogHeader>

          <div className="space-y-4">
            <div className="space-y-2">
              <Label htmlFor="provider-instance-name">Instance Name</Label>
              <Input
                id="provider-instance-name"
                type="text"
                value={newProviderName}
                onChange={(e) => setNewProviderName(e.target.value)}
                placeholder="e.g., openai-prod, anthropic-dev"
                className="bg-background font-mono"
                autoFocus
              />
              <p className="text-xs text-muted-foreground">
                Unique identifier for this provider instance.
              </p>
            </div>

            <div className="space-y-2">
              <Label htmlFor="provider-type">Provider Type</Label>
              <Select value={newProviderType} onValueChange={setNewProviderType}>
                <SelectTrigger id="provider-type" className="w-full bg-background">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {providerTypeOptions.map(opt => (
                    <SelectItem key={opt.value} value={opt.value}>
                      {opt.label}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>

            <div className="rounded-lg border bg-background p-3">
              <div className="mb-2 text-xs font-medium text-muted-foreground">
                This provider type requires:
              </div>
              <div className="space-y-1">
                {getProviderFields(newProviderType).map(field => (
                  <div key={field.key} className="flex items-center gap-2 text-sm text-secondary-foreground">
                    <Key className="size-4 text-primary" />
                    {field.label}
                  </div>
                ))}
                {getProviderFields(newProviderType).length === 0 && (
                  <div className="text-sm text-muted-foreground">
                    No required fields — just the type.
                  </div>
                )}
              </div>
            </div>

            {error && (
              <Alert variant="destructive">
                <AlertCircle />
                <AlertDescription>{error}</AlertDescription>
              </Alert>
            )}
          </div>

          <DialogFooter>
            <Button
              type="button"
              variant="secondary"
              onClick={() => {
                setShowAddProvider(false)
                setNewProviderName('')
                setNewProviderType('openai')
                setError(null)
              }}
            >
              Cancel
            </Button>
            <Button onClick={handleAddProvider} disabled={saving || !newProviderName.trim()}>
              {saving ? <Loader2 className="animate-spin" /> : <Plus />}
              {saving ? 'Creating...' : 'Add Provider'}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* Model Selector Modal for Default Configuration */}
      <ProviderModelSelector
        isOpen={showModelSelector}
        onClose={() => setShowModelSelector(false)}
        onSelect={(modelId) => {
          setGeneralForm({ ...generalForm, default_model: modelId })
          setShowModelSelector(false)
        }}
        currentModel={generalForm.default_model}
        provider={generalForm.default_provider}
      />
    </>
  )
}
