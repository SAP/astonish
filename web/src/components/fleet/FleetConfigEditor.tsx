import { useEffect, useMemo, useRef, useState } from 'react'
import { Check, Loader, Plus, Settings, Trash2, Users, X } from 'lucide-react'

import type { SetupProfileSummary } from '../../api/fleetChat'
import type { FleetAgentDef, FleetPlanData, FleetSettings } from './fleetUtils'
import { getAgentColor, slugifyAgentKey, createDefaultFleetAgent } from './fleetUtils'
import type { FleetDetailTab } from './fleetHooks'
import {
  capabilityMapFromKeys,
  enabledCapabilityKeys,
  FLEET_AGENT_MODES,
  FLEET_MEMORY_VISIBILITY,
  FLEET_ROUTING_MODES,
  FLEET_TASK_CLAIM_POLICIES,
  FLEET_WORKSPACE_MODES,
  isValidCapabilityKey,
  normalizeCapabilityKey,
} from './fleetConstants'

const TABS: FleetDetailTab[] = ['overview', 'settings', 'agents']

export function FleetDetailTabs({ activeTab, onChange }: { activeTab: FleetDetailTab; onChange: (tab: FleetDetailTab) => void }) {
  return (
    <div className="flex items-center gap-1 rounded-lg p-1" style={{ background: 'var(--bg-secondary)', border: '1px solid var(--border-color)' }}>
      {TABS.map(tab => (
        <button
          key={tab}
          onClick={() => onChange(tab)}
          className="px-3 py-1.5 text-xs font-medium rounded-md capitalize transition-colors"
          style={activeTab === tab
            ? { background: 'var(--brand-muted)', color: 'var(--brand)' }
            : { color: 'var(--text-secondary)' }}
        >
          {tab}
        </button>
      ))}
    </div>
  )
}

interface SettingsEditorProps {
  settings: FleetSettings
  setupProfileKey?: string
  setupProfiles?: SetupProfileSummary[]
  onSave?: (settings: FleetSettings, setupProfileKey?: string) => Promise<void>
  readOnly?: boolean
}

export function FleetSettingsEditor({ settings, setupProfileKey, setupProfiles, onSave, readOnly }: SettingsEditorProps) {
  const [draft, setDraft] = useState<FleetSettings>(settings || {})
  const [draftSetupProfileKey, setDraftSetupProfileKey] = useState(setupProfileKey || 'generic')
  const [status, setStatus] = useState<'idle' | 'saving' | 'saved' | 'error'>('idle')
  const disabled = Boolean(readOnly || !onSave)

  const bundledProfiles = useMemo(
    () => (setupProfiles || []).filter(p => p.source === 'bundled').sort((a, b) => a.name.localeCompare(b.name)),
    [setupProfiles],
  )
  const customProfiles = useMemo(
    () => (setupProfiles || []).filter(p => p.source !== 'bundled').sort((a, b) => a.name.localeCompare(b.name)),
    [setupProfiles],
  )

  useEffect(() => {
    setDraft(settings || {})
    setDraftSetupProfileKey(setupProfileKey || 'generic')
    setStatus('idle')
  }, [settings, setupProfileKey])

  const update = <K extends keyof FleetSettings>(key: K, value: FleetSettings[K]) => {
    if (disabled) return
    setDraft(prev => ({ ...prev, [key]: value }))
  }

  const save = async () => {
    if (!onSave || status === 'saving' || disabled) return
    setStatus('saving')
    try {
      await onSave(draft, setupProfiles ? draftSetupProfileKey : undefined)
      setStatus('saved')
      setTimeout(() => setStatus('idle'), 1800)
    } catch {
      setStatus('error')
    }
  }

  return (
    <div className="rounded-lg p-4 space-y-4" style={{ background: 'var(--bg-secondary)', border: '1px solid var(--border-color)' }}>
      <div className="flex items-center justify-between">
        <h3 className="text-sm font-semibold flex items-center gap-1.5" style={{ color: 'var(--text-primary)' }}>
          <Settings size={14} /> Fleet Settings
        </h3>
        {!disabled && onSave && (
          <button
            onClick={save}
            disabled={status === 'saving'}
            className="flex items-center gap-1.5 px-3 py-1.5 text-xs font-medium rounded-lg bg-primary hover:bg-primary/90 text-white disabled:opacity-50"
          >
            {status === 'saving' ? <Loader size={12} className="animate-spin" /> : status === 'saved' ? <Check size={12} /> : null}
            {status === 'saving' ? 'Saving...' : status === 'saved' ? 'Saved' : 'Save Settings'}
          </button>
        )}
      </div>
      {readOnly && (
        <p className="text-xs" style={{ color: 'var(--text-muted)' }}>
          Bundled Astonish templates are read-only. Clone this template to customize settings.
        </p>
      )}
      {status === 'error' && <p className="text-xs text-red-400">Save failed. Check validation and try again.</p>}
      {setupProfiles && setupProfiles.length > 0 && (
        <div className="rounded-lg p-3 space-y-2" style={{ background: 'var(--brand-muted)', border: '1px solid color-mix(in oklab, var(--brand) 20%, transparent)' }}>
          <SetupProfileSelectField
            label="Setup profile"
            value={draftSetupProfileKey}
            bundledProfiles={bundledProfiles}
            customProfiles={customProfiles}
            disabled={disabled}
            onChange={setDraftSetupProfileKey}
          />
          <p className="text-xs" style={{ color: 'var(--text-muted)' }}>
            Used when creating plans from this template — controls wizard steps, prompts, and validation.
          </p>
        </div>
      )}
      <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
        <NumberField label="Max turns per agent" value={draft.max_turns_per_agent} onChange={v => update('max_turns_per_agent', v)} disabled={disabled} />
        <NumberField label="Max parallel agents" value={draft.max_parallel_agents} onChange={v => update('max_parallel_agents', v)} disabled={disabled} />
        <NumberField label="Max wall-clock minutes" value={draft.max_wall_clock_minutes} onChange={v => update('max_wall_clock_minutes', v)} disabled={disabled} />
        <SelectField
          label="Routing mode"
          value={draft.routing_mode || 'llm_mentions'}
          options={[...FLEET_ROUTING_MODES]}
          onChange={v => update('routing_mode', v as FleetSettings['routing_mode'])}
          disabled={disabled}
        />
        <SelectField
          label="Memory visibility"
          value={draft.memory_visibility || 'scoped'}
          options={[...FLEET_MEMORY_VISIBILITY]}
          onChange={v => update('memory_visibility', v as FleetSettings['memory_visibility'])}
          disabled={disabled}
        />
      </div>
      <div className="rounded-lg p-3 space-y-3" style={{ background: 'rgba(255,255,255,0.03)', border: '1px solid var(--border-color)' }}>
        <p className="text-xs font-medium" style={{ color: 'var(--text-secondary)' }}>
          Task board (always on) — durable work items claimed by capability match
        </p>
        <SelectField
          label="Task claim policy"
          value={draft.task_board?.claim_policy || 'capability_match'}
          options={[...FLEET_TASK_CLAIM_POLICIES]}
          onChange={v => update('task_board', { ...(draft.task_board || {}), claim_policy: v as NonNullable<FleetSettings['task_board']>['claim_policy'] })}
          disabled={disabled}
        />
      </div>
    </div>
  )
}

