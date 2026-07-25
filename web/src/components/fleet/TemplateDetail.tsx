import { useEffect, useState } from 'react'
import { AlertCircle, ArrowRight, Copy, ExternalLink, Loader, Radio, Settings2, Users } from 'lucide-react'

import { cloneFleet, fetchFleet, saveFleet } from '../../api/fleetChat'
import type { FleetDefinition, SetupProfileSummary } from '../../api/fleetChat'
import type { CommFlowNode, FleetAgentDef, FleetPlanData, FleetSettings } from './fleetUtils'
import { addAgentToFleetConfig, getAgentColor, removeAgentFromFleetConfig, renameAgentInFleetConfig, updateFleetSettings } from './fleetUtils'
import { FleetAgentsEditor, AgentEditorPanel, FleetDetailTabs, FleetSettingsEditor } from './FleetConfigEditor'
import { useFleetDetailTab } from './fleetHooks'
import FleetTemplateDialog from './FleetTemplateDialog'
import SetupWizard from './SetupWizard'

interface TemplateDetailProps {
  templateKey: string
  templates: FleetDefinition[]
  setupProfiles?: SetupProfileSummary[]
  onNavigateToSetupProfile?: (profileKey: string) => void
  onCreatePlan?: (key: string, draftId?: string) => void
  onCloned?: (newKey: string) => void
  onPlanCreated?: (planKey: string) => void
}

interface TemplateState {
  config: FleetPlanData | null
  key: string | null
  source: 'bundled' | 'custom' | null
  error: string | null
}

