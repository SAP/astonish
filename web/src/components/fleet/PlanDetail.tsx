import { useCallback, useEffect, useState } from 'react'
import { AlertCircle, ArrowRight, Code, Copy, GitBranch, Loader, Play, Radio, Rocket, RotateCcw, Trash2 } from 'lucide-react'

import YamlDrawer from '../YamlDrawer'
import {
  activateFleetPlan, deactivateFleetPlan, deleteFleetPlan, duplicateFleetPlan,
  fetchFleetPlan, fetchFleetPlanYaml, getFleetPlanStatus, patchFleetPlanAgent,
  retryFleetIssue, saveFleetPlan, saveFleetPlanYaml, startFleetSession,
} from '../../api/fleetChat'
import { buildPath } from '../../hooks/useHashRouter'
import type { CommFlowNode, FleetAgentDef, FleetArtifactDef, FleetPlanData, FleetPlanStatusExt, FleetSettings } from './fleetUtils'
import { addAgentToFleetConfig, formatTimeAgo, getAgentColor, removeAgentFromFleetConfig, renameAgentInFleetConfig, updateFleetSettings } from './fleetUtils'
import { FleetAgentsEditor, AgentEditorPanel, FleetDetailTabs, FleetSettingsEditor } from './FleetConfigEditor'
import { useFleetDetailTab } from './fleetHooks'

interface PlanDetailProps {
  planKey: string
  onNavigate: (path: string) => void
  onRefresh?: () => void
  theme: string
}