function NumberField({ label, value, onChange, disabled }: { label: string; value?: number; onChange: (value: number | undefined) => void; disabled?: boolean }) {
  return (
    <label className="block text-xs space-y-1" style={{ color: 'var(--text-secondary)' }}>
      <span>{label}</span>
      <input
        type="number"
        min={0}
        value={value ?? ''}
        disabled={disabled}
        onChange={e => onChange(e.target.value === '' ? undefined : Number(e.target.value))}
        className="w-full px-3 py-2 rounded-lg focus:outline-none focus:ring-1 focus:ring-primary disabled:opacity-60"
        style={{ background: 'var(--bg-tertiary)', color: 'var(--text-primary)', border: '1px solid var(--border-color)' }}
      />
    </label>
  )
}

function SetupProfileSelectField({
  label,
  value,
  bundledProfiles,
  customProfiles,
  onChange,
  disabled,
}: {
  label: string
  value: string
  bundledProfiles: SetupProfileSummary[]
  customProfiles: SetupProfileSummary[]
  onChange: (value: string) => void
  disabled?: boolean
}) {
  const selected = [...bundledProfiles, ...customProfiles].find(p => p.key === value)
  const knownKeys = new Set([...bundledProfiles, ...customProfiles].map(p => p.key))
  return (
    <label className="block text-xs space-y-1" style={{ color: 'var(--text-secondary)' }}>
      <span>{label}</span>
      <select
        value={value}
        disabled={disabled}
        onChange={e => onChange(e.target.value)}
        className="w-full px-3 py-2 rounded-lg focus:outline-none focus:ring-1 focus:ring-primary disabled:opacity-60"
        style={{ background: 'var(--bg-tertiary)', color: 'var(--text-primary)', border: '1px solid var(--border-color)' }}
      >
        {!knownKeys.has(value) && value && (
          <option value={value}>{value} (current)</option>
        )}
        {bundledProfiles.length > 0 && (
          <optgroup label="Bundled setup profiles">
            {bundledProfiles.map(profile => (
              <option key={profile.key} value={profile.key}>
                {profile.name} ({profile.key})
              </option>
            ))}
          </optgroup>
        )}
        {customProfiles.length > 0 && (
          <optgroup label="Your setup profiles">
            {customProfiles.map(profile => (
              <option key={profile.key} value={profile.key}>
                {profile.name} ({profile.key})
              </option>
            ))}
          </optgroup>
        )}
      </select>
      {selected?.description && (
        <p className="text-[11px] pt-0.5" style={{ color: 'var(--text-muted)' }}>{selected.description}</p>
      )}
    </label>
  )
}

