import { useState, useEffect, useMemo } from 'react'
import { Search, Download, Check, AlertCircle, Loader2, Package, RefreshCw, X, Tag, Trash2 } from 'lucide-react'
import { teamFetch } from '../api/teamContext'

// --- Types ---

interface Tap {
  name: string
  [key: string]: unknown
}

interface Flow {
  name: string
  fullName: string
  tapName: string
  description: string
  tags?: string[]
  installed: boolean
  [key: string]: unknown
}

interface FlowStoreData {
  taps: Tap[]
  flows: Flow[]
}

// API functions for Flow Store
const fetchFlowStore = async (teamSlug?: string): Promise<FlowStoreData> => {
  const res = await teamFetch('/api/flow-store', undefined, teamSlug)
  if (!res.ok) throw new Error('Failed to fetch flow store')
  return res.json()
}


const installFlow = async (tapName: string, flowName: string, teamSlug?: string): Promise<Record<string, unknown>> => {
  const res = await teamFetch(`/api/flow-store/${encodeURIComponent(tapName)}/${encodeURIComponent(flowName)}/install`, {
    method: 'POST'
  }, teamSlug)
  if (!res.ok) {
    const errorText = await res.text()
    throw new Error(errorText || 'Failed to install flow')
  }
  return res.json()
}

const uninstallFlow = async (tapName: string, flowName: string, teamSlug?: string): Promise<Record<string, unknown>> => {
  const res = await teamFetch(`/api/flow-store/${encodeURIComponent(tapName)}/${encodeURIComponent(flowName)}`, {
    method: 'DELETE'
  }, teamSlug)
  if (!res.ok) {
    const errorText = await res.text()
    throw new Error(errorText || 'Failed to uninstall flow')
  }
  return res.json()
}

const updateStore = async (teamSlug?: string): Promise<Record<string, unknown>> => {
  const res = await teamFetch('/api/flow-store/update', { method: 'POST' }, teamSlug)
  if (!res.ok) throw new Error('Failed to update store')
  return res.json()
}