export default function PlanDetail({ planKey, onNavigate, onRefresh, theme }: PlanDetailProps) {
  const [plan, setPlan] = useState<FleetPlanData | null>(null)
  const [planStatus, setPlanStatus] = useState<FleetPlanStatusExt | null>(null)
  const [yamlContent, setYamlContent] = useState('')
  const [showYaml, setShowYaml] = useState(false)
  const [isLoading, setIsLoading] = useState(true)
  const [isSaving, setIsSaving] = useState(false)
  const [isActivating, setIsActivating] = useState(false)
  const [isDuplicating, setIsDuplicating] = useState(false)
  const [isDeleting, setIsDeleting] = useState(false)
  const [isLaunching, setIsLaunching] = useState(false)
  const [launchMessage, setLaunchMessage] = useState('')
  const [showLaunchDialog, setShowLaunchDialog] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [statusError, setStatusError] = useState<string | null>(null)
  const [saveStatus, setSaveStatus] = useState<'saved' | 'error' | null>(null)
  const [retryingIssue, setRetryingIssue] = useState<number | null>(null)
  const [tab, setTab] = useFleetDetailTab('plan', planKey)
  const [editingAgentKey, setEditingAgentKey] = useState<string | null>(null)

  const loadPlan = useCallback(async () => {
    setIsLoading(true)
    setError(null)
    try {
      const [planData, yamlData] = await Promise.all([
        fetchFleetPlan(planKey),
        fetchFleetPlanYaml(planKey),
      ])
      const nextPlan = planData.plan as FleetPlanData
      setPlan(nextPlan)
      setYamlContent(yamlData)
      if (nextPlan?.channel?.type && nextPlan.channel.type !== 'chat') {
        try {
          const status = await getFleetPlanStatus(planKey)
          setPlanStatus(status as FleetPlanStatusExt)
          setStatusError(null)
        } catch (statusErr) {
          setPlanStatus(null)
          setStatusError(statusErr instanceof Error ? statusErr.message : 'Failed to load activation status')
        }
      } else {
        setPlanStatus(null)
        setStatusError(null)
      }
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setIsLoading(false)
    }
  }, [planKey])

  useEffect(() => { loadPlan() }, [loadPlan])

  useEffect(() => {
    setEditingAgentKey(null)
    setShowYaml(false)
  }, [planKey])

  const handleTabChange = (next: typeof tab) => {
    setEditingAgentKey(null)
    setTab(next)
  }

  const handleActivateToggle = async () => {
    if (isActivating) return
    setIsActivating(true)
    try {
      if (planStatus?.activated) await deactivateFleetPlan(planKey)
      else await activateFleetPlan(planKey)
      const newStatus = await getFleetPlanStatus(planKey)
      setPlanStatus(newStatus as FleetPlanStatusExt)
      onRefresh?.()
    } catch (err) {
      alert('Activation failed: ' + (err instanceof Error ? err.message : String(err)))
    } finally {
      setIsActivating(false)
    }
  }

  const handleSaveYaml = async () => {
    if (isSaving) return
    setIsSaving(true)
    setSaveStatus(null)
    try {
      await saveFleetPlanYaml(planKey, yamlContent)
      setSaveStatus('saved')
      await loadPlan()
      onRefresh?.()
      setTimeout(() => setSaveStatus(null), 2000)
    } catch (err) {
      setSaveStatus('error')
      alert('Save failed: ' + (err instanceof Error ? err.message : String(err)))
    } finally {
      setIsSaving(false)
    }
  }

  const handleSaveSettings = async (settings: FleetSettings) => {
    if (!plan) return
    const nextPlan = updateFleetSettings(plan, settings)
    await saveFleetPlan(planKey, nextPlan as Record<string, unknown>)
    setPlan(nextPlan)
    onRefresh?.()
  }

  const handleSaveAgent = async (agentKey: string, agent: FleetAgentDef, nextKey?: string) => {
    if (!plan) return
    const targetKey = nextKey && nextKey !== agentKey ? nextKey : agentKey
    if (targetKey !== agentKey) {
      const renamed = renameAgentInFleetConfig(plan, agentKey, targetKey)
      const nextPlan: FleetPlanData = {
        ...renamed,
        agents: { ...(renamed.agents || {}), [targetKey]: agent },
      }
      await saveFleetPlan(planKey, nextPlan as Record<string, unknown>)
      setPlan(nextPlan)
      if (editingAgentKey === agentKey) setEditingAgentKey(targetKey)
    } else {
      await patchFleetPlanAgent(planKey, agentKey, agent as Record<string, unknown>)
      setPlan(prev => prev ? { ...prev, agents: { ...(prev.agents || {}), [agentKey]: agent } } : prev)
    }
    onRefresh?.()
  }

  const handleAddAgent = async (agentKey: string, agent: FleetAgentDef) => {
    if (!plan) return
    const nextPlan = addAgentToFleetConfig(plan, agentKey, agent)
    await saveFleetPlan(planKey, nextPlan as Record<string, unknown>)
    setPlan(nextPlan)
    onRefresh?.()
  }

  const handleDeleteAgent = async (agentKey: string) => {
    if (!plan) return
    const nextPlan = removeAgentFromFleetConfig(plan, agentKey)
    await saveFleetPlan(planKey, nextPlan as Record<string, unknown>)
    setPlan(nextPlan)
    onRefresh?.()
  }

  const handleDuplicate = async () => {
    if (isDuplicating) return
    setIsDuplicating(true)
    try {
      const result = await duplicateFleetPlan(planKey)
      onRefresh?.()
      onNavigate(buildPath('fleet', { subView: 'plan', subKey: result.key }))
    } catch (err) {
      alert('Duplicate failed: ' + (err instanceof Error ? err.message : String(err)))
    } finally {
      setIsDuplicating(false)
    }
  }

  const handleDelete = async () => {
    if (isDeleting || !plan) return
    if (!window.confirm(`Delete fleet plan "${plan.name || planKey}"? This cannot be undone.`)) return
    setIsDeleting(true)
    try {
      await deleteFleetPlan(planKey)
      onRefresh?.()
      onNavigate(buildPath('fleet'))
    } catch (err) {
      alert('Delete failed: ' + (err instanceof Error ? err.message : String(err)))
    } finally {
      setIsDeleting(false)
    }
  }

  const handleLaunch = async () => {
    if (isLaunching) return
    setIsLaunching(true)
    try {
      const result = await startFleetSession({ planKey, message: launchMessage })
      setShowLaunchDialog(false)
      setLaunchMessage('')
      onRefresh?.()
      const isChat = plan?.channel?.type === 'chat' || !plan?.channel?.type
      onNavigate(isChat
        ? buildPath('chat', { sessionId: result.session_id })
        : buildPath('fleet', { subView: 'session', subKey: result.session_id }))
    } catch (err) {
      alert('Launch failed: ' + (err instanceof Error ? err.message : String(err)))
    } finally {
      setIsLaunching(false)
    }
  }

  const handleRetryIssue = async (issueNumber: number) => {
    if (retryingIssue) return
    setRetryingIssue(issueNumber)
    try {
      const result = await retryFleetIssue(planKey, issueNumber)
      const newStatus = await getFleetPlanStatus(planKey)
      setPlanStatus(newStatus as FleetPlanStatusExt)
      onRefresh?.()
      if (result.session_id) onNavigate(buildPath('fleet', { subView: 'session', subKey: result.session_id }))
    } catch (err) {
      alert('Retry failed: ' + (err instanceof Error ? err.message : String(err)))
    } finally {
      setRetryingIssue(null)
    }
  }

  if (isLoading) {
    return (
      <div className="flex-1 flex items-center justify-center">
        <Loader size={24} className="animate-spin text-primary" />
      </div>
    )
  }

  if (error || !plan) {
    return (
      <div className="flex-1 flex items-center justify-center">
        <div className="text-center">
          <AlertCircle size={32} className="mx-auto mb-2 text-red-400" />
          <p className="text-sm" style={{ color: 'var(--text-muted)' }}>{error || 'Plan not found'}</p>
        </div>
      </div>
    )
  }

  const agents: [string, FleetAgentDef][] = plan.agents ? Object.entries(plan.agents) : []
  const commFlow: CommFlowNode[] = plan.communication?.flow || []
  const artifacts: [string, FleetArtifactDef][] = plan.artifacts ? Object.entries(plan.artifacts) : []
  const editingAgent = editingAgentKey ? agents.find(([key]) => key === editingAgentKey) || null : null
  const canDeleteAgent = agents.length > 1
  const bottomDock = Boolean(showYaml || editingAgent)

  return (
    <div className="flex-1 flex flex-col overflow-hidden">
      <div className={`${bottomDock ? 'h-1/2' : 'flex-1'} overflow-y-auto`}>
        <div className="max-w-4xl mx-auto p-6 space-y-6">
          <div className="flex items-start justify-between gap-4">
            <div>
              <h1 className="text-xl font-bold" style={{ color: 'var(--text-primary)' }}>{plan.name}</h1>
              {plan.description && <p className="text-sm mt-1" style={{ color: 'var(--text-secondary)' }}>{plan.description}</p>}
              <div className="flex items-center gap-3 mt-2">
                {plan.created_from && <span className="text-xs px-2 py-0.5 rounded" style={{ background: 'var(--bg-tertiary)', color: 'var(--text-muted)' }}>Base: {plan.created_from}</span>}
                <span className="text-xs px-2 py-0.5 rounded" style={{ background: 'var(--brand-muted)', color: 'var(--brand)' }}>{plan.channel?.type || 'chat'}</span>
                <span className="text-xs" style={{ color: 'var(--text-muted)' }}>{agents.length} agent{agents.length !== 1 ? 's' : ''}</span>
              </div>
            </div>
            <PlanActions
              showYaml={showYaml}
              isDuplicating={isDuplicating}
              isDeleting={isDeleting}
              onToggleYaml={() => {
                setEditingAgentKey(null)
                setShowYaml(v => !v)
              }}
              onLaunch={() => setShowLaunchDialog(true)}
              onDuplicate={handleDuplicate}
              onDelete={handleDelete}
            />
          </div>

          <FleetDetailTabs activeTab={tab} onChange={handleTabChange} />

          {tab === 'overview' && (
            <>
              <ActivationPanel
                plan={plan}
                status={planStatus}
                statusError={statusError}
                isActivating={isActivating}
                onToggle={handleActivateToggle}
                onRetryStatus={loadPlan}
              />
              <FailedSessions status={planStatus} retryingIssue={retryingIssue} onRetry={handleRetryIssue} onNavigate={onNavigate} />
              <RetryingSessions status={planStatus} />
              <CommunicationFlow flow={commFlow} />
              <Artifacts artifacts={artifacts} />
              <ChannelConfig config={plan.channel?.config} />
            </>
          )}

          {tab === 'settings' && (
            <FleetSettingsEditor settings={plan.settings || {}} onSave={handleSaveSettings} />
          )}

          {tab === 'agents' && (
            <FleetAgentsEditor
              agents={agents}
              fleetSettings={plan.settings}
              selectedKey={editingAgentKey}
              onSelectedKeyChange={(key) => {
                setShowYaml(false)
                setEditingAgentKey(key)
              }}
              onSaveAgent={handleSaveAgent}
              onAddAgent={handleAddAgent}
              onDeleteAgent={async (agentKey) => {
                await handleDeleteAgent(agentKey)
                if (editingAgentKey === agentKey) setEditingAgentKey(null)
              }}
            />
          )}
        </div>
      </div>

      {editingAgent && (
        <div className="h-1/2" style={{ borderTop: '1px solid var(--border-color)' }}>
          <AgentEditorPanel
            agentKey={editingAgent[0]}
            agent={editingAgent[1]}
            siblingAgentKeys={agents.map(([key]) => key).filter(key => key !== editingAgent[0])}
            canDelete={canDeleteAgent}
            onClose={() => setEditingAgentKey(null)}
            onSave={handleSaveAgent}
            onDelete={async () => {
              await handleDeleteAgent(editingAgent[0])
              setEditingAgentKey(null)
            }}
          />
        </div>
      )}

      {showYaml && !editingAgent && (
        <div className="h-1/2" style={{ borderTop: '1px solid var(--border-color)' }}>
          <YamlDrawer
            content={yamlContent}
            onChange={setYamlContent}
            onClose={() => setShowYaml(false)}
            theme={theme as 'dark' | 'light'}
            subtitle="Fleet plan configuration"
            onSave={handleSaveYaml}
            isSaving={isSaving}
            saveStatus={saveStatus}
          />
        </div>
      )}

      {showLaunchDialog && (
        <LaunchDialog
          planName={plan.name || planKey}
          message={launchMessage}
          isLaunching={isLaunching}
          onMessage={setLaunchMessage}
          onCancel={() => { setShowLaunchDialog(false); setLaunchMessage('') }}
          onLaunch={handleLaunch}
        />
      )}
    </div>
  )
}