function SelectField({ label, value, options, onChange, disabled }: { label: string; value: string; options: string[]; onChange: (value: string) => void; disabled?: boolean }) {
  return (
    <label className="block text-xs space-y-1" style={{ color: 'var(--text-secondary)' }}>
      <span>{label}</span>
      <select
        value={value}
        disabled={disabled}
        onChange={e => onChange(e.target.value)}
        className="w-full px-3 py-2 rounded-lg focus:outline-none focus:ring-1 focus:ring-primary disabled:opacity-60"
        style={{ background: 'var(--bg-tertiary)', color: 'var(--text-primary)', border: '1px solid var(--border-color)' }}
      >
        {options.map(option => <option key={option} value={option}>{option}</option>)}
      </select>
    </label>
  )
}

interface AgentsEditorProps {
  agents: [string, FleetAgentDef][]
  fleetSettings?: FleetSettings
  onSaveAgent?: (agentKey: string, agent: FleetAgentDef, nextKey?: string) => Promise<void>
  onAddAgent?: (agentKey: string, agent: FleetAgentDef) => Promise<void>
  onDeleteAgent?: (agentKey: string) => Promise<void>
  readOnly?: boolean
  /** Controlled selection for docking the editor in the parent. */
  selectedKey?: string | null
  onSelectedKeyChange?: (key: string | null) => void
}

export function FleetAgentsEditor({
  agents,
  onSaveAgent,
  onAddAgent,
  onDeleteAgent,
  readOnly,
  selectedKey: controlledSelectedKey,
  onSelectedKeyChange,
}: AgentsEditorProps) {
  const [uncontrolledSelectedKey, setUncontrolledSelectedKey] = useState<string | null>(null)
  const selectedKey = onSelectedKeyChange ? (controlledSelectedKey ?? null) : uncontrolledSelectedKey
  const setSelectedKey = (key: string | null) => {
    if (onSelectedKeyChange) onSelectedKeyChange(key)
    else setUncontrolledSelectedKey(key)
  }
  const [addOpen, setAddOpen] = useState(false)
  const [busyKey, setBusyKey] = useState<string | null>(null)
  const [listError, setListError] = useState<string | null>(null)
  const selected = useMemo(() => agents.find(([key]) => key === selectedKey) || null, [agents, selectedKey])
  const canMutate = !readOnly && Boolean(onAddAgent || onDeleteAgent)
  const canDelete = Boolean(onDeleteAgent) && agents.length > 1
  const dockExternally = Boolean(onSelectedKeyChange)

  const handleDelete = async (agentKey: string) => {
    if (!onDeleteAgent || !canDelete || busyKey) return
    if (!window.confirm(`Delete agent "@${agentKey}"? This cannot be undone.`)) return
    setBusyKey(agentKey)
    setListError(null)
    try {
      await onDeleteAgent(agentKey)
      if (selectedKey === agentKey) setSelectedKey(null)
    } catch (err) {
      setListError(err instanceof Error ? err.message : String(err))
    } finally {
      setBusyKey(null)
    }
  }

  return (
    <>
      <div className="rounded-lg p-4" style={{ background: 'var(--bg-secondary)', border: '1px solid var(--border-color)' }}>
        <div className="flex items-center justify-between gap-3 mb-3">
          <h3 className="text-sm font-semibold flex items-center gap-1.5" style={{ color: 'var(--text-primary)' }}>
            <Users size={14} /> Agents ({agents.length})
          </h3>
          {!readOnly && onAddAgent && (
            <button
              onClick={() => { setListError(null); setAddOpen(true) }}
              className="flex items-center gap-1.5 px-2.5 py-1.5 text-xs font-medium rounded-lg bg-primary hover:bg-primary/90 text-white transition-colors"
            >
              <Plus size={12} /> Add Agent
            </button>
          )}
        </div>
        {readOnly && (
          <p className="text-xs mb-3" style={{ color: 'var(--text-muted)' }}>
            Bundled Astonish templates are read-only. Clone this template to add or remove agents.
          </p>
        )}
        {listError && <p className="text-xs text-red-400 mb-3">{listError}</p>}
        {agents.length === 0 ? (
          <div className="rounded-lg p-4 text-xs" style={{ background: 'rgba(255,255,255,0.03)', border: '1px dashed var(--border-color)', color: 'var(--text-muted)' }}>
            {canMutate ? 'No agents yet. Add an agent to get started.' : 'No agents configured.'}
          </div>
        ) : (
          <div className="space-y-2">
            {agents.map(([key, agent]) => {
              const color = getAgentColor(key)
              const isActive = selectedKey === key
              return (
                <div
                  key={key}
                  className="w-full text-left rounded-lg p-3 transition-colors"
                  style={{
                    background: color.bg,
                    border: `1px solid ${color.border}`,
                    boxShadow: isActive ? `inset 0 0 0 1px ${color.text}` : undefined,
                  }}
                >
                  <div className="flex items-start gap-2">
                    <button
                      onClick={() => setSelectedKey(isActive ? null : key)}
                      className="flex-1 text-left min-w-0 hover:opacity-90"
                    >
                      <div className="flex items-center justify-between gap-3">
                        <span className="text-sm font-medium" style={{ color: color.text }}>{key}</span>
                        <span className="text-xs" style={{ color: 'var(--text-muted)' }}>{agent.name || agent.mode || 'agent'}</span>
                      </div>
                      {agent.description && <p className="text-xs mt-1" style={{ color: 'var(--text-secondary)' }}>{agent.description}</p>}
                      {(agent.execution?.parallelizable || (agent.execution?.workspace && agent.execution.workspace !== 'shared')) && (
                        <div className="flex flex-wrap gap-1 mt-2">
                          {agent.execution?.parallelizable && <Pill>parallel</Pill>}
                          {agent.execution?.workspace && agent.execution.workspace !== 'shared' && (
                            <Pill>{agent.execution.workspace}</Pill>
                          )}
                        </div>
                      )}
                    </button>
                    {!readOnly && onDeleteAgent && (
                      <button
                        onClick={() => handleDelete(key)}
                        disabled={!canDelete || busyKey === key}
                        title={canDelete ? `Delete @${key}` : 'At least one agent is required'}
                        className="p-1.5 rounded hover:bg-red-500/20 transition-colors disabled:opacity-40 shrink-0"
                      >
                        {busyKey === key ? <Loader size={12} className="animate-spin text-red-400" /> : <Trash2 size={12} className="text-red-400" />}
                      </button>
                    )}
                  </div>
                </div>
              )
            })}
          </div>
        )}
      </div>
      {!dockExternally && selected && (
        <AgentEditorPanel
          agentKey={selected[0]}
          agent={selected[1]}
          siblingAgentKeys={agents.map(([key]) => key).filter(key => key !== selected[0])}
          readOnly={readOnly}
          canDelete={canDelete}
          onClose={() => setSelectedKey(null)}
          onSave={onSaveAgent}
          onDelete={onDeleteAgent ? () => handleDelete(selected[0]) : undefined}
        />
      )}
      {addOpen && onAddAgent && (
        <AddAgentDialog
          existingKeys={agents.map(([key]) => key)}
          onClose={() => setAddOpen(false)}
          onSubmit={async ({ key, name }) => {
            await onAddAgent(key, createDefaultFleetAgent(name))
            setAddOpen(false)
            setSelectedKey(key)
          }}
        />
      )}
    </>
  )
}

