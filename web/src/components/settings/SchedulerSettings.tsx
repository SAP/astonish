import { useState, useEffect, useCallback } from 'react'
import { Save, AlertCircle, Check, Play, Trash2, Clock, Loader2, CheckCircle, XCircle, Pause, ChevronRight, ChevronDown, X, AlertTriangle, Users, Mail, MessageSquare, Hash, Upload, Download } from 'lucide-react'
import { saveFullConfigSection, hintStyle, sectionBorderStyle, saveButtonStyle } from './settingsApi'
import { teamFetch } from '../../api/teamContext'

// --- Types ---

interface SchedulerJob {
  id: string
  name: string
  enabled: boolean
  mode: string
  /** personal = owner credentials; team = shared team credentials */
  scope?: 'personal' | 'team' | string
  schedule?: {
    cron?: string
    timezone?: string
  }
  last_status?: string
  last_run?: string
  next_run?: string
  delivery?: {
    channel?: string
    target?: string
    mode?: string
    member_ids?: string[]
    channel_filter?: string[]
    member_channels?: Record<string, string[]>
  }
  owner_id?: string
  consecutive_failures?: number
  payload?: {
    instructions?: string
    flow?: string
    params?: Record<string, unknown>
  }
  last_error?: string
  [key: string]: unknown
}

interface TeamMember {
  user_id: string
  display_name: string
  email: string
  role: string
  linked_channels: string[]
}

interface SchedulerConfig {
  enabled?: boolean
  [key: string]: unknown
}

interface SchedulerSettingsProps {
  config: SchedulerConfig | null
  onSaved?: () => void
  /** Explicit team slug — overrides global active team for API calls */
  teamSlug?: string
  /** Platform mode shows Personal + Team sections like Credentials */
  isPlatform?: boolean
}

// API helpers
const fetchJobs = async (teamSlug?: string): Promise<{ jobs?: SchedulerJob[]; is_team_admin?: boolean }> => {
  const res = await teamFetch('/api/scheduler/jobs', undefined, teamSlug)
  if (!res.ok) throw new Error('Failed to fetch jobs')
  return res.json()
}

const publishJob = async (id: string, teamSlug?: string): Promise<Record<string, unknown>> => {
  const res = await teamFetch('/api/scheduler/jobs/publish', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ id })
  }, teamSlug)
  if (!res.ok) {
    const text = await res.text()
    throw new Error(text || 'Failed to promote job')
  }
  return res.json()
}

const forkJob = async (id: string, teamSlug?: string): Promise<Record<string, unknown>> => {
  const res = await teamFetch('/api/scheduler/jobs/fork', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ id })
  }, teamSlug)
  if (!res.ok) {
    const text = await res.text()
    throw new Error(text || 'Failed to fork job')
  }
  return res.json()
}

const updateJob = async (id: string, data: Record<string, unknown>, teamSlug?: string): Promise<Record<string, unknown>> => {
  const res = await teamFetch(`/api/scheduler/jobs/${encodeURIComponent(id)}`, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(data)
  }, teamSlug)
  if (!res.ok) {
    const text = await res.text()
    throw new Error(text || 'Failed to update job')
  }
  return res.json()
}

const deleteJob = async (id: string, teamSlug?: string): Promise<Record<string, unknown>> => {
  const res = await teamFetch(`/api/scheduler/jobs/${encodeURIComponent(id)}`, {
    method: 'DELETE'
  }, teamSlug)
  if (!res.ok) {
    const text = await res.text()
    throw new Error(text || 'Failed to delete job')
  }
  return res.json()
}

const runJobNow = async (id: string, teamSlug?: string): Promise<Record<string, unknown>> => {
  const res = await teamFetch(`/api/scheduler/jobs/${encodeURIComponent(id)}/run`, {
    method: 'POST'
  }, teamSlug)
  if (!res.ok) {
    const text = await res.text()
    throw new Error(text || 'Failed to run job')
  }
  return res.json()
}

function formatRelativeTime(dateStr: string): string {
  if (!dateStr) return 'Never'
  const date = new Date(dateStr)
  const now = new Date()
  const diffMs = date.getTime() - now.getTime()
  const absDiff = Math.abs(diffMs)

  if (absDiff < 60000) return diffMs > 0 ? 'in < 1 min' : '< 1 min ago'
  if (absDiff < 3600000) {
    const mins = Math.round(absDiff / 60000)
    return diffMs > 0 ? `in ${mins} min` : `${mins} min ago`
  }
  if (absDiff < 86400000) {
    const hours = Math.round(absDiff / 3600000)
    return diffMs > 0 ? `in ${hours}h` : `${hours}h ago`
  }
  const days = Math.round(absDiff / 86400000)
  return diffMs > 0 ? `in ${days}d` : `${days}d ago`
}