function PlanActions({ showYaml, isDuplicating, isDeleting, onToggleYaml, onLaunch, onDuplicate, onDelete }: { showYaml: boolean; isDuplicating: boolean; isDeleting: boolean; onToggleYaml: () => void; onLaunch: () => void; onDuplicate: () => void; onDelete: () => void }) {
  return (
    <div className="flex items-center gap-2 flex-wrap justify-end">
      <button onClick={onToggleYaml} className="flex items-center gap-1.5 px-3 py-1.5 text-xs font-medium rounded-lg transition-colors" style={{ background: showYaml ? 'var(--brand-muted)' : 'var(--bg-tertiary)', color: showYaml ? 'var(--brand)' : 'var(--text-secondary)' }}>
        <Code size={12} /> {showYaml ? 'Hide Source' : 'Import/Export YAML'}
      </button>
      <button onClick={onLaunch} className="flex items-center gap-1.5 px-3 py-1.5 text-xs font-medium rounded-lg bg-primary hover:bg-primary/90 text-white transition-colors">
        <Play size={12} /> Launch
      </button>
      <button onClick={onDuplicate} disabled={isDuplicating} className="flex items-center gap-1.5 px-3 py-1.5 text-xs font-medium rounded-lg hover:bg-white/10 transition-colors disabled:opacity-50" style={{ color: 'var(--text-secondary)', border: '1px solid var(--border-color)' }}>
        <Copy size={12} /> {isDuplicating ? 'Duplicating...' : 'Duplicate'}
      </button>
      <button onClick={onDelete} disabled={isDeleting} className="flex items-center gap-1.5 px-3 py-1.5 text-xs font-medium rounded-lg hover:bg-red-500/20 transition-colors disabled:opacity-50 text-red-400" style={{ border: '1px solid rgba(239, 68, 68, 0.3)' }}>
        <Trash2 size={12} /> {isDeleting ? 'Deleting...' : 'Delete'}
      </button>
    </div>
  )
}

