import { useState } from 'react'
import { Key, Server, ChevronRight, Save, Plus, Trash2, Check, AlertCircle, Code, LayoutGrid, Loader2, Package, Search, Play, Download, RefreshCw } from 'lucide-react'
import MCPStoreModal from '../MCPStoreModal'
import MCPInspector from '../MCPInspector'
import CodeMirror from '@uiw/react-codemirror'
import { json } from '@codemirror/lang-json'
import { search, searchKeymap, highlightSelectionMatches } from '@codemirror/search'
import { keymap, EditorView } from '@codemirror/view'
import { saveMCPConfig, refreshMCPServer, toggleMCPServer, fetchMCPStatus } from './settingsApi'
import type { MCPServerConfig, MCPServerStatusEntry, StandardServer } from './settingsApi'
import { teamFetch } from '../../api/teamContext'

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
              <div className="mb-4 p-4 rounded-xl border" style={{ borderColor: 'var(--border-color)', background: 'var(--card)' }}>
                <h4 className="text-sm font-medium mb-2 flex items-center gap-2" style={{ color: 'var(--text-primary)' }}>
                  <Search size={14} style={{ color: 'var(--brand)' }} />
                  Web Search Providers
                </h4>
                <p className="text-xs mb-3" style={{ color: 'var(--text-muted)' }}>
                  Configure a web search provider to enable web search and content extraction.
                </p>
                <div className="grid grid-cols-1 md:grid-cols-3 gap-3">
                  {standardServers.map(srv => (
                    <div key={srv.id} className="p-3 rounded-lg border transition-all" style={{ 
                      borderColor: srv.installed ? 'color-mix(in oklab, var(--success, #22c55e) 40%, transparent)' : 'var(--border-color)',
                      background: srv.installed
                        ? 'color-mix(in oklab, var(--success, #22c55e) 8%, var(--card))'
                        : 'var(--bg-secondary)',
                    }}>
                      <div className="flex items-center justify-between mb-1">
                        <span className="text-sm font-medium" style={{ color: 'var(--text-primary)' }}>
                          {srv.displayName}
                          {srv.isDefault && !srv.installed && (
                            <span className="ml-1 text-xs px-1.5 py-0.5 rounded-full" style={{ background: 'var(--brand-muted)', color: 'var(--brand)' }}>
                              recommended
                            </span>
                          )}
                        </span>
                        {srv.installed && <Check size={14} style={{ color: 'var(--success, #22c55e)' }} />}
                      </div>
                      <p className="text-xs mb-2" style={{ color: 'var(--text-muted)' }}>
                        {srv.envVars?.length === 0 ? 'Browser Automation' : srv.capabilities.webSearch && srv.capabilities.webExtract ? 'Search + Extract' : 'Search only'}
                      </p>
                      {srv.envVars?.length === 0 && srv.installed ? (
                        <div className="flex items-center gap-2">
                          <span className="text-xs" style={{ color: '#22c55e' }}>Active</span>
                          <span className="text-xs" style={{ color: 'var(--text-muted)' }}>No setup required</span>
                        </div>
                      ) : setupServer === srv.id ? (
                        <div className="space-y-2">
                          {srv.envVars.map(ev => (
                            <input
                              key={ev.name}
                              type="password"
                              placeholder={ev.name}
                              value={setupEnv[ev.name] || ''}
                              onChange={(e) => setSetupEnv({ ...setupEnv, [ev.name]: e.target.value })}
                              className="w-full px-2 py-1.5 rounded border text-xs"
                              style={{ background: 'var(--bg-primary)', borderColor: 'var(--border-color)', color: 'var(--text-primary)' }}
                            />
                          ))}
                          {setupError && (
                            <p className="text-xs" style={{ color: '#ef4444' }}>{setupError}</p>
                          )}
                          <div className="flex gap-2">
                            <button
                              onClick={async () => {
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
                                  setSetupServer(null)
                                  setSetupEnv({})
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
                              }}
                              disabled={setupLoading || srv.envVars.some(ev => ev.required && !setupEnv[ev.name])}
                              className="flex items-center gap-1 px-2 py-1 rounded text-xs font-medium text-white disabled:opacity-50"
                              style={{ background: 'var(--brand)' }}
                            >
                              {setupLoading ? <Loader2 size={12} className="animate-spin" /> : <Download size={12} />}
                              {srv.installed ? 'Reconfigure' : 'Install'}
                            </button>
                            <button
                              onClick={() => { setSetupServer(null); setSetupEnv({}); setSetupError(null) }}
                              className="px-2 py-1 rounded text-xs"
                              style={{ color: 'var(--text-muted)' }}
                            >
                              Cancel
                            </button>
                          </div>
                        </div>
                      ) : srv.installed ? (
                        <div className="flex items-center gap-2">
                          <span className="text-xs" style={{ color: '#22c55e' }}>Configured</span>
                          <button
                            onClick={() => { setSetupServer(srv.id); setSetupEnv({}); setSetupError(null) }}
                            className="text-xs px-1.5 py-0.5 rounded transition-colors"
                            style={{ color: 'var(--text-muted)' }}
                          >
                            Reconfigure
                          </button>
                          {srv.envVars?.length > 0 && (
                            <button
                              onClick={async () => {
                                try {
                                  const res = await teamFetch(`/api/standard-servers/${srv.id}${scopeQuery}`, { method: 'DELETE' }, effectiveTeamSlug)
                                  if (!res.ok) throw new Error('Failed to remove server')
                                  await loadData()
                                  if (onToolsRefresh) onToolsRefresh()
                                } catch (err) {
                                  console.error('Failed to remove standard server:', err)
                                }
                              }}
                              className="p-0.5 rounded transition-colors hover:bg-red-500/10"
                              style={{ color: 'var(--text-muted)' }}
                              title="Remove configuration"
                            >
                              <Trash2 size={12} />
                            </button>
                          )}
                        </div>
                      ) : (
                        <button
                          onClick={() => { setSetupServer(srv.id); setSetupEnv({}); setSetupError(null) }}
                          className="text-xs font-medium px-2 py-1 rounded transition-colors"
                          style={{ color: 'var(--brand)', background: 'var(--brand-muted)' }}
                        >
                          Setup
                        </button>
                      )}
                    </div>
                  ))}
                </div>
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