function AddAgentDialog({
  existingKeys,
  onClose,
  onSubmit,
}: {
  existingKeys: string[]
  onClose: () => void
  onSubmit: (result: { key: string; name: string }) => Promise<void>
}) {
  const [name, setName] = useState('')
  const [key, setKey] = useState('')
  const [keyTouched, setKeyTouched] = useState(false)
  const [error, setError] = useState('')
  const [submitting, setSubmitting] = useState(false)
  const inputRef = useRef<HTMLInputElement>(null)

  useEffect(() => {
    requestAnimationFrame(() => inputRef.current?.focus())
  }, [])

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()
    const trimmedName = name.trim()
    const trimmedKey = key.trim()
    if (!trimmedName) {
      setError('Please enter an agent name')
      return
    }
    if (!trimmedKey) {
      setError('Please enter an agent key')
      return
    }
    if (!/^[a-z0-9][a-z0-9_-]*$/.test(trimmedKey)) {
      setError('Key must start with a letter or number and use only lowercase letters, numbers, hyphens, or underscores')
      return
    }
    if (existingKeys.includes(trimmedKey)) {
      setError(`Agent key "${trimmedKey}" already exists`)
      return
    }
    setSubmitting(true)
    setError('')
    try {
      await onSubmit({ key: trimmedKey, name: trimmedName })
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
      setSubmitting(false)
    }
  }

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center" onKeyDown={(e) => { if (e.key === 'Escape' && !submitting) onClose() }}>
      <div className="absolute inset-0 bg-black/60 backdrop-blur-sm" onClick={() => { if (!submitting) onClose() }} />
      <div className="relative w-full max-w-md mx-4 rounded-2xl shadow-2xl overflow-hidden" style={{ background: 'var(--bg-secondary)' }}>
        <div className="px-6 py-5" style={{ background: 'linear-gradient(135deg, var(--brand) 0%, var(--brand-strong) 100%)' }}>
          <div className="flex items-center justify-between">
            <div className="flex items-center gap-3">
              <div className="w-10 h-10 rounded-xl bg-white/20 flex items-center justify-center">
                <Plus size={20} className="text-white" />
              </div>
              <div>
                <h2 className="text-lg font-semibold text-white">Add Agent</h2>
                <p className="text-sm text-white/70">Add a role to this fleet template</p>
              </div>
            </div>
            <button type="button" onClick={onClose} disabled={submitting} className="p-2 rounded-lg hover:bg-white/10 transition-colors disabled:opacity-50">
              <X size={20} className="text-white" />
            </button>
          </div>
        </div>
        <form onSubmit={handleSubmit} className="p-6 space-y-5">
          <div>
            <label className="block text-sm font-medium mb-2" style={{ color: 'var(--text-secondary)' }}>
              Agent Name <span className="text-red-400">*</span>
            </label>
            <input
              ref={inputRef}
              type="text"
              value={name}
              disabled={submitting}
              onChange={(e) => {
                setName(e.target.value)
                setError('')
                if (!keyTouched) setKey(slugifyAgentKey(e.target.value))
              }}
              placeholder="e.g. Product Owner"
              className="w-full px-4 py-3 rounded-xl border text-base transition-all focus:outline-none focus:ring-2 focus:ring-primary disabled:opacity-60"
              style={{ background: 'var(--bg-primary)', borderColor: error ? '#EF4444' : 'var(--border-color)', color: 'var(--text-primary)' }}
            />
          </div>
          <div>
            <label className="block text-sm font-medium mb-2" style={{ color: 'var(--text-secondary)' }}>
              Agent Key <span className="text-red-400">*</span>
            </label>
            <input
              type="text"
              value={key}
              disabled={submitting}
              onChange={(e) => {
                setKeyTouched(true)
                setKey(e.target.value.toLowerCase().replace(/[^a-z0-9_-]/g, ''))
                setError('')
              }}
              placeholder="e.g. po"
              className="w-full px-4 py-3 rounded-xl border text-base font-mono transition-all focus:outline-none focus:ring-2 focus:ring-primary disabled:opacity-60"
              style={{ background: 'var(--bg-primary)', borderColor: error ? '#EF4444' : 'var(--border-color)', color: 'var(--text-primary)' }}
            />
            <p className="text-xs mt-1.5" style={{ color: 'var(--text-muted)' }}>
              Used in mentions as <code className="px-1 py-0.5 rounded bg-primary/15 text-primary">@{key || '…'}</code>
            </p>
            {error && <p className="text-xs mt-1.5 text-red-400">{error}</p>}
          </div>
          <div className="flex gap-3 pt-2">
            <button type="button" onClick={onClose} disabled={submitting} className="flex-1 px-4 py-3 rounded-xl text-sm font-medium transition-colors disabled:opacity-50" style={{ background: 'var(--bg-tertiary)', color: 'var(--text-secondary)' }}>
              Cancel
            </button>
            <button type="submit" disabled={submitting} className="flex-1 flex items-center justify-center gap-2 px-4 py-3 rounded-xl text-sm font-medium text-white transition-colors disabled:opacity-50" style={{ background: 'linear-gradient(135deg, var(--brand) 0%, var(--brand-strong) 100%)' }}>
              {submitting ? <Loader size={18} className="animate-spin" /> : <Plus size={18} />}
              {submitting ? 'Adding…' : 'Create Agent'}
            </button>
          </div>
        </form>
      </div>
    </div>
  )
}

