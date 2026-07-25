import { useState, useEffect, useRef, useCallback } from 'react'
import { Sparkles, ChevronRight, ChevronLeft, Check, Loader2, Key, Zap, AlertCircle, Plus, Folder, Search, Globe, Monitor, Shield, ShieldAlert, ExternalLink, Server, Database } from 'lucide-react'
import { fetchStandardServers, installStandardServer, StandardServer, McpInstallResult } from '../api/agents'
import { fetchSandboxStatus, fetchOptionalTools, initSandbox, SandboxStatus, OptionalTool } from '../api/sandbox'
import { initializePlatform, initializeSQLitePlatform } from '../api/platform'

// --- Local types ---

interface ProviderField {
  key: string
  label: string
  placeholder: string
}

interface ProviderDef {
  id: string
  name: string
  description: string
  icon: string
  fields: ProviderField[]
  defaultModel: string
}

interface BrowserEngine {
  id: string
  name: string
  description: string
  recommended?: boolean
}

interface SettingsProvider {
  name: string
  type: string
  fields?: Record<string, string>
  configured?: boolean
  [key: string]: unknown
}

interface Settings {
  providers: SettingsProvider[]
  [key: string]: unknown
}

interface BrowserConfig {
  chrome_path?: string
  remote_cdp_url?: string
  fingerprint_seed?: string
  fingerprint_platform?: string
}

interface InstallResult extends McpInstallResult {
  webSearchTool?: string
  webExtractTool?: string
}

interface StandardServerExt extends StandardServer {
  displayName?: string
  isDefault?: boolean
  capabilities?: { webSearch?: boolean; webExtract?: boolean }
  envVars?: { name: string; description?: string }[]
}

const PROVIDERS: ProviderDef[] = [
  { id: 'gemini', name: 'Google Gemini', description: "Google's most capable AI models", icon: '✨', fields: [{ key: 'api_key', label: 'API Key', placeholder: 'Enter your Gemini API key' }], defaultModel: 'gemini-2.0-flash' },
  { id: 'openai', name: 'OpenAI', description: 'GPT-4o and other powerful models', icon: '🤖', fields: [{ key: 'api_key', label: 'API Key', placeholder: 'sk-...' }], defaultModel: 'gpt-4o' },
  { id: 'anthropic', name: 'Anthropic Claude', description: 'Claude models with strong reasoning', icon: '🧠', fields: [{ key: 'api_key', label: 'API Key', placeholder: 'sk-ant-...' }], defaultModel: 'claude-sonnet-4-20250514' },
  { id: 'ollama', name: 'Ollama (Local)', description: 'Run models locally on your machine', icon: '🦙', fields: [{ key: 'base_url', label: 'Base URL', placeholder: 'http://localhost:11434' }], defaultModel: 'llama3.2' },
  { id: 'groq', name: 'Groq', description: 'Ultra-fast inference', icon: '⚡', fields: [{ key: 'api_key', label: 'API Key', placeholder: 'gsk_...' }], defaultModel: 'llama-3.3-70b-versatile' },
  { id: 'openrouter', name: 'OpenRouter', description: 'Access multiple providers via one API', icon: '🔀', fields: [{ key: 'api_key', label: 'API Key', placeholder: 'sk-or-...' }], defaultModel: 'google/gemini-2.0-flash-001' },
  { id: 'poe', name: 'Poe', description: 'Access multiple AI models via Poe', icon: '💬', fields: [{ key: 'api_key', label: 'API Key', placeholder: 'Enter your Poe API key' }], defaultModel: 'gpt-4o' },
  { id: 'xai', name: 'xAI Grok', description: 'Grok models from xAI', icon: '🚀', fields: [{ key: 'api_key', label: 'API Key', placeholder: 'xai-...' }], defaultModel: 'grok-2-latest' },
  { id: 'lm_studio', name: 'LM Studio', description: 'Local models via LM Studio', icon: '💻', fields: [{ key: 'base_url', label: 'Base URL', placeholder: 'http://localhost:1234/v1' }], defaultModel: 'local-model' },
  { id: 'litellm', name: 'LiteLLM', description: 'Unified interface for 100+ LLM providers', icon: '🌐', fields: [{ key: 'api_key', label: 'API Key', placeholder: 'sk-...' }, { key: 'base_url', label: 'Base URL', placeholder: 'http://localhost:4000/v1' }], defaultModel: 'gpt-4' },
  { id: 'openai_compat', name: 'OpenAI Compatible', description: 'Connect to any OpenAI-compatible API endpoint', icon: '🔄', fields: [{ key: 'api_key', label: 'API Key', placeholder: 'sk-...' }, { key: 'base_url', label: 'Base URL', placeholder: 'https://api.example.com/v1' }], defaultModel: 'gpt-4o' },
  { id: 'sap_ai_core', name: 'SAP AI Core', description: 'Enterprise AI from SAP Business AI', icon: '🏢', fields: [{ key: 'auth_url', label: 'Auth URL', placeholder: 'https://your-tenant.authentication.sap.hana.ondemand.com' }, { key: 'client_id', label: 'Client ID', placeholder: 'sb-xxx' }, { key: 'client_secret', label: 'Client Secret', placeholder: 'Your client secret' }, { key: 'base_url', label: 'Base URL', placeholder: 'https://api.ai.prod.region.aws.ml.hana.ondemand.com/v2' }, { key: 'resource_group', label: 'Resource Group', placeholder: 'default' }], defaultModel: 'gpt-4o' }
]

const BROWSER_ENGINES: BrowserEngine[] = [
  { id: 'default', name: 'Default Chromium', description: 'Auto-downloaded by Astonish. No setup needed.', recommended: true },
  { id: 'cloakbrowser', name: 'CloakBrowser', description: 'Anti-detect Chromium with C++ stealth patches. Install via CLI.' },
  { id: 'custom', name: 'Custom Chrome', description: 'Use your own Chrome/Chromium binary.' },
  { id: 'remote', name: 'Remote Browser', description: 'Connect to Chrome running on another machine via CDP.' },
]

const TOTAL_STEPS = 10

const fetchSettings = async (): Promise<Settings> => {
  const res = await fetch('/api/settings/config')
  if (!res.ok) throw new Error('Failed to fetch settings')
  return res.json()
}

const saveProviderConfig = async (instanceName: string, config: Record<string, unknown>) => {
  const res = await fetch('/api/settings/config', { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ providers: { [instanceName]: config } }) })
  if (!res.ok) throw new Error('Failed to save provider config')
  return res.json()
}

const replaceAllProviders = async (providers: Record<string, unknown>[]) => {
  const res = await fetch('/api/settings/config', {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ providers: { '__replace_all__': { '__array__': JSON.stringify(providers) } } })
  })
  if (!res.ok) throw new Error('Failed to replace providers')
  return res.json()
}

const saveGeneralSettings = async (provider: string | null, model: string) => {
  const res = await fetch('/api/settings/config', { method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ general: { default_provider: provider, default_model: model } }) })
  if (!res.ok) throw new Error('Failed to save general settings')
  return res.json()
}

const fetchProviderModels = async (providerId: string): Promise<{ models: string[] }> => {
  const res = await fetch(`/api/providers/${providerId}/models`)
  if (!res.ok) throw new Error('Failed to fetch models')
  return res.json()
}

const isSensitiveField = (key: string) => {
  const sensitiveKeys = ['api_key', 'client_secret', 'token', 'password', 'secret', 'key']
  return sensitiveKeys.some(k => key.toLowerCase().includes(k))
}

const maskValue = (value: string) => {
  if (!value) return ''
  if (value.length <= 4) return '••••'
  return value.slice(0, 2) + '••••' + value.slice(-2)
}

function StepIndicator({ step, totalSteps }: { step: number; totalSteps: number }) {
  return (
    <div className="flex items-center justify-center gap-2 mb-8">
      {Array.from({ length: totalSteps }, (_, i) => (
        <div key={i} className={`w-2 h-2 rounded-full transition-all ${i === step ? 'w-8 bg-primary' : i < step ? 'bg-primary' : 'bg-gray-600'}`} />
      ))}
    </div>
  )
}