function ActivationPanel({ plan, status, statusError, isActivating, onToggle, onRetryStatus }: { plan: FleetPlanData; status: FleetPlanStatusExt | null; statusError: string | null; isActivating: boolean; onToggle: () => void; onRetryStatus: () => void }) {
  if (!plan.channel?.type || plan.channel.type === 'chat') return null
  if (statusError) {
    return (
      <div className="rounded-lg p-4" style={{ background: 'rgba(239, 68, 68, 0.08)', border: '1px solid rgba(239, 68, 68, 0.25)' }}>
        <div className="flex items-center justify-between gap-3">
          <div className="flex items-center gap-3">
            <AlertCircle size={16} className="text-red-400" />
            <div>
              <span className="text-sm font-medium text-red-400">Could not load activation status</span>
              <p className="text-xs mt-0.5" style={{ color: 'var(--text-muted)' }}>{statusError}</p>
            </div>
          </div>
          <button onClick={onRetryStatus} className="px-3 py-1.5 text-xs font-medium rounded-lg" style={{ background: 'var(--bg-tertiary)', color: 'var(--text-secondary)' }}>Retry</button>
        </div>
      </div>
    )
  }

  if (status?.activated) {
    return (
      <div className="rounded-lg p-4" style={{ background: 'rgba(34, 197, 94, 0.08)', border: '1px solid rgba(34, 197, 94, 0.25)' }}>
        <div className="flex items-center justify-between">
          <div className="flex items-center gap-3">
            <div className="w-3 h-3 rounded-full bg-green-400 animate-pulse" />
            <span className="text-sm font-medium" style={{ color: 'var(--text-primary)' }}>Channel Monitoring <span className="text-xs ml-2 text-green-400">Active</span></span>
          </div>
          <button onClick={onToggle} disabled={isActivating} className="px-4 py-1.5 text-xs font-medium rounded-lg bg-red-600/20 text-red-400 hover:bg-red-600/30 disabled:opacity-50">{isActivating ? 'Working...' : 'Deactivate'}</button>
        </div>
        <div className="mt-3 grid grid-cols-3 gap-4 text-xs" style={{ color: 'var(--text-muted)' }}>
          <InfoBlock label="Last Poll" value={status.last_poll_at ? `${formatTimeAgo(status.last_poll_at)}${status.last_poll_status ? ` (${status.last_poll_status})` : ''}` : 'Never'} />
          <InfoBlock label="Sessions Started" value={String(status.sessions_started || 0)} />
          <InfoBlock label="Schedule" value={plan.channel?.schedule || 'Default'} />
          {status.last_poll_error && <div className="col-span-3 text-red-400">Error: {status.last_poll_error}</div>}
        </div>
        {status.last_start_error && (
          <div className="mt-3 rounded-lg p-3" style={{ background: 'rgba(239, 68, 68, 0.08)', border: '1px solid rgba(239, 68, 68, 0.25)' }}>
            <div className="flex items-center gap-2 mb-1">
              <AlertCircle size={14} className="text-red-400 flex-shrink-0" />
              <span className="text-xs font-semibold text-red-400">Session Start Error</span>
              {status.last_start_error_at && <span className="text-[10px]" style={{ color: 'var(--text-muted)' }}>{formatTimeAgo(status.last_start_error_at)}</span>}
            </div>
            <p className="text-xs ml-5" style={{ color: 'var(--text-muted)' }}>{status.last_start_error}</p>
          </div>
        )}
      </div>
    )
  }

  return (
    <div className="rounded-lg p-4" style={{ background: 'rgba(234, 179, 8, 0.08)', border: '1px solid rgba(234, 179, 8, 0.3)' }}>
      <div className="flex items-center justify-between gap-3">
        <div className="flex items-center gap-3">
          <div className="w-3 h-3 rounded-full bg-yellow-500" />
          <div>
            <span className="text-sm font-medium" style={{ color: 'var(--text-primary)' }}>Not Activated</span>
            <p className="text-xs mt-0.5" style={{ color: 'var(--text-muted)' }}>This plan monitors <strong className="text-yellow-400">{plan.channel.type.replace('_', ' ')}</strong>.</p>
          </div>
        </div>
        <button onClick={onToggle} disabled={isActivating} className="px-5 py-2 text-sm font-semibold rounded-lg bg-green-600 hover:bg-green-500 text-white disabled:opacity-50">{isActivating ? 'Activating...' : 'Activate'}</button>
      </div>
    </div>
  )
}

