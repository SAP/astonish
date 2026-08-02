import { useState } from 'react'
import { Key, Server, ChevronRight, Save, Plus, Trash2, Check, AlertCircle, Code, LayoutGrid, Loader2, Package, Search, Play, Download, RefreshCw, Bot, Sparkles } from 'lucide-react'
import MCPStoreModal from '../MCPStoreModal'
import MCPInspector from '../MCPInspector'
import CodeMirror from '@uiw/react-codemirror'
import { json } from '@codemirror/lang-json'
import { search, searchKeymap, highlightSelectionMatches } from '@codemirror/search'
import { keymap, EditorView } from '@codemirror/view'
import {
  saveMCPConfig,
  refreshMCPServer,
  toggleMCPServer,
  fetchMCPStatus,
  fetchPerplexityWebSearchOptions,
  savePerplexityWebSearchConfig,
  clearPerplexityWebSearchConfig,
} from './settingsApi'
import type { MCPServerConfig, MCPServerStatusEntry, StandardServer, PerplexityProviderOption } from './settingsApi'
import { teamFetch } from '../../api/teamContext'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { cn } from '@/lib/utils'

interface MCPServersSettingsProps {
  mcpServers: Record<string, MCPServerConfig>
  setMcpServers: (servers: Record<string, MCPServerConfig>) => void
  mcpServerNames: Record<string, string>
  setMcpServerNames: (names: Record<string, string>) => void
  mcpServerArgs: Record<string, string>
  setMcpServerArgs: (args: Record<string, string>) => void
  setMcpHasChanges: (hasChanges: boolean) => void
  standardServers: StandardServer[]
  saving: boolean
  setSaving: (saving: boolean) => void
  setSaveSuccess: (success: boolean) => void
  setError: (error: string | null) => void
  onToolsRefresh?: () => void
  loadData: () => void
  setGeneralForm: (fn: (prev: any) => any) => void
  theme?: string
  teamSlug?: string
  scope?: 'team' | 'org' | 'platform'  // explicit scope override; when set, teamSlug is ignored for URL routing
}

