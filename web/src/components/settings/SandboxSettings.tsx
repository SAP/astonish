import { useState, useEffect, useCallback } from 'react'
import { Save, AlertCircle, Check, Shield, ShieldOff, Loader2, Trash2, RefreshCw, Plus, Camera, ArrowUpCircle, Eye, ChevronDown, ChevronRight, Server, Box, Globe, X, ExternalLink, Pin } from 'lucide-react'
import { saveFullConfigSection, inputClass, inputStyle, labelStyle, hintStyle, sectionBorderStyle, saveButtonStyle } from './settingsApi'
import {
  fetchSandboxDetails, fetchContainers, deleteContainer, pruneOrphans,
  fetchTemplates, fetchTemplateInfo, createTemplate, deleteTemplate,
  snapshotTemplate, promoteTemplate, refreshTemplates,
  exposePort, unexposePort, pinContainer
} from '../../api/sandbox'

// --- Local types ---

interface SandboxConfig {
  enabled?: boolean
  memory?: string
  cpu?: number
  processes?: number
  [key: string]: unknown
}

interface SandboxForm {
  enabled: boolean
  memory: string
  cpu: number
  processes: number
}

interface SandboxDetailsData {
  backend?: string
  platform?: string
  runtimeAvailable?: boolean
  baseTemplateExists?: boolean
  incus_version?: string
  storage_backend?: string
  overlay_ready?: boolean
  template_count?: number
  container_count?: number
  orphan_count?: number
  reason?: string
  // K8s-specific
  server_version?: string
  namespace?: string
  overlay_mode?: string
  // Deprecated: use runtimeAvailable
  incusAvailable?: boolean
  [key: string]: unknown
}

interface ContainerItem {
  session_id: string
  name: string
  status: string
  template?: string
  pinned?: boolean
  exposed_ports?: number[]
  host_ports?: Record<string, string>
  proxy_hosts?: Record<string, string>
  created?: string
  [key: string]: unknown
}

interface ContainerData {
  containers?: ContainerItem[]
  orphans?: ContainerItem[]
  [key: string]: unknown
}

interface TemplateItem {
  name: string
  description?: string
  created?: string
  last_snapshot?: string
  fleet_plans?: string[]
  [key: string]: unknown
}

interface TemplateData {
  templates?: TemplateItem[]
  [key: string]: unknown
}

interface TemplateDetailData {
  container_name?: string
  container_status?: string
  snapshot_ready?: boolean
  based_on?: string
  binary_hash?: string
  [key: string]: unknown
}

// --- Badge components ---

interface StatusBadgeProps {
  status: string
}

function StatusBadge({ status }: StatusBadgeProps) {
  const colors: Record<string, { bg: string; color: string }> = {
    running: { bg: 'rgba(34, 197, 94, 0.15)', color: '#22c55e' },
    stopped: { bg: 'rgba(107, 114, 128, 0.15)', color: '#9ca3af' },
    missing: { bg: 'rgba(239, 68, 68, 0.15)', color: '#ef4444' },
  }
  const c = colors[status] || colors.stopped
  return (
    <span className="text-xs px-1.5 py-0.5 rounded inline-flex items-center gap-1"
      style={{ background: c.bg, color: c.color }}>
      <span className="w-1.5 h-1.5 rounded-full" style={{ background: c.color }} />
      {status}
    </span>
  )
}

interface TemplateBadgeProps {
  name: string
}

function TemplateBadge({ name }: TemplateBadgeProps) {
  const isBase = name === 'base'
  return (
    <span className="text-xs px-1.5 py-0.5 rounded"
      style={{
        background: isBase ? 'var(--brand-muted)' : 'rgba(59, 130, 246, 0.15)',
        color: isBase ? 'var(--brand)' : '#3b82f6'
      }}>
      @{name}
    </span>
  )
}

// --- Main component ---

interface SandboxSettingsProps {
  config: SandboxConfig | null
  onSaved?: () => void
}