function InfoBlock({ label, value }: { label: string; value: string }) {
  return (
    <div>
      <div className="font-medium mb-0.5" style={{ color: 'var(--text-secondary)' }}>{label}</div>
      {value}
    </div>
  )
}

function FailedSessions({ status, retryingIssue, onRetry, onNavigate }: { status: FleetPlanStatusExt | null; retryingIssue: number | null; onRetry: (issue: number) => void; onNavigate: (path: string) => void }) {
  const issues = status?.failed_issues || []
  if (issues.length === 0) return null
  return (
    <div className="rounded-lg p-4" style={{ background: 'rgba(239, 68, 68, 0.06)', border: '1px solid rgba(239, 68, 68, 0.2)' }}>
      <h3 className="text-sm font-semibold text-red-400 mb-3 flex items-center gap-2"><AlertCircle size={16} /> Failed Sessions ({issues.length})</h3>
      <div className="space-y-2">
        {issues.map(issue => (
          <div key={issue.issue_number} className="rounded-lg p-3 flex items-start justify-between gap-3" style={{ background: 'rgba(0,0,0,0.15)', border: '1px solid rgba(239,68,68,0.15)' }}>
            <div className="flex-1 min-w-0">
              <div className="flex items-center gap-2 mb-1">
                <span className="text-sm font-medium" style={{ color: 'var(--text-primary)' }}>Issue #{issue.issue_number}</span>
                {issue.session_id && <button onClick={() => onNavigate(buildPath('fleet', { subView: 'session', subKey: issue.session_id! }))} className="text-[10px] px-1.5 py-0.5 rounded hover:bg-white/10" style={{ color: 'var(--brand)', background: 'var(--brand-muted)' }}>trace {issue.session_id.slice(0, 8)}</button>}
                {issue.failed_at && <span className="text-[10px]" style={{ color: 'var(--text-muted)' }}>{formatTimeAgo(issue.failed_at)}</span>}
              </div>
              <p className="text-xs truncate" style={{ color: 'var(--text-muted)' }}>{issue.error || 'Unknown error'}</p>
            </div>
            <button onClick={() => onRetry(issue.issue_number)} disabled={retryingIssue === issue.issue_number} className="flex items-center gap-1.5 px-3 py-1.5 text-xs font-medium rounded-lg bg-amber-600/20 text-amber-400 hover:bg-amber-600/30 disabled:opacity-50">
              <RotateCcw size={12} className={retryingIssue === issue.issue_number ? 'animate-spin' : ''} />
              {retryingIssue === issue.issue_number ? 'Retrying...' : 'Continue'}
            </button>
          </div>
        ))}
      </div>
    </div>
  )
}