function Pill({ children }: { children: React.ReactNode }) {
  return (
    <span className="text-[10px] px-1.5 py-0.5 rounded" style={{ background: 'rgba(0,0,0,0.25)', color: 'var(--text-secondary)' }}>
      {children}
    </span>
  )
}

type AgentEditorTab = 'identity' | 'execution' | 'advanced'

const AGENT_EDITOR_TABS: { id: AgentEditorTab; label: string }[] = [
  { id: 'identity', label: 'Identity' },
  { id: 'execution', label: 'Execution' },
  { id: 'advanced', label: 'Advanced' },
]

/** Split free-text capability tags on commas/whitespace; normalize and dedupe. */
function parseCapabilityTags(raw: string): string[] {
  const seen = new Set<string>()
  const out: string[] = []
  for (const part of raw.split(/[,\s]+/)) {
    const key = normalizeCapabilityKey(part)
    if (!key || !isValidCapabilityKey(key) || seen.has(key)) continue
    seen.add(key)
    out.push(key)
  }
  return out
}

/** Bottom-docked agent editor (Flows-style), not a right-side overlay. */
export function AgentEditorPanel({
  agentKey,
  agent,
  siblingAgentKeys,
  readOnly,
  canDelete,
  onClose,
  onSave,
  onDelete,
}: {
  agentKey: string
  agent: FleetAgentDef
  siblingAgentKeys: string[]
  readOnly?: boolean
  canDelete?: boolean
  onClose: () => void
  onSave?: (agentKey: string, agent: FleetAgentDef, nextKey?: string) => Promise<void>
  onDelete?: () => Promise<void>
}) {
  const [draft, setDraft] = useState<FleetAgentDef>(agent)
  const [draftKey, setDraftKey] = useState(agentKey)
  const [saving, setSaving] = useState(false)
  const [deleting, setDeleting] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [tab, setTab] = useState<AgentEditorTab>('identity')
  const [capsText, setCapsText] = useState(() => enabledCapabilityKeys(agent.capabilities).join(', '))
  const [claimsText, setClaimsText] = useState(() => (agent.task_policy?.claims || []).join(', '))

  useEffect(() => {
    setDraft(agent)
    setDraftKey(agentKey)
    setError(null)
    setCapsText(enabledCapabilityKeys(agent.capabilities).join(', '))
    setClaimsText((agent.task_policy?.claims || []).join(', '))
  }, [agent, agentKey])

  useEffect(() => {
    setTab('identity')
  }, [agentKey])

  const update = <K extends keyof FleetAgentDef>(key: K, value: FleetAgentDef[K]) => {
    if (readOnly) return
    setDraft(prev => ({ ...prev, [key]: value }))
  }

  const setCapabilities = (keys: string[]) => {
    update('capabilities', capabilityMapFromKeys(keys))
  }

  const setTaskClaims = (keys: string[]) => {
    update('task_policy', { ...(draft.task_policy || {}), claims: keys })
  }

  const onCapsTextChange = (value: string) => {
    setCapsText(value)
    setCapabilities(parseCapabilityTags(value))
  }

  const onClaimsTextChange = (value: string) => {
    setClaimsText(value)
    setTaskClaims(parseCapabilityTags(value))
  }

  const save = async () => {
    if (!onSave || saving || readOnly) return
    const nextKey = slugifyAgentKey(draftKey)
    if (!nextKey) {
      setError('Role key is required')
      return
    }
    if (nextKey !== agentKey && siblingAgentKeys.includes(nextKey)) {
      setError(`Role "@${nextKey}" already exists`)
      return
    }
    setSaving(true)
    setError(null)
    try {
      await onSave(agentKey, draft, nextKey !== agentKey ? nextKey : undefined)
      onClose()
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setSaving(false)
    }
  }

  const remove = async () => {
    if (!onDelete || deleting || !canDelete) return
    if (!window.confirm(`Delete agent "@${agentKey}"? This cannot be undone.`)) return
    setDeleting(true)
    setError(null)
    try {
      await onDelete()
      onClose()
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
      setDeleting(false)
    }
  }

  const headerKey = draftKey || agentKey
  const roleColor = getAgentColor(headerKey)

  return (
    <div className="h-full flex flex-col overflow-hidden" style={{ background: 'var(--bg-secondary)' }}>
      <div className="flex items-center justify-between px-5 py-3 shrink-0" style={{ borderBottom: '1px solid var(--border-color)' }}>
        <div>
          <h2 className="text-base font-semibold" style={{ color: roleColor.text }}>Edit @{headerKey}</h2>
          <p className="text-xs" style={{ color: 'var(--text-muted)' }}>{readOnly ? 'Template preview' : 'Agent configuration'}</p>
        </div>
        <button onClick={onClose} className="p-1 rounded hover:bg-white/10" style={{ color: 'var(--text-secondary)' }}>
          <X size={18} />
        </button>
      </div>

      <div className="flex gap-1 px-5 shrink-0" style={{ borderBottom: '1px solid var(--border-color)' }}>
        {AGENT_EDITOR_TABS.map(t => (
          <button
            key={t.id}
            type="button"
            onClick={() => setTab(t.id)}
            className={`px-3 py-2 text-xs font-medium capitalize border-b-2 -mb-px transition-colors ${
              tab === t.id ? 'border-primary text-primary' : 'border-transparent'
            }`}
            style={{ color: tab === t.id ? undefined : 'var(--text-muted)' }}
          >
            {t.label}
          </button>
        ))}
      </div>

      <div className="flex-1 min-h-0 overflow-y-auto p-5 space-y-4">
        {tab === 'identity' && (
          <>
            <TextField
              label="Role"
              value={draftKey}
              onChange={v => setDraftKey(v.toLowerCase().replace(/[^a-z0-9_-]/g, ''))}
              disabled={readOnly}
              placeholder="e.g. po, architect, dev"
              hint="Map key used in the communication graph and for color coding"
            />
            <TextField label="Name" value={draft.name || ''} onChange={v => update('name', v)} disabled={readOnly} />
            <div className="grid grid-cols-1 md:grid-cols-2 gap-4 items-stretch">
              <TextareaField label="Identity" value={draft.identity || ''} rows={12} onChange={v => update('identity', v)} disabled={readOnly} />
              <TextareaField label="Behaviors" value={draft.behaviors || ''} rows={12} onChange={v => update('behaviors', v)} disabled={readOnly} />
            </div>
          </>
        )}

        {tab === 'execution' && (
          <>
            <SelectField
              label="Mode"
              value={draft.mode || 'agentic'}
              options={[...FLEET_AGENT_MODES]}
              onChange={v => update('mode', v)}
              disabled={readOnly}
            />
            <TextareaField label="Description" value={draft.description || ''} rows={2} onChange={v => update('description', v)} disabled={readOnly} />
            <div className="grid grid-cols-2 gap-3">
              <NumberField label="Max turns" value={draft.execution?.max_turns} onChange={v => update('execution', { ...(draft.execution || {}), max_turns: v })} disabled={readOnly} />
              <NumberField label="Timeout minutes" value={draft.execution?.timeout_minutes} onChange={v => update('execution', { ...(draft.execution || {}), timeout_minutes: v })} disabled={readOnly} />
            </div>
            <label className="flex items-center gap-2 text-xs" style={{ color: 'var(--text-secondary)' }}>
              <input
                type="checkbox"
                checked={draft.execution?.parallelizable || false}
                disabled={readOnly}
                onChange={e => update('execution', { ...(draft.execution || {}), parallelizable: e.target.checked })}
                className="accent-primary"
              />
              Parallelizable
            </label>
            <SelectField
              label="Workspace"
              value={draft.execution?.workspace || 'shared'}
              options={[...FLEET_WORKSPACE_MODES]}
              onChange={v => update('execution', { ...(draft.execution || {}), workspace: v as NonNullable<FleetAgentDef['execution']>['workspace'] })}
              disabled={readOnly}
            />
            <CheckboxMultiSelectField
              label="Receives from agents"
              options={siblingAgentKeys}
              selected={draft.memory?.receives || []}
              onChange={keys => update('memory', { ...(draft.memory || {}), receives: keys })}
              disabled={readOnly}
              emptyHint={siblingAgentKeys.length === 0 ? 'No other agents in this template yet.' : undefined}
            />
            <label className="flex items-center gap-2 text-xs" style={{ color: 'var(--text-secondary)' }}>
              <input
                type="checkbox"
                checked={draft.memory?.private_work || false}
                disabled={readOnly}
                onChange={e => update('memory', { ...(draft.memory || {}), private_work: e.target.checked })}
                className="accent-primary"
              />
              Private work
            </label>
          </>
        )}

        {tab === 'advanced' && (
          <>
            <p className="text-xs" style={{ color: 'var(--text-muted)' }}>
              Use free-form capability tags for mailbox/task-board orchestration. Tags are lowercase
              (letters, numbers, dots, hyphens); separate multiple with commas.
            </p>
            <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
              <div className="space-y-1">
                <label htmlFor={`agent-caps-${agentKey}`} className="block text-xs" style={{ color: 'var(--text-secondary)' }}>
                  Capabilities
                </label>
                <textarea
                  id={`agent-caps-${agentKey}`}
                  value={capsText}
                  disabled={readOnly}
                  rows={4}
                  onChange={e => onCapsTextChange(e.target.value)}
                  placeholder={"code.write, research\nanalysis"}
                  className="w-full px-3 py-2 rounded-lg text-xs font-mono resize-y overflow-auto focus:outline-none focus:ring-1 focus:ring-primary disabled:opacity-60"
                  style={{ background: 'var(--bg-tertiary)', color: 'var(--text-primary)', border: '1px solid var(--border-color)', minHeight: '5.5rem' }}
                />
                <p className="text-[11px] leading-snug" style={{ color: 'var(--text-muted)' }}>
                  What this agent can do. Shown in the agent prompt and matched against tasks’
                  required capabilities when claiming work. Separate tags with commas or newlines.
                </p>
              </div>
              <div className="space-y-1">
                <label htmlFor={`agent-claims-${agentKey}`} className="block text-xs" style={{ color: 'var(--text-secondary)' }}>
                  Task claims
                </label>
                <textarea
                  id={`agent-claims-${agentKey}`}
                  value={claimsText}
                  disabled={readOnly}
                  rows={4}
                  onChange={e => onClaimsTextChange(e.target.value)}
                  placeholder="code.write"
                  className="w-full px-3 py-2 rounded-lg text-xs font-mono resize-y overflow-auto focus:outline-none focus:ring-1 focus:ring-primary disabled:opacity-60"
                  style={{ background: 'var(--bg-tertiary)', color: 'var(--text-primary)', border: '1px solid var(--border-color)', minHeight: '5.5rem' }}
                />
                <p className="text-[11px] leading-snug" style={{ color: 'var(--text-muted)' }}>
                  Which capability tags this agent will claim from the task board. At least one
                  fleet agent usually needs claims when the board is in use. Separate tags with commas or newlines.
                </p>
              </div>
            </div>
            <NumberField label="Task max concurrent" value={draft.task_policy?.max_concurrent} onChange={v => update('task_policy', { ...(draft.task_policy || {}), max_concurrent: v })} disabled={readOnly} />
          </>
        )}

        {error && <p className="text-xs text-red-400">{error}</p>}
      </div>

      <div className="flex items-center justify-between gap-2 px-5 py-3 shrink-0" style={{ borderTop: '1px solid var(--border-color)' }}>
        <div>
          {!readOnly && onDelete && (
            <button
              onClick={remove}
              disabled={deleting || !canDelete}
              title={canDelete ? `Delete @${agentKey}` : 'At least one agent is required'}
              className="flex items-center gap-1.5 px-3 py-2 text-sm rounded-lg hover:bg-red-500/15 text-red-400 disabled:opacity-40"
            >
              {deleting ? <Loader size={14} className="animate-spin" /> : <Trash2 size={14} />}
              Delete
            </button>
          )}
        </div>
        <div className="flex gap-2">
          <button onClick={onClose} className="px-4 py-2 text-sm rounded-lg hover:bg-white/5" style={{ color: 'var(--text-secondary)' }}>Cancel</button>
          {!readOnly && (
            <button onClick={save} disabled={saving} className="px-4 py-2 text-sm bg-primary hover:bg-primary/90 text-white rounded-lg disabled:opacity-50">
              {saving ? 'Saving...' : 'Save Agent'}
            </button>
          )}
        </div>
      </div>
    </div>
  )
}