export default function TemplateDetail({ templateKey, templates, setupProfiles = [], onNavigateToSetupProfile, onCreatePlan, onCloned, onPlanCreated }: TemplateDetailProps) {
  const template = templates.find(t => t.key === templateKey)
  const [state, setState] = useState<TemplateState>({ config: null, key: null, source: null, error: null })
  const [tab, setTab] = useFleetDetailTab('template', templateKey)
  const [saveError, setSaveError] = useState<string | null>(null)
  const [cloneOpen, setCloneOpen] = useState(false)
  const [setupOpen, setSetupOpen] = useState(false)
  const [cloning, setCloning] = useState(false)
  const [cloneError, setCloneError] = useState<string | null>(null)
  const [editingAgentKey, setEditingAgentKey] = useState<string | null>(null)

  const loading = Boolean(templateKey && templateKey !== state.key)
  const fullConfig = state.key === templateKey ? state.config : null
  const error = state.key === templateKey ? state.error : null
  const source = (state.key === templateKey ? state.source : null) || template?.source || 'custom'
  const isBundled = source === 'bundled'

  useEffect(() => {
    if (!templateKey) return
    let cancelled = false
    fetchFleet(templateKey)
      .then(data => {
        if (!cancelled) {
          setState({
            config: data.fleet as FleetPlanData,
            key: templateKey,
            source: data.source || template?.source || 'custom',
            error: null,
          })
        }
      })
      .catch((err: Error) => {
        if (!cancelled) setState({ config: null, key: templateKey, source: null, error: err.message })
      })
    return () => { cancelled = true }
  }, [templateKey, template?.source])

  useEffect(() => {
    setEditingAgentKey(null)
  }, [templateKey])

  if (!template) {
    return (
      <div className="flex-1 flex items-center justify-center">
        <p className="text-sm" style={{ color: 'var(--text-muted)' }}>Template not found</p>
      </div>
    )
  }

  const agents: [string, FleetAgentDef][] = fullConfig?.agents ? Object.entries(fullConfig.agents) : []
  const settings = fullConfig?.settings || {}
  const displayName = fullConfig?.name || template.name
  const setupProfileKey = fullConfig?.setup_profile?.trim() || 'generic'
  const setupProfile = setupProfiles.find(p => p.key === setupProfileKey)
  const setupProfileLabel = setupProfile?.name || setupProfileKey
  const editingAgent = editingAgentKey ? agents.find(([key]) => key === editingAgentKey) || null : null
  const canDeleteAgent = Boolean(!isBundled && agents.length > 1)

  const handleTabChange = (next: typeof tab) => {
    setEditingAgentKey(null)
    setTab(next)
  }

  const handleCloneSubmit = async ({ key, name }: { key: string; name: string }) => {
    setCloning(true)
    setCloneError(null)
    try {
      const result = await cloneFleet(templateKey, key, name)
      setCloneOpen(false)
      onCloned?.(result.key)
    } catch (err) {
      setCloneError(err instanceof Error ? err.message : String(err))
    } finally {
      setCloning(false)
    }
  }

  const handleSaveSettings = async (nextSettings: FleetSettings, nextSetupProfileKey?: string) => {
    if (!fullConfig || isBundled) return
    setSaveError(null)
    const nextConfig = updateFleetSettings(fullConfig, nextSettings, nextSetupProfileKey)
    await saveFleet(templateKey, nextConfig as Record<string, unknown>)
    setState({ config: nextConfig, key: templateKey, source: 'custom', error: null })
  }

  const handleSaveAgent = async (agentKey: string, agent: FleetAgentDef, nextKey?: string) => {
    if (!fullConfig || isBundled) return
    setSaveError(null)
    const targetKey = nextKey && nextKey !== agentKey ? nextKey : agentKey
    let nextConfig = fullConfig
    if (targetKey !== agentKey) {
      nextConfig = renameAgentInFleetConfig(nextConfig, agentKey, targetKey)
    }
    nextConfig = {
      ...nextConfig,
      agents: { ...(nextConfig.agents || {}), [targetKey]: agent },
    }
    await saveFleet(templateKey, nextConfig as Record<string, unknown>)
    setState({ config: nextConfig, key: templateKey, source: 'custom', error: null })
    if (editingAgentKey === agentKey && targetKey !== agentKey) {
      setEditingAgentKey(targetKey)
    }
  }

  const handleAddAgent = async (agentKey: string, agent: FleetAgentDef) => {
    if (!fullConfig || isBundled) return
    setSaveError(null)
    const nextConfig = addAgentToFleetConfig(fullConfig, agentKey, agent)
    await saveFleet(templateKey, nextConfig as Record<string, unknown>)
    setState({ config: nextConfig, key: templateKey, source: 'custom', error: null })
  }

  const handleDeleteAgent = async (agentKey: string) => {
    if (!fullConfig || isBundled) return
    setSaveError(null)
    const nextConfig = removeAgentFromFleetConfig(fullConfig, agentKey)
    await saveFleet(templateKey, nextConfig as Record<string, unknown>)
    setState({ config: nextConfig, key: templateKey, source: 'custom', error: null })
    if (editingAgentKey === agentKey) setEditingAgentKey(null)
  }

  return (
    <div className="flex-1 flex flex-col overflow-hidden">
      <div className={`${editingAgent ? 'h-1/2' : 'flex-1'} overflow-y-auto`}>
        <div className="max-w-4xl mx-auto p-6 space-y-6">
          <div className="flex items-start justify-between gap-4">
            <div>
              <h1 className="text-xl font-bold" style={{ color: 'var(--text-primary)' }}>{displayName}</h1>
              {(fullConfig?.description || template.description) && (
                <p className="text-sm mt-1" style={{ color: 'var(--text-secondary)' }}>{fullConfig?.description || template.description}</p>
              )}
              <div className="flex items-center gap-3 mt-2 flex-wrap">
                <span
                  className="text-xs px-2 py-0.5 rounded"
                  style={{
                    background: isBundled ? 'var(--brand-muted)' : 'var(--bg-tertiary)',
                    color: isBundled ? 'var(--brand)' : 'var(--text-muted)',
                  }}
                >
                  {isBundled ? 'Astonish template' : 'Your template'}
                </span>
                <span className="text-xs font-mono px-2 py-0.5 rounded" style={{ background: 'var(--bg-tertiary)', color: 'var(--text-muted)' }}>
                  key: {templateKey}
                </span>
                <span className="text-xs" style={{ color: 'var(--text-muted)' }}>
                  {template.agent_count} agent{template.agent_count !== 1 ? 's' : ''}
                </span>
                {!loading && (
                  <button
                    type="button"
                    onClick={() => onNavigateToSetupProfile?.(setupProfileKey)}
                    className="flex items-center gap-1 text-xs px-2 py-0.5 rounded transition-colors hover:bg-primary/10"
                    style={{ background: 'var(--brand-muted)', color: 'var(--brand)', border: '1px solid color-mix(in oklab, var(--brand) 25%, transparent)' }}
                    title={`Setup profile: ${setupProfileKey}`}
                  >
                    <Settings2 size={10} />
                    Setup: {setupProfileLabel}
                    {onNavigateToSetupProfile && <ExternalLink size={10} className="opacity-70" />}
                  </button>
                )}
              </div>
            </div>
            <div className="flex items-center gap-2">
              <button
                onClick={() => { setCloneError(null); setCloneOpen(true) }}
                className="flex items-center gap-1.5 px-3 py-1.5 text-xs font-medium rounded-lg transition-colors"
                style={{ background: 'var(--bg-tertiary)', color: 'var(--text-primary)', border: '1px solid var(--border-color)' }}
              >
                <Copy size={12} />
                {isBundled ? 'Clone to edit' : 'Clone'}
              </button>
              <button
                onClick={() => setSetupOpen(true)}
                className="flex items-center gap-1.5 px-3 py-1.5 text-xs font-medium rounded-lg bg-primary hover:bg-primary/90 text-white transition-colors"
              >
                <Users size={12} /> Create Plan
              </button>
              <button
                onClick={() => onCreatePlan?.(templateKey)}
                className="flex items-center gap-1.5 px-3 py-1.5 text-xs font-medium rounded-lg transition-colors"
                style={{ background: 'var(--bg-tertiary)', color: 'var(--text-primary)', border: '1px solid var(--border-color)' }}
              >
                AI Guide
              </button>
            </div>
          </div>

          {isBundled && (
            <div className="rounded-lg p-3 text-xs" style={{ background: 'var(--brand-muted)', border: '1px solid color-mix(in oklab, var(--brand) 25%, transparent)', color: 'var(--text-secondary)' }}>
              This template ships with Astonish and cannot be edited. Clone it to create a customizable copy stored in your team database.
            </div>
          )}

          <FleetDetailTabs activeTab={tab} onChange={handleTabChange} />

          {loading && (
            <div className="flex items-center gap-2 text-xs" style={{ color: 'var(--text-muted)' }}>
              <Loader size={12} className="animate-spin" /> Loading template details...
            </div>
          )}
          {error && (
            <div className="flex items-center gap-2 text-xs" style={{ color: '#f87171' }}>
              <AlertCircle size={12} /> Failed to load details: {error}
            </div>
          )}
          {saveError && (
            <div className="flex items-center gap-2 text-xs" style={{ color: '#f87171' }}>
              <AlertCircle size={12} /> {saveError}
            </div>
          )}

          {tab === 'overview' && (
            <>
              <SetupProfileCard
                profileKey={setupProfileKey}
                profileName={setupProfileLabel}
                profileDescription={setupProfile?.description}
                stepCount={setupProfile?.step_count}
                explicit={Boolean(fullConfig?.setup_profile?.trim())}
                onOpen={() => onNavigateToSetupProfile?.(setupProfileKey)}
              />
              <CommunicationFlow flow={fullConfig?.communication?.flow || []} />
              <div className="rounded-lg p-4" style={{ background: 'var(--brand-muted)', border: '1px solid color-mix(in oklab, var(--brand) 20%, transparent)' }}>
                <p className="text-xs" style={{ color: 'var(--text-secondary)' }}>
                  Templates are base fleet configurations. Create a fleet plan from this template to add environment-specific channel and artifact settings.
                </p>
              </div>
            </>
          )}

          {tab === 'settings' && fullConfig && (
            <FleetSettingsEditor
              settings={settings}
              setupProfileKey={setupProfileKey}
              setupProfiles={setupProfiles}
              readOnly={isBundled}
              onSave={isBundled ? undefined : (settings, setupProfileKey) => handleSaveSettings(settings, setupProfileKey).catch(err => {
                setSaveError(err instanceof Error ? err.message : String(err))
                throw err
              })}
            />
          )}

          {tab === 'agents' && (
            fullConfig || !loading ? (
              <FleetAgentsEditor
                agents={agents}
                fleetSettings={fullConfig?.settings}
                selectedKey={editingAgentKey}
                onSelectedKeyChange={setEditingAgentKey}
                readOnly={isBundled}
                onSaveAgent={isBundled ? undefined : (agentKey, agent, nextKey) => handleSaveAgent(agentKey, agent, nextKey).catch(err => {
                  setSaveError(err instanceof Error ? err.message : String(err))
                  throw err
                })}
                onAddAgent={isBundled ? undefined : (agentKey, agent) => handleAddAgent(agentKey, agent).catch(err => {
                  setSaveError(err instanceof Error ? err.message : String(err))
                  throw err
                })}
                onDeleteAgent={isBundled ? undefined : (agentKey) => handleDeleteAgent(agentKey).catch(err => {
                  setSaveError(err instanceof Error ? err.message : String(err))
                  throw err
                })}
              />
            ) : (
              <FallbackAgents names={template.agent_names || []} />
            )
          )}
        </div>
      </div>

      {editingAgent && (
        <div className="h-1/2" style={{ borderTop: '1px solid var(--border-color)' }}>
          <AgentEditorPanel
            agentKey={editingAgent[0]}
            agent={editingAgent[1]}
            siblingAgentKeys={agents.map(([key]) => key).filter(key => key !== editingAgent[0])}
            readOnly={isBundled}
            canDelete={canDeleteAgent}
            onClose={() => setEditingAgentKey(null)}
            onSave={isBundled ? undefined : (agentKey, agent, nextKey) => handleSaveAgent(agentKey, agent, nextKey).catch(err => {
              setSaveError(err instanceof Error ? err.message : String(err))
              throw err
            })}
            onDelete={isBundled ? undefined : async () => {
              await handleDeleteAgent(editingAgent[0])
            }}
          />
        </div>
      )}

      <FleetTemplateDialog
        isOpen={cloneOpen}
        mode="clone"
        sourceName={displayName}
        sourceKey={templateKey}
        submitting={cloning}
        error={cloneError}
        onClose={() => { if (!cloning) { setCloneOpen(false); setCloneError(null) } }}
        onSubmit={handleCloneSubmit}
      />

      {setupOpen && (
        <SetupWizard
          templateKey={templateKey}
          templateName={displayName}
          onClose={() => setSetupOpen(false)}
          onComplete={(planKey) => {
            setSetupOpen(false)
            onPlanCreated?.(planKey)
          }}
          onOpenChatGuide={(key, draftId) => {
            setSetupOpen(false)
            onCreatePlan?.(key, draftId)
          }}
        />
      )}
    </div>
  )
}

