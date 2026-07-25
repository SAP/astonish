import { useEffect, useState } from 'react'
import { Loader } from 'lucide-react'

import { fetchSetupToolCatalog } from '../../api/fleetChat'
import type { SetupField, SetupProfile, SetupStep } from '../../api/fleetChat'
import SetupMarkdownBlock from './SetupMarkdownBlock'
import SetupStepTools from './SetupStepTools'

interface ToolCatalogEntry {
  name: string
  group: string
  label: string
  description: string
}

interface SetupProfileStepEditorProps {
  profile: SetupProfile
  readOnly?: boolean
  onProfileChange?: (profile: SetupProfile) => void
}

type StepTab = 'overview' | 'fields' | 'prompt' | 'tools'

function updateStep(profile: SetupProfile, stepId: string, patch: Partial<SetupStep>): SetupProfile {
  return {
    ...profile,
    steps: profile.steps.map(s => (s.id === stepId ? { ...s, ...patch } : s)),
  }
}

export default function SetupProfileStepEditor({ profile, readOnly = false, onProfileChange }: SetupProfileStepEditorProps) {
  const [selectedId, setSelectedId] = useState(profile.steps[0]?.id || '')
  const [tab, setTab] = useState<StepTab>('overview')
  const [catalog, setCatalog] = useState<ToolCatalogEntry[]>([])
  const [catalogLoading, setCatalogLoading] = useState(true)

  const selected = profile.steps.find(s => s.id === selectedId) || profile.steps[0]

  useEffect(() => {
    fetchSetupToolCatalog()
      .then(res => setCatalog(res.tools || []))
      .catch(() => setCatalog([]))
      .finally(() => setCatalogLoading(false))
  }, [])

  useEffect(() => {
    if (!profile.steps.some(s => s.id === selectedId)) {
      setSelectedId(profile.steps[0]?.id || '')
    }
  }, [profile.steps, selectedId])

  const patchStep = (patch: Partial<SetupStep>) => {
    if (!selected || readOnly || !onProfileChange) return
    onProfileChange(updateStep(profile, selected.id, patch))
  }

  const patchField = (fieldId: string, patch: Partial<SetupField>) => {
    if (!selected?.fields || readOnly || !onProfileChange) return
    const fields = selected.fields.map(f => (f.id === fieldId ? { ...f, ...patch } : f))
    onProfileChange(updateStep(profile, selected.id, { fields }))
  }

  const toggleTool = (toolName: string) => {
    if (!selected || readOnly || !onProfileChange) return
    const current = selected.tools || []
    const next = current.includes(toolName) ? current.filter(t => t !== toolName) : [...current, toolName]
    patchStep({ tools: next })
  }

  if (!selected) {
    return <p className="text-sm" style={{ color: 'var(--text-muted)' }}>No steps defined.</p>
  }

  return (
    <div className="flex gap-4 flex-1 min-h-0">
      <nav className="w-48 shrink-0 space-y-1 overflow-y-auto">
        {profile.steps.map((step, i) => {
          const active = step.id === selected.id
          return (
            <button
              key={step.id}
              type="button"
              onClick={() => { setSelectedId(step.id); setTab('overview') }}
              className={`w-full text-left px-2 py-2 rounded-lg text-xs transition-colors ${active ? 'bg-primary/15 border border-primary/30' : 'hover:bg-white/5'}`}
            >
              <div className="flex items-center gap-2">
                <span className="w-5 h-5 rounded-full flex items-center justify-center text-[10px] font-mono shrink-0 bg-white/5" style={{ color: 'var(--text-muted)' }}>
                  {i + 1}
                </span>
                <span className="font-medium truncate" style={{ color: active ? 'var(--brand)' : 'var(--text-secondary)' }}>
                  {step.title}
                </span>
              </div>
              <p className="mt-0.5 pl-7 truncate capitalize" style={{ color: 'var(--text-muted)' }}>{step.type}</p>
            </button>
          )
        })}
      </nav>

      <div className="flex-1 min-w-0 flex flex-col rounded-lg overflow-hidden" style={{ border: '1px solid var(--border-color)' }}>
        <div className="flex gap-1 px-3 pt-2" style={{ borderBottom: '1px solid var(--border-color)' }}>
          {(['overview', 'fields', 'prompt', 'tools'] as StepTab[]).map(t => (
            <button
              key={t}
              type="button"
              onClick={() => setTab(t)}
              className={`px-3 py-2 text-xs font-medium capitalize border-b-2 -mb-px transition-colors ${
                tab === t ? 'border-primary text-primary' : 'border-transparent'
              }`}
              style={{ color: tab === t ? undefined : 'var(--text-muted)' }}
            >
              {t}
            </button>
          ))}
        </div>

        <div className="flex-1 overflow-y-auto p-4 space-y-4">
          {tab === 'overview' && (
            <>
              <div className="grid gap-3 sm:grid-cols-2">
                <label className="space-y-1">
                  <span className="text-xs" style={{ color: 'var(--text-muted)' }}>Title</span>
                  <input
                    value={selected.title}
                    disabled={readOnly}
                    onChange={e => patchStep({ title: e.target.value })}
                    className="w-full text-sm px-2 py-1.5 rounded-lg disabled:opacity-70"
                    style={{ background: 'var(--bg-tertiary)', color: 'var(--text-primary)', border: '1px solid var(--border-color)' }}
                  />
                </label>
                <label className="space-y-1">
                  <span className="text-xs" style={{ color: 'var(--text-muted)' }}>Type</span>
                  <input
                    value={selected.type}
                    disabled={readOnly}
                    onChange={e => patchStep({ type: e.target.value })}
                    className="w-full text-sm px-2 py-1.5 rounded-lg disabled:opacity-70 font-mono"
                    style={{ background: 'var(--bg-tertiary)', color: 'var(--text-primary)', border: '1px solid var(--border-color)' }}
                  />
                </label>
                <label className="space-y-1">
                  <span className="text-xs" style={{ color: 'var(--text-muted)' }}>Icon</span>
                  <input
                    value={selected.icon || ''}
                    disabled={readOnly}
                    onChange={e => patchStep({ icon: e.target.value || undefined })}
                    className="w-full text-sm px-2 py-1.5 rounded-lg disabled:opacity-70 font-mono"
                    style={{ background: 'var(--bg-tertiary)', color: 'var(--text-primary)', border: '1px solid var(--border-color)' }}
                  />
                </label>
                <label className="space-y-1">
                  <span className="text-xs" style={{ color: 'var(--text-muted)' }}>When</span>
                  <input
                    value={selected.when || ''}
                    disabled={readOnly}
                    onChange={e => patchStep({ when: e.target.value || undefined })}
                    className="w-full text-sm px-2 py-1.5 rounded-lg disabled:opacity-70 font-mono"
                    style={{ background: 'var(--bg-tertiary)', color: 'var(--text-primary)', border: '1px solid var(--border-color)' }}
                    placeholder="channel.type == 'github_issues'"
                  />
                </label>
              </div>
              <label className="block space-y-1">
                <span className="text-xs" style={{ color: 'var(--text-muted)' }}>Summary</span>
                <input
                  value={selected.summary || ''}
                  disabled={readOnly}
                  onChange={e => patchStep({ summary: e.target.value || undefined })}
                  className="w-full text-sm px-2 py-1.5 rounded-lg disabled:opacity-70"
                  style={{ background: 'var(--bg-tertiary)', color: 'var(--text-primary)', border: '1px solid var(--border-color)' }}
                />
              </label>
              {selected.content && (
                <div>
                  <p className="text-xs mb-2" style={{ color: 'var(--text-muted)' }}>Content preview</p>
                  <SetupMarkdownBlock content={selected.content} />
                </div>
              )}
              <SetupStepTools step={selected} />
            </>
          )}

          {tab === 'fields' && (
            <>
              {!selected.fields?.length ? (
                <p className="text-xs" style={{ color: 'var(--text-muted)' }}>No fields on this step.</p>
              ) : (
                <div className="overflow-x-auto">
                  <table className="w-full text-xs">
                    <thead>
                      <tr style={{ color: 'var(--text-muted)' }}>
                        <th className="text-left py-1 pr-2">ID</th>
                        <th className="text-left py-1 pr-2">Label</th>
                        <th className="text-left py-1 pr-2">Type</th>
                        <th className="text-left py-1">maps_to</th>
                      </tr>
                    </thead>
                    <tbody>
                      {selected.fields.map(field => (
                        <tr key={field.id} style={{ color: 'var(--text-secondary)' }}>
                          <td className="py-1.5 pr-2 font-mono">{field.id}</td>
                          <td className="py-1.5 pr-2">
                            <input
                              value={field.label}
                              disabled={readOnly}
                              onChange={e => patchField(field.id, { label: e.target.value })}
                              className="w-full px-1.5 py-0.5 rounded disabled:opacity-70"
                              style={{ background: 'var(--bg-tertiary)', border: '1px solid var(--border-color)' }}
                            />
                          </td>
                          <td className="py-1.5 pr-2 font-mono">{field.type}</td>
                          <td className="py-1.5 font-mono opacity-70">{field.maps_to || '—'}</td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
              )}
            </>
          )}

          {tab === 'prompt' && (
            <>
              <label className="block space-y-1">
                <span className="text-xs" style={{ color: 'var(--text-muted)' }}>Prompt (chat + AI guide)</span>
                <textarea
                  value={selected.prompt || selected.guidance || ''}
                  disabled={readOnly}
                  onChange={e => patchStep({ prompt: e.target.value, guidance: undefined })}
                  rows={10}
                  className="w-full text-xs font-mono px-2 py-1.5 rounded-lg disabled:opacity-70"
                  style={{ background: 'var(--bg-tertiary)', color: 'var(--text-secondary)', border: '1px solid var(--border-color)' }}
                />
              </label>
              <label className="block space-y-1">
                <span className="text-xs" style={{ color: 'var(--text-muted)' }}>Content (markdown for info steps)</span>
                <textarea
                  value={selected.content || ''}
                  disabled={readOnly}
                  onChange={e => patchStep({ content: e.target.value || undefined })}
                  rows={8}
                  className="w-full text-xs font-mono px-2 py-1.5 rounded-lg disabled:opacity-70"
                  style={{ background: 'var(--bg-tertiary)', color: 'var(--text-secondary)', border: '1px solid var(--border-color)' }}
                />
              </label>
              {selected.content && <SetupMarkdownBlock content={selected.content} />}
            </>
          )}

          {tab === 'tools' && (
            <>
              {catalogLoading ? (
                <Loader size={16} className="animate-spin text-primary" />
              ) : (
                <div className="space-y-2">
                  <p className="text-xs" style={{ color: 'var(--text-muted)' }}>
                    Select tools available during this step. Profile defaults: {(profile.pinned_tool_groups || []).join(', ') || 'none'}
                  </p>
                  <div className="flex flex-wrap gap-2">
                    {catalog.map(tool => {
                      const selectedTool = (selected.tools || []).includes(tool.name)
                      return (
                        <button
                          key={tool.name}
                          type="button"
                          disabled={readOnly}
                          onClick={() => toggleTool(tool.name)}
                          title={tool.description}
                          className={`text-[10px] px-2 py-1 rounded-full font-mono transition-colors disabled:opacity-70 ${
                            selectedTool ? 'bg-primary/20 text-primary border border-primary/40' : 'bg-white/5 text-gray-400 border border-transparent hover:border-white/10'
                          }`}
                        >
                          {tool.name}
                        </button>
                      )
                    })}
                  </div>
                  {(selected.pinned_tool_groups || []).length > 0 && (
                    <p className="text-xs mt-3" style={{ color: 'var(--text-muted)' }}>
                      Step tool groups: {selected.pinned_tool_groups!.join(', ')}
                    </p>
                  )}
                </div>
              )}
            </>
          )}
        </div>
      </div>
    </div>
  )
}