function CheckboxMultiSelectField({
  label,
  options,
  selected,
  onChange,
  disabled,
  emptyHint,
}: {
  label: string
  options: string[]
  selected: string[]
  onChange: (selected: string[]) => void
  disabled?: boolean
  emptyHint?: string
}) {
  const toggle = (option: string) => {
    if (disabled) return
    if (selected.includes(option)) {
      onChange(selected.filter(item => item !== option))
    } else {
      onChange([...selected, option])
    }
  }

  return (
    <fieldset className="block text-xs space-y-2" disabled={disabled}>
      <legend style={{ color: 'var(--text-secondary)' }}>{label}</legend>
      {options.length === 0 ? (
        <p style={{ color: 'var(--text-muted)' }}>{emptyHint || 'No options available.'}</p>
      ) : (
        <div className="flex flex-wrap gap-2">
          {options.map(option => (
            <label
              key={option}
              className="flex items-center gap-1.5 px-2 py-1 rounded-lg cursor-pointer"
              style={{ background: 'var(--bg-tertiary)', border: '1px solid var(--border-color)' }}
            >
              <input
                type="checkbox"
                checked={selected.includes(option)}
                onChange={() => toggle(option)}
                className="accent-primary"
              />
              <span style={{ color: 'var(--text-primary)' }}>{option}</span>
            </label>
          ))}
        </div>
      )}
    </fieldset>
  )
}