export default function MCPServersSettings({
  mcpServers,
  setMcpServers,
  mcpServerNames,
  setMcpServerNames,
  mcpServerArgs,
  setMcpServerArgs,
  setMcpHasChanges,
  standardServers,
  saving,
  setSaving,
  setSaveSuccess,
  setError,
  onToolsRefresh,
  loadData,
  setGeneralForm,
  theme = 'dark',
  teamSlug,
  scope
}: MCPServersSettingsProps) {
  // Resolve the effective scope: explicit scope prop overrides teamSlug inference
  const effectiveScope = scope || (teamSlug ? 'team' : undefined)
  const scopeQuery = effectiveScope ? `?scope=${effectiveScope}` : ''
  const effectiveTeamSlug = scope === 'platform' ? undefined : teamSlug
  const [mcpViewMode, setMcpViewMode] = useState<'editor' | 'source'>('editor')
  const [mcpSourceText, setMcpSourceText] = useState('')
  const [mcpSourceError, setMcpSourceError] = useState<string | null>(null)
  const [expandedMcpServer, setExpandedMcpServer] = useState<string | null>(null)
  const [savingServer, setSavingServer] = useState<string | null>(null)
  const [showMCPStore, setShowMCPStore] = useState(false)
  const [mcpServerStatus, setMcpServerStatus] = useState<Record<string, MCPServerStatusEntry>>({})
  const [inspectServer, setInspectServer] = useState<string | null>(null)

  // Standard server setup state
  const [setupServer, setSetupServer] = useState<string | null>(null)
  const [setupEnv, setSetupEnv] = useState<Record<string, string>>({})
  const [setupLoading, setSetupLoading] = useState(false)
  const [setupError, setSetupError] = useState<string | null>(null)
  const [perplexityOptions, setPerplexityOptions] = useState<PerplexityProviderOption[]>([])
  const [perplexityProvider, setPerplexityProvider] = useState('')
  const [perplexityModel, setPerplexityModel] = useState('')
  const [perplexityContextSize, setPerplexityContextSize] = useState('medium')
  const [perplexityMaxResults, setPerplexityMaxResults] = useState(5)

  const loadMcpServerStatus = async () => {
    try {
      const data = await fetchMCPStatus(effectiveTeamSlug, effectiveScope)
      const statusMap: Record<string, MCPServerStatusEntry> = {}
      for (const server of (data.servers || [])) {
        statusMap[server.name] = server
      }
      setMcpServerStatus(statusMap)
    } catch (err: any) {
      console.error('Failed to fetch MCP status:', err)
    }
  }

  // Load status on mount
  useState(() => {
    loadMcpServerStatus()
  })

  const handleAddMcpServer = () => {
    const newName = `server_${Date.now()}`
    setMcpServers({
      [newName]: { command: '', args: [], env: {}, transport: 'stdio' },
      ...mcpServers
    })
    setMcpServerNames({ [newName]: 'new-server', ...mcpServerNames })
    setMcpServerArgs({ [newName]: '', ...mcpServerArgs })
    setExpandedMcpServer(newName)
  }

  const handleRefreshMcpServer = async (serverName: string) => {
    setMcpServerStatus(prev => ({
      ...prev,
      [serverName]: { ...(prev?.[serverName] || {} as MCPServerStatusEntry), name: serverName, status: 'loading', error: null }
    }))
    
    try {
      await refreshMCPServer(serverName, effectiveTeamSlug, effectiveScope)
      loadMcpServerStatus()
      if (onToolsRefresh) onToolsRefresh()
    } catch (err: any) {
      console.error("Failed to refresh server:", err)
      setMcpServerStatus(prev => ({
        ...prev,
        [serverName]: { ...(prev?.[serverName] || {} as MCPServerStatusEntry), name: serverName, status: 'error', error: err.message }
      }))
    }
  }

  const handleToggleMcpServer = async (serverId: string, serverName: string, currentEnabled: boolean) => {
    const newEnabled = !currentEnabled
    
    setMcpServers({
      ...mcpServers,
      [serverId]: { ...mcpServers[serverId], enabled: newEnabled }
    })
    
    try {
      await toggleMCPServer(serverName, newEnabled, effectiveTeamSlug, effectiveScope)
      if (onToolsRefresh) onToolsRefresh()
      loadMcpServerStatus()
    } catch (err: any) {
      setMcpServers({
        ...mcpServers,
        [serverId]: { ...mcpServers[serverId], enabled: currentEnabled }
      })
      setError(`Failed to ${newEnabled ? 'enable' : 'disable'} server: ${err.message}`)
    }
  }

  const handleDeleteMcpServer = async (name: string) => {
    const newServers = { ...mcpServers }
    delete newServers[name]
    setMcpServers(newServers)
    const newNames = { ...mcpServerNames }
    delete newNames[name]
    setMcpServerNames(newNames)
    const newArgs = { ...mcpServerArgs }
    delete newArgs[name]
    setMcpServerArgs(newArgs)
    if (expandedMcpServer === name) {
      setExpandedMcpServer(null)
    }
    try {
      const finalServers: Record<string, MCPServerConfig> = {}
      Object.entries(newServers).forEach(([id, server]) => {
        const finalName = newNames[id] || id
        const argsString = newArgs[id] || ''
        finalServers[finalName] = {
          ...server,
          args: argsString.split(',').map(s => s.trim()).filter(Boolean)
        }
      })
      await saveMCPConfig({ mcpServers: finalServers }, effectiveTeamSlug, effectiveScope)
      if (onToolsRefresh) onToolsRefresh()
    } catch (err: any) {
      setError(err.message)
    }
  }

  const setupSrv = standardServers.find(s => s.id === setupServer) || null
  const configuredCount = standardServers.filter(s => s.installed).length

  const closeSetupDialog = () => {
    setSetupServer(null)
    setSetupEnv({})
    setSetupError(null)
  }

  const beginSetupServer = async (srv: StandardServer) => {
    setSetupServer(srv.id)
    setSetupEnv({})
    setSetupError(null)
    if (srv.id === 'perplexity') {
      setSetupLoading(true)
      try {
        const data = await fetchPerplexityWebSearchOptions(effectiveScope)
        const options = data.options || []
        setPerplexityOptions(options)
        const preferredProvider = srv.details?.provider
        const preferredModel = srv.details?.model
        const match = options.find(o => o.provider === preferredProvider)
        const first = match || options.find(o => o.models?.length > 0) || options[0]
        setPerplexityProvider(first?.provider || '')
        const models = first?.models || []
        setPerplexityModel(
          preferredModel && models.includes(preferredModel)
            ? preferredModel
            : (models[0] || '')
        )
      } catch (err: any) {
        setSetupError(err.message)
      } finally {
        setSetupLoading(false)
      }
    }
  }

  const handleSavePerplexity = async (srv: StandardServer) => {
    setSetupLoading(true)
    setSetupError(null)
    try {
      const result: any = await savePerplexityWebSearchConfig({
        provider: perplexityProvider,
        model: perplexityModel,
        search_context_size: perplexityContextSize,
        max_results: perplexityMaxResults
      }, effectiveScope)
      closeSetupDialog()
      await loadData()
      if (onToolsRefresh) onToolsRefresh()
      setGeneralForm((prev: any) => ({
        ...prev,
        web_search_tool: result.webSearchTool || srv.webSearchTool || 'perplexity:perplexity_web_search'
      }))
    } catch (err: any) {
      setSetupError(err.message)
    } finally {
      setSetupLoading(false)
    }
  }

  const handleInstallMcpStandardServer = async (srv: StandardServer) => {
    setSetupLoading(true)
    setSetupError(null)
    try {
      const url = effectiveScope
        ? `/api/standard-servers/${srv.id}/install?scope=${effectiveScope}`
        : `/api/standard-servers/${srv.id}/install`
      const res = await teamFetch(url, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ env: setupEnv })
      }, effectiveTeamSlug)
      if (!res.ok) {
        const text = await res.text()
        throw new Error(text)
      }
      const result = await res.json()
      closeSetupDialog()
      await loadData()
      if (onToolsRefresh) onToolsRefresh()
      if (result.webSearchTool) {
        setGeneralForm((prev: any) => ({
          ...prev,
          web_search_tool: result.webSearchTool,
          web_extract_tool: result.webExtractTool || prev.web_extract_tool
        }))
      }
    } catch (err: any) {
      setSetupError(err.message)
    } finally {
      setSetupLoading(false)
    }
  }

  const handleRemoveStandardServer = async (srv: StandardServer) => {
    try {
      if (srv.id === 'perplexity') {
        await clearPerplexityWebSearchConfig(effectiveScope)
      } else {
        const res = await teamFetch(`/api/standard-servers/${srv.id}${scopeQuery}`, { method: 'DELETE' }, effectiveTeamSlug)
        if (!res.ok) throw new Error('Failed to remove server')
      }
      await loadData()
      if (onToolsRefresh) onToolsRefresh()
    } catch (err: any) {
      setError(err.message || 'Failed to remove web search provider')
    }
  }

  const providerIcon = (srv: StandardServer) => {
    if (srv.id === 'perplexity' || srv.kind === 'model') return Bot
    if (srv.capabilities?.webExtract) return Sparkles
    return Search
  }

  const handleSaveSingleMcpServer = async (serverId: string) => {
    setSavingServer(serverId)
    try {
      const finalServers: Record<string, MCPServerConfig> = {}
      Object.entries(mcpServers).forEach(([id, server]) => {
        const finalName = mcpServerNames[id] || id
        const argsString = mcpServerArgs[id] || ''
        finalServers[finalName] = {
          ...server,
          args: argsString.split(',').map(s => s.trim()).filter(Boolean)
        }
      })
      await saveMCPConfig({ mcpServers: finalServers }, effectiveTeamSlug, effectiveScope)
      setMcpHasChanges(false)
      if (onToolsRefresh) onToolsRefresh()
      loadMcpServerStatus()
      setExpandedMcpServer(null)
    } catch (err: any) {
      setError(err.message)
    } finally {
      setSavingServer(null)
    }
  }

  return (
    <>
      <div className={mcpViewMode === 'source' ? 'h-full flex flex-col' : 'overflow-y-auto p-6 space-y-4'} style={mcpViewMode === 'source' ? undefined : { maxHeight: '100%' }}>
        {/* View toggle */}
        <div className="flex items-center gap-2">
          <div className="flex rounded-lg overflow-hidden border" style={{ borderColor: 'var(--border-color)' }}>
            <button
              onClick={() => {
                setMcpViewMode('editor')
                setMcpSourceError(null)
              }}
              className={`flex items-center gap-2 px-3 py-1.5 text-sm font-medium transition-colors ${
                mcpViewMode === 'editor'
                  ? 'text-white shadow-sm'
                  : 'hover:bg-gray-600/20'
              }`}
              style={{
                background: mcpViewMode === 'editor' ? 'var(--brand)' : 'transparent',
                color: mcpViewMode !== 'editor' ? 'var(--text-secondary)' : undefined
              }}
            >
              <LayoutGrid size={14} />
              Editor
            </button>
            <button
              onClick={() => {
                setMcpViewMode('source')
                setMcpSourceText(JSON.stringify({ mcpServers }, null, 2))
                setMcpSourceError(null)
              }}
              className={`flex items-center gap-2 px-3 py-1.5 text-sm font-medium transition-all ${
                mcpViewMode === 'source'
                  ? 'text-white shadow-sm'
                  : 'hover:bg-gray-600/20'
              }`}
              style={{
                background: mcpViewMode === 'source' ? 'var(--brand)' : undefined,
                color: mcpViewMode !== 'source' ? 'var(--text-secondary)' : undefined
              }}
            >
              <Code size={14} />
              Source
            </button>
          </div>
        </div>

        {/* Editor View */}
        {mcpViewMode === 'editor' && (
          <>
            {/* Standard Web Servers Section */}
            {standardServers.length > 0 && (
              <div className="mb-4 space-y-2">
                <div className="rounded-xl border bg-card p-3">
                  <div className="mb-2 flex items-center justify-between gap-2">
                    <h4 className="text-sm font-medium flex items-center gap-1.5 text-foreground">
                      <Search size={13} className="text-primary" />
                      Web Search Providers
                    </h4>
                    <span className="text-[11px] text-muted-foreground">
                      {configuredCount > 0
                        ? `${configuredCount} configured`
                        : 'None configured yet'}
                    </span>
                  </div>
                  <p className="mb-2 text-[11px] text-muted-foreground">
                    Install credentials or Perplexity model here. Then choose which tool the agent uses in General → Web Tools.
                  </p>

                  <div className="grid grid-cols-1 gap-2 sm:grid-cols-2 lg:grid-cols-3">
                    {standardServers.map(srv => {
                      const Icon = providerIcon(srv)
                      const canRemove = srv.installed && (srv.id === 'perplexity' || (srv.envVars?.length || 0) > 0)
                      const caps: string[] = []
                      if (srv.capabilities?.webSearch) caps.push('Search')
                      if (srv.capabilities?.webExtract) caps.push('Extract')
                      if (srv.kind === 'model' || srv.id === 'perplexity') caps.push('Model')

                      return (
                        <div
                          key={srv.id}
                          className={cn(
                            'rounded-lg border px-2.5 py-2 transition-colors',
                            srv.installed
                              ? 'border-emerald-500/35 bg-emerald-500/[0.06]'
                              : 'border-border bg-muted/15'
                          )}
                        >
                          <div className="flex items-start gap-2">
                            <Icon size={13} className={cn('mt-0.5 shrink-0', srv.installed ? 'text-emerald-500' : 'text-muted-foreground')} />
                            <div className="min-w-0 flex-1">
                              <div className="flex items-center gap-1.5 min-w-0">
                                <span className="truncate text-xs font-medium text-foreground">{srv.displayName}</span>
                                {srv.installed ? (
                                  <Check size={11} className="shrink-0 text-emerald-500" />
                                ) : srv.isDefault ? (
                                  <Badge variant="secondary" className="h-4 px-1 text-[9px]">rec</Badge>
                                ) : null}
                              </div>
                              <p className="text-[10px] text-muted-foreground truncate">
                                {caps.join(' · ') || 'Web'}
                                {srv.installed && srv.details?.model
                                  ? ` · ${srv.details.model}`
                                  : ''}
                              </p>
                            </div>
                          </div>
                          <div className="mt-1.5 flex items-center gap-1.5">
                            {!srv.installed ? (
                              <Button size="sm" className="h-6 px-2 text-[11px]" onClick={() => beginSetupServer(srv)}>
                                Setup
                              </Button>
                            ) : (
                              <>
                                <span className="text-[10px] text-emerald-600 dark:text-emerald-400">Configured</span>
                                <button
                                  type="button"
                                  onClick={() => beginSetupServer(srv)}
                                  className="text-[10px] text-muted-foreground hover:text-foreground"
                                >
                                  Change
                                </button>
                                {canRemove && (
                                  <button
                                    type="button"
                                    onClick={() => handleRemoveStandardServer(srv)}
                                    className="ml-auto p-0.5 text-muted-foreground hover:text-destructive"
                                    title="Remove configuration"
                                  >
                                    <Trash2 size={11} />
                                  </button>
                                )}
                              </>
                            )}
                          </div>
                        </div>
                      )
                    })}
                  </div>
                </div>

                <Dialog open={!!setupServer} onOpenChange={(open) => { if (!open) closeSetupDialog() }}>
                  <DialogContent className="sm:max-w-md">
                    <DialogHeader>
                      <DialogTitle>
                        {setupSrv?.installed ? 'Change' : 'Setup'} {setupSrv?.displayName || 'provider'}
                      </DialogTitle>
                      <DialogDescription>
                        {setupSrv?.id === 'perplexity'
                          ? 'Select an existing model provider and a Perplexity/Sonar model. This becomes the active web search tool.'
                          : 'Provide the required API key to install this web search provider and make it active.'}
                      </DialogDescription>
                    </DialogHeader>

                    {setupSrv?.id === 'perplexity' ? (
                      <div className="space-y-3">
                        <div className="space-y-1.5">
                          <label className="text-xs font-medium text-foreground">Provider</label>
                          <select
                            value={perplexityProvider}
                            onChange={(e) => {
                              const provider = e.target.value
                              const opt = perplexityOptions.find(o => o.provider === provider)
                              setPerplexityProvider(provider)
                              setPerplexityModel(opt?.models?.[0] || '')
                            }}
                            className="w-full rounded-md border border-input bg-background px-3 py-2 text-sm"
                          >
                            <option value="">Select provider…</option>
                            {perplexityOptions.map(opt => (
                              <option key={opt.provider} value={opt.provider}>
                                {opt.provider}{opt.models?.length ? '' : ' (no matching models)'}
                              </option>
                            ))}
                          </select>
                        </div>
                        <div className="space-y-1.5">
                          <label className="text-xs font-medium text-foreground">Model</label>
                          <select
                            value={perplexityModel}
                            onChange={(e) => setPerplexityModel(e.target.value)}
                            disabled={!perplexityProvider}
                            className="w-full rounded-md border border-input bg-background px-3 py-2 text-sm disabled:opacity-50"
                          >
                            <option value="">Select model…</option>
                            {(perplexityOptions.find(o => o.provider === perplexityProvider)?.models || []).map(model => (
                              <option key={model} value={model}>{model}</option>
                            ))}
                          </select>
                          {perplexityProvider && (perplexityOptions.find(o => o.provider === perplexityProvider)?.models || []).length === 0 && (
                            <p className="text-xs text-muted-foreground">
                              This provider is configured, but its model list did not include IDs containing “perplexity”, “sonar”, or “pplx”.
                            </p>
                          )}
                        </div>
                        <div className="grid grid-cols-2 gap-3">
                          <div className="space-y-1.5">
                            <label className="text-xs font-medium text-foreground">Context size</label>
                            <select
                              value={perplexityContextSize}
                              onChange={(e) => setPerplexityContextSize(e.target.value)}
                              className="w-full rounded-md border border-input bg-background px-3 py-2 text-sm"
                            >
                              <option value="low">Low</option>
                              <option value="medium">Medium</option>
                              <option value="high">High</option>
                            </select>
                          </div>
                          <div className="space-y-1.5">
                            <label className="text-xs font-medium text-foreground">Max results</label>
                            <input
                              type="number"
                              min={1}
                              max={20}
                              value={perplexityMaxResults}
                              onChange={(e) => setPerplexityMaxResults(Number(e.target.value) || 5)}
                              className="w-full rounded-md border border-input bg-background px-3 py-2 text-sm"
                            />
                          </div>
                        </div>
                        {perplexityOptions.length === 0 && !setupLoading && (
                          <p className="text-xs text-muted-foreground">
                            No configured model providers were found. Add a provider such as SAP AI Core first.
                          </p>
                        )}
                      </div>
                    ) : setupSrv ? (
                      <div className="space-y-3">
                        {setupSrv.envVars.map(ev => (
                          <div key={ev.name} className="space-y-1.5">
                            <label className="text-xs font-medium text-foreground">{ev.name}</label>
                            <input
                              type="password"
                              placeholder={ev.description || ev.name}
                              value={setupEnv[ev.name] || ''}
                              onChange={(e) => setSetupEnv({ ...setupEnv, [ev.name]: e.target.value })}
                              className="w-full rounded-md border border-input bg-background px-3 py-2 text-sm"
                            />
                          </div>
                        ))}
                      </div>
                    ) : null}

                    {setupError && (
                      <p className="text-xs text-destructive">{setupError}</p>
                    )}

                    <DialogFooter>
                      <Button variant="outline" onClick={closeSetupDialog} disabled={setupLoading}>
                        Cancel
                      </Button>
                      {setupSrv?.id === 'perplexity' ? (
                        <Button
                          onClick={() => setupSrv && handleSavePerplexity(setupSrv)}
                          disabled={setupLoading || !perplexityProvider || !perplexityModel}
                        >
                          {setupLoading ? <Loader2 size={14} className="animate-spin" /> : <Download size={14} />}
                          Save & activate
                        </Button>
                      ) : (
                        <Button
                          onClick={() => setupSrv && handleInstallMcpStandardServer(setupSrv)}
                          disabled={
                            setupLoading ||
                            !setupSrv ||
                            setupSrv.envVars.some(ev => ev.required && !setupEnv[ev.name])
                          }
                        >
                          {setupLoading ? <Loader2 size={14} className="animate-spin" /> : <Download size={14} />}
                          {setupSrv?.installed ? 'Save & activate' : 'Install & activate'}
                        </Button>
                      )}
                    </DialogFooter>
                  </DialogContent>
                </Dialog>
              </div>
            )}

            <div className="flex items-center gap-3">
              <button
                onClick={() => setShowMCPStore(true)}
                className="flex items-center gap-2 px-4 py-2 rounded-lg font-medium transition-all shadow-md hover:shadow-lg hover:scale-[1.02] active:scale-95"
                style={{ background: 'var(--brand)', color: '#fff' }}
              >
                <Package size={16} />
                Browse Store
              </button>
              <button
                onClick={handleAddMcpServer}
                className="flex items-center gap-2 px-4 py-2 rounded-lg border font-medium transition-colors"
                style={{ borderColor: 'var(--border-color)', color: 'var(--text-secondary)', background: 'var(--bg-tertiary)' }}
              >
                <Plus size={16} />
                Add Manual
              </button>
            </div>

            {/* Grid of MCP Server Cards (excludes standard web servers) */}
            <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
              {Object.entries(mcpServers)
                .filter(([name]) => !standardServers.some(s => s.id === name))
                .map(([name, server]) => {
                const isExpanded = expandedMcpServer === name
                const displayName = mcpServerNames[name] || name
                const isSaving = savingServer === name
                const serverStatus = mcpServerStatus[displayName]
                const hasError = serverStatus?.status === 'error'
                const isEnabled = server.enabled !== false
                
                return (
                  <div
                    key={name}
                    className={`rounded-lg border cursor-pointer transition-all ${
                      isExpanded ? 'border-primary ring-1 ring-primary/30 md:col-span-2' : 
                      hasError ? 'border-red-500/50' : 'hover:border-primary/50'
                    }`}
                    style={{ 
                      background: 'var(--bg-secondary)', 
                      borderColor: isExpanded ? undefined : 'var(--border-color)' 
                    }}
                  >
                    {/* Card Header - Always Visible */}
                    <div 
                      className="p-4"
                      onClick={() => setExpandedMcpServer(isExpanded ? null : name)}
                    >
                      {/* Top row: icon, title, actions */}
                      <div className="flex items-center gap-3">
                        {/* Server Icon with Status Indicator */}
                        <div className="relative shrink-0">
                          <div 
                            className="w-10 h-10 rounded-lg flex items-center justify-center"
                            style={{ 
                              background: hasError 
                                ? 'linear-gradient(135deg, rgba(239, 68, 68, 0.2) 0%, rgba(220, 38, 38, 0.2) 100%)'
                                : !isEnabled
                                  ? 'rgba(107, 114, 128, 0.15)'
                                  : 'linear-gradient(135deg, var(--brand-muted) 0%, var(--brand-muted) 100%)',
                              border: hasError 
                                ? '1px solid rgba(239, 68, 68, 0.3)'
                                : !isEnabled
                                  ? '1px solid var(--border-color)'
                                  : '1px solid color-mix(in oklab, var(--brand) 35%, transparent)'
                            }}
                          >
                            <Server size={18} style={{ color: hasError ? '#ef4444' : !isEnabled ? 'var(--text-muted)' : 'var(--brand)' }} />
                          </div>
                          {/* Status dot */}
                          {serverStatus && isEnabled && (
                            <div 
                              className="absolute -top-1 -right-1 w-3 h-3 rounded-full border-2"
                              style={{ 
                                background: serverStatus.status === 'healthy' ? '#22c55e' : 
                                            serverStatus.status === 'error' ? '#ef4444' : '#f59e0b',
                                borderColor: 'var(--bg-secondary)'
                              }}
                              title={serverStatus.status === 'healthy' 
                                ? `Healthy - ${serverStatus.tool_count} tools` 
                                : serverStatus.error || 'Unknown status'}
                            />
                          )}
                        </div>
                        
                        {/* Title */}
                        <div className="flex-1 min-w-0">
                          <h3 className="font-semibold text-base truncate" style={{ color: isEnabled ? 'var(--text-primary)' : 'var(--text-muted)' }}>
                            {displayName}
                          </h3>
                        </div>

                        {/* Actions row - vertically centered */}
                        <div className="flex items-center gap-2 shrink-0">
                          {/* Test button */}
                          <button
                            onClick={(e) => {
                              e.stopPropagation()
                              setInspectServer(displayName)
                            }}
                            className="flex items-center gap-1.5 px-2.5 py-1.5 rounded-lg text-xs font-medium transition-all hover:scale-[1.02]"
                            style={{ 
                              background: 'linear-gradient(135deg, var(--brand-muted) 0%, var(--brand-muted) 100%)',
                              color: 'var(--brand)',
                              border: '1px solid color-mix(in oklab, var(--brand) 35%, transparent)',
                              opacity: isEnabled ? 1 : 0.4
                            }}
                            title="Test tools from this server"
                            disabled={!isEnabled}
                          >
                            <Play size={12} />
                            Test
                          </button>

                          {/* Enable/Disable Toggle */}
                          <button
                            onClick={(e) => {
                              e.stopPropagation()
                              handleToggleMcpServer(name, displayName, isEnabled)
                            }}
                            className="relative inline-flex h-5 w-9 items-center rounded-full transition-colors focus:outline-none"
                            style={{ 
                              background: isEnabled 
                                ? 'var(--brand)' 
                                : 'var(--bg-tertiary)',
                              border: isEnabled ? 'none' : '1px solid var(--border-color)'
                            }}
                            title={isEnabled ? 'Disable server' : 'Enable server'}
                          >
                            <span
                              className={`inline-block h-3.5 w-3.5 transform rounded-full bg-white shadow transition-transform ${
                                isEnabled ? 'translate-x-[18px]' : 'translate-x-[3px]'
                              }`}
                            />
                          </button>

                          <ChevronRight 
                            size={18} 
                            className={`transition-transform ${isExpanded ? 'rotate-90' : ''}`}
                            style={{ color: 'var(--text-muted)' }}
                          />
                        </div>
                      </div>
                      
                      {/* Details row below title */}
                      <div className="mt-2 ml-[52px]" style={{ opacity: isEnabled ? 1 : 0.5 }}>
                        {/* Command/URL line */}
                        <div className="flex items-center gap-2">
                          <code 
                            className="text-xs font-mono px-2 py-1 rounded truncate max-w-[200px]"
                            style={{ 
                              background: 'var(--bg-primary)', 
                              color: 'var(--text-secondary)',
                              border: '1px solid var(--border-color)'
                            }}
                          >
                            {(server.transport || 'stdio') === 'stdio' 
                              ? server.command || 'no command' 
                              : server.url || 'no url'}
                          </code>
                          
                          {/* Transport Badge */}
                          <span 
                            className="shrink-0 text-xs font-medium px-2 py-1 rounded flex items-center gap-1"
                            style={{ 
                              background: (server.transport || 'stdio') === 'stdio' 
                                ? 'rgba(34, 197, 94, 0.15)' 
                                : 'rgba(59, 130, 246, 0.15)',
                              color: (server.transport || 'stdio') === 'stdio' 
                                ? '#22c55e' 
                                : '#3b82f6',
                              border: `1px solid ${(server.transport || 'stdio') === 'stdio' ? 'rgba(34, 197, 94, 0.3)' : 'rgba(59, 130, 246, 0.3)'}`
                            }}
                          >
                            {server.transport || 'stdio'}
                          </span>
                        </div>
                        
                        {/* Environment Variables - show as subtle tags */}
                        {server.env && Object.keys(server.env).length > 0 && !isExpanded && (
                          <div className="flex items-center gap-1.5 mt-2">
                            <Key size={12} style={{ color: 'var(--text-muted)' }} />
                            <div className="flex flex-wrap gap-1">
                              {Object.keys(server.env).slice(0, 2).map(key => (
                                <span 
                                  key={key}
                                  className="text-xs px-1.5 py-0.5 rounded"
                                  style={{ 
                                    background: 'var(--brand-muted)', 
                                    color: 'var(--text-muted)',
                                    border: '1px solid var(--brand-muted)'
                                  }}
                                >
                                  {key}
                                </span>
                              ))}
                              {Object.keys(server.env).length > 2 && (
                                <span 
                                  className="text-xs px-1.5 py-0.5 rounded"
                                  style={{ color: 'var(--text-muted)' }}
                                >
                                  +{Object.keys(server.env).length - 2} more
                                </span>
                              )}
                            </div>
                          </div>
                        )}

                        {/* Error message display */}
                        {hasError && !isExpanded && (
                          <div 
                            className="flex items-start gap-2 mt-2 p-2 rounded text-xs"
                            style={{ 
                              background: 'rgba(239, 68, 68, 0.1)', 
                              border: '1px solid rgba(239, 68, 68, 0.2)',
                              color: '#f87171'
                            }}
                          >
                            <AlertCircle size={14} className="shrink-0 mt-0.5" />
                            <div className="flex-1">
                              <div className="font-medium">Failed to load</div>
                              <div className="opacity-80 mt-0.5">{serverStatus.error}</div>
                            </div>
                            {serverStatus?.status === 'loading' ? (
                              <Loader2 size={14} className="animate-spin shrink-0 mt-0.5 opacity-50" />
                            ) : (
                              <button 
                                onClick={(e) => { e.stopPropagation(); handleRefreshMcpServer(displayName) }}
                                className="p-1 hover:bg-white/10 rounded transition-colors"
                                title="Retry"
                              >
                                <RefreshCw size={14} />
                              </button>
                            )}
                          </div>
                        )}
                      </div>
                    </div>
                    
                    {/* Expanded Form */}
                    {isExpanded && (
                      <div className="px-4 pb-4 pt-0 border-t" style={{ borderColor: 'var(--border-color)' }}>
                        <div className="pt-4 space-y-4">
                          {/* Server Name */}
                          <div>
                            <label className="block text-sm mb-1" style={{ color: 'var(--text-muted)' }}>Server Name</label>
                            <input
                              type="text"
                              value={displayName}
                              onChange={(e) => setMcpServerNames({ ...mcpServerNames, [name]: e.target.value })}
                              onClick={(e) => e.stopPropagation()}
                              className="w-full px-3 py-2 rounded border text-sm"
                              style={{ background: 'var(--bg-primary)', borderColor: 'var(--border-color)', color: 'var(--text-primary)' }}
                              placeholder="Server name"
                            />
                          </div>
                          
                          {/* Transport & Command/URL */}
                          <div className="grid grid-cols-2 gap-4">
                            <div>
                              <label className="block text-sm mb-1" style={{ color: 'var(--text-muted)' }}>Transport</label>
                              <select
                                value={server.transport || 'stdio'}
                                onChange={(e) => {
                                  e.stopPropagation()
                                  setMcpServers({
                                    ...mcpServers,
                                    [name]: { ...server, transport: e.target.value }
                                  })
                                }}
                                onClick={(e) => e.stopPropagation()}
                                className="w-full px-3 py-2 rounded border text-sm"
                                style={{ background: 'var(--bg-primary)', borderColor: 'var(--border-color)', color: 'var(--text-primary)' }}
                              >
                                <option value="stdio">stdio</option>
                                <option value="sse">sse</option>
                              </select>
                            </div>
                            {(server.transport || 'stdio') === 'stdio' ? (
                              <div>
                                <label className="block text-sm mb-1" style={{ color: 'var(--text-muted)' }}>Command</label>
                                <input
                                  type="text"
                                  value={server.command || ''}
                                  onChange={(e) => {
                                    e.stopPropagation()
                                    setMcpServers({
                                      ...mcpServers,
                                      [name]: { ...server, command: e.target.value }
                                    })
                                  }}
                                  onClick={(e) => e.stopPropagation()}
                                  placeholder="e.g., npx"
                                  className="w-full px-3 py-2 rounded border text-sm font-mono"
                                  style={{ background: 'var(--bg-primary)', borderColor: 'var(--border-color)', color: 'var(--text-primary)' }}
                                />
                              </div>
                            ) : (
                              <div>
                                <label className="block text-sm mb-1" style={{ color: 'var(--text-muted)' }}>URL</label>
                                <input
                                  type="text"
                                  value={server.url || ''}
                                  onChange={(e) => {
                                    e.stopPropagation()
                                    setMcpServers({
                                      ...mcpServers,
                                      [name]: { ...server, url: e.target.value }
                                    })
                                  }}
                                  onClick={(e) => e.stopPropagation()}
                                  placeholder="e.g., http://localhost:8080/sse"
                                  className="w-full px-3 py-2 rounded border text-sm font-mono"
                                  style={{ background: 'var(--bg-primary)', borderColor: 'var(--border-color)', color: 'var(--text-primary)' }}
                                />
                              </div>
                            )}
                          </div>
                          
                          {/* Args (for stdio only) */}
                          {(server.transport || 'stdio') === 'stdio' && (
                            <div>
                              <label className="block text-sm mb-1" style={{ color: 'var(--text-muted)' }}>Args (comma-separated)</label>
                              <input
                                type="text"
                                value={mcpServerArgs[name] !== undefined ? mcpServerArgs[name] : (server.args || []).join(', ')}
                                onChange={(e) => {
                                  e.stopPropagation()
                                  setMcpServerArgs({ ...mcpServerArgs, [name]: e.target.value })
                                }}
                                onClick={(e) => e.stopPropagation()}
                                placeholder="e.g., -y, @anthropic-ai/mcp-server-github"
                                className="w-full px-3 py-2 rounded border text-sm font-mono"
                                style={{ background: 'var(--bg-primary)', borderColor: 'var(--border-color)', color: 'var(--text-primary)' }}
                              />
                            </div>
                          )}
                          
                          {/* Environment Variables */}
                          <div>
                            <label className="block text-sm mb-1" style={{ color: 'var(--text-muted)' }}>Environment (JSON)</label>
                            <textarea
                              value={Object.keys(server.env || {}).length > 0 ? JSON.stringify(server.env, null, 2) : ''}
                              onChange={(e) => {
                                e.stopPropagation()
                                try {
                                  const env = e.target.value ? JSON.parse(e.target.value) : {}
                                  setMcpServers({
                                    ...mcpServers,
                                    [name]: { ...server, env }
                                  })
                                } catch { /* JSON parse validation — ignore malformed input */ }
                              }}
                              onClick={(e) => e.stopPropagation()}
                              placeholder={'{\n  "KEY": "value"\n}'}
                              rows={4}
                              className="w-full px-3 py-2 rounded border text-sm font-mono resize-y"
                              style={{ background: 'var(--bg-primary)', borderColor: 'var(--border-color)', color: 'var(--text-primary)' }}
                            />
                          </div>
                          
                          {/* Action Buttons */}
                          <div className="flex items-center justify-between pt-2">
                            <button
                              onClick={(e) => {
                                e.stopPropagation()
                                handleDeleteMcpServer(name)
                              }}
                              className="flex items-center gap-2 px-3 py-1.5 rounded text-sm text-red-400 hover:text-red-300 hover:bg-red-500/20 transition-colors"
                            >
                              <Trash2 size={14} />
                              Delete
                            </button>
                            <button
                              onClick={(e) => {
                                e.stopPropagation()
                                handleSaveSingleMcpServer(name)
                              }}
                              disabled={isSaving}
                              className="flex items-center gap-2 px-4 py-2 rounded-lg font-medium transition-all shadow-md hover:shadow-lg hover:scale-[1.02] active:scale-95 disabled:opacity-50"
                              style={{ background: 'var(--brand)', color: '#fff' }}
                            >
                              {isSaving ? (
                                <>
                                  <Loader2 size={14} className="animate-spin" />
                                  Saving...
                                </>
                              ) : (
                                <>
                                  <Save size={14} />
                                  Save
                                </>
                              )}
                            </button>
                          </div>
                        </div>
                      </div>
                    )}
                  </div>
                )
              })}
            </div>

            {Object.keys(mcpServers).length === 0 && (
              <div className="text-center py-8" style={{ color: 'var(--text-muted)' }}>
                <Server size={48} className="mx-auto mb-3 opacity-30" />
                <p>No MCP servers configured.</p>
                <p className="text-sm mt-1">Click "Browse Store" or "Add Manual" to add one.</p>
              </div>
            )}
          </>
        )}

        {/* Source View */}
        {mcpViewMode === 'source' && (
          <div className="flex flex-col h-full">
            <div className="flex items-center justify-between px-6 pt-4 mb-4 flex-shrink-0">
              <p className="text-sm" style={{ color: 'var(--text-muted)' }}>
                Edit the raw JSON configuration below. Changes will be synced when you save or switch back to Editor view.
              </p>
              {mcpSourceError && (
                <div className="flex items-center gap-2 px-3 py-1.5 rounded-lg" style={{ background: 'rgba(239, 68, 68, 0.1)', color: 'var(--danger)' }}>
                  <AlertCircle size={14} />
                  <span className="text-sm">{mcpSourceError}</span>
                </div>
              )}
            </div>
            <div className="flex-1 overflow-hidden mx-6 mb-4" style={{ maxHeight: 'calc(100vh - 220px)' }}>
              <div className="h-full rounded-lg border" style={{ borderColor: 'var(--border-color)' }}>
                <CodeMirror
                  value={mcpSourceText}
                  onChange={(value) => {
                    setMcpSourceText(value)
                    try {
                      JSON.parse(value)
                      setMcpSourceError(null)
                    } catch { /* validation only */ }
                  }}
                  height="100%"
                  className="h-full"
                  extensions={[
                    json(),
                    search({ scrollToMatch: (range) => EditorView.scrollIntoView(range, { y: 'center', yMargin: 100 }) }),
                    highlightSelectionMatches(),
                    keymap.of(searchKeymap),
                  ]}
                  theme={theme === 'dark' ? 'dark' : 'light'}
                  basicSetup={{
                    lineNumbers: true,
                    highlightActiveLineGutter: true,
                    highlightActiveLine: true,
                    foldGutter: true,
                  }}
                />
              </div>
            </div>
            <div className="flex items-center justify-end gap-3 px-6 pb-6 flex-shrink-0">
              <button
                onClick={async () => {
                  try {
                    const parsed = JSON.parse(mcpSourceText)
                    if (parsed.mcpServers && typeof parsed.mcpServers === 'object') {
                      setSaving(true)
                      await saveMCPConfig({ mcpServers: parsed.mcpServers }, effectiveTeamSlug, effectiveScope)
                      setMcpServers(parsed.mcpServers)
                      const names: Record<string, string> = {}
                      const args: Record<string, string> = {}
                      Object.entries(parsed.mcpServers).forEach(([name, server]: [string, any]) => {
                        names[name] = name
                        args[name] = Array.isArray(server.args) ? server.args.join(', ') : ''
                      })
                      setMcpServerNames(names)
                      setMcpServerArgs(args)
                      setMcpSourceError(null)
                      setMcpHasChanges(false)
                      setSaveSuccess(true)
                      if (onToolsRefresh) onToolsRefresh()
                      setTimeout(() => setSaveSuccess(false), 2000)
                      setSaving(false)
                    } else {
                      setMcpSourceError('Invalid format: expected { "mcpServers": { ... } }')
                    }
                  } catch (e: any) {
                    setMcpSourceError(`Invalid JSON: ${e.message}`)
                    setSaving(false)
                  }
                }}
                disabled={saving}
                className="flex items-center gap-2 px-4 py-2 rounded-lg font-medium transition-all shadow-md hover:shadow-lg hover:scale-[1.02] active:scale-95 disabled:opacity-50"
                style={{ background: 'var(--brand)', color: '#fff' }}
              >
                <Save size={16} />
                {saving ? 'Saving...' : 'Apply & Save'}
              </button>
            </div>
          </div>
        )}
      </div>

      {/* MCP Store Modal */}
      <MCPStoreModal
        isOpen={showMCPStore}
        onClose={() => setShowMCPStore(false)}
        onInstall={() => {
          setShowMCPStore(false)
          loadData()
          loadMcpServerStatus()
          if (onToolsRefresh) onToolsRefresh()
        }}
        teamSlug={effectiveTeamSlug}
        scope={effectiveScope}
      />

      {/* MCP Inspector Modal */}
      {inspectServer && (
        <MCPInspector
          serverName={inspectServer}
          teamSlug={effectiveTeamSlug}
          scope={effectiveScope}
          onClose={() => setInspectServer(null)}
        />
      )}
    </>
  )
}