function RetryingSessions({ status }: { status: FleetPlanStatusExt | null }) {
  const issues = status?.issues_retrying || []
  if (issues.length === 0) return null
  return (
    <div className="rounded-lg p-4" style={{ background: 'rgba(234, 179, 8, 0.06)', border: '1px solid rgba(234, 179, 8, 0.2)' }}>
      <h3 className="text-sm font-semibold text-amber-400 mb-3 flex items-center gap-2"><AlertCircle size={16} /> Retrying ({issues.length})</h3>
      <div className="space-y-2">
        {issues.map(issue => (
          <div key={issue.issue_number} className="rounded-lg p-3" style={{ background: 'rgba(0,0,0,0.15)', border: '1px solid rgba(234,179,8,0.15)' }}>
            <div className="flex items-center gap-2 mb-1">
              <span className="text-sm font-medium" style={{ color: 'var(--text-primary)' }}>Issue #{issue.issue_number}</span>
              <span className="text-[10px] px-1.5 py-0.5 rounded bg-amber-600/20 text-amber-400">retry {issue.retry_count}/3</span>
              {issue.last_failed_at && <span className="text-[10px]" style={{ color: 'var(--text-muted)' }}>{formatTimeAgo(issue.last_failed_at)}</span>}
            </div>
            <p className="text-xs truncate" style={{ color: 'var(--text-muted)' }}>{issue.error || 'Unknown error'}</p>
          </div>
        ))}
      </div>
    </div>
  )
}

function CommunicationFlow({ flow }: { flow: CommFlowNode[] }) {
  if (flow.length === 0) return null
  return (
    <div className="rounded-lg p-4" style={{ background: 'var(--bg-secondary)', border: '1px solid var(--border-color)' }}>
      <h3 className="text-sm font-semibold mb-3" style={{ color: 'var(--text-primary)' }}>Communication Flow</h3>
      <div className="flex items-center flex-wrap gap-2">
        {flow.map((node, i) => {
          const color = getAgentColor(node.role)
          return (
            <div key={node.role} className="flex items-center gap-2">
              <div className="flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-xs font-medium" style={{ background: color.bg, border: `1px solid ${color.border}`, color: color.text }}>
                {node.entry_point && <Radio size={10} />}
                {node.role}
              </div>
              {i < flow.length - 1 && <ArrowRight size={14} style={{ color: 'var(--text-muted)' }} />}
            </div>
          )
        })}
      </div>
      <div className="mt-3 space-y-1">
        {flow.map(node => (
          <div key={`talks-${node.role}`} className="text-xs" style={{ color: 'var(--text-muted)' }}>
            <span style={{ color: getAgentColor(node.role).text }}>{node.role}</span>
            {' talks to: '}
            {node.talks_to?.join(', ') || 'none'}
          </div>
        ))}
      </div>
    </div>
  )
}