function TextField({
  label,
  value,
  onChange,
  disabled,
  placeholder,
  hint,
}: {
  label: string
  value: string
  onChange: (value: string) => void
  disabled?: boolean
  placeholder?: string
  hint?: string
}) {
  return (
    <label className="block text-xs space-y-1" style={{ color: 'var(--text-secondary)' }}>
      <span>{label}</span>
      <input
        value={value}
        disabled={disabled}
        placeholder={placeholder}
        onChange={e => onChange(e.target.value)}
        className="w-full px-3 py-2 rounded-lg focus:outline-none focus:ring-1 focus:ring-primary disabled:opacity-60"
        style={{ background: 'var(--bg-tertiary)', color: 'var(--text-primary)', border: '1px solid var(--border-color)' }}
      />
      {hint && <span className="block text-[11px]" style={{ color: 'var(--text-muted)' }}>{hint}</span>}
    </label>
  )
}

function TextareaField({ label, value, rows, onChange, disabled }: { label: string; value: string; rows: number; onChange: (value: string) => void; disabled?: boolean }) {
  return (
    <label className="block text-xs space-y-1" style={{ color: 'var(--text-secondary)' }}>
      <span>{label}</span>
      <textarea
        value={value}
        rows={rows}
        disabled={disabled}
        onChange={e => onChange(e.target.value)}
        className="w-full px-3 py-2 rounded-lg resize-y focus:outline-none focus:ring-1 focus:ring-primary disabled:opacity-60"
        style={{ background: 'var(--bg-tertiary)', color: 'var(--text-primary)', border: '1px solid var(--border-color)' }}
      />
    </label>
  )
}