export default function FlowStorePanel({ teamSlug, canManage = true }: { teamSlug?: string; canManage?: boolean }) {
  const [taps, setTaps] = useState<Tap[]>([])
  const [flows, setFlows] = useState<Flow[]>([])
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [searchQuery, setSearchQuery] = useState('')
  const [installing, setInstalling] = useState<string | null>(null)
  const [installSuccess, setInstallSuccess] = useState<string | null>(null)
  const [selectedTags, setSelectedTags] = useState<string[]>([])
  const [selectedRepo, setSelectedRepo] = useState('all') // 'all' or specific tap name

  // Collect all unique tags from flows
  const allTags = useMemo(() => {
    const tagSet = new Set<string>()
    flows.forEach(f => {
      if (f.tags) {
        f.tags.forEach(t => tagSet.add(t.toLowerCase()))
      }
    })
    return Array.from(tagSet).sort()
  }, [flows])

  const loadStore = async () => {
    setLoading(true)
    setError(null)
    try {
      const data = await fetchFlowStore(teamSlug)
      setTaps(data.taps || [])
      setFlows(data.flows || [])
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Unknown error')
    } finally {
      setLoading(false)
    }
  }

  useEffect(() => {
    loadStore()
  }, [teamSlug]) // eslint-disable-line react-hooks/exhaustive-deps

  const handleInstall = async (flow: Flow) => {
    setInstalling(flow.fullName)
    setError(null)
    try {
      await installFlow(flow.tapName, flow.name, teamSlug)
      setInstallSuccess(flow.fullName)
      await loadStore()
      setTimeout(() => setInstallSuccess(null), 3000)
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Unknown error')
    } finally {
      setInstalling(null)
    }
  }

  const handleUninstall = async (flow: Flow) => {
    setInstalling(flow.fullName)
    setError(null)
    try {
      await uninstallFlow(flow.tapName, flow.name, teamSlug)
      await loadStore()
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Unknown error')
    } finally {
      setInstalling(null)
    }
  }

  const handleRefresh = async () => {
    setLoading(true)
    setError(null)
    try {
      await updateStore(teamSlug)
      await loadStore()
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Unknown error')
    } finally {
      setLoading(false)
    }
  }

  // Filter flows by search, selected tags, and selected repo
  const filteredFlows = flows.filter(f => {
    // Repo filter
    const matchesRepo = selectedRepo === 'all' || f.tapName === selectedRepo
    
    // Search filter
    const matchesSearch = !searchQuery || 
      f.name.toLowerCase().includes(searchQuery.toLowerCase()) ||
      f.description.toLowerCase().includes(searchQuery.toLowerCase()) ||
      (f.tags && f.tags.some(t => t.toLowerCase().includes(searchQuery.toLowerCase())))
    
    // Tag filter (OR logic)
    const matchesTags = selectedTags.length === 0 || 
      (f.tags && f.tags.some(t => selectedTags.includes(t.toLowerCase())))
    
    return matchesRepo && matchesSearch && matchesTags
  })

  const toggleTag = (tag: string) => {
    setSelectedTags(prev => 
      prev.includes(tag) 
        ? prev.filter(t => t !== tag)
        : [...prev, tag]
    )
  }

  return (
    <div className="h-full flex flex-col">
      {/* Header */}
      <div className="flex items-center gap-4 mb-4">
        <div className="flex items-center gap-2">
          <Package size={18} style={{ color: 'var(--text-secondary)' }} />
          <span className="font-medium" style={{ color: 'var(--text-primary)' }}>Browse Flows</span>
          <span className="text-xs px-2 py-0.5 rounded" style={{ background: 'var(--bg-tertiary)', color: 'var(--text-muted)' }}>
            {flows.length} flows from {taps.length} repos
          </span>
        </div>
        
        {canManage && (
        <button
          onClick={handleRefresh}
          disabled={loading}
          className="p-2 rounded-lg hover:bg-white/10 transition-colors ml-auto"
          style={{ color: 'var(--text-secondary)' }}
          title="Refresh stores"
        >
          <RefreshCw size={18} className={loading ? 'animate-spin' : ''} />
        </button>
        )}
      </div>

      {/* Error Banner */}
      {error && (
        <div className="mb-4 px-4 py-2 rounded-lg bg-red-500/20 border border-red-500/50 flex items-center gap-2">
          <AlertCircle size={16} className="text-red-400" />
          <span className="text-red-300 text-sm flex-1">{error}</span>
          <button onClick={() => setError(null)} className="text-red-400 hover:text-red-300">×</button>
        </div>
      )}

      {/* Content */}
      <div className="flex-1 overflow-hidden">
        <div className="h-full flex flex-col">
            {/* Search and Repo Filter */}
            <div className="mb-4 flex gap-3">
              {/* Repo Filter Dropdown */}
              <select
                value={selectedRepo}
                onChange={(e: React.ChangeEvent<HTMLSelectElement>) => setSelectedRepo(e.target.value)}
                className="px-3 py-2 rounded-lg border bg-transparent outline-none focus:border-blue-500 transition-colors"
                style={{ borderColor: 'var(--border-color)', color: 'var(--text-primary)', background: 'var(--bg-primary)' }}
              >
                <option value="all">All Repositories</option>
                {taps.map(tap => (
                  <option key={tap.name} value={tap.name}>
                    {tap.name} ({flows.filter(f => f.tapName === tap.name).length})
                  </option>
                ))}
              </select>
              
              {/* Search */}
              <div className="relative flex-1">
                <Search size={18} className="absolute left-3 top-1/2 -translate-y-1/2 text-gray-400" />
                <input
                  type="text"
                  placeholder="Search flows..."
                  value={searchQuery}
                  onChange={(e: React.ChangeEvent<HTMLInputElement>) => setSearchQuery(e.target.value)}
                  className="w-full pl-10 pr-4 py-2 rounded-lg border bg-transparent outline-none focus:border-blue-500 transition-colors"
                  style={{ borderColor: 'var(--border-color)', color: 'var(--text-primary)' }}
                />
              </div>
            </div>

            {/* Tag Filters */}
            {allTags.length > 0 && (
              <div className="mb-4">
                <div className="flex items-center gap-2 mb-2">
                  <Tag size={14} style={{ color: 'var(--text-muted)' }} />
                  <span className="text-xs font-medium" style={{ color: 'var(--text-muted)' }}>
                    Filter by tag:
                  </span>
                  {selectedTags.length > 0 && (
                    <button
                      onClick={() => setSelectedTags([])}
                      className="text-xs px-2 py-0.5 rounded hover:bg-gray-600/20 transition-colors"
                      style={{ color: 'var(--text-secondary)' }}
                    >
                      Clear all
                    </button>
                  )}
                </div>
                <div className="flex flex-wrap gap-2">
                  {allTags.map(tag => {
                    const isSelected = selectedTags.includes(tag)
                    return (
                      <button
                        key={tag}
                        onClick={() => toggleTag(tag)}
                        className={`flex items-center gap-1.5 px-2.5 py-1 rounded-full text-xs font-medium transition-all ${
                          isSelected
                            ? 'bg-blue-500 text-white'
                            : 'bg-gray-500/20 hover:bg-gray-500/30'
                        }`}
                        style={!isSelected ? { color: 'var(--text-secondary)' } : undefined}
                      >
                        {tag}
                        {isSelected && <X size={12} />}
                      </button>
                    )
                  })}
                </div>
              </div>
            )}

            {/* Flow List */}
            <div className="flex-1 overflow-y-auto">
              {loading ? (
                <div className="flex items-center justify-center h-32">
                  <Loader2 size={24} className="animate-spin text-blue-400" />
                </div>
              ) : filteredFlows.length === 0 ? (
                <div className="text-center py-8" style={{ color: 'var(--text-muted)' }}>
                  {searchQuery ? 'No flows match your search' : 'No flows available'}
                </div>
              ) : (
                <div className="grid gap-3">
                  {filteredFlows.map(flow => (
                    <div
                      key={flow.fullName}
                      className="p-4 rounded-lg border transition-colors hover:border-blue-500/50"
                      style={{ borderColor: 'var(--border-color)', background: 'var(--bg-secondary)' }}
                    >
                      <div className="flex items-start justify-between gap-4">
                        <div className="flex-1 min-w-0">
                          <div className="flex items-center gap-2 flex-wrap">
                            <h3 className="font-medium" style={{ color: 'var(--text-primary)' }}>
                              {flow.fullName}
                            </h3>
                            {flow.tapName === 'official' && (
                              <span className="text-xs px-1.5 py-0.5 rounded bg-blue-500/20 text-blue-400">
                                official
                              </span>
                            )}
                            {flow.installed && (
                              <span className="text-xs px-1.5 py-0.5 rounded bg-green-500/20 text-green-400">
                                installed
                              </span>
                            )}
                          </div>
                          <p className="text-sm mt-1 line-clamp-2" style={{ color: 'var(--text-secondary)' }}>
                            {flow.description}
                          </p>
                          {/* Tags */}
                          {flow.tags && flow.tags.length > 0 && (
                            <div className="flex flex-wrap gap-1.5 mt-2">
                              {flow.tags.map(tag => (
                                <button
                                  key={tag}
                                  onClick={() => toggleTag(tag.toLowerCase())}
                                  className={`text-xs px-2 py-0.5 rounded-full transition-colors ${
                                    selectedTags.includes(tag.toLowerCase())
                                      ? 'bg-blue-500/30 text-blue-400'
                                      : 'bg-gray-500/20 hover:bg-gray-500/30'
                                  }`}
                                  style={!selectedTags.includes(tag.toLowerCase()) ? { color: 'var(--text-muted)' } : undefined}
                                  title={`Filter by "${tag}"`}
                                >
                                  {tag}
                                </button>
                              ))}
                            </div>
                          )}
                        </div>
                        <div className="flex items-center gap-2 flex-shrink-0">
                          {canManage && (flow.installed ? (
                            <button
                              onClick={() => handleUninstall(flow)}
                              disabled={installing === flow.fullName}
                              className="px-3 py-1.5 rounded-lg text-sm font-medium transition-colors bg-red-500/20 text-red-400 hover:bg-red-500/30"
                            >
                              {installing === flow.fullName ? (
                                <Loader2 size={14} className="animate-spin" />
                              ) : (
                                <>
                                  <Trash2 size={14} className="inline mr-1" />
                                  Uninstall
                                </>
                              )}
                            </button>
                          ) : (
                            <button
                              onClick={() => handleInstall(flow)}
                              disabled={installing === flow.fullName}
                              className="px-3 py-1.5 rounded-lg text-sm font-medium transition-all shadow-md hover:shadow-lg hover:scale-[1.02] active:scale-95 text-white disabled:opacity-50"
                              style={{ background: 'linear-gradient(135deg, var(--brand) 0%, var(--brand-strong) 100%)' }}
                            >
                              {installing === flow.fullName ? (
                                <Loader2 size={14} className="animate-spin" />
                              ) : installSuccess === flow.fullName ? (
                                <>
                                  <Check size={14} className="inline mr-1" />
                                  Installed
                                </>
                              ) : (
                                <>
                                  <Download size={14} className="inline mr-1" />
                                  Install
                                </>
                              )}
                            </button>
                          ))}
                          {!canManage && flow.installed && (
                            <span className="px-3 py-1.5 rounded-lg text-sm font-medium bg-green-500/20 text-green-400">
                              Installed
                            </span>
                          )}
                        </div>
                      </div>
                    </div>
                  ))}
                </div>
              )}
            </div>
          </div>
      </div>
    </div>
  )
}