function StatusBadge({ status }: { status: string | undefined }) {
  if (!status || status === 'pending') {
    return (
      <span className="text-xs px-1.5 py-0.5 rounded" style={{ background: 'rgba(100,100,100,0.2)', color: 'var(--text-muted)' }}>
        pending
      </span>
    )
  }
  if (status === 'success') {
    return (
      <span className="text-xs px-1.5 py-0.5 rounded flex items-center gap-1" style={{ background: 'rgba(34, 197, 94, 0.15)', color: '#22c55e' }}>
        <CheckCircle size={10} /> success
      </span>
    )
  }
  return (
    <span className="text-xs px-1.5 py-0.5 rounded flex items-center gap-1" style={{ background: 'rgba(239, 68, 68, 0.1)', color: '#f87171' }}>
      <XCircle size={10} /> failed
    </span>
  )
}

function ModeBadge({ mode }: { mode: string }) {
  const isAdaptive = mode === 'adaptive'
  return (
    <span className="text-xs px-1.5 py-0.5 rounded" style={{
      background: isAdaptive ? 'var(--brand-muted)' : 'rgba(59, 130, 246, 0.15)',
      color: isAdaptive ? 'var(--brand)' : '#3b82f6'
    }}>
      {mode}
    </span>
  )
}

const SCOPE_COLORS: Record<string, string> = {
  personal: '#8b5cf6',
  team: '#3b82f6',
}

function ScopeBadge({ scope }: { scope: string }) {
  const color = SCOPE_COLORS[scope] || '#6b7280'
  return (
    <span className="text-[10px] px-1.5 py-0.5 rounded capitalize" style={{
      background: `${color}22`,
      color,
      border: `1px solid ${color}44`,
    }} title={scope === 'personal' ? 'Personal credentials (+ team fallback)' : 'Team credentials only'}>
      {scope}
    </span>
  )
}

const deliveryModeLabels: Record<string, string> = {
  owner: 'Owner Only',
  team: 'All Team Members',
  members: 'Specific Members',
  target: 'Direct Target',
}

const deliveryModeColors: Record<string, string> = {
  owner: '#8b5cf6',
  team: '#3b82f6',
  members: '#f59e0b',
  target: '#6b7280',
}

function DeliveryModeBadge({ mode }: { mode: string }) {
  const label = deliveryModeLabels[mode] || mode
  const color = deliveryModeColors[mode] || 'var(--text-muted)'
  return (
    <span className="text-xs px-1.5 py-0.5 rounded inline-flex items-center gap-1" style={{
      background: `${color}20`,
      color: color,
    }}>
      {label}
    </span>
  )
}