export default function SandboxSettings({ config, onSaved }: SandboxSettingsProps) {
  // Config form (enable/disable)
  const [form, setForm] = useState<SandboxForm>({ enabled: true, memory: '2GB', cpu: 2, processes: 500 })
  const [saving, setSaving] = useState(false)
  const [saveSuccess, setSaveSuccess] = useState(false)
  const [saveError, setSaveError] = useState<string | null>(null)

  // Details / status
  const [details, setDetails] = useState<SandboxDetailsData | null>(null)
  const [detailsLoading, setDetailsLoading] = useState(true)

  // Containers
  const [containerData, setContainerData] = useState<ContainerData | null>(null)
  const [containersLoading, setContainersLoading] = useState(false)
  const [deleteConfirm, setDeleteConfirm] = useState<string | null>(null)
  const [pruneConfirm, setPruneConfirm] = useState(false)
  const [actionLoading, setActionLoading] = useState<string | null>(null)
  const [actionError, setActionError] = useState<string | null>(null)

  // Port exposure
  const [exposeContainer, setExposeContainer] = useState<string | null>(null)
  const [exposePortInput, setExposePortInput] = useState('')
  const [exposeLoading, setExposeLoading] = useState(false)

  // Templates
  const [templateData, setTemplateData] = useState<TemplateData | null>(null)
  const [templatesLoading, setTemplatesLoading] = useState(false)
  const [expandedTemplate, setExpandedTemplate] = useState<string | null>(null)
  const [templateDetail, setTemplateDetail] = useState<TemplateDetailData | null>(null)
  const [templateDetailLoading, setTemplateDetailLoading] = useState(false)
  const [templateDeleteConfirm, setTemplateDeleteConfirm] = useState<string | null>(null)
  const [promoteConfirm, setPromoteConfirm] = useState<string | null>(null)
  const [showCreateForm, setShowCreateForm] = useState(false)
  const [createName, setCreateName] = useState('')
  const [createDesc, setCreateDesc] = useState('')
  const [templateActionLoading, setTemplateActionLoading] = useState<string | null>(null)
  const [templateError, setTemplateError] = useState<string | null>(null)

  // Init form from config
  useEffect(() => {
    if (config) setForm({
      enabled: config.enabled ?? true,
      memory: config.memory || '2GB',
      cpu: config.cpu || 2,
      processes: config.processes || 500
    })
  }, [config])

  // Load details on mount
  const loadDetails = useCallback(() => {
    setDetailsLoading(true)
    fetchSandboxDetails()
      .then(setDetails)
      .catch(() => setDetails(null))
      .finally(() => setDetailsLoading(false))
  }, [])

  useEffect(() => { loadDetails() }, [loadDetails])

  // Load containers
  const loadContainers = useCallback(() => {
    setContainersLoading(true)
    setActionError(null)
    fetchContainers()
      .then((data) => setContainerData({
        containers: (data.containers || []) as unknown as ContainerItem[],
        orphans: (data.orphans || []).map((name: string) => ({ name, session_id: name, status: 'orphan' })) as unknown as ContainerItem[],
      }))
      .catch(() => setContainerData(null))
      .finally(() => setContainersLoading(false))
  }, [])

  // Load templates
  const loadTemplates = useCallback(() => {
    setTemplatesLoading(true)
    setTemplateError(null)
    fetchTemplates()
      .then((data) => setTemplateData({ templates: (data.templates || []) as unknown as TemplateItem[] }))
      .catch(() => setTemplateData(null))
      .finally(() => setTemplatesLoading(false))
  }, [])

  // Auto-load containers and templates when runtime is available
  useEffect(() => {
    if (details?.runtimeAvailable ?? details?.incusAvailable) {
      loadContainers()
      loadTemplates()
    }
  }, [details, loadContainers, loadTemplates])

  // Save config
  const handleSave = async () => {
    setSaving(true)
    setSaveSuccess(false)
    setSaveError(null)
    try {
      await saveFullConfigSection('sandbox', form as unknown as Record<string, unknown>)
      setSaveSuccess(true)
      if (onSaved) onSaved()
      setTimeout(() => setSaveSuccess(false), 2000)
    } catch (err: any) {
      setSaveError(err.message)
    } finally {
      setSaving(false)
    }
  }

  // Container actions
  const handleDeleteContainer = async (sessionId: string) => {
    setActionLoading(sessionId)
    setActionError(null)
    try {
      await deleteContainer(sessionId)
      setDeleteConfirm(null)
      loadContainers()
      loadDetails()
    } catch (err: any) {
      setActionError(err.message)
    } finally {
      setActionLoading(null)
    }
  }

  const handlePrune = async () => {
    setActionLoading('prune')
    setActionError(null)
    try {
      await pruneOrphans()
      setPruneConfirm(false)
      loadContainers()
      loadDetails()
    } catch (err: any) {
      setActionError(err.message)
    } finally {
      setActionLoading(null)
    }
  }

  // Port exposure actions
  const handleExposePort = async (containerId: string) => {
    const port = parseInt(exposePortInput, 10)
    if (!port || port < 1 || port > 65535) return
    setExposeLoading(true)
    setActionError(null)
    try {
      const result = await exposePort(containerId, port)
      if (result.proxy_error) {
        setActionError(`Port exposed but proxy listener failed: ${result.proxy_error}`)
      }
      setExposePortInput('')
      loadContainers()
    } catch (err: any) {
      setActionError(err.message)
    } finally {
      setExposeLoading(false)
    }
  }

  const handleUnexposePort = async (containerId: string, port: number) => {
    setExposeLoading(true)
    setActionError(null)
    try {
      await unexposePort(containerId, port)
      loadContainers()
    } catch (err: any) {
      setActionError(err.message)
    } finally {
      setExposeLoading(false)
    }
  }

  const handleTogglePin = async (containerId: string, currentPinned: boolean | undefined) => {
    setActionError(null)
    try {
      await pinContainer(containerId, !currentPinned)
      loadContainers()
    } catch (err: any) {
      setActionError(err.message)
    }
  }

  // Template actions
  const handleCreateTemplate = async () => {
    if (!createName.trim()) return
    setTemplateActionLoading('create')
    setTemplateError(null)
    try {
      await createTemplate(createName.trim(), createDesc.trim())
      setShowCreateForm(false)
      setCreateName('')
      setCreateDesc('')
      loadTemplates()
      loadDetails()
    } catch (err: any) {
      setTemplateError(err.message)
    } finally {
      setTemplateActionLoading(null)
    }
  }

  const handleDeleteTemplate = async (name: string) => {
    setTemplateActionLoading(name)
    setTemplateError(null)
    try {
      await deleteTemplate(name)
      setTemplateDeleteConfirm(null)
      if (expandedTemplate === name) setExpandedTemplate(null)
      loadTemplates()
      loadDetails()
    } catch (err: any) {
      setTemplateError(err.message)
    } finally {
      setTemplateActionLoading(null)
    }
  }

  const handleSnapshot = async (name: string) => {
    setTemplateActionLoading(`snapshot-${name}`)
    setTemplateError(null)
    try {
      await snapshotTemplate(name)
      loadTemplates()
      if (expandedTemplate === name) handleExpandTemplate(name)
    } catch (err: any) {
      setTemplateError(err.message)
    } finally {
      setTemplateActionLoading(null)
    }
  }

  const handlePromote = async (name: string) => {
    setTemplateActionLoading(`promote-${name}`)
    setTemplateError(null)
    try {
      await promoteTemplate(name)
      setPromoteConfirm(null)
      loadTemplates()
      loadDetails()
    } catch (err: any) {
      setTemplateError(err.message)
    } finally {
      setTemplateActionLoading(null)
    }
  }

  const handleRefresh = async () => {
    setTemplateActionLoading('refresh')
    setTemplateError(null)
    try {
      await refreshTemplates()
      loadTemplates()
    } catch (err: any) {
      setTemplateError(err.message)
    } finally {
      setTemplateActionLoading(null)
    }
  }

  const handleExpandTemplate = (name: string) => {
    if (expandedTemplate === name) {
      setExpandedTemplate(null)
      setTemplateDetail(null)
      return
    }
    setExpandedTemplate(name)
    setTemplateDetailLoading(true)
    fetchTemplateInfo(name)
      .then(setTemplateDetail)
      .catch(() => setTemplateDetail(null))
      .finally(() => setTemplateDetailLoading(false))
  }

  // --- Render ---

  const renderStatus = () => {
    if (detailsLoading) {
      return (
        <div className="flex items-center gap-2 text-xs" style={{ color: 'var(--text-muted)' }}>
          <Loader2 size={14} className="animate-spin" /> Checking runtime...
        </div>
      )
    }
    if (!details) return null

    const runtimeAvailable = details.runtimeAvailable ?? details.incusAvailable ?? false
    const isK8s = details.backend === 'k8s'
    const isOpenShell = details.backend === 'openshell'

    if (details.platform === 'unsupported' && !isK8s && !isOpenShell) {
      return (
        <div className="flex items-center gap-2 px-3 py-2 rounded-lg text-xs"
          style={{ background: 'rgba(239, 68, 68, 0.1)', border: '1px solid rgba(239, 68, 68, 0.3)' }}>
          <ShieldOff size={14} style={{ color: '#ef4444' }} />
          <span style={{ color: '#ef4444' }}>
            Platform not supported{details.reason ? ` \u2014 ${details.reason}` : ''}
          </span>
        </div>
      )
    }

    if (!runtimeAvailable) {
      const hint = isK8s
        ? `Kubernetes sandbox not reachable${details.reason ? ` \u2014 ${details.reason}` : ''}`
        : isOpenShell
          ? `OpenShell gateway not reachable${details.reason ? ` \u2014 ${details.reason}` : ''}`
          : 'Incus not available \u2014 install with sudo apt install incus'
      return (
        <div className="flex items-center gap-2 px-3 py-2 rounded-lg text-xs"
          style={{ background: 'rgba(234, 179, 8, 0.1)', border: '1px solid rgba(234, 179, 8, 0.3)' }}>
          <Shield size={14} style={{ color: '#eab308' }} />
          <span style={{ color: '#eab308' }}>
            {(isK8s || isOpenShell) ? hint : <>Incus not available &mdash; install with <code className="font-mono">sudo apt install incus</code></>}
          </span>
        </div>
      )
    }

    if (!details.baseTemplateExists) {
      return (
        <div className="flex items-center gap-2 px-3 py-2 rounded-lg text-xs"
          style={{ background: 'rgba(234, 179, 8, 0.1)', border: '1px solid rgba(234, 179, 8, 0.3)' }}>
          <Shield size={14} style={{ color: '#eab308' }} />
          <span style={{ color: '#eab308' }}>
            Base template not initialized &mdash; run the Setup Wizard to create it
          </span>
        </div>
      )
    }

    return (
      <div className="px-3 py-2 rounded-lg text-xs space-y-1"
        style={{ background: 'rgba(34, 197, 94, 0.05)', border: '1px solid rgba(34, 197, 94, 0.2)' }}>
        <div className="flex items-center justify-between">
          <div className="flex items-center gap-2">
            <Shield size={14} style={{ color: '#22c55e' }} />
            <span style={{ color: '#22c55e' }}>Runtime ready</span>
          </div>
          <button onClick={loadDetails} className="p-1 rounded hover:bg-white/10 transition-colors"
            title="Refresh status">
            <RefreshCw size={12} style={{ color: 'var(--text-muted)' }} />
          </button>
        </div>
        <div className="flex flex-wrap gap-x-4 gap-y-0.5 pl-5" style={{ color: 'var(--text-muted)' }}>
          {isK8s && details.server_version && <span>K8s {details.server_version}</span>}
          {isK8s && details.namespace && <span>Namespace: {details.namespace}</span>}
          {isK8s && details.overlay_mode && <span>Overlay: {details.overlay_mode}</span>}
          {isOpenShell && details.namespace && <span>Gateway: {details.namespace}</span>}
          {isOpenShell && <span>Backend: OpenShell</span>}
          {!isK8s && !isOpenShell && details.incus_version && <span>Incus {details.incus_version}</span>}
          {details.storage_backend && <span>Storage: {details.storage_backend}</span>}
          {!isK8s && !isOpenShell && <span>Overlay: {details.overlay_ready ? 'ready' : 'not configured'}</span>}
          {!isOpenShell && <span>{details.template_count} template{details.template_count !== 1 ? 's' : ''}</span>}
          {!isOpenShell && <span>{details.container_count} container{details.container_count !== 1 ? 's' : ''}</span>}
          {!isOpenShell && (details.orphan_count ?? 0) > 0 && (
            <span style={{ color: '#f59e0b' }}>{details.orphan_count} orphan{details.orphan_count !== 1 ? 's' : ''}</span>
          )}
        </div>
      </div>
    )
  }

  const renderContainers = () => {
    if (!(details?.runtimeAvailable ?? details?.incusAvailable)) return null

    const containers: ContainerItem[] = containerData?.containers || []
    const orphans: ContainerItem[] = containerData?.orphans || []

    return (
      <div className="space-y-3">
        <div className="flex items-center justify-between">
          <div className="flex items-center gap-2">
            <h4 className="text-sm font-medium" style={{ color: 'var(--text-primary)' }}>
              Session Containers
            </h4>
            {containers.length > 0 && (
              <span className="text-xs px-1.5 py-0.5 rounded"
                style={{ background: 'rgba(107, 114, 128, 0.15)', color: 'var(--text-muted)' }}>
                {containers.length}
              </span>
            )}
          </div>
          <div className="flex items-center gap-2">
            {orphans.length > 0 && (
              <button
                onClick={() => setPruneConfirm(true)}
                className="flex items-center gap-1 px-2 py-1 rounded text-xs transition-colors"
                style={{ background: 'rgba(239, 68, 68, 0.1)', color: '#f87171', border: '1px solid rgba(239, 68, 68, 0.2)' }}
              >
                <Trash2 size={12} />
                Prune {orphans.length} orphan{orphans.length !== 1 ? 's' : ''}
              </button>
            )}
            <button onClick={loadContainers} className="p-1.5 rounded transition-colors hover:bg-white/10"
              title="Refresh" disabled={containersLoading}>
              <RefreshCw size={14} className={containersLoading ? 'animate-spin' : ''}
                style={{ color: 'var(--text-muted)' }} />
            </button>
          </div>
        </div>

        {/* Prune confirmation */}
        {pruneConfirm && (
          <div className="p-3 rounded-lg text-sm"
            style={{ background: 'rgba(239, 68, 68, 0.05)', border: '1px solid rgba(239, 68, 68, 0.2)' }}>
            <p className="font-medium mb-2" style={{ color: 'var(--text-primary)' }}>
              Prune {orphans.length} orphaned container{orphans.length !== 1 ? 's' : ''}?
            </p>
            <p className="text-xs mb-3" style={{ color: 'var(--text-muted)' }}>
              This will destroy containers whose sessions no longer exist. This cannot be undone.
            </p>
            <div className="flex gap-2">
              <button onClick={handlePrune}
                disabled={actionLoading === 'prune'}
                className="flex items-center gap-1 px-3 py-1.5 rounded text-xs font-medium text-white"
                style={{ background: '#ef4444' }}>
                {actionLoading === 'prune' ? <Loader2 size={12} className="animate-spin" /> : <Trash2 size={12} />}
                Prune
              </button>
              <button onClick={() => setPruneConfirm(false)}
                className="px-3 py-1.5 rounded text-xs"
                style={{ color: 'var(--text-secondary)', border: '1px solid var(--border-color)' }}>
                Cancel
              </button>
            </div>
          </div>
        )}

        {actionError && (
          <div className="flex items-center gap-1 text-xs" style={{ color: '#ef4444' }}>
            <AlertCircle size={12} /> {actionError}
          </div>
        )}

        {/* Container list */}
        {containers.length === 0 && !containersLoading ? (
          <div className="flex flex-col items-center justify-center py-6 rounded-lg border border-dashed"
            style={{ borderColor: 'var(--border-color)' }}>
            <Server size={32} className="mb-2" style={{ color: 'var(--text-muted)', opacity: 0.3 }} />
            <p className="text-xs" style={{ color: 'var(--text-muted)' }}>No session containers</p>
          </div>
        ) : (
          <div className="space-y-1">
            {containers.map((c: ContainerItem) => {
              const isExposePanelOpen = exposeContainer === c.session_id
              const exposedPorts: number[] = c.exposed_ports || []
              const isRunning = c.status === 'running'
              return (
                <div key={c.session_id} className="rounded-lg overflow-hidden"
                  style={{ background: 'var(--bg-secondary)', border: '1px solid var(--border-color)' }}>
                  {/* Main row */}
                  <div className="flex items-center justify-between px-3 py-2 group">
                    <div className="flex-1 min-w-0 mr-3">
                      <div className="flex items-center gap-2">
                        <span className="font-mono text-xs truncate" style={{ color: 'var(--text-primary)' }}>
                          {c.name}
                        </span>
                        <StatusBadge status={c.status} />
                        <TemplateBadge name={c.template || 'base'} />
                        {exposedPorts.length > 0 && (
                          <span className="text-xs px-1.5 py-0.5 rounded inline-flex items-center gap-1"
                            style={{ background: 'rgba(34, 197, 94, 0.15)', color: '#22c55e' }}>
                            <Globe size={10} />
                            {exposedPorts.length} port{exposedPorts.length !== 1 ? 's' : ''}
                          </span>
                        )}
                        {c.pinned && (
                          <span className="text-xs px-1.5 py-0.5 rounded inline-flex items-center gap-1"
                            style={{ background: 'rgba(234, 179, 8, 0.15)', color: '#eab308' }}
                            title="Pinned — exempt from automatic cleanup">
                            <Pin size={10} />
                            pinned
                          </span>
                        )}
                      </div>
                      <div className="flex items-center gap-2 mt-0.5">
                        <span className="text-xs font-mono truncate" style={{ color: 'var(--text-muted)' }}>
                          {c.session_id.length > 12 ? c.session_id.slice(0, 12) + '...' : c.session_id}
                        </span>
                        <span className="text-xs" style={{ color: 'var(--text-muted)' }}>{c.created}</span>
                      </div>
                    </div>
                    <div className="flex items-center gap-1 flex-shrink-0">
                      {/* Pin toggle */}
                      <button
                        onClick={() => handleTogglePin(c.session_id, c.pinned)}
                        className="p-1.5 rounded transition-colors hover:bg-white/10"
                        title={c.pinned ? 'Unpin (allow automatic cleanup)' : 'Pin (prevent automatic cleanup)'}>
                        <Pin size={14} style={{ color: c.pinned ? '#eab308' : 'var(--text-muted)' }} />
                      </button>
                      {/* Port expose toggle (only for running containers) */}
                      {isRunning && (
                        <button
                          onClick={() => setExposeContainer(isExposePanelOpen ? null : c.session_id)}
                          className="p-1.5 rounded transition-colors hover:bg-white/10"
                          title="Expose ports">
                          <Globe size={14} style={{ color: isExposePanelOpen ? '#22c55e' : 'var(--text-muted)' }} />
                        </button>
                      )}
                      {deleteConfirm === c.session_id ? (
                        <>
                          <button onClick={() => handleDeleteContainer(c.session_id)}
                            disabled={actionLoading === c.session_id}
                            className="px-2 py-1 rounded text-xs font-medium text-white"
                            style={{ background: '#ef4444' }}>
                            {actionLoading === c.session_id ? <Loader2 size={12} className="animate-spin" /> : 'Delete'}
                          </button>
                          <button onClick={() => setDeleteConfirm(null)}
                            className="px-2 py-1 rounded text-xs"
                            style={{ color: 'var(--text-muted)' }}>
                            Cancel
                          </button>
                        </>
                      ) : (
                        <button onClick={() => setDeleteConfirm(c.session_id)}
                          className="p-1.5 rounded opacity-0 group-hover:opacity-100 hover:bg-red-500/20 transition-all"
                          title="Delete container">
                          <Trash2 size={14} className="text-red-400" />
                        </button>
                      )}
                    </div>
                  </div>
                  {/* Port exposure panel */}
                  {isExposePanelOpen && isRunning && (
                    <div className="px-3 pb-3 pt-1 border-t space-y-2"
                      style={{ borderColor: 'var(--border-color)' }}>
                      {/* Expose new port input */}
                      <div className="flex items-center gap-2">
                        <input
                          type="number"
                          value={exposePortInput}
                          onChange={(e: React.ChangeEvent<HTMLInputElement>) => setExposePortInput(e.target.value)}
                          onKeyDown={(e: React.KeyboardEvent<HTMLInputElement>) => { if (e.key === 'Enter') handleExposePort(c.session_id) }}
                          placeholder="Port (e.g. 3000)"
                          min="1"
                          max="65535"
                          className="w-36 px-2 py-1 rounded text-xs font-mono"
                          style={{ background: 'var(--bg-tertiary)', border: '1px solid var(--border-color)', color: 'var(--text-primary)' }}
                        />
                        <button
                          onClick={() => handleExposePort(c.session_id)}
                          disabled={exposeLoading || !exposePortInput}
                          className="flex items-center gap-1 px-2 py-1 rounded text-xs font-medium text-white disabled:opacity-50"
                          style={saveButtonStyle}>
                          {exposeLoading ? <Loader2 size={10} className="animate-spin" /> : <Globe size={10} />}
                          Expose
                        </button>
                      </div>
                      {/* List of exposed ports */}
                      {exposedPorts.length > 0 ? (
                        <div className="space-y-1">
                          {exposedPorts.map((port: number) => {
                            const hostPorts: Record<string, string> = (c.host_ports || {}) as Record<string, string>
                            const proxyHosts: Record<string, string> = (c.proxy_hosts || {}) as Record<string, string>
                            const hp = hostPorts[String(port)]
                            const ph = proxyHosts[String(port)]
                            // Prefer subdomain URL (works with HTTPS via reverse proxy),
                            // fall back to per-port direct URL, then "Proxy not running"
                            const proxyUrl = ph
                              ? `${window.location.protocol}//${ph}/`
                              : hp
                                ? `http://${window.location.hostname}:${hp}/`
                                : null
                            const proxyLabel = ph || (hp ? `${window.location.hostname}:${hp}` : null)
                            return (
                              <div key={port} className="flex items-center justify-between px-2 py-1 rounded text-xs"
                                style={{ background: 'var(--bg-tertiary)' }}>
                                <div className="flex items-center gap-2">
                                  <span className="font-mono" style={{ color: '#22c55e' }}>:{port}</span>
                                  {proxyUrl ? (
                                    <a href={proxyUrl} target="_blank" rel="noopener noreferrer"
                                      className="flex items-center gap-1 transition-colors hover:underline"
                                      style={{ color: 'var(--text-secondary)' }}>
                                      <ExternalLink size={10} />
                                      {proxyLabel}
                                    </a>
                                  ) : (
                                    <span style={{ color: 'var(--text-muted)' }}>Proxy not running</span>
                                  )}
                                </div>
                                <button
                                  onClick={() => handleUnexposePort(c.session_id, port)}
                                  disabled={exposeLoading}
                                  className="p-1 rounded hover:bg-red-500/20 transition-colors"
                                  title="Unexpose port">
                                  <X size={12} className="text-red-400" />
                                </button>
                              </div>
                            )
                          })}
                        </div>
                      ) : (
                        <p className="text-xs" style={{ color: 'var(--text-muted)' }}>
                          No ports exposed. Enter a port number to make a container service accessible via the Studio proxy.
                        </p>
                      )}
                    </div>
                  )}
                </div>
              )
            })}
          </div>
        )}
      </div>
    )
  }

  const renderTemplates = () => {
    if (!(details?.runtimeAvailable ?? details?.incusAvailable)) return null

    const templates: TemplateItem[] = templateData?.templates || []

    return (
      <div className="space-y-3">
        <div className="flex items-center justify-between">
          <div className="flex items-center gap-2">
            <h4 className="text-sm font-medium" style={{ color: 'var(--text-primary)' }}>
              Templates
            </h4>
            {templates.length > 0 && (
              <span className="text-xs px-1.5 py-0.5 rounded"
                style={{ background: 'rgba(107, 114, 128, 0.15)', color: 'var(--text-muted)' }}>
                {templates.length}
              </span>
            )}
          </div>
          <div className="flex items-center gap-2">
            <button onClick={() => setShowCreateForm(!showCreateForm)}
              className="flex items-center gap-1 px-2 py-1 rounded text-xs font-medium text-white transition-all hover:scale-[1.02]"
              style={saveButtonStyle}>
              <Plus size={12} /> Create
            </button>
            <button onClick={handleRefresh}
              disabled={templateActionLoading === 'refresh'}
              className="flex items-center gap-1 px-2 py-1 rounded text-xs transition-colors"
              style={{ color: 'var(--text-secondary)', border: '1px solid var(--border-color)' }}
              title="Refresh binary in all templates">
              {templateActionLoading === 'refresh'
                ? <Loader2 size={12} className="animate-spin" />
                : <RefreshCw size={12} />}
              Refresh Binary
            </button>
            <button onClick={loadTemplates} className="p-1.5 rounded transition-colors hover:bg-white/10"
              title="Reload list" disabled={templatesLoading}>
              <RefreshCw size={14} className={templatesLoading ? 'animate-spin' : ''}
                style={{ color: 'var(--text-muted)' }} />
            </button>
          </div>
        </div>

        {/* Create form */}
        {showCreateForm && (
          <div className="p-3 rounded-lg space-y-2"
            style={{ background: 'var(--bg-secondary)', border: '1px solid var(--border-color)' }}>
            <input type="text" value={createName}
              onChange={(e: React.ChangeEvent<HTMLInputElement>) => setCreateName(e.target.value)}
              placeholder="Template name"
              className="w-full px-3 py-1.5 rounded text-sm"
              style={{ background: 'var(--bg-tertiary)', border: '1px solid var(--border-color)', color: 'var(--text-primary)' }}
            />
            <input type="text" value={createDesc}
              onChange={(e: React.ChangeEvent<HTMLInputElement>) => setCreateDesc(e.target.value)}
              placeholder="Description (optional)"
              className="w-full px-3 py-1.5 rounded text-sm"
              style={{ background: 'var(--bg-tertiary)', border: '1px solid var(--border-color)', color: 'var(--text-primary)' }}
            />
            <div className="flex gap-2">
              <button onClick={handleCreateTemplate}
                disabled={!createName.trim() || templateActionLoading === 'create'}
                className="flex items-center gap-1 px-3 py-1.5 rounded text-xs font-medium text-white disabled:opacity-50"
                style={saveButtonStyle}>
                {templateActionLoading === 'create' ? <Loader2 size={12} className="animate-spin" /> : <Plus size={12} />}
                Create from @base
              </button>
              <button onClick={() => { setShowCreateForm(false); setCreateName(''); setCreateDesc('') }}
                className="px-3 py-1.5 rounded text-xs"
                style={{ color: 'var(--text-secondary)', border: '1px solid var(--border-color)' }}>
                Cancel
              </button>
            </div>
          </div>
        )}

        {templateError && (
          <div className="flex items-center gap-1 text-xs" style={{ color: '#ef4444' }}>
            <AlertCircle size={12} /> {templateError}
          </div>
        )}

        {/* Promote confirmation */}
        {promoteConfirm && (
          <div className="p-3 rounded-lg text-sm"
            style={{ background: 'rgba(234, 179, 8, 0.05)', border: '1px solid rgba(234, 179, 8, 0.2)' }}>
            <p className="font-medium mb-2" style={{ color: 'var(--text-primary)' }}>
              Promote &quot;{promoteConfirm}&quot; to @base?
            </p>
            <p className="text-xs mb-3" style={{ color: 'var(--text-muted)' }}>
              This will replace the current @base template. All new sessions will use this template. This cannot be undone.
            </p>
            <div className="flex gap-2">
              <button onClick={() => handlePromote(promoteConfirm)}
                disabled={templateActionLoading === `promote-${promoteConfirm}`}
                className="flex items-center gap-1 px-3 py-1.5 rounded text-xs font-medium text-white"
                style={{ background: '#f59e0b' }}>
                {templateActionLoading === `promote-${promoteConfirm}`
                  ? <Loader2 size={12} className="animate-spin" />
                  : <ArrowUpCircle size={12} />}
                Promote
              </button>
              <button onClick={() => setPromoteConfirm(null)}
                className="px-3 py-1.5 rounded text-xs"
                style={{ color: 'var(--text-secondary)', border: '1px solid var(--border-color)' }}>
                Cancel
              </button>
            </div>
          </div>
        )}

        {/* Template delete confirmation */}
        {templateDeleteConfirm && (
          <div className="p-3 rounded-lg text-sm"
            style={{ background: 'rgba(239, 68, 68, 0.05)', border: '1px solid rgba(239, 68, 68, 0.2)' }}>
            <p className="font-medium mb-2" style={{ color: 'var(--text-primary)' }}>
              Delete template &quot;{templateDeleteConfirm}&quot;?
            </p>
            <p className="text-xs mb-3" style={{ color: 'var(--text-muted)' }}>
              This will remove the template container and all its data. This cannot be undone.
            </p>
            <div className="flex gap-2">
              <button onClick={() => handleDeleteTemplate(templateDeleteConfirm)}
                disabled={templateActionLoading === templateDeleteConfirm}
                className="flex items-center gap-1 px-3 py-1.5 rounded text-xs font-medium text-white"
                style={{ background: '#ef4444' }}>
                {templateActionLoading === templateDeleteConfirm
                  ? <Loader2 size={12} className="animate-spin" />
                  : <Trash2 size={12} />}
                Delete
              </button>
              <button onClick={() => setTemplateDeleteConfirm(null)}
                className="px-3 py-1.5 rounded text-xs"
                style={{ color: 'var(--text-secondary)', border: '1px solid var(--border-color)' }}>
                Cancel
              </button>
            </div>
          </div>
        )}

        {/* Template list */}
        {templates.length === 0 && !templatesLoading ? (
          <div className="flex flex-col items-center justify-center py-6 rounded-lg border border-dashed"
            style={{ borderColor: 'var(--border-color)' }}>
            <Box size={32} className="mb-2" style={{ color: 'var(--text-muted)', opacity: 0.3 }} />
            <p className="text-xs" style={{ color: 'var(--text-muted)' }}>
              No templates \u2014 run the Setup Wizard to create the base template
            </p>
          </div>
        ) : (
          <div className="space-y-2">
            {templates.map((t: TemplateItem) => {
              const isBase = t.name === 'base'
              const isExpanded = expandedTemplate === t.name
              return (
                <div key={t.name} className="rounded-lg border transition-all"
                  style={{
                    background: 'var(--bg-secondary)',
                    borderColor: isExpanded ? 'color-mix(in oklab, var(--brand) 40%, transparent)' : 'var(--border-color)'
                  }}>
                  {/* Header */}
                  <div className="px-3 py-2 flex items-center gap-3">
                    <div className="flex-1 min-w-0">
                      <div className="flex items-center gap-2">
                        <span className="text-sm font-medium" style={{ color: 'var(--text-primary)' }}>
                          @{t.name}
                        </span>
                        {isBase && (
                          <span className="text-xs px-1.5 py-0.5 rounded"
                            style={{ background: 'var(--brand-muted)', color: 'var(--brand)' }}>
                            default
                          </span>
                        )}
                        {t.fleet_plans?.length && t.fleet_plans.length > 0 && t.fleet_plans.map((p: string) => (
                          <span key={p} className="text-xs px-1.5 py-0.5 rounded"
                            style={{ background: 'rgba(6, 182, 212, 0.15)', color: '#06b6d4' }}>
                            {p}
                          </span>
                        ))}
                      </div>
                      <div className="flex items-center gap-3 mt-0.5 text-xs" style={{ color: 'var(--text-muted)' }}>
                        {t.description && <span className="truncate max-w-[200px]">{t.description}</span>}
                        <span>Created: {t.created}</span>
                        {t.last_snapshot && <span>Snapshot: {t.last_snapshot}</span>}
                      </div>
                    </div>
                    {/* Actions */}
                    <div className="flex items-center gap-1 flex-shrink-0">
                      <button onClick={() => handleExpandTemplate(t.name)}
                        className="p-1.5 rounded transition-colors hover:bg-white/10"
                        title="Details">
                        {isExpanded ? <ChevronDown size={14} style={{ color: 'var(--brand)' }} /> : <Eye size={14} style={{ color: 'var(--text-muted)' }} />}
                      </button>
                      <button onClick={() => handleSnapshot(t.name)}
                        disabled={!!templateActionLoading}
                        className="p-1.5 rounded transition-colors hover:bg-white/10"
                        title="Snapshot">
                        {templateActionLoading === `snapshot-${t.name}`
                          ? <Loader2 size={14} className="animate-spin" style={{ color: 'var(--text-muted)' }} />
                          : <Camera size={14} style={{ color: 'var(--text-muted)' }} />}
                      </button>
                      {!isBase && (
                        <>
                          <button onClick={() => setPromoteConfirm(t.name)}
                            disabled={!!templateActionLoading}
                            className="p-1.5 rounded transition-colors hover:bg-white/10"
                            title="Promote to @base">
                            <ArrowUpCircle size={14} style={{ color: 'var(--text-muted)' }} />
                          </button>
                          <button onClick={() => setTemplateDeleteConfirm(t.name)}
                            disabled={!!templateActionLoading}
                            className="p-1.5 rounded transition-colors hover:bg-red-500/20"
                            title="Delete template">
                            <Trash2 size={14} className="text-red-400" />
                          </button>
                        </>
                      )}
                    </div>
                  </div>
                  {/* Expanded detail */}
                  {isExpanded && (
                    <div className="px-3 pb-3 pt-1 border-t text-xs space-y-1"
                      style={{ borderColor: 'var(--border-color)' }}>
                      {templateDetailLoading ? (
                        <div className="flex items-center gap-2 py-2" style={{ color: 'var(--text-muted)' }}>
                          <Loader2 size={12} className="animate-spin" /> Loading details...
                        </div>
                      ) : templateDetail ? (
                        <div className="grid grid-cols-2 gap-x-4 gap-y-1" style={{ color: 'var(--text-muted)' }}>
                          <span>Container:</span>
                          <span className="font-mono">{templateDetail.container_name}</span>
                          <span>Status:</span>
                          <span>{templateDetail.container_status}</span>
                          <span>Snapshot:</span>
                          <span>{templateDetail.snapshot_ready ? 'Ready (cloneable)' : 'Not ready'}</span>
                          {templateDetail.based_on && (
                            <>
                              <span>Based on:</span>
                              <span>@{templateDetail.based_on}</span>
                            </>
                          )}
                          {templateDetail.binary_hash && (
                            <>
                              <span>Binary hash:</span>
                              <span className="font-mono">{templateDetail.binary_hash}</span>
                            </>
                          )}
                        </div>
                      ) : (
                        <span style={{ color: 'var(--text-muted)' }}>Failed to load details</span>
                      )}
                    </div>
                  )}
                </div>
              )
            })}
          </div>
        )}
      </div>
    )
  }

  return (
    <div className="max-w-2xl space-y-6">
      {/* Status */}
      {renderStatus()}

      {/* Enabled Toggle */}
      <div className="flex items-center justify-between">
        <div>
          <label className="text-sm font-medium" style={{ color: 'var(--text-primary)' }}>
            Sandbox Isolation
          </label>
          <p className="text-xs mt-0.5" style={hintStyle}>
            When enabled, all tool execution runs inside isolated containers. When disabled, tools execute directly on the host.
          </p>
        </div>
        <button
          onClick={() => setForm({ ...form, enabled: !form.enabled })}
          className="relative w-11 h-6 rounded-full transition-colors"
          style={{
            background: form.enabled ? 'var(--brand)' : 'var(--bg-tertiary)',
            border: `1px solid ${form.enabled ? 'var(--brand)' : 'var(--border-color)'}`
          }}
        >
          <span
            className="absolute top-0.5 left-0.5 w-4 h-4 rounded-full transition-transform bg-white"
            style={{ transform: form.enabled ? 'translateX(20px)' : 'translateX(0)' }}
          />
        </button>
      </div>

      {!form.enabled && (
        <div className="flex items-start gap-2 p-3 rounded-lg text-xs"
          style={{ background: 'rgba(239, 68, 68, 0.1)', border: '1px solid rgba(239, 68, 68, 0.3)' }}>
          <AlertCircle size={14} className="mt-0.5 flex-shrink-0" style={{ color: '#ef4444' }} />
          <span style={{ color: '#ef4444' }}>
            Sandbox is disabled. AI tools will execute directly on your host system with full access to files, network, and system resources.
          </span>
        </div>
      )}

      {/* Resource Limits */}
      {form.enabled && (
        <div className="pt-4 border-t" style={sectionBorderStyle}>
          <div className="mb-4">
            <h4 className="text-sm font-medium" style={{ color: 'var(--text-primary)' }}>
              Resource Limits
            </h4>
            <p className="text-xs mt-0.5" style={hintStyle}>
              Limits applied to each session container. Changes apply to new containers only.
            </p>
          </div>
          <div className="grid grid-cols-3 gap-4">
            <div>
              <label className="block text-sm font-medium mb-2" style={labelStyle}>
                Memory
              </label>
              <input
                type="text"
                value={form.memory}
                onChange={(e: React.ChangeEvent<HTMLInputElement>) => setForm({ ...form, memory: e.target.value })}
                placeholder="2GB"
                className={inputClass}
                style={inputStyle}
              />
              <p className="text-xs mt-1" style={hintStyle}>e.g. 2GB, 4GB, 512MB</p>
            </div>
            <div>
              <label className="block text-sm font-medium mb-2" style={labelStyle}>
                CPU Cores
              </label>
              <input
                type="number"
                value={form.cpu}
                onChange={(e: React.ChangeEvent<HTMLInputElement>) => setForm({ ...form, cpu: parseInt(e.target.value) || 0 })}
                min="1"
                max="64"
                className={inputClass}
                style={inputStyle}
              />
              <p className="text-xs mt-1" style={hintStyle}>Default: 2</p>
            </div>
            <div>
              <label className="block text-sm font-medium mb-2" style={labelStyle}>
                Max Processes
              </label>
              <input
                type="number"
                value={form.processes}
                onChange={(e: React.ChangeEvent<HTMLInputElement>) => setForm({ ...form, processes: parseInt(e.target.value) || 0 })}
                min="50"
                max="10000"
                className={inputClass}
                style={inputStyle}
              />
              <p className="text-xs mt-1" style={hintStyle}>Default: 500</p>
            </div>
          </div>
        </div>
      )}

      {/* Save */}
      <div className="flex items-center gap-3">
        <button onClick={handleSave} disabled={saving}
          className="flex items-center gap-2 px-4 py-2 rounded-lg text-white font-medium transition-all shadow-md hover:shadow-lg hover:scale-[1.02] active:scale-95 disabled:opacity-50"
          style={saveButtonStyle}>
          <Save size={16} />
          {saving ? 'Saving...' : 'Save Changes'}
        </button>
        {saveSuccess && (
          <span className="flex items-center gap-1 text-green-400 text-sm">
            <Check size={16} /> Saved
          </span>
        )}
        {saveError && (
          <span className="flex items-center gap-1 text-sm" style={{ color: 'var(--danger)' }}>
            <AlertCircle size={16} /> {saveError}
          </span>
        )}
      </div>

      {/* Session Containers section */}
      {(details?.runtimeAvailable ?? details?.incusAvailable) && (
        <div className="pt-4 border-t" style={{ borderColor: 'var(--border-color)' }}>
          {renderContainers()}
        </div>
      )}

      {/* Templates section */}
      {(details?.runtimeAvailable ?? details?.incusAvailable) && (
        <div className="pt-4 border-t" style={{ borderColor: 'var(--border-color)' }}>
          {renderTemplates()}
        </div>
      )}
    </div>
  )
}