function Artifacts({ artifacts }: { artifacts: [string, FleetArtifactDef][] }) {
  if (artifacts.length === 0) return null
  return (
    <div className="rounded-lg p-4" style={{ background: 'var(--bg-secondary)', border: '1px solid var(--border-color)' }}>
      <h3 className="text-sm font-semibold mb-3" style={{ color: 'var(--text-primary)' }}>Artifacts</h3>
      <div className="space-y-2">
        {artifacts.map(([key, artifact]) => (
          <div key={key} className="flex items-center gap-3 text-xs">
            <GitBranch size={14} className="text-primary" />
            <span className="font-medium" style={{ color: 'var(--text-secondary)' }}>{key}</span>
            <span style={{ color: 'var(--text-muted)' }}>{artifact.type === 'git_repo' ? artifact.repo : artifact.path}</span>
            {artifact.auto_pr && <span className="px-1.5 py-0.5 rounded text-[10px] bg-primary/20 text-primary">auto-PR</span>}
          </div>
        ))}
      </div>
    </div>
  )
}

function ChannelConfig({ config }: { config?: Record<string, unknown> }) {
  if (!config || Object.keys(config).length === 0) return null
  return (
    <div className="rounded-lg p-4" style={{ background: 'var(--bg-secondary)', border: '1px solid var(--border-color)' }}>
      <h3 className="text-sm font-semibold mb-3" style={{ color: 'var(--text-primary)' }}>Channel Configuration</h3>
      <div className="space-y-1">
        {Object.entries(config).map(([k, v]) => (
          <div key={k} className="flex items-center gap-2 text-xs">
            <span className="font-medium" style={{ color: 'var(--text-secondary)' }}>{k}:</span>
            <span style={{ color: 'var(--text-muted)' }}>{typeof v === 'object' ? JSON.stringify(v) : String(v)}</span>
          </div>
        ))}
      </div>
    </div>
  )
}

function LaunchDialog({ planName, message, isLaunching, onMessage, onCancel, onLaunch }: { planName: string; message: string; isLaunching: boolean; onMessage: (value: string) => void; onCancel: () => void; onLaunch: () => void }) {
  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/60">
      <div className="w-full max-w-md rounded-xl shadow-2xl" style={{ background: 'var(--bg-secondary)', border: '1px solid var(--border-color)' }}>
        <div className="px-6 py-4" style={{ borderBottom: '1px solid var(--border-color)' }}>
          <h2 className="text-lg font-semibold flex items-center gap-2" style={{ color: 'var(--text-primary)' }}>
            <Rocket size={20} className="text-primary" />
            Launch Fleet Session
          </h2>
          <p className="text-xs mt-1" style={{ color: 'var(--text-muted)' }}>Launch "{planName}" with an optional initial task</p>
        </div>
        <div className="px-6 py-4 space-y-4">
          <label className="block">
            <span className="block text-xs font-medium mb-1.5" style={{ color: 'var(--text-secondary)' }}>Initial request (optional)</span>
            <textarea
              value={message}
              onChange={e => onMessage(e.target.value)}
              placeholder="Describe what you want the team to work on..."
              rows={3}
              className="w-full px-3 py-2 rounded-lg text-sm focus:outline-none focus:ring-2 focus:ring-primary resize-none"
              style={{ background: 'var(--bg-tertiary)', color: 'var(--text-primary)', border: '1px solid var(--border-color)' }}
            />
          </label>
          <div className="flex justify-end gap-2 pt-2">
            <button onClick={onCancel} className="px-4 py-2 text-sm rounded-lg hover:bg-white/5" style={{ color: 'var(--text-secondary)' }}>Cancel</button>
            <button onClick={onLaunch} disabled={isLaunching} className="px-4 py-2 text-sm bg-primary hover:bg-primary/90 text-white rounded-lg disabled:opacity-50">{isLaunching ? 'Launching...' : 'Launch'}</button>
          </div>
        </div>
      </div>
    </div>
  )
}