export default function SchedulerSettings({ config, onSaved, teamSlug, isPlatform = false }: SchedulerSettingsProps) {
  const [enabled, setEnabled] = useState(true)
  const [jobs, setJobs] = useState<SchedulerJob[]>([])
  const [isTeamAdmin, setIsTeamAdmin] = useState(false)
  const [jobsLoading, setJobsLoading] = useState(false)
  const [jobsError, setJobsError] = useState<string | null>(null)
  const [expandedJob, setExpandedJob] = useState<string | null>(null)
  const [runningJob, setRunningJob] = useState<string | null>(null)
  const [publishingJob, setPublishingJob] = useState<string | null>(null)
  const [forkingJob, setForkingJob] = useState<string | null>(null)
  const [deleteConfirm, setDeleteConfirm] = useState<SchedulerJob | null>(null)
  const [actionError, setActionError] = useState<string | null>(null)
  const [teamMembers, setTeamMembers] = useState<TeamMember[]>([])
  const [membersLoading, setMembersLoading] = useState(false)

  // Config save state
  const [saving, setSaving] = useState(false)
  const [saveSuccess, setSaveSuccess] = useState(false)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    if (config) {
      setEnabled(config.enabled !== false)
    }
  }, [config])

  const loadJobs = useCallback(async () => {
    setJobsLoading(true)
    setJobsError(null)
    try {
      const data = await fetchJobs(teamSlug)
      setJobs(data.jobs || [])
      setIsTeamAdmin(!!data.is_team_admin)
    } catch (err: any) {
      setJobsError(err.message)
    } finally {
      setJobsLoading(false)
    }
  }, [teamSlug])

  useEffect(() => {
    loadJobs()
  }, [loadJobs])

  // Load team members when a job is expanded (for delivery configuration)
  const loadTeamMembers = useCallback(async () => {
    if (teamMembers.length > 0) return // already loaded
    setMembersLoading(true)
    try {
      const res = await teamFetch('/api/team/members/channels', undefined, teamSlug)
      if (res.ok) {
        const data = await res.json()
        setTeamMembers(data.members || [])
      }
    } catch {
      // Non-critical — member picker won't show
    } finally {
      setMembersLoading(false)
    }
  }, [teamSlug, teamMembers.length])

  useEffect(() => {
    if (expandedJob) {
      loadTeamMembers()
    }
  }, [expandedJob, loadTeamMembers])

  const handleSaveConfig = async () => {
    setSaving(true)
    setSaveSuccess(false)
    setError(null)
    try {
      await saveFullConfigSection('scheduler', { enabled })
      setSaveSuccess(true)
      if (onSaved) onSaved()
      setTimeout(() => setSaveSuccess(false), 2000)
    } catch (err: any) {
      setError(err.message)
    } finally {
      setSaving(false)
    }
  }

  const handleToggleJob = async (job: SchedulerJob) => {
    setActionError(null)
    try {
      await updateJob(job.id, { ...job, enabled: !job.enabled }, teamSlug)
      loadJobs()
    } catch (err: any) {
      setActionError(err.message)
    }
  }

  const handleRunJob = async (job: SchedulerJob) => {
    setRunningJob(job.id)
    setActionError(null)
    try {
      await runJobNow(job.id, teamSlug)
      // Reload after a brief delay to show updated status
      setTimeout(() => loadJobs(), 1000)
    } catch (err: any) {
      setActionError(err.message)
    } finally {
      setRunningJob(null)
    }
  }

  const handleDeleteJob = async (id: string) => {
    setActionError(null)
    try {
      await deleteJob(id, teamSlug)
      setDeleteConfirm(null)
      if (expandedJob === id) setExpandedJob(null)
      loadJobs()
    } catch (err: any) {
      setActionError(err.message)
    }
  }

  const handlePromoteJob = async (job: SchedulerJob) => {
    setPublishingJob(job.id)
    setActionError(null)
    try {
      await publishJob(job.id, teamSlug)
      if (expandedJob === job.id) setExpandedJob(null)
      await loadJobs()
    } catch (err: any) {
      setActionError(err.message)
    } finally {
      setPublishingJob(null)
    }
  }

  const handleForkJob = async (job: SchedulerJob) => {
    setForkingJob(job.id)
    setActionError(null)
    try {
      await forkJob(job.id, teamSlug)
      await loadJobs()
    } catch (err: any) {
      setActionError(err.message)
    } finally {
      setForkingJob(null)
    }
  }

  const handleChangeDeliveryMode = async (job: SchedulerJob, newMode: string) => {
    if (job.scope === 'personal' && newMode !== 'owner') {
      setActionError('Personal jobs can only deliver to the owner. Promote to team for shared delivery.')
      return
    }
    setActionError(null)
    try {
      const updatedDelivery = { ...(job.delivery || {}), mode: newMode }
      await updateJob(job.id, { delivery: updatedDelivery }, teamSlug)
      loadJobs()
    } catch (err: any) {
      setActionError(err.message)
    }
  }

  const handleToggleChannelFilter = async (job: SchedulerJob, channelType: string) => {
    setActionError(null)
    try {
      const currentFilter = job.delivery?.channel_filter || []
      let newFilter: string[]
      if (currentFilter.includes(channelType)) {
        newFilter = currentFilter.filter(c => c !== channelType)
      } else {
        newFilter = [...currentFilter, channelType]
      }
      const updatedDelivery = { ...(job.delivery || {}), channel_filter: newFilter }
      await updateJob(job.id, { delivery: updatedDelivery }, teamSlug)
      loadJobs()
    } catch (err: any) {
      setActionError(err.message)
    }
  }

  const handleToggleMember = async (job: SchedulerJob, memberID: string) => {
    setActionError(null)
    try {
      const currentMembers = job.delivery?.member_ids || []
      let newMembers: string[]
      if (currentMembers.includes(memberID)) {
        newMembers = currentMembers.filter(m => m !== memberID)
      } else {
        newMembers = [...currentMembers, memberID]
      }
      const updatedDelivery = { ...(job.delivery || {}), member_ids: newMembers }
      await updateJob(job.id, { delivery: updatedDelivery }, teamSlug)
      loadJobs()
    } catch (err: any) {
      setActionError(err.message)
    }
  }

  const handleToggleMemberChannel = async (job: SchedulerJob, memberID: string, channelType: string) => {
    setActionError(null)
    try {
      const currentMemberChannels = { ...(job.delivery?.member_channels || {}) }
      const memberChannels = currentMemberChannels[memberID] || []
      let newChannels: string[]
      if (memberChannels.includes(channelType)) {
        newChannels = memberChannels.filter(c => c !== channelType)
      } else {
        newChannels = [...memberChannels, channelType]
      }
      if (newChannels.length === 0) {
        delete currentMemberChannels[memberID]
      } else {
        currentMemberChannels[memberID] = newChannels
      }
      const updatedDelivery = { ...(job.delivery || {}), member_channels: currentMemberChannels }
      await updateJob(job.id, { delivery: updatedDelivery }, teamSlug)
      loadJobs()
    } catch (err: any) {
      setActionError(err.message)
    }
  }

  const personalJobs = isPlatform
    ? jobs.filter(j => j.scope === 'personal' || !j.scope)
    : jobs
  const teamJobs = isPlatform ? jobs.filter(j => j.scope === 'team') : []

  const renderJobCards = (sectionJobs: SchedulerJob[], sectionScope?: string) => {
    if (!jobsLoading && sectionJobs.length === 0 && !jobsError) {
      return (
        <div className="py-6 text-center rounded-lg" style={{ background: 'var(--bg-secondary)', border: '1px solid var(--border-color)' }}>
          <Clock size={28} className="mx-auto mb-2" style={{ color: 'var(--text-muted)', opacity: 0.5 }} />
          <p className="text-sm" style={hintStyle}>
            No {sectionScope ? `${sectionScope} ` : ''}scheduled jobs yet.
          </p>
          <p className="text-xs mt-1" style={hintStyle}>
            {sectionScope === 'personal'
              ? 'Ask the AI to schedule a task — new jobs default to personal scope.'
              : sectionScope === 'team'
                ? 'Promote a personal job, fork a team job to personal, or create a team job as a team admin.'
                : 'Jobs are created through chat. Ask the AI to schedule a task for you.'}
          </p>
        </div>
      )
    }
    return (
      <div className="space-y-2">
        {sectionJobs.map(job => {
          const isExpanded = expandedJob === job.id
          const isRunning = runningJob === job.id
          const isPublishing = publishingJob === job.id
          const isForking = forkingJob === job.id
          const isPersonalJob = (job.scope === 'personal') || (!!sectionScope && sectionScope === 'personal' && !job.scope)
          const isTeamJob = job.scope === 'team' || sectionScope === 'team'
          const deliveryModes = isPersonalJob ? ['owner'] : ['owner', 'team', 'members', 'target']

          return (
              <div
                key={job.id}
                className="rounded-lg border transition-all"
                style={{
                  background: 'var(--bg-secondary)',
                  borderColor: isExpanded ? 'rgba(168, 85, 247, 0.4)' : 'var(--border-color)'
                }}
              >
                {/* Job Header */}
                <div
                  className="px-4 py-3 cursor-pointer flex items-center gap-3"
                  onClick={() => setExpandedJob(isExpanded ? null : job.id)}
                >
                  {isExpanded
                    ? <ChevronDown size={14} style={{ color: 'var(--text-muted)' }} />
                    : <ChevronRight size={14} style={{ color: 'var(--text-muted)' }} />
                  }

                  {/* Enable/Disable indicator */}
                  <div className="flex-shrink-0" title={job.enabled ? 'Enabled' : 'Disabled'}>
                    {job.enabled ? (
                      <CheckCircle size={16} style={{ color: '#22c55e' }} />
                    ) : (
                      <Pause size={16} style={{ color: 'var(--text-muted)' }} />
                    )}
                  </div>

                  {/* Name & badges */}
                  <div className="flex-1 min-w-0">
                    <div className="flex items-center gap-2">
                      <span className="text-sm font-medium truncate" style={{ color: job.enabled ? 'var(--text-primary)' : 'var(--text-muted)' }}>
                        {job.name}
                      </span>
                      <ModeBadge mode={job.mode} />
                      {job.scope && <ScopeBadge scope={job.scope} />}
                      <StatusBadge status={job.last_status} />
                    </div>
                    <div className="text-xs flex items-center gap-3 mt-0.5" style={hintStyle}>
                      <span className="font-mono">{job.schedule?.cron}</span>
                      {job.schedule?.timezone && (
                        <span>{job.schedule.timezone}</span>
                      )}
                      {job.next_run && job.enabled && (
                        <span>Next: {formatRelativeTime(job.next_run)}</span>
                      )}
                    </div>
                  </div>

                  {/* Quick actions */}
                  <div className="flex items-center gap-1 flex-shrink-0" onClick={(e) => e.stopPropagation()}>
                    <button
                      onClick={() => handleToggleJob(job)}
                      className="p-1.5 rounded-lg transition-colors hover:bg-gray-600/30"
                      style={{ color: 'var(--text-muted)' }}
                      title={job.enabled ? 'Disable' : 'Enable'}
                    >
                      {job.enabled ? <Pause size={14} /> : <Play size={14} />}
                    </button>
                    <button
                      onClick={() => handleRunJob(job)}
                      disabled={isRunning}
                      className="p-1.5 rounded-lg transition-colors hover:bg-gray-600/30 disabled:opacity-50"
                      style={{ color: 'var(--accent)' }}
                      title="Run now"
                    >
                      {isRunning ? <Loader2 size={14} className="animate-spin" /> : <Play size={14} />}
                    </button>

                    {isPersonalJob && isTeamAdmin && (
                      <button
                        onClick={() => handlePromoteJob(job)}
                        disabled={isPublishing}
                        className="p-1.5 rounded-lg transition-colors hover:bg-gray-600/30 disabled:opacity-50"
                        style={{ color: 'var(--text-muted)' }}
                        title="Promote to Team"
                      >
                        {isPublishing ? <Loader2 size={14} className="animate-spin" /> : <Upload size={14} />}
                      </button>
                    )}

                    {isPlatform && isTeamJob && isTeamAdmin && (
                      <button
                        onClick={() => handleForkJob(job)}
                        disabled={isForking}
                        className="p-1.5 rounded-lg transition-colors hover:bg-gray-600/30 disabled:opacity-50"
                        style={{ color: 'var(--text-muted)' }}
                        title="Fork to Personal"
                      >
                        {isForking ? <Loader2 size={14} className="animate-spin" /> : <Download size={14} />}
                      </button>
                    )}

                    <button
                      onClick={() => setDeleteConfirm(job)}
                      className="p-1.5 rounded-lg transition-colors hover:bg-red-600/20"
                      style={{ color: 'var(--text-muted)' }}
                      title="Delete"
                    >
                      <Trash2 size={14} />
                    </button>
                  </div>
                </div>

                {/* Expanded Details */}
                {isExpanded && (
                  <div className="px-4 pb-4 border-t space-y-3" style={sectionBorderStyle}>
                    <div className="pt-3 grid grid-cols-2 gap-4 text-xs">
                      <div>
                        <div className="font-medium mb-1" style={hintStyle}>Mode</div>
                        <div style={{ color: 'var(--text-primary)' }}>
                          {job.mode === 'adaptive' ? 'Adaptive (LLM agent)' : 'Routine (headless flow)'}
                        </div>
                      </div>
                      <div>
                        <div className="font-medium mb-1" style={hintStyle}>Schedule</div>
                        <div className="font-mono" style={{ color: 'var(--text-primary)' }}>
                          {job.schedule?.cron}
                          {job.schedule?.timezone && <span className="ml-1 font-sans" style={hintStyle}>({job.schedule.timezone})</span>}
                        </div>
                      </div>
                      <div>
                        <div className="font-medium mb-1" style={hintStyle}>Last Run</div>
                        <div style={{ color: 'var(--text-primary)' }}>
                          {job.last_run ? formatRelativeTime(job.last_run) : 'Never'}
                        </div>
                      </div>
                      <div>
                        <div className="font-medium mb-1" style={hintStyle}>Next Run</div>
                        <div style={{ color: 'var(--text-primary)' }}>
                          {job.enabled && job.next_run ? formatRelativeTime(job.next_run) : 'Disabled'}
                        </div>
                      </div>
                      {job.delivery?.channel && (
                        <div>
                          <div className="font-medium mb-1" style={hintStyle}>Delivery</div>
                          <div style={{ color: 'var(--text-primary)' }}>
                            {job.delivery.channel}{job.delivery.target ? ` (${job.delivery.target})` : ''}
                          </div>
                        </div>
                      )}
                      {job.delivery?.mode && (
                        <div>
                          <div className="font-medium mb-1" style={hintStyle}>Delivery Mode</div>
                          <div style={{ color: 'var(--text-primary)' }}>
                            <DeliveryModeBadge mode={job.delivery.mode} />
                          </div>
                        </div>
                      )}
                      {job.owner_id && (
                        <div>
                          <div className="font-medium mb-1" style={hintStyle}>Owner</div>
                          <div className="font-mono text-xs truncate" style={{ color: 'var(--text-primary)' }}>
                            {job.owner_id}
                          </div>
                        </div>
                      )}
                      {(job.consecutive_failures ?? 0) > 0 && (
                        <div>
                          <div className="font-medium mb-1" style={hintStyle}>Failures</div>
                          <div className="flex items-center gap-1" style={{ color: '#f87171' }}>
                            <AlertTriangle size={12} /> {job.consecutive_failures} consecutive
                          </div>
                        </div>
                      )}
                    </div>

                    {/* Payload */}
                    {job.mode === 'adaptive' && job.payload?.instructions && (
                      <div>
                        <div className="text-xs font-medium mb-1" style={hintStyle}>Instructions</div>
                        <div className="p-2 rounded text-xs font-mono whitespace-pre-wrap" style={{ background: 'var(--bg-primary)', color: 'var(--text-secondary)' }}>
                          {job.payload.instructions}
                        </div>
                      </div>
                    )}
                    {job.mode === 'routine' && job.payload?.flow && (
                      <div>
                        <div className="text-xs font-medium mb-1" style={hintStyle}>Flow</div>
                        <div className="text-xs font-mono" style={{ color: 'var(--text-primary)' }}>
                          {job.payload.flow}
                          {job.payload.params && Object.keys(job.payload.params).length > 0 && (
                            <span style={hintStyle}> with {Object.keys(job.payload.params).length} param(s)</span>
                          )}
                        </div>
                      </div>
                    )}

                    {/* Last Error */}
                    {job.last_error && (
                      <div>
                        <div className="text-xs font-medium mb-1" style={{ color: '#f87171' }}>Last Error</div>
                        <div className="p-2 rounded text-xs font-mono whitespace-pre-wrap" style={{ background: 'rgba(239, 68, 68, 0.05)', color: '#f87171' }}>
                          {job.last_error}
                        </div>
                      </div>
                    )}

                    {/* Delivery Configuration */}
                    <div className="border-t pt-3 space-y-3" style={sectionBorderStyle}>
                      {/* Delivery Mode */}
                      <div>
                        <div className="text-xs font-medium mb-2" style={hintStyle}>Delivery Mode</div>
                        <div className="flex flex-wrap gap-1.5">
                          {deliveryModes.map(mode => (
                            <button
                              key={mode}
                              onClick={() => handleChangeDeliveryMode(job, mode)}
                              className="text-xs px-2.5 py-1 rounded-lg transition-all"
                              style={{
                                background: (job.delivery?.mode || '') === mode
                                  ? `${deliveryModeColors[mode]}30`
                                  : 'var(--bg-tertiary)',
                                color: (job.delivery?.mode || '') === mode
                                  ? deliveryModeColors[mode]
                                  : 'var(--text-muted)',
                                border: `1px solid ${(job.delivery?.mode || '') === mode
                                  ? deliveryModeColors[mode]
                                  : 'var(--border-color)'}`,
                              }}
                            >
                              {deliveryModeLabels[mode]}
                            </button>
                          ))}
                        </div>
                      </div>

                      {/* Channel Filter — show for owner/team/members modes */}
                      {job.delivery?.mode && job.delivery.mode !== 'target' && (
                        <div>
                          <div className="text-xs font-medium mb-2 flex items-center gap-1" style={hintStyle}>
                            <Hash size={11} /> Delivery Channels
                          </div>
                          <div className="flex flex-wrap gap-1.5">
                            {['telegram', 'email', 'slack'].map(ch => {
                              const isActive = !job.delivery?.channel_filter?.length || job.delivery.channel_filter.includes(ch)
                              const channelIcons: Record<string, any> = { telegram: MessageSquare, email: Mail, slack: Hash }
                              const Icon = channelIcons[ch] || Hash
                              return (
                                <button
                                  key={ch}
                                  onClick={() => handleToggleChannelFilter(job, ch)}
                                  className="text-xs px-2.5 py-1 rounded-lg transition-all flex items-center gap-1"
                                  style={{
                                    background: isActive ? 'rgba(34, 197, 94, 0.15)' : 'var(--bg-tertiary)',
                                    color: isActive ? '#22c55e' : 'var(--text-muted)',
                                    border: `1px solid ${isActive ? '#22c55e' : 'var(--border-color)'}`,
                                  }}
                                >
                                  <Icon size={11} /> {ch}
                                </button>
                              )
                            })}
                          </div>
                          <p className="text-xs mt-1" style={hintStyle}>
                            {!job.delivery?.channel_filter?.length
                              ? 'All linked channels (no filter)'
                              : `Only: ${job.delivery.channel_filter.join(', ')}`}
                          </p>
                        </div>
                      )}

                      {/* Member Selector — show for "members" mode */}
                      {job.delivery?.mode === 'members' && (
                        <div>
                          <div className="text-xs font-medium mb-2 flex items-center gap-1" style={hintStyle}>
                            <Users size={11} /> Recipients
                          </div>
                          {membersLoading ? (
                            <div className="flex items-center gap-2 py-2">
                              <Loader2 size={12} className="animate-spin" style={{ color: 'var(--accent)' }} />
                              <span className="text-xs" style={hintStyle}>Loading team members...</span>
                            </div>
                          ) : teamMembers.length === 0 ? (
                            <p className="text-xs" style={hintStyle}>No team members found.</p>
                          ) : (
                            <div className="space-y-1.5">
                              {teamMembers.map(member => {
                                const isSelected = (job.delivery?.member_ids || []).includes(member.user_id)
                                const memberChannelOverrides = job.delivery?.member_channels?.[member.user_id] || []
                                return (
                                  <div key={member.user_id} className="rounded-lg p-2" style={{
                                    background: isSelected ? 'rgba(168, 85, 247, 0.08)' : 'transparent',
                                    border: `1px solid ${isSelected ? 'rgba(168, 85, 247, 0.3)' : 'var(--border-color)'}`,
                                  }}>
                                    <div className="flex items-center gap-2">
                                      <button
                                        onClick={() => handleToggleMember(job, member.user_id)}
                                        className="flex-shrink-0 w-4 h-4 rounded border flex items-center justify-center"
                                        style={{
                                          borderColor: isSelected ? 'var(--brand)' : 'var(--border-color)',
                                          background: isSelected ? 'var(--brand)' : 'transparent',
                                        }}
                                      >
                                        {isSelected && <Check size={10} color="white" />}
                                      </button>
                                      <div className="flex-1 min-w-0">
                                        <span className="text-xs font-medium" style={{ color: 'var(--text-primary)' }}>
                                          {member.display_name || member.email}
                                        </span>
                                        {member.display_name && (
                                          <span className="text-xs ml-1.5" style={hintStyle}>{member.email}</span>
                                        )}
                                      </div>
                                      {/* Show linked channel badges */}
                                      <div className="flex gap-1 flex-shrink-0">
                                        {(member.linked_channels || []).map(ch => (
                                          <span key={ch} className="text-xs px-1 py-0.5 rounded" style={{
                                            background: 'rgba(100,100,100,0.15)',
                                            color: 'var(--text-muted)',
                                            fontSize: '10px',
                                          }}>
                                            {ch}
                                          </span>
                                        ))}
                                      </div>
                                    </div>
                                    {/* Per-member channel selection (only when selected) */}
                                    {isSelected && (member.linked_channels || []).length > 1 && (
                                      <div className="mt-1.5 ml-6 flex items-center gap-1.5">
                                        <span className="text-xs" style={{ ...hintStyle, fontSize: '10px' }}>via:</span>
                                        {(member.linked_channels || []).map(ch => {
                                          const isChActive = memberChannelOverrides.length === 0 || memberChannelOverrides.includes(ch)
                                          return (
                                            <button
                                              key={ch}
                                              onClick={() => handleToggleMemberChannel(job, member.user_id, ch)}
                                              className="text-xs px-1.5 py-0.5 rounded transition-all"
                                              style={{
                                                background: isChActive ? 'rgba(34, 197, 94, 0.15)' : 'rgba(100,100,100,0.1)',
                                                color: isChActive ? '#22c55e' : 'var(--text-muted)',
                                                border: `1px solid ${isChActive ? 'rgba(34, 197, 94, 0.4)' : 'transparent'}`,
                                                fontSize: '10px',
                                              }}
                                            >
                                              {ch}
                                            </button>
                                          )
                                        })}
                                        {memberChannelOverrides.length === 0 && (
                                          <span className="text-xs" style={{ ...hintStyle, fontSize: '10px' }}>(all)</span>
                                        )}
                                      </div>
                                    )}
                                  </div>
                                )
                              })}
                            </div>
                          )}
                        </div>
                      )}
                    </div>

                    <div className="text-xs font-mono pt-1" style={{ color: 'var(--text-muted)', opacity: 0.5 }}>
                      ID: {job.id}
                    </div>
                  </div>
                )}
              </div>
          )
        })}
      </div>
    )
  }

  return (
    <div className="max-w-2xl space-y-6">
      {/* Master Toggle */}
      <div className="flex items-center justify-between">
        <div>
          <label className="text-sm font-medium" style={{ color: 'var(--text-primary)' }}>
            Enable Scheduler
          </label>
          <p className="text-xs mt-0.5" style={hintStyle}>
            Run scheduled jobs automatically via cron expressions. Default: enabled.
          </p>
        </div>
        <button
          onClick={() => setEnabled(!enabled)}
          className="relative w-11 h-6 rounded-full transition-colors"
          style={{
            background: enabled ? 'var(--brand)' : 'var(--bg-tertiary)',
            border: `1px solid ${enabled ? 'var(--brand)' : 'var(--border-color)'}`
          }}
        >
          <span
            className="absolute top-0.5 left-0.5 w-4 h-4 rounded-full transition-transform bg-white"
            style={{ transform: enabled ? 'translateX(20px)' : 'translateX(0)' }}
          />
        </button>
      </div>

      {/* Save Config */}
      <div className="flex items-center gap-3">
        <button
          onClick={handleSaveConfig}
          disabled={saving}
          className="flex items-center gap-2 px-4 py-2 rounded-lg text-white font-medium transition-all shadow-md hover:shadow-lg hover:scale-[1.02] active:scale-95 disabled:opacity-50"
          style={saveButtonStyle}
        >
          <Save size={16} />
          {saving ? 'Saving...' : 'Save'}
        </button>
        {saveSuccess && (
          <span className="flex items-center gap-1 text-green-400 text-sm">
            <Check size={16} /> Saved
          </span>
        )}
        {error && (
          <span className="flex items-center gap-1 text-sm" style={{ color: 'var(--danger)' }}>
            <AlertCircle size={16} /> {error}
          </span>
        )}
      </div>

      {actionError && (
        <div className="flex items-center gap-2 p-3 rounded-lg text-sm"
          style={{ background: 'rgba(239, 68, 68, 0.1)', color: 'var(--danger)' }}>
          <AlertCircle size={14} /> {actionError}
          <button onClick={() => setActionError(null)} className="ml-auto p-0.5"><X size={14} /></button>
        </div>
      )}

      {jobsLoading && (
        <div className="flex items-center gap-2 py-4">
          <Loader2 size={16} className="animate-spin" style={{ color: 'var(--accent)' }} />
          <span className="text-sm" style={hintStyle}>Loading jobs...</span>
        </div>
      )}

      {jobsError && (
        <div className="flex items-center gap-2 p-3 rounded-lg text-sm"
          style={{ background: 'rgba(239, 68, 68, 0.1)', color: 'var(--danger)' }}>
          <AlertCircle size={14} /> {jobsError}
        </div>
      )}

      {/* Jobs — Credentials-style Personal / Team sections in platform mode */}
      <div className="pt-4 border-t space-y-4" style={sectionBorderStyle}>
        {isPlatform ? (
          <>
            <div className="rounded-xl p-4" style={{ background: 'var(--bg-primary)', border: '1px solid var(--border-color)' }}>
              <div className="mb-3">
                <h3 className="text-sm font-medium flex items-center gap-2 mb-1" style={{ color: 'var(--text-primary)' }}>
                  <ScopeBadge scope="personal" />
                  Personal Jobs ({personalJobs.length})
                </h3>
                <p className="text-xs" style={hintStyle}>
                  Your private schedules. Run with personal credentials (team credentials as fallback). Delivery is limited to you.
                </p>
              </div>
              {!jobsLoading && renderJobCards(personalJobs, 'personal')}
            </div>
            <div className="rounded-xl p-4" style={{ background: 'var(--bg-primary)', border: '1px solid var(--border-color)' }}>
              <div className="mb-3">
                <h3 className="text-sm font-medium flex items-center gap-2 mb-1" style={{ color: 'var(--text-primary)' }}>
                  <ScopeBadge scope="team" />
                  Team Jobs ({teamJobs.length})
                </h3>
                <p className="text-xs" style={hintStyle}>
                  Shared team schedules. Use team credentials only. Team admins manage these jobs.
                </p>
              </div>
              {!jobsLoading && renderJobCards(teamJobs, 'team')}
            </div>
          </>
        ) : (
          <>
            <h4 className="text-sm font-medium" style={{ color: 'var(--text-primary)' }}>
              Scheduled Jobs
              {!jobsLoading && jobs.length > 0 && (
                <span className="ml-2 text-xs font-normal" style={hintStyle}>
                  {jobs.filter(j => j.enabled).length} active, {jobs.length} total
                </span>
              )}
            </h4>
            {!jobsLoading && renderJobCards(jobs)}
          </>
        )}
      </div>

      {/* Delete Confirmation */}      {/* Delete Confirmation */}
      {deleteConfirm && (
        <div className="fixed inset-0 z-50 flex items-center justify-center p-4" style={{ background: 'rgba(0,0,0,0.7)' }}>
          <div className="rounded-xl w-full max-w-sm p-6 shadow-2xl"
            style={{ background: 'var(--bg-secondary)', border: '1px solid var(--border-color)' }}>
            <h3 className="text-lg font-semibold mb-2" style={{ color: 'var(--text-primary)' }}>Delete Job</h3>
            <p className="text-sm mb-4" style={hintStyle}>
              Are you sure you want to delete <strong style={{ color: 'var(--text-primary)' }}>{deleteConfirm.name}</strong>? This cannot be undone.
            </p>
            <div className="flex justify-end gap-3">
              <button
                onClick={() => setDeleteConfirm(null)}
                className="px-4 py-2 rounded-lg text-sm font-medium"
                style={{ color: 'var(--text-secondary)', background: 'var(--bg-tertiary)', border: '1px solid var(--border-color)' }}
              >
                Cancel
              </button>
              <button
                onClick={() => handleDeleteJob(deleteConfirm.id)}
                className="flex items-center gap-2 px-4 py-2 rounded-lg text-white text-sm font-medium transition-all"
                style={{ background: '#ef4444' }}
              >
                <Trash2 size={14} /> Delete
              </button>
            </div>
          </div>
        </div>
      )}
    </div>
  )
}