function SetupProfileCard({
  profileKey,
  profileName,
  profileDescription,
  stepCount,
  explicit,
  onOpen,
}: {
  profileKey: string
  profileName: string
  profileDescription?: string
  stepCount?: number
  explicit: boolean
  onOpen?: () => void
}) {
  return (
    <div className="rounded-lg p-4" style={{ background: 'var(--bg-secondary)', border: '1px solid var(--border-color)' }}>
      <div className="flex items-start justify-between gap-3">
        <div className="min-w-0">
          <div className="flex items-center gap-2 mb-1">
            <Settings2 size={14} className="text-primary shrink-0" />
            <h3 className="text-sm font-semibold" style={{ color: 'var(--text-primary)' }}>Setup Profile</h3>
          </div>
          <p className="text-xs mb-2" style={{ color: 'var(--text-secondary)' }}>
            Plan creation uses this profile for wizard steps, prompts, and validation.
            {!explicit && ' This template has no explicit setup_profile; the default generic profile applies.'}
          </p>
          <div className="flex items-center gap-2 flex-wrap">
            <span className="text-sm font-medium" style={{ color: 'var(--brand)' }}>{profileName}</span>
            <span className="text-xs font-mono px-1.5 py-0.5 rounded" style={{ background: 'var(--bg-tertiary)', color: 'var(--text-muted)' }}>
              {profileKey}
            </span>
            {typeof stepCount === 'number' && (
              <span className="text-xs" style={{ color: 'var(--text-muted)' }}>
                {stepCount} step{stepCount !== 1 ? 's' : ''}
              </span>
            )}
          </div>
          {profileDescription && (
            <p className="text-xs mt-2" style={{ color: 'var(--text-muted)' }}>{profileDescription}</p>
          )}
        </div>
        {onOpen && (
          <button
            type="button"
            onClick={onOpen}
            className="flex items-center gap-1.5 px-3 py-1.5 text-xs font-medium rounded-lg shrink-0 transition-colors hover:bg-primary/10"
            style={{ background: 'var(--bg-tertiary)', color: 'var(--text-secondary)', border: '1px solid var(--border-color)' }}
          >
            View profile <ExternalLink size={12} className="text-primary" />
          </button>
        )}
      </div>
    </div>
  )
}

function CommunicationFlow({ flow }: { flow: CommFlowNode[] }) {
  if (flow.length === 0) {
    return (
      <div className="rounded-lg p-4 text-sm" style={{ background: 'var(--bg-secondary)', border: '1px solid var(--border-color)', color: 'var(--text-muted)' }}>
        No communication flow configured.
      </div>
    )
  }

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

function FallbackAgents({ names }: { names: string[] }) {
  return (
    <div className="rounded-lg p-4" style={{ background: 'var(--bg-secondary)', border: '1px solid var(--border-color)' }}>
      <h3 className="text-sm font-semibold mb-3" style={{ color: 'var(--text-primary)' }}>Agents</h3>
      <div className="flex flex-wrap gap-2">
        {names.map(name => {
          const color = getAgentColor(name)
          return (
            <div key={name} className="flex items-center gap-1.5 px-3 py-1.5 rounded-lg text-xs font-medium" style={{ background: color.bg, border: `1px solid ${color.border}`, color: color.text }}>
              {name}
            </div>
          )
        })}
      </div>
    </div>
  )
}