export default function SetupWizard({ onComplete }: { onComplete: () => void }) {
  const [step, setStep] = useState(0)
  const [settings, setSettings] = useState<Settings | null>(null)
  const [selectedInstance, setSelectedInstance] = useState<string | null>(null)
  const [_isNewProvider, setIsNewProvider] = useState(false)
  const [selectedProvider, setSelectedProvider] = useState<string | null>(null)
  const [instanceName, setInstanceName] = useState('')
  const [credentials, setCredentials] = useState<Record<string, string>>({})
  const [selectedModel, setSelectedModel] = useState('')
  const [availableModels, setAvailableModels] = useState<string[]>([])
  const [isLoading, setIsLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [testSuccess, setTestSuccess] = useState(false)

  // Step 1: Deployment Mode state
  const [deploymentMode, setDeploymentMode] = useState<'platform' | 'sqlite' | null>(null)
  const [pgHost, setPgHost] = useState('localhost')
  const [pgPort, setPgPort] = useState('5432')
  const [pgUser, setPgUser] = useState('postgres')
  const [pgPassword, setPgPassword] = useState('')
  const [pgSSLMode, setPgSSLMode] = useState('prefer')
  const [orgName, setOrgName] = useState('My Organization')
  const [orgSlug, setOrgSlug] = useState('my-org')
  const [platformInitializing, setPlatformInitializing] = useState(false)
  const [platformInitDone, setPlatformInitDone] = useState(false)
  const [platformError, setPlatformError] = useState<string | null>(null)
  const [restartRequired, setRestartRequired] = useState(false)
  // SQLite-specific state
  const [sqliteAdminEmail, setSqliteAdminEmail] = useState('')
  const [sqliteAdminName, setSqliteAdminName] = useState('')
  const [sqliteAdminPassword, setSqliteAdminPassword] = useState('')

  // Step 6: Web Search state
  const [standardServers, setStandardServers] = useState<StandardServerExt[]>([])
  const [selectedWebServer, setSelectedWebServer] = useState<string | null>(null)
  const [webApiKey, setWebApiKey] = useState('')
  const [webInstalled, setWebInstalled] = useState(false)
  const [webInstalledName, setWebInstalledName] = useState('')
  const [webSkipped, setWebSkipped] = useState(false)

  // Step 6: Browser Engine state
  const [browserEngine, setBrowserEngine] = useState('default')
  const [browserCustomPath, setBrowserCustomPath] = useState('')
  const [browserRemoteHost, setBrowserRemoteHost] = useState('')
  const [browserRemotePort, setBrowserRemotePort] = useState('9222')
  const [browserSaved, setBrowserSaved] = useState(false)

  // Step 7: Sandbox state
  const [sandboxStatus, setSandboxStatus] = useState<SandboxStatus | null>(null)
  const [optionalTools, setOptionalTools] = useState<OptionalTool[]>([])
  const [selectedTools, setSelectedTools] = useState<Record<string, boolean>>({})
  const [sandboxInitializing, setSandboxInitializing] = useState(false)
  const [sandboxProgress, setSandboxProgress] = useState<string[]>([])
  const [sandboxDone, setSandboxDone] = useState(false)
  const [sandboxSkipped, setSandboxSkipped] = useState(false)
  const [sandboxSkipConfirm, setSandboxSkipConfirm] = useState(false)
  const sandboxAbortRef = useRef<(() => void) | null>(null)
  const progressEndRef = useRef<HTMLDivElement | null>(null)

  const loadSettings = useCallback(async () => {
    try {
      const data = await fetchSettings()
      setSettings(data)
    } catch (err: any) { console.error('Failed to load settings:', err) }
  }, [])

  useEffect(() => { loadSettings() }, [loadSettings])

  // Auto-scroll sandbox progress log
  useEffect(() => {
    if (progressEndRef.current) {
      progressEndRef.current.scrollIntoView({ behavior: 'smooth' })
    }
  }, [sandboxProgress])

  const getProviderInfo = (providerId: string | null): ProviderDef => PROVIDERS.find(p => p.id === providerId) || { name: providerId || 'unknown', icon: '⚙️', fields: [], id: providerId || '', description: '', defaultModel: '' }

  const handleTestConnection = async () => {
    if (!selectedProvider) return
    setIsLoading(true)
    setError(null)
    setTestSuccess(false)
    try {
      const provider = getProviderInfo(selectedProvider)
      let configToSave: Record<string, unknown> = { ...credentials, type: selectedProvider }
      if (provider.id === 'sap_ai_core' && !configToSave.resource_group) configToSave.resource_group = 'default'
      const saveName = '__new_provider__'
      await saveProviderConfig(saveName, configToSave)
      const data = await fetchProviderModels(saveName)
      if (data.models && data.models.length > 0) {
        setAvailableModels(data.models)
        setSelectedModel(provider.defaultModel || data.models[0])
        setTestSuccess(true)
      } else { throw new Error('No models returned - check your credentials') }
    } catch (err: any) { setError(err.message || 'Connection failed. Please check your credentials.') }
    finally { setIsLoading(false) }
  }

  const handleComplete = async () => {
    setIsLoading(true)
    setError(null)
    try { await saveGeneralSettings(selectedInstance, selectedModel); onComplete() }
    catch (err: any) { setError(err.message || 'Failed to save settings') }
    finally { setIsLoading(false) }
  }

  const _handleAddNewProvider = () => { setIsNewProvider(true); setSelectedInstance(null); setSelectedProvider(null); setInstanceName(''); setCredentials({}); setTestSuccess(false); setStep(2) }

  const handleSelectExisting = (instName: string) => {
    setIsNewProvider(false)
    setSelectedInstance(instName)
    const providerData = settings!.providers.find(p => p.name === instName)
    const providerType = providerData?.type || instName
    setSelectedProvider(providerType)
    setInstanceName(instName)
    const existingCredentials: Record<string, string> = {}
    if (providerData?.fields) {
      for (const [key, value] of Object.entries(providerData.fields)) {
        existingCredentials[key] = isSensitiveField(key) ? maskValue(value) : value
      }
    }
    setCredentials(existingCredentials)
    setTestSuccess(!!providerData?.configured)
    setStep(3)
  }

  const handleProviderTypeSelect = (providerId: string) => { setSelectedProvider(providerId); setCredentials({}); setTestSuccess(false) }

  const saveProviderInstance = async () => {
    const provider = getProviderInfo(selectedProvider)
    let configToSave: Record<string, unknown> = { ...credentials, type: selectedProvider }
    if (provider.id === 'sap_ai_core' && !configToSave.resource_group) configToSave.resource_group = 'default'

    const currentProviders = (settings?.providers || []).map(p => {
      const prov: Record<string, unknown> = { name: p.name, type: p.type }
      if (p.fields) {
        for (const [key, val] of Object.entries(p.fields)) {
          prov[key] = val
        }
      }
      return prov
    })

    const updatedProviders = currentProviders.filter(p => p.name !== '__new_provider__' && p.name !== instanceName)
    updatedProviders.push({ name: instanceName, ...configToSave })

    await replaceAllProviders(updatedProviders)
    setSelectedInstance(instanceName)
  }

  const handleSaveProvider = async () => {
    setIsLoading(true)
    setError(null)
    try {
      await saveProviderInstance()
      setStep(4)
    } catch (err: any) { setError(err.message || 'Failed to save provider') }
    finally { setIsLoading(false) }
  }

  // Step 6: Load standard servers on entering web search step
  const loadStandardServers = async () => {
    setIsLoading(true)
    try {
      const data = await fetchStandardServers()
      setStandardServers((data.servers || []) as StandardServerExt[])
      // Check if any is already installed
      const installed = ((data.servers || []) as StandardServerExt[]).find(s => s.installed)
      if (installed) {
        setWebInstalled(true)
        setWebInstalledName(installed.displayName || installed.name)
      }
    } catch (err: any) {
      console.error('Failed to load standard servers:', err)
    } finally {
      setIsLoading(false)
    }
  }

  const handleInstallWebServer = async () => {
    if (!selectedWebServer) return
    setIsLoading(true)
    setError(null)
    try {
      const srv = standardServers.find(s => s.id === selectedWebServer)
      const envMap: Record<string, string> = {}
      if (srv?.envVars?.length && srv.envVars.length > 0 && webApiKey) {
        envMap[srv.envVars[0].name] = webApiKey
      }
      const result = await installStandardServer(selectedWebServer, envMap) as InstallResult
      setWebInstalled(true)
      setWebInstalledName(srv?.displayName || srv?.name || '')
      // Auto-set as default web tools
      if (result.webSearchTool) {
        await fetch('/api/settings/config', {
          method: 'PUT',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ general: { web_search_tool: result.webSearchTool, web_extract_tool: result.webExtractTool || '' } })
        })
      }
    } catch (err: any) {
      setError(err.message || 'Failed to install web search provider')
    } finally {
      setIsLoading(false)
    }
  }

  // Step 6: Save browser config
  const handleSaveBrowser = async () => {
    setIsLoading(true)
    setError(null)
    try {
      const browserConfig: BrowserConfig = {}
      if (browserEngine === 'default') {
        browserConfig.chrome_path = ''
        browserConfig.remote_cdp_url = ''
        browserConfig.fingerprint_seed = ''
        browserConfig.fingerprint_platform = ''
      } else if (browserEngine === 'custom') {
        browserConfig.chrome_path = browserCustomPath
        browserConfig.remote_cdp_url = ''
        browserConfig.fingerprint_seed = ''
        browserConfig.fingerprint_platform = ''
      } else if (browserEngine === 'remote') {
        const port = browserRemotePort || '9222'
        browserConfig.remote_cdp_url = `ws://${browserRemoteHost}:${port}`
        browserConfig.chrome_path = ''
        browserConfig.fingerprint_seed = ''
        browserConfig.fingerprint_platform = ''
      }
      // CloakBrowser is configured via CLI only — no changes needed

      if (browserEngine !== 'cloakbrowser') {
        const res = await fetch('/api/settings/full', {
          method: 'PUT',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ browser: browserConfig })
        })
        if (!res.ok) throw new Error('Failed to save browser config')
      }
      setBrowserSaved(true)
    } catch (err: any) {
      setError(err.message || 'Failed to save browser settings')
    } finally {
      setIsLoading(false)
    }
  }

  // Step 7: Load sandbox status on entering sandbox step
  const loadSandboxStatus = async () => {
    setIsLoading(true)
    try {
      const status = await fetchSandboxStatus()
      setSandboxStatus(status)
      if (((status as any).runtimeAvailable ?? (status as any).incusAvailable) && !(status as any).baseTemplateExists) {
        const toolsData = await fetchOptionalTools()
        setOptionalTools(toolsData)
        // Pre-select recommended tools
        const defaults: Record<string, boolean> = {}
        for (const t of toolsData) {
          if ((t as any).recommended) defaults[t.id] = true
        }
        setSelectedTools(defaults)
      }
    } catch (err: any) {
      console.error('Failed to load sandbox status:', err)
    } finally {
      setIsLoading(false)
    }
  }

  const handleSandboxInit = () => {
    setSandboxInitializing(true)
    setSandboxProgress([])
    setSandboxDone(false)
    setError(null)

    const { abort } = initSandbox({
      installTools: Object.entries(selectedTools).filter(([, v]) => v).map(([k]) => k),
      onProgress: (msg: string) => {
        setSandboxProgress(prev => [...prev, msg])
      },
      onDone: () => {
        setSandboxDone(true)
        setSandboxInitializing(false)
      },
      onError: (msg: string) => {
        setError(msg)
        setSandboxInitializing(false)
      },
    })
    sandboxAbortRef.current = abort
  }

  const canProceed = () => {
    switch (step) {
      case 0: return true
      case 1: return platformInitDone || restartRequired
      case 2: return selectedProvider !== null
      case 3: return Object.values(credentials).some(v => v)
      case 4: return instanceName.trim() !== ''
      case 5: return selectedModel !== ''
      case 6: return webInstalled || webSkipped
      case 7: return browserSaved || browserEngine === 'default'
      case 8: return sandboxDone || sandboxSkipped || (sandboxStatus as any)?.baseTemplateExists
      default: return true
    }
  }

  const goNext = async () => {
    if (!canProceed()) return
    if (step === 4) {
      try {
        await saveProviderInstance()
      } catch (err: any) {
        setError(err.message || 'Failed to save provider')
        return
      }
    }
    // If restart is required (platform mode configured), the user needs to restart.
    // They can still proceed through provider setup, but show a note.
    const nextStep = step + 1
    setStep(nextStep)
    setError(null)

    // Trigger data loads for upcoming steps
    if (nextStep === 6) loadStandardServers()
    if (nextStep === 8) loadSandboxStatus()
  }
  const goBack = () => { if (step > 0) { setStep(step - 1); setError(null) } }

  const renderStep = () => {
    switch (step) {
      case 0:
        return (
          <div className="text-center">
            <div className="inline-flex items-center justify-center w-20 h-20 rounded-full bg-gradient-to-br from-primary to-[var(--accent3)] mb-6"><Sparkles size={40} className="text-white" /></div>
            <h1 className="text-3xl font-bold mb-4" style={{ color: 'var(--text-primary)' }}>Welcome to Astonish Studio</h1>
            <p className="text-lg mb-6 max-w-md mx-auto" style={{ color: 'var(--text-muted)' }}>Build powerful AI agents visually. Let's get you set up in just a few steps.</p>
            <div className="flex flex-col gap-3 max-w-sm mx-auto text-left p-4 rounded-lg" style={{ background: 'var(--bg-tertiary)' }}>
              <div className="flex items-center gap-3"><div className="w-6 h-6 rounded-full bg-primary/20 flex items-center justify-center text-primary text-sm">1</div><span style={{ color: 'var(--text-secondary)' }}>Choose deployment mode</span></div>
              <div className="flex items-center gap-3"><div className="w-6 h-6 rounded-full bg-primary/20 flex items-center justify-center text-primary text-sm">2</div><span style={{ color: 'var(--text-secondary)' }}>Connect an AI provider</span></div>
              <div className="flex items-center gap-3"><div className="w-6 h-6 rounded-full bg-primary/20 flex items-center justify-center text-primary text-sm">3</div><span style={{ color: 'var(--text-secondary)' }}>Configure web search & tools</span></div>
            </div>
          </div>
        )

      // Step 1: Deployment Mode
      case 1:
        return (
          <div>
            <h2 className="text-2xl font-bold mb-2 text-center" style={{ color: 'var(--text-primary)' }}>Database Backend</h2>
            <p className="text-center mb-6" style={{ color: 'var(--text-muted)' }}>Choose your database backend for Astonish.</p>

            <div className="grid grid-cols-2 gap-4 max-w-2xl mx-auto mb-6">
              <button
                onClick={() => { setDeploymentMode('sqlite'); setPlatformError(null) }}
                className={`p-5 rounded-xl border-2 text-left transition-all hover:scale-[1.02] ${deploymentMode === 'sqlite' ? 'border-primary bg-primary/10' : 'border-transparent hover:border-gray-600'}`}
                style={{ background: deploymentMode === 'sqlite' ? undefined : 'var(--bg-tertiary)' }}
              >
                <div className="flex items-center gap-3 mb-3">
                  <Database size={24} className="text-emerald-400" />
                  <span className="text-base font-semibold" style={{ color: 'var(--text-primary)' }}>SQLite (Recommended)</span>
                </div>
                <p className="text-xs" style={{ color: 'var(--text-muted)' }}>
                  Multi-user with teams and auth. Zero infrastructure needed.
                </p>
              </button>

              <button
                onClick={() => { setDeploymentMode('platform'); setPlatformError(null) }}
                className={`p-5 rounded-xl border-2 text-left transition-all hover:scale-[1.02] ${deploymentMode === 'platform' ? 'border-primary bg-primary/10' : 'border-transparent hover:border-gray-600'}`}
                style={{ background: deploymentMode === 'platform' ? undefined : 'var(--bg-tertiary)' }}
              >
                <div className="flex items-center gap-3 mb-3">
                  <Server size={24} className="text-orange-400" />
                  <span className="text-base font-semibold" style={{ color: 'var(--text-primary)' }}>PostgreSQL</span>
                </div>
                <p className="text-xs" style={{ color: 'var(--text-muted)' }}>
                  Multi-user, scalable. Requires an external PostgreSQL server.
                </p>
              </button>
            </div>

            {/* SQLite Platform Form */}
            {deploymentMode === 'sqlite' && !platformInitDone && !restartRequired && (
              <div className="max-w-xl mx-auto mt-4 p-6 rounded-xl" style={{ background: 'var(--bg-tertiary)', border: '1px solid var(--border-color)' }}>
                <h3 className="text-lg font-semibold mb-4 flex items-center gap-2" style={{ color: 'var(--text-primary)' }}>
                  <Shield size={18} className="text-emerald-400" />
                  Create Admin Account
                </h3>
                <div className="space-y-4 mb-4">
                  <div>
                    <label className="block text-xs font-medium mb-1" style={{ color: 'var(--text-secondary)' }}>Admin Email</label>
                    <input type="email" value={sqliteAdminEmail} onChange={e => setSqliteAdminEmail(e.target.value)} placeholder="admin@example.com" className="w-full px-3 py-2 rounded-lg border text-sm" style={{ background: 'var(--bg-primary)', borderColor: 'var(--border-color)', color: 'var(--text-primary)' }} />
                  </div>
                  <div>
                    <label className="block text-xs font-medium mb-1" style={{ color: 'var(--text-secondary)' }}>Display Name (optional)</label>
                    <input type="text" value={sqliteAdminName} onChange={e => setSqliteAdminName(e.target.value)} placeholder="Admin" className="w-full px-3 py-2 rounded-lg border text-sm" style={{ background: 'var(--bg-primary)', borderColor: 'var(--border-color)', color: 'var(--text-primary)' }} />
                  </div>
                  <div>
                    <label className="block text-xs font-medium mb-1" style={{ color: 'var(--text-secondary)' }}>Password</label>
                    <input type="password" value={sqliteAdminPassword} onChange={e => setSqliteAdminPassword(e.target.value)} placeholder="Minimum 8 characters" className="w-full px-3 py-2 rounded-lg border text-sm" style={{ background: 'var(--bg-primary)', borderColor: 'var(--border-color)', color: 'var(--text-primary)' }} />
                  </div>
                </div>
                <div className="grid grid-cols-2 gap-4 mb-4">
                  <div>
                    <label className="block text-xs font-medium mb-1" style={{ color: 'var(--text-secondary)' }}>Organization Name</label>
                    <input type="text" value={orgName} onChange={e => setOrgName(e.target.value)} className="w-full px-3 py-2 rounded-lg border text-sm" style={{ background: 'var(--bg-primary)', borderColor: 'var(--border-color)', color: 'var(--text-primary)' }} />
                  </div>
                  <div>
                    <label className="block text-xs font-medium mb-1" style={{ color: 'var(--text-secondary)' }}>Organization Slug</label>
                    <input type="text" value={orgSlug} onChange={e => setOrgSlug(e.target.value)} placeholder="lowercase-with-hyphens" className="w-full px-3 py-2 rounded-lg border text-sm" style={{ background: 'var(--bg-primary)', borderColor: 'var(--border-color)', color: 'var(--text-primary)' }} />
                  </div>
                </div>

                {platformError && (
                  <div className="mb-4 p-3 rounded-lg bg-red-500/10 border border-red-500/30 flex items-center gap-2">
                    <AlertCircle size={18} className="text-red-400 shrink-0" />
                    <span className="text-sm text-red-400">{platformError}</span>
                  </div>
                )}

                <button
                  onClick={async () => {
                    setPlatformInitializing(true)
                    setPlatformError(null)
                    try {
                      const result = await initializeSQLitePlatform({
                        org_name: orgName,
                        org_slug: orgSlug,
                        admin_email: sqliteAdminEmail,
                        admin_name: sqliteAdminName,
                        admin_password: sqliteAdminPassword,
                      })
                      if (result.success) {
                        setPlatformInitDone(true)
                        if (result.restart_required) {
                          setRestartRequired(true)
                        }
                      }
                    } catch (err: any) {
                      setPlatformError(err.message || 'Failed to initialize SQLite platform')
                    } finally {
                      setPlatformInitializing(false)
                    }
                  }}
                  disabled={platformInitializing || !sqliteAdminEmail || sqliteAdminPassword.length < 8}
                  className="w-full py-3 rounded-lg font-medium flex items-center justify-center gap-2 transition-all disabled:opacity-50"
                  style={{ background: 'linear-gradient(to right, #059669, #3b82f6)', color: 'white' }}
                >
                  {platformInitializing ? (
                    <><Loader2 size={18} className="animate-spin" />Initializing Platform...</>
                  ) : (
                    <><Database size={18} />Initialize Platform</>
                  )}
                </button>
              </div>
            )}

            {/* PostgreSQL Platform Form */}
            {deploymentMode === 'platform' && !platformInitDone && !restartRequired && (
              <div className="max-w-xl mx-auto mt-4 p-6 rounded-xl" style={{ background: 'var(--bg-tertiary)', border: '1px solid var(--border-color)' }}>
                <h3 className="text-lg font-semibold mb-4 flex items-center gap-2" style={{ color: 'var(--text-primary)' }}>
                  <Server size={18} className="text-primary" />
                  PostgreSQL Connection
                </h3>
                <div className="grid grid-cols-2 gap-4 mb-4">
                  <div>
                    <label className="block text-xs font-medium mb-1" style={{ color: 'var(--text-secondary)' }}>Host</label>
                    <input type="text" value={pgHost} onChange={e => setPgHost(e.target.value)} className="w-full px-3 py-2 rounded-lg border text-sm" style={{ background: 'var(--bg-primary)', borderColor: 'var(--border-color)', color: 'var(--text-primary)' }} />
                  </div>
                  <div>
                    <label className="block text-xs font-medium mb-1" style={{ color: 'var(--text-secondary)' }}>Port</label>
                    <input type="text" value={pgPort} onChange={e => setPgPort(e.target.value)} className="w-full px-3 py-2 rounded-lg border text-sm" style={{ background: 'var(--bg-primary)', borderColor: 'var(--border-color)', color: 'var(--text-primary)' }} />
                  </div>
                  <div>
                    <label className="block text-xs font-medium mb-1" style={{ color: 'var(--text-secondary)' }}>Username</label>
                    <input type="text" value={pgUser} onChange={e => setPgUser(e.target.value)} className="w-full px-3 py-2 rounded-lg border text-sm" style={{ background: 'var(--bg-primary)', borderColor: 'var(--border-color)', color: 'var(--text-primary)' }} />
                  </div>
                  <div>
                    <label className="block text-xs font-medium mb-1" style={{ color: 'var(--text-secondary)' }}>Password</label>
                    <input type="password" value={pgPassword} onChange={e => setPgPassword(e.target.value)} className="w-full px-3 py-2 rounded-lg border text-sm" style={{ background: 'var(--bg-primary)', borderColor: 'var(--border-color)', color: 'var(--text-primary)' }} />
                  </div>
                </div>
                <div className="mb-4">
                  <label className="block text-xs font-medium mb-1" style={{ color: 'var(--text-secondary)' }}>SSL Mode</label>
                  <select value={pgSSLMode} onChange={e => setPgSSLMode(e.target.value)} className="w-full px-3 py-2 rounded-lg border text-sm" style={{ background: 'var(--bg-primary)', borderColor: 'var(--border-color)', color: 'var(--text-primary)' }}>
                    <option value="disable">disable</option>
                    <option value="prefer">prefer (recommended)</option>
                    <option value="require">require</option>
                  </select>
                </div>
                <div className="grid grid-cols-2 gap-4 mb-4">
                  <div>
                    <label className="block text-xs font-medium mb-1" style={{ color: 'var(--text-secondary)' }}>Organization Name</label>
                    <input type="text" value={orgName} onChange={e => setOrgName(e.target.value)} className="w-full px-3 py-2 rounded-lg border text-sm" style={{ background: 'var(--bg-primary)', borderColor: 'var(--border-color)', color: 'var(--text-primary)' }} />
                  </div>
                  <div>
                    <label className="block text-xs font-medium mb-1" style={{ color: 'var(--text-secondary)' }}>Organization Slug</label>
                    <input type="text" value={orgSlug} onChange={e => setOrgSlug(e.target.value)} placeholder="lowercase-with-hyphens" className="w-full px-3 py-2 rounded-lg border text-sm" style={{ background: 'var(--bg-primary)', borderColor: 'var(--border-color)', color: 'var(--text-primary)' }} />
                  </div>
                </div>

                {platformError && (
                  <div className="mb-4 p-3 rounded-lg bg-red-500/10 border border-red-500/30 flex items-center gap-2">
                    <AlertCircle size={18} className="text-red-400 shrink-0" />
                    <span className="text-sm text-red-400">{platformError}</span>
                  </div>
                )}

                <button
                  onClick={async () => {
                    setPlatformInitializing(true)
                    setPlatformError(null)
                    try {
                      const result = await initializePlatform({
                        host: pgHost,
                        port: parseInt(pgPort) || 5432,
                        user: pgUser,
                        password: pgPassword,
                        ssl_mode: pgSSLMode,
                        org_name: orgName,
                        org_slug: orgSlug,
                      })
                      if (result.success) {
                        setPlatformInitDone(true)
                        if (result.restart_required) {
                          setRestartRequired(true)
                        }
                      }
                    } catch (err: any) {
                      setPlatformError(err.message || 'Failed to initialize platform')
                    } finally {
                      setPlatformInitializing(false)
                    }
                  }}
                  disabled={platformInitializing || !pgUser || !pgPassword}
                  className="w-full py-3 rounded-lg font-medium flex items-center justify-center gap-2 transition-all disabled:opacity-50"
                  style={{ background: 'linear-gradient(to right, var(--brand), var(--brand-strong))', color: 'white' }}
                >
                  {platformInitializing ? (
                    <><Loader2 size={18} className="animate-spin" />Initializing Platform...</>
                  ) : (
                    <><Database size={18} />Initialize Platform</>
                  )}
                </button>
              </div>
            )}

            {platformInitDone && restartRequired && (
              <div className="max-w-xl mx-auto mt-4 p-6 rounded-xl bg-green-500/5 border border-green-500/30">
                <div className="flex items-center gap-3 mb-3">
                  <Check size={24} className="text-green-400" />
                  <span className="text-lg font-semibold" style={{ color: 'var(--text-primary)' }}>Platform Initialized</span>
                </div>
                <p className="text-sm mb-3" style={{ color: 'var(--text-muted)' }}>
                  The platform database has been created and configured. You can continue setting up your AI provider now.
                </p>
                <div className="p-3 rounded-lg bg-yellow-500/10 border border-yellow-500/30">
                  <p className="text-sm font-medium text-yellow-400">Restart Required</p>
                  <p className="text-xs mt-1" style={{ color: 'var(--text-muted)' }}>
                    After completing setup, restart Astonish to activate platform mode with authentication and teams.
                  </p>
                </div>
              </div>
            )}
          </div>
        )

      case 2:
        return (
          <div>
            <h2 className="text-2xl font-bold mb-2 text-center" style={{ color: 'var(--text-primary)' }}>Select Provider</h2>
            <p className="text-center mb-6" style={{ color: 'var(--text-muted)' }}>Choose an existing provider to edit or add a new one.</p>
            {settings?.providers && settings.providers.length > 0 && (
              <>
                <p className="text-sm font-medium mb-3" style={{ color: 'var(--text-secondary)' }}>Existing Providers</p>
                <div className="grid grid-cols-2 gap-3 max-w-2xl mx-auto mb-6">
                  {settings.providers.map(p => { const providerInfo = getProviderInfo(p.type || p.name); return (
                    <button key={p.name} onClick={() => handleSelectExisting(p.name)} className="p-4 rounded-xl border-2 text-left transition-all hover:scale-[1.02] border-transparent hover:border-gray-600" style={{ background: 'var(--bg-tertiary)' }}>
                      <div className="flex items-center gap-3"><span className="text-2xl">{providerInfo.icon}</span><div><div className="font-semibold" style={{ color: 'var(--text-primary)' }}>{p.name}</div><div className="text-xs" style={{ color: 'var(--text-muted)' }}>{providerInfo.name}</div></div></div>
                    </button>
                  )})}
                </div>
              </>
            )}
            <div className="border-t pt-6" style={{ borderColor: 'var(--border-color)' }}>
              <p className="text-sm font-medium mb-3" style={{ color: 'var(--text-secondary)' }}>{settings?.providers && settings.providers.length > 0 ? 'Or add a new provider' : 'Add Your First Provider'}</p>
              <div className="grid grid-cols-4 gap-3 max-w-2xl mx-auto">
                {PROVIDERS.map(p => (
                  <button key={p.id} onClick={() => handleProviderTypeSelect(p.id)} className={`p-3 rounded-xl border-2 text-left transition-all hover:scale-[1.02] ${selectedProvider === p.id ? 'border-primary bg-primary/10' : 'border-transparent hover:border-gray-600'}`} style={{ background: selectedProvider === p.id ? undefined : 'var(--bg-tertiary)' }}>
                    <div className="flex items-center gap-2 mb-1"><span className="text-xl">{p.icon}</span><span className="font-semibold text-sm" style={{ color: 'var(--text-primary)' }}>{p.name}</span></div>
                    <p className="text-xs" style={{ color: 'var(--text-muted)' }}>{p.description}</p>
                  </button>
                ))}
              </div>
            </div>
          </div>
        )
      case 3: {
        const provider = getProviderInfo(selectedProvider)
        return (
          <div className="max-w-md mx-auto">
            <h2 className="text-2xl font-bold mb-2 text-center" style={{ color: 'var(--text-primary)' }}>Configure {provider.name}</h2>
            <p className="text-center mb-6" style={{ color: 'var(--text-muted)' }}>Enter your credentials to connect to {provider.name}.</p>
            <div className="space-y-4">
              {provider.fields.map(field => (
                <div key={field.key}>
                  <label className="block text-sm font-medium mb-2" style={{ color: 'var(--text-secondary)' }}>{field.label}{field.key === 'resource_group' ? ' (optional)' : ''}</label>
                  <div className="relative">
                    <Key size={18} className="absolute left-3 top-1/2 -translate-y-1/2 text-gray-500" />
                    <input type={field.key.includes('secret') || field.key === 'client_secret' ? 'password' : 'text'} value={credentials[field.key] || ''} onChange={e => setCredentials(prev => ({ ...prev, [field.key]: e.target.value }))} onFocus={(e) => { if (isSensitiveField(field.key) && credentials[field.key]?.startsWith('••')) e.target.value = '' }} placeholder={field.placeholder} className="w-full pl-10 pr-4 py-3 rounded-lg border focus:ring-2 focus:ring-primary focus:border-transparent transition-all" style={{ background: 'var(--bg-tertiary)', borderColor: 'var(--border-color)', color: 'var(--text-primary)' }} />
                  </div>
                </div>
              ))}
            </div>
            {error && <div className="mt-4 p-3 rounded-lg bg-red-500/10 border border-red-500/30 flex items-center gap-2"><AlertCircle size={18} className="text-red-400" /><span className="text-sm text-red-400">{error}</span></div>}
            <button onClick={handleTestConnection} disabled={isLoading || !Object.values(credentials).some(v => v)} className="mt-6 w-full py-3 rounded-lg font-medium flex items-center justify-center gap-2 transition-all disabled:opacity-50" style={{ background: testSuccess ? 'var(--bg-tertiary)' : 'linear-gradient(to right, var(--brand), var(--brand-strong))', color: testSuccess ? 'var(--text-secondary)' : 'white' }}>
              {isLoading ? <><Loader2 size={18} className="animate-spin" />Testing Connection...</> : testSuccess ? <><Check size={18} />Connection Verified</> : <><Zap size={18} />Test Connection</>}
            </button>
          </div>
        )
      }
      case 4:
        return (
          <div className="max-w-md mx-auto">
            <h2 className="text-2xl font-bold mb-2 text-center" style={{ color: 'var(--text-primary)' }}>Provider Instance Name</h2>
            <p className="text-center mb-6" style={{ color: 'var(--text-muted)' }}>Give this provider instance a unique name.</p>
            <div className="relative">
              <Folder size={18} className="absolute left-3 top-1/2 -translate-y-1/2 text-gray-500" />
              <input type="text" value={instanceName} onChange={e => setInstanceName(e.target.value)} placeholder="e.g., openai-prod, anthropic-dev" className="w-full pl-10 pr-4 py-3 rounded-lg border focus:ring-2 focus:ring-primary focus:border-transparent transition-all" style={{ background: 'var(--bg-tertiary)', borderColor: 'var(--border-color)', color: 'var(--text-primary)' }} autoFocus />
            </div>
            {error && <div className="mt-4 p-3 rounded-lg bg-red-500/10 border border-red-500/30 flex items-center gap-2"><AlertCircle size={18} className="text-red-400" /><span className="text-sm text-red-400">{error}</span></div>}
            <button onClick={handleSaveProvider} disabled={isLoading || !instanceName.trim()} className="mt-6 w-full py-3 rounded-lg font-medium flex items-center justify-center gap-2 transition-all disabled:opacity-50" style={{ background: 'linear-gradient(to right, var(--brand), var(--brand-strong))', color: 'white' }}>
              {isLoading ? <><Loader2 size={18} className="animate-spin" />Saving...</> : <><Check size={18} />Save Provider</>}
            </button>
          </div>
        )
      case 5:
        return (
          <div className="max-w-md mx-auto">
            <h2 className="text-2xl font-bold mb-2 text-center" style={{ color: 'var(--text-primary)' }}>Select Default Model</h2>
            <p className="text-center mb-6" style={{ color: 'var(--text-muted)' }}>Choose the model to use by default. You can change this anytime.</p>
            <div className="space-y-2 max-h-80 overflow-y-auto">
              {availableModels.map(model => (
                <button key={model} onClick={() => setSelectedModel(model)} className={`w-full p-3 rounded-lg text-left transition-all ${selectedModel === model ? 'bg-primary/20 border-primary' : 'hover:bg-gray-700/50'}`} style={{ background: selectedModel === model ? undefined : 'var(--bg-tertiary)', border: `2px solid ${selectedModel === model ? 'var(--brand)' : 'transparent'}` }}>
                  <span style={{ color: 'var(--text-primary)' }}>{model}</span>
                </button>
              ))}
            </div>
          </div>
        )

      // Step 6: Web Search
      case 6:
        return (
          <div className="max-w-2xl mx-auto">
            <h2 className="text-2xl font-bold mb-2 text-center" style={{ color: 'var(--text-primary)' }}>Web Search Tools</h2>
            <p className="text-center mb-6" style={{ color: 'var(--text-muted)' }}>Enable web search so your AI can find information online.</p>

            {webInstalled ? (
              <div className="text-center">
                <div className="inline-flex items-center justify-center w-16 h-16 rounded-full bg-green-500/10 mb-4"><Check size={32} className="text-green-400" /></div>
                <p className="text-lg font-medium mb-2" style={{ color: 'var(--text-primary)' }}>{webInstalledName} configured</p>
                <p className="text-sm" style={{ color: 'var(--text-muted)' }}>Web search and content extraction are ready.</p>
              </div>
            ) : isLoading && standardServers.length === 0 ? (
              <div className="text-center py-8"><Loader2 size={24} className="animate-spin mx-auto mb-2 text-primary" /><p style={{ color: 'var(--text-muted)' }}>Loading providers...</p></div>
            ) : (
              <>
                <div className="grid grid-cols-3 gap-3 mb-6">
                  {standardServers.map(srv => (
                    <button
                      key={srv.id}
                      onClick={() => { setSelectedWebServer(srv.id); setWebApiKey(''); setError(null) }}
                      className={`p-4 rounded-xl border-2 text-left transition-all hover:scale-[1.02] ${selectedWebServer === srv.id ? 'border-primary bg-primary/10' : srv.installed ? 'border-green-500/50' : 'border-transparent hover:border-gray-600'}`}
                      style={{ background: selectedWebServer === srv.id ? undefined : 'var(--bg-tertiary)' }}
                    >
                      <div className="flex items-center gap-2 mb-2">
                        <Search size={18} className="text-primary" />
                        <span className="font-semibold text-sm" style={{ color: 'var(--text-primary)' }}>{srv.displayName || srv.name}</span>
                        {srv.isDefault && !srv.installed && <span className="text-[10px] px-1.5 py-0.5 rounded-full bg-primary/20 text-primary">recommended</span>}
                        {srv.installed && <Check size={14} className="text-green-400" />}
                      </div>
                      <p className="text-xs mb-1" style={{ color: 'var(--text-muted)' }}>{srv.description?.slice(0, 80)}</p>
                      <p className="text-[10px]" style={{ color: 'var(--text-muted)' }}>
                        {srv.capabilities?.webSearch && srv.capabilities?.webExtract ? 'Search + Extract' : 'Search only'}
                      </p>
                    </button>
                  ))}
                </div>

                {selectedWebServer && (() => {
                  const srv = standardServers.find(s => s.id === selectedWebServer)
                  if (!srv) return null
                  const needsKey = srv.envVars && srv.envVars.length > 0
                  return (
                    <div className="p-4 rounded-lg mb-4" style={{ background: 'var(--bg-tertiary)' }}>
                      {needsKey ? (
                        <>
                          <label className="block text-sm font-medium mb-2" style={{ color: 'var(--text-secondary)' }}>{srv.envVars![0].name}</label>
                          <div className="flex gap-3">
                            <input
                              type="password"
                              value={webApiKey}
                              onChange={e => setWebApiKey(e.target.value)}
                              placeholder={srv.envVars![0].description || 'Enter API key'}
                              className="flex-1 px-4 py-2.5 rounded-lg border text-sm focus:ring-2 focus:ring-primary focus:border-transparent"
                              style={{ background: 'var(--bg-secondary)', borderColor: 'var(--border-color)', color: 'var(--text-primary)' }}
                            />
                            <button
                              onClick={handleInstallWebServer}
                              disabled={isLoading || !webApiKey}
                              className="px-4 py-2 rounded-lg font-medium text-sm transition-all disabled:opacity-50"
                              style={{ background: 'linear-gradient(to right, var(--brand), var(--brand-strong))', color: 'white' }}
                            >
                              {isLoading ? <Loader2 size={16} className="animate-spin" /> : 'Install'}
                            </button>
                          </div>
                        </>
                      ) : (
                        <div className="flex items-center justify-between">
                          <span className="text-sm" style={{ color: 'var(--text-secondary)' }}>No API key required</span>
                          <button
                            onClick={handleInstallWebServer}
                            disabled={isLoading}
                            className="px-4 py-2 rounded-lg font-medium text-sm transition-all disabled:opacity-50"
                            style={{ background: 'linear-gradient(to right, var(--brand), var(--brand-strong))', color: 'white' }}
                          >
                            {isLoading ? <Loader2 size={16} className="animate-spin" /> : 'Install'}
                          </button>
                        </div>
                      )}
                    </div>
                  )
                })()}

                {error && <div className="p-3 rounded-lg bg-red-500/10 border border-red-500/30 flex items-center gap-2 mb-4"><AlertCircle size={18} className="text-red-400" /><span className="text-sm text-red-400">{error}</span></div>}

                {!webSkipped && (
                  <button onClick={() => setWebSkipped(true)} className="w-full py-2 text-sm transition-all" style={{ color: 'var(--text-muted)' }}>
                    Skip for now
                  </button>
                )}
              </>
            )}
          </div>
        )

      // Step 7: Browser Engine
      case 7:
        return (
          <div className="max-w-2xl mx-auto">
            <h2 className="text-2xl font-bold mb-2 text-center" style={{ color: 'var(--text-primary)' }}>Browser Engine</h2>
            <p className="text-center mb-6" style={{ color: 'var(--text-muted)' }}>Choose which browser to use for web automation and content extraction.</p>

            <div className="grid grid-cols-2 gap-3 mb-6">
              {BROWSER_ENGINES.map(eng => (
                <button
                  key={eng.id}
                  onClick={() => { setBrowserEngine(eng.id); setBrowserSaved(false); setError(null) }}
                  className={`p-4 rounded-xl border-2 text-left transition-all hover:scale-[1.02] ${browserEngine === eng.id ? 'border-primary bg-primary/10' : 'border-transparent hover:border-gray-600'}`}
                  style={{ background: browserEngine === eng.id ? undefined : 'var(--bg-tertiary)' }}
                >
                  <div className="flex items-center gap-2 mb-1">
                    <Monitor size={18} className="text-primary" />
                    <span className="font-semibold text-sm" style={{ color: 'var(--text-primary)' }}>{eng.name}</span>
                    {eng.recommended && <span className="text-[10px] px-1.5 py-0.5 rounded-full bg-primary/20 text-primary">recommended</span>}
                  </div>
                  <p className="text-xs" style={{ color: 'var(--text-muted)' }}>{eng.description}</p>
                </button>
              ))}
            </div>

            {/* Engine-specific configuration */}
            {browserEngine === 'custom' && (
              <div className="p-4 rounded-lg mb-4" style={{ background: 'var(--bg-tertiary)' }}>
                <label className="block text-sm font-medium mb-2" style={{ color: 'var(--text-secondary)' }}>Chrome/Chromium binary path</label>
                <input
                  type="text"
                  value={browserCustomPath}
                  onChange={e => { setBrowserCustomPath(e.target.value); setBrowserSaved(false) }}
                  placeholder="/usr/bin/google-chrome"
                  className="w-full px-4 py-2.5 rounded-lg border text-sm font-mono focus:ring-2 focus:ring-primary focus:border-transparent"
                  style={{ background: 'var(--bg-secondary)', borderColor: 'var(--border-color)', color: 'var(--text-primary)' }}
                />
              </div>
            )}

            {browserEngine === 'remote' && (
              <div className="p-4 rounded-lg mb-4 space-y-3" style={{ background: 'var(--bg-tertiary)' }}>
                <p className="text-xs" style={{ color: 'var(--text-muted)' }}>Launch Chrome with --remote-debugging-port=9222, then enter its address here.</p>
                <div className="grid grid-cols-3 gap-3">
                  <div className="col-span-2">
                    <label className="block text-sm font-medium mb-1" style={{ color: 'var(--text-secondary)' }}>Host / IP</label>
                    <input
                      type="text"
                      value={browserRemoteHost}
                      onChange={e => { setBrowserRemoteHost(e.target.value); setBrowserSaved(false) }}
                      placeholder="192.168.1.100"
                      className="w-full px-4 py-2.5 rounded-lg border text-sm font-mono focus:ring-2 focus:ring-primary focus:border-transparent"
                      style={{ background: 'var(--bg-secondary)', borderColor: 'var(--border-color)', color: 'var(--text-primary)' }}
                    />
                  </div>
                  <div>
                    <label className="block text-sm font-medium mb-1" style={{ color: 'var(--text-secondary)' }}>Port</label>
                    <input
                      type="text"
                      value={browserRemotePort}
                      onChange={e => { setBrowserRemotePort(e.target.value); setBrowserSaved(false) }}
                      placeholder="9222"
                      className="w-full px-4 py-2.5 rounded-lg border text-sm font-mono focus:ring-2 focus:ring-primary focus:border-transparent"
                      style={{ background: 'var(--bg-secondary)', borderColor: 'var(--border-color)', color: 'var(--text-primary)' }}
                    />
                  </div>
                </div>
              </div>
            )}

            {browserEngine === 'cloakbrowser' && (
              <div className="p-4 rounded-lg mb-4 bg-primary/5 border border-primary/20">
                <p className="text-sm" style={{ color: 'var(--text-secondary)' }}>
                  CloakBrowser requires dependency installation via the CLI.
                  Run <code className="text-primary">astonish config browser</code> to set it up.
                </p>
              </div>
            )}

            {error && <div className="p-3 rounded-lg bg-red-500/10 border border-red-500/30 flex items-center gap-2 mb-4"><AlertCircle size={18} className="text-red-400" /><span className="text-sm text-red-400">{error}</span></div>}

            {browserEngine !== 'default' && browserEngine !== 'cloakbrowser' && (
              <button
                onClick={handleSaveBrowser}
                disabled={isLoading || (browserEngine === 'custom' && !browserCustomPath) || (browserEngine === 'remote' && !browserRemoteHost)}
                className="w-full py-3 rounded-lg font-medium flex items-center justify-center gap-2 transition-all disabled:opacity-50"
                style={{ background: browserSaved ? 'var(--bg-tertiary)' : 'linear-gradient(to right, var(--brand), var(--brand-strong))', color: browserSaved ? 'var(--text-secondary)' : 'white' }}
              >
                {isLoading ? <><Loader2 size={18} className="animate-spin" />Saving...</> : browserSaved ? <><Check size={18} />Saved</> : <><Check size={18} />Save Browser Config</>}
              </button>
            )}
          </div>
        )

      // Step 8: Sandbox
      case 8:
        return (
          <div className="max-w-2xl mx-auto">
            <h2 className="text-2xl font-bold mb-2 text-center" style={{ color: 'var(--text-primary)' }}>Sandbox</h2>
            <p className="text-center mb-6" style={{ color: 'var(--text-muted)' }}>Container isolation for AI tool execution. Prevents tools from accessing your host system directly.</p>

            {isLoading && !sandboxStatus ? (
              <div className="text-center py-8"><Loader2 size={24} className="animate-spin mx-auto mb-2 text-primary" /><p style={{ color: 'var(--text-muted)' }}>Detecting sandbox environment...</p></div>
            ) : (sandboxStatus as any)?.baseTemplateExists ? (
              <div className="text-center">
                <div className="inline-flex items-center justify-center w-16 h-16 rounded-full bg-green-500/10 mb-4"><Shield size={32} className="text-green-400" /></div>
                <p className="text-lg font-medium mb-2" style={{ color: 'var(--text-primary)' }}>Sandbox already configured</p>
                <p className="text-sm" style={{ color: 'var(--text-muted)' }}>Base template exists. AI tools will run inside isolated containers.</p>
              </div>
            ) : ((sandboxStatus as any)?.runtimeAvailable ?? (sandboxStatus as any)?.incusAvailable) ? (
              <>
                {/* Tool selection */}
                {!sandboxInitializing && !sandboxDone && (
                  <>
                    {optionalTools.length > 0 && (
                      <>
                        <p className="text-sm font-medium mb-3" style={{ color: 'var(--text-secondary)' }}>Optional tools to install in the sandbox:</p>
                        <div className="space-y-3 mb-6">
                          {optionalTools.map(tool => (
                            <button
                              key={tool.id}
                              onClick={() => setSelectedTools(prev => ({ ...prev, [tool.id]: !prev[tool.id] }))}
                              className={`w-full p-4 rounded-xl border-2 text-left transition-all ${selectedTools[tool.id] ? 'border-primary bg-primary/10' : 'border-transparent hover:border-gray-600'}`}
                              style={{ background: selectedTools[tool.id] ? undefined : 'var(--bg-tertiary)' }}
                            >
                              <div className="flex items-center justify-between">
                                <div className="flex items-center gap-3">
                                  <div className={`w-5 h-5 rounded border-2 flex items-center justify-center transition-all ${selectedTools[tool.id] ? 'bg-primary border-primary' : 'border-gray-500'}`}>
                                    {selectedTools[tool.id] && <Check size={12} className="text-white" />}
                                  </div>
                                  <div>
                                    <span className="font-semibold text-sm" style={{ color: 'var(--text-primary)' }}>{tool.name}</span>
                                    {(tool as any).recommended && <span className="ml-2 text-[10px] px-1.5 py-0.5 rounded-full bg-primary/20 text-primary">recommended</span>}
                                    {(tool as any).requiresNesting && <span className="ml-2 text-[10px] px-1.5 py-0.5 rounded-full bg-yellow-500/20 text-yellow-400">needs nesting</span>}
                                  </div>
                                </div>
                                {(tool as any).url && (
                                  <a href={(tool as any).url} target="_blank" rel="noopener noreferrer" onClick={e => e.stopPropagation()} className="text-primary hover:text-primary">
                                    <ExternalLink size={14} />
                                  </a>
                                )}
                              </div>
                              <p className="text-xs mt-1 ml-8" style={{ color: 'var(--text-muted)' }}>{tool.description}</p>
                            </button>
                          ))}
                        </div>
                      </>
                    )}

                    <button
                      onClick={handleSandboxInit}
                      className="w-full py-3 rounded-lg font-medium flex items-center justify-center gap-2 transition-all"
                      style={{ background: 'linear-gradient(to right, var(--brand), var(--brand-strong))', color: 'white' }}
                    >
                      <Shield size={18} />Initialize Sandbox
                    </button>
                  </>
                )}

                {/* Progress display */}
                {(sandboxInitializing || sandboxDone) && (
                  <div>
                    {sandboxDone ? (
                      <div className="text-center mb-4">
                        <div className="inline-flex items-center justify-center w-12 h-12 rounded-full bg-green-500/10 mb-3"><Check size={24} className="text-green-400" /></div>
                        <p className="font-medium" style={{ color: 'var(--text-primary)' }}>Sandbox initialized</p>
                      </div>
                    ) : (
                      <div className="flex items-center gap-2 mb-4">
                        <Loader2 size={18} className="animate-spin text-primary" />
                        <span className="text-sm font-medium" style={{ color: 'var(--text-secondary)' }}>Initializing sandbox...</span>
                      </div>
                    )}
                    <div className="p-3 rounded-lg max-h-48 overflow-y-auto font-mono text-xs" style={{ background: 'var(--bg-tertiary)', color: 'var(--text-muted)' }}>
                      {sandboxProgress.map((msg, i) => (
                        <div key={i} className="py-0.5">{msg}</div>
                      ))}
                      <div ref={progressEndRef} />
                    </div>
                  </div>
                )}

                {error && !sandboxInitializing && (
                  <div className="mt-4">
                    <div className="p-3 rounded-lg bg-red-500/10 border border-red-500/30 flex items-center gap-2 mb-3"><AlertCircle size={18} className="text-red-400" /><span className="text-sm text-red-400">{error}</span></div>
                    <button
                      onClick={handleSandboxInit}
                      className="w-full py-2 rounded-lg font-medium text-sm transition-all"
                      style={{ background: 'linear-gradient(to right, var(--brand), var(--brand-strong))', color: 'white' }}
                    >
                      Retry
                    </button>
                  </div>
                )}
              </>
            ) : (
              // Incus not available
              <div>
                <div className="p-4 rounded-lg mb-4 bg-yellow-500/5 border border-yellow-500/20">
                  <div className="flex items-start gap-3">
                    <ShieldAlert size={20} className="text-yellow-400 mt-0.5 shrink-0" />
                    <div>
                      <p className="font-medium text-sm mb-2" style={{ color: 'var(--text-primary)' }}>Incus not available</p>
                      {sandboxStatus?.reason && <p className="text-xs mb-3" style={{ color: 'var(--text-muted)' }}>{sandboxStatus.reason}</p>}
                      <div className="text-xs space-y-1" style={{ color: 'var(--text-secondary)' }}>
                        <p>To install Incus (Ubuntu/Debian):</p>
                        <code className="block p-2 rounded text-xs font-mono" style={{ background: 'var(--bg-secondary)' }}>sudo apt install incus && sudo incus admin init</code>
                      </div>
                    </div>
                  </div>
                </div>

                <div className="flex gap-3">
                  <button
                    onClick={loadSandboxStatus}
                    className="flex-1 py-2.5 rounded-lg font-medium text-sm flex items-center justify-center gap-2 transition-all"
                    style={{ background: 'linear-gradient(to right, var(--brand), var(--brand-strong))', color: 'white' }}
                  >
                    {isLoading ? <Loader2 size={16} className="animate-spin" /> : null}
                    I've installed Incus — Retry
                  </button>
                  <button
                    onClick={() => setSandboxSkipConfirm(true)}
                    className="px-4 py-2.5 rounded-lg text-sm transition-all"
                    style={{ background: 'var(--bg-tertiary)', color: 'var(--text-muted)' }}
                  >
                    Skip
                  </button>
                </div>

                {sandboxSkipConfirm && !sandboxSkipped && (
                  <div className="mt-4 p-4 rounded-lg bg-red-500/5 border border-red-500/20">
                    <p className="text-sm font-medium mb-2" style={{ color: 'var(--text-primary)' }}>Continue without sandbox?</p>
                    <p className="text-xs mb-3" style={{ color: 'var(--text-muted)' }}>Without sandbox, AI tools will execute directly on your host system with full access to your files, network, and system resources.</p>
                    <div className="flex gap-3">
                      <button
                        onClick={() => { setSandboxSkipped(true); setSandboxSkipConfirm(false) }}
                        className="px-4 py-2 rounded-lg text-sm font-medium bg-red-500/20 text-red-400 hover:bg-red-500/30 transition-all"
                      >
                        Yes, I accept the risk
                      </button>
                      <button
                        onClick={() => setSandboxSkipConfirm(false)}
                        className="px-4 py-2 rounded-lg text-sm transition-all"
                        style={{ background: 'var(--bg-tertiary)', color: 'var(--text-secondary)' }}
                      >
                        Go back
                      </button>
                    </div>
                  </div>
                )}
              </div>
            )}
          </div>
        )

      // Step 9: All Set
      case 9: {
        const finalProvider = getProviderInfo(selectedProvider)
        return (
          <div className="text-center">
            <div className="inline-flex items-center justify-center w-20 h-20 rounded-full bg-gradient-to-br from-green-500 to-emerald-400 mb-6"><Check size={40} className="text-white" /></div>
            <h2 className="text-2xl font-bold mb-4" style={{ color: 'var(--text-primary)' }}>You're All Set!</h2>
            <p className="text-lg mb-6" style={{ color: 'var(--text-muted)' }}>Astonish Studio is ready to use.</p>
            <div className="inline-block text-left p-4 rounded-lg space-y-3 min-w-[280px]" style={{ background: 'var(--bg-tertiary)' }}>
              {(deploymentMode === 'platform' || deploymentMode === 'sqlite') && (
                <div className="flex items-center gap-2 text-xs pb-3 border-b" style={{ borderColor: 'var(--border-color)' }}>
                  <Database size={14} className="text-green-400" />
                  <span style={{ color: 'var(--text-secondary)' }}>Mode:</span>
                  <span style={{ color: 'var(--text-muted)' }}>Platform ({deploymentMode === 'sqlite' ? 'SQLite' : 'PostgreSQL'})</span>
                  {restartRequired && <span className="text-yellow-400 text-[10px] px-1.5 py-0.5 bg-yellow-500/10 rounded">Restart needed</span>}
                </div>
              )}
              <div className="flex items-center gap-3">
                <span className="text-2xl">{finalProvider.icon}</span>
                <div><div className="font-medium text-sm" style={{ color: 'var(--text-primary)' }}>{instanceName}</div><div className="text-xs" style={{ color: 'var(--text-muted)' }}>{selectedModel}</div></div>
              </div>
              <div className="border-t pt-3 space-y-2" style={{ borderColor: 'var(--border-color)' }}>
                <div className="flex items-center gap-2 text-xs">
                  <Search size={14} className={webInstalled ? 'text-green-400' : 'text-gray-500'} />
                  <span style={{ color: 'var(--text-secondary)' }}>Web Search:</span>
                  <span style={{ color: 'var(--text-muted)' }}>{webInstalled ? webInstalledName : 'Not configured'}</span>
                </div>
                <div className="flex items-center gap-2 text-xs">
                  <Monitor size={14} className="text-primary" />
                  <span style={{ color: 'var(--text-secondary)' }}>Browser:</span>
                  <span style={{ color: 'var(--text-muted)' }}>{BROWSER_ENGINES.find(e => e.id === browserEngine)?.name || 'Default'}</span>
                </div>
                <div className="flex items-center gap-2 text-xs">
                  <Shield size={14} className={sandboxDone || (sandboxStatus as any)?.baseTemplateExists ? 'text-green-400' : sandboxSkipped ? 'text-yellow-400' : 'text-gray-500'} />
                  <span style={{ color: 'var(--text-secondary)' }}>Sandbox:</span>
                  <span style={{ color: 'var(--text-muted)' }}>
                    {sandboxDone ? `Enabled${Object.entries(selectedTools).filter(([,v]) => v).length > 0 ? ` (${Object.entries(selectedTools).filter(([,v]) => v).map(([k]) => optionalTools.find(t => t.id === k)?.name || k).join(', ')})` : ''}` :
                     (sandboxStatus as any)?.baseTemplateExists ? 'Already configured' :
                     sandboxSkipped ? 'Skipped' : 'Not configured'}
                  </span>
                </div>
              </div>
            </div>
            <p className="text-sm mt-6" style={{ color: 'var(--text-muted)' }}>You can change all settings anytime in the Settings panel.</p>
          </div>
        )
      }
      default: return null
    }
  }

  const isLastStep = step === TOTAL_STEPS - 1

  return (
    <div className="fixed inset-0 flex items-center justify-center p-8 z-50" style={{ background: 'var(--bg-primary)' }}>
      <div className="w-full max-w-3xl p-8 rounded-2xl shadow-2xl" style={{ background: 'var(--bg-secondary)', border: '1px solid var(--border-color)' }}>
        <StepIndicator step={step} totalSteps={TOTAL_STEPS} />
        <div className="min-h-[400px] flex flex-col justify-center">{renderStep()}</div>
        <div className="flex justify-between mt-8 pt-6" style={{ borderTop: '1px solid var(--border-color)' }}>
          <button onClick={goBack} disabled={step === 0 || sandboxInitializing} className="flex items-center gap-2 px-4 py-2 rounded-lg transition-all disabled:opacity-30" style={{ color: 'var(--text-secondary)' }}><ChevronLeft size={18} />Back</button>
          {isLastStep ? (
            <button onClick={handleComplete} disabled={isLoading} className="flex items-center gap-2 px-6 py-2 rounded-lg font-medium transition-all" style={{ background: 'linear-gradient(to right, var(--brand), var(--brand-strong))', color: 'white' }}>
              {isLoading ? <><Loader2 size={18} className="animate-spin" />Saving...</> : <><Sparkles size={18} />Get Started</>}
            </button>
          ) : (
            <button onClick={goNext} disabled={!canProceed() || sandboxInitializing} className="flex items-center gap-2 px-6 py-2 rounded-lg font-medium transition-all disabled:opacity-50" style={{ background: canProceed() && !sandboxInitializing ? 'linear-gradient(to right, var(--brand), var(--brand-strong))' : 'var(--bg-tertiary)', color: canProceed() && !sandboxInitializing ? 'white' : 'var(--text-muted)' }}>
              Continue<ChevronRight size={18} />
            </button>
          )}
        </div>
      </div>
    </div>
  )
}
