import { useCallback, useEffect, useMemo, useState } from 'react'
import { ChevronLeft, ChevronRight, Loader, X } from 'lucide-react'

import {
  createSetupDraft,
  fetchFleet,
  fetchSetupProfile,
  finalizeSetupDraft,
  patchSetupDraft,
  validateSetupStep,
} from '../../api/fleetChat'
import type { SetupDraft, SetupField, SetupProfile, SetupStep } from '../../api/fleetChat'
import SetupChannelCards from './SetupChannelCards'
import SetupCredentialsPanel from './SetupCredentialsPanel'
import SetupMarkdownBlock from './SetupMarkdownBlock'
import SetupProvisionPanel from './SetupProvisionPanel'
import SetupStepper from './SetupStepper'
import SetupStepTools from './SetupStepTools'
import { renderSetupField } from './setupFieldRenderers'

interface SetupWizardProps {
  templateKey: string
  templateName: string
  onClose: () => void
  onComplete: (planKey: string) => void
  onOpenChatGuide?: (templateKey: string, draftId: string) => void
}

function effectiveType(step?: SetupStep): string {
  if (!step) return ''
  if (step.type === 'template_agents') return 'agent_select'
  return step.type
}

function stepActive(step: SetupStep, collected: Record<string, Record<string, unknown>>): boolean {
  if (!step.when) return true
  const match = step.when.match(/^(\w+(?:\.\w+)*)\s*==\s*'([^']+)'$/)
  if (!match) return true
  const [, path, expected] = match
  const parts = path.split('.')
  const stepValues = collected[parts[0]]
  if (!stepValues) return false
  const val = stepValues[parts.slice(1).join('.') || parts[1]]
  return String(val ?? '') === expected
}

function fieldActive(field: SetupField, collected: Record<string, Record<string, unknown>>): boolean {
  if (!field.when) return true
  const match = field.when.match(/^(\w+(?:\.\w+)*)\s*==\s*'([^']+)'$/)
  if (!match) return true
  const [, path, expected] = match
  const parts = path.split('.')
  const stepValues = collected[parts[0]]
  if (!stepValues) return false
  const fieldKey = parts.slice(1).join('.') || parts[1]
  return String(stepValues[fieldKey] ?? '') === expected
}

export default function SetupWizard({ templateKey, templateName, onClose, onComplete, onOpenChatGuide }: SetupWizardProps) {
  const [loading, setLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [draft, setDraft] = useState<SetupDraft | null>(null)
  const [profile, setProfile] = useState<SetupProfile | null>(null)
  const [stepIndex, setStepIndex] = useState(0)
  const [busy, setBusy] = useState(false)
  const [agentKeys, setAgentKeys] = useState<string[]>([])

  const collected = useMemo(() => {
    const out: Record<string, Record<string, unknown>> = {}
    if (!draft?.collected) return out
    for (const [k, v] of Object.entries(draft.collected)) {
      if (v && typeof v === 'object') out[k] = v as Record<string, unknown>
    }
    return out
  }, [draft])

  const activeSteps = useMemo(
    () => (profile?.steps || []).filter(s => stepActive(s, collected)),
    [profile, collected],
  )

  const currentStep = activeSteps[stepIndex]
  const stepType = effectiveType(currentStep)

  useEffect(() => {
    let cancelled = false
    ;(async () => {
      try {
        const { draft: created } = await createSetupDraft(templateKey)
        const { profile: p } = await fetchSetupProfile(created.setup_profile_key)
        const fleet = await fetchFleet(templateKey).catch(() => null)
        const keys = fleet?.fleet?.agents ? Object.keys(fleet.fleet.agents as Record<string, unknown>) : []
        if (!cancelled) {
          setDraft(created)
          setProfile(p)
          setAgentKeys(keys)
          setLoading(false)
        }
      } catch (err) {
        if (!cancelled) {
          setError(err instanceof Error ? err.message : String(err))
          setLoading(false)
        }
      }
    })()
    return () => { cancelled = true }
  }, [templateKey])

  const updateField = useCallback((stepId: string, fieldId: string, value: unknown) => {
    setDraft(prev => {
      if (!prev) return prev
      const nextCollected = { ...prev.collected } as Record<string, Record<string, unknown>>
      const stepValues = { ...(nextCollected[stepId] || {}) }
      stepValues[fieldId] = value
      nextCollected[stepId] = stepValues
      return { ...prev, collected: nextCollected as SetupDraft['collected'] }
    })
  }, [])

  const persistDraft = useCallback(async (extraCollected?: Record<string, Record<string, unknown>>) => {
    if (!draft) return
    const merged = extraCollected
      ? { ...draft.collected, ...extraCollected } as SetupDraft['collected']
      : draft.collected
    const { draft: updated } = await patchSetupDraft(draft.id, {
      collected: merged as unknown as Record<string, unknown>,
      current_step: currentStep?.id,
    })
    setDraft(updated)
  }, [draft, currentStep])

  const goNext = async () => {
    if (!draft || !currentStep) return
    setBusy(true)
    setError(null)
    try {
      let nextCollected = draft.collected
      if (stepType === 'info') {
        const ack = { ...(collected[currentStep.id] || {}), _ack: true }
        nextCollected = { ...draft.collected, [currentStep.id]: ack } as SetupDraft['collected']
        setDraft(prev => prev ? { ...prev, collected: nextCollected } : prev)
      }
      await persistDraft(stepType === 'info' ? { [currentStep.id]: { ...(collected[currentStep.id] || {}), _ack: true } } : undefined)
      if (stepType !== 'info' && stepType !== 'review') {
        await validateSetupStep(draft.id, currentStep.id)
      }
      if (stepType === 'review') {
        const channelType = collected.channel?.type as string | undefined
        const result = await finalizeSetupDraft(draft.id, channelType !== 'github_issues')
        onComplete(result.key)
        return
      }
      setStepIndex(i => Math.min(i + 1, activeSteps.length - 1))
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setBusy(false)
    }
  }

  const goBack = () => setStepIndex(i => Math.max(i - 1, 0))

  const channelTypeField = currentStep?.fields?.find(f => f.id === 'type' && f.maps_to?.includes('channel.type'))

  if (loading) {
    return (
      <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/50">
        <div className="rounded-lg p-6 flex items-center gap-2" style={{ background: 'var(--bg-secondary)' }}>
          <Loader size={18} className="animate-spin text-primary" />
          <span className="text-sm" style={{ color: 'var(--text-secondary)' }}>Loading setup profile...</span>
        </div>
      </div>
    )
  }

  if (error && !profile) {
    return (
      <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/50">
        <div className="rounded-lg p-6 max-w-md" style={{ background: 'var(--bg-secondary)' }}>
          <p className="text-sm text-red-400 mb-4">{error}</p>
          <button onClick={onClose} className="text-sm px-3 py-1.5 rounded bg-primary text-white">Close</button>
        </div>
      </div>
    )
  }

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/50 p-4">
      <div className="w-full max-w-4xl max-h-[90vh] flex flex-col rounded-xl shadow-2xl overflow-hidden" style={{ background: 'var(--bg-secondary)', border: '1px solid var(--border-color)' }}>
        <div className="flex items-center justify-between px-5 py-4" style={{ borderBottom: '1px solid var(--border-color)' }}>
          <div>
            <h2 className="text-base font-semibold" style={{ color: 'var(--text-primary)' }}>Create Plan</h2>
            <p className="text-xs" style={{ color: 'var(--text-muted)' }}>{profile?.name}{templateName ? ` · ${templateName}` : ''}</p>
          </div>
          <button onClick={onClose} className="p-1 rounded hover:bg-white/10"><X size={18} /></button>
        </div>

        <div className="flex-1 overflow-hidden flex min-h-0">
          <div className="p-4 overflow-y-auto">
            <SetupStepper
              steps={activeSteps}
              currentIndex={stepIndex}
              collected={collected}
              stepActive={s => stepActive(s, collected)}
            />
          </div>

          <div className="flex-1 overflow-y-auto p-5 space-y-4 border-l" style={{ borderColor: 'var(--border-color)' }}>
            {currentStep && (
              <>
                <div>
                  <h3 className="text-sm font-semibold" style={{ color: 'var(--text-primary)' }}>{currentStep.title}</h3>
                  {currentStep.summary && <p className="text-xs mt-0.5" style={{ color: 'var(--text-muted)' }}>{currentStep.summary}</p>}
                </div>
                <SetupStepTools step={currentStep} />
              </>
            )}

            {stepType === 'info' && (
              <SetupMarkdownBlock content={currentStep?.content || profile?.description || ''} />
            )}

            {stepType === 'provision' && draft && currentStep && (
              <SetupProvisionPanel
                step={currentStep}
                draftId={draft.id}
                templateKey={templateKey}
                collected={collected}
                onOpenChat={() => onOpenChatGuide?.(templateKey, draft.id)}
                onProvisioned={(template, containerDir) => {
                  updateField('provisioning', 'template', template)
                  updateField('provisioning', 'container_workspace_dir', containerDir)
                }}
              />
            )}

            {stepType === 'agent_select' && (
              <div className="space-y-3">
                <p className="text-xs" style={{ color: 'var(--text-muted)' }}>Optionally limit which agents are included.</p>
                <div className="flex flex-wrap gap-2">
                  {agentKeys.map(key => {
                    const selected = (collected.agents?.include_agents as string[] | undefined) || agentKeys
                    const checked = Array.isArray(selected) ? selected.includes(key) : true
                    return (
                      <label key={key} className="flex items-center gap-1.5 text-xs px-2 py-1 rounded-lg" style={{ background: 'var(--bg-tertiary)', color: 'var(--text-secondary)' }}>
                        <input
                          type="checkbox"
                          checked={checked}
                          onChange={e => {
                            const current = (collected.agents?.include_agents as string[] | undefined) || [...agentKeys]
                            const next = e.target.checked ? [...new Set([...current, key])] : current.filter(k => k !== key)
                            updateField('agents', 'include_agents', next)
                          }}
                          className="accent-primary"
                        />
                        {key}
                      </label>
                    )
                  })}
                </div>
              </div>
            )}

            {stepType === 'review' && (
              <div className="space-y-2 text-xs font-mono rounded-lg p-3 overflow-x-auto" style={{ background: 'var(--bg-tertiary)', color: 'var(--text-secondary)' }}>
                <pre>{JSON.stringify(collected, null, 2)}</pre>
              </div>
            )}

            {stepType === 'credentials' && currentStep && (
              <SetupCredentialsPanel
                fields={currentStep.fields || []}
                stepId={currentStep.id}
                collected={collected}
                onFieldChange={(fieldId, v) => updateField(currentStep.id, fieldId, v)}
              />
            )}

            {stepType === 'form' && currentStep && (
              <div className="space-y-4">
                {currentStep.id === 'channel' && channelTypeField && (
                  <SetupChannelCards
                    profile={profile}
                    channelField={channelTypeField}
                    value={String(collected.channel?.type ?? channelTypeField.default ?? 'chat')}
                    onChange={v => updateField('channel', 'type', v)}
                  />
                )}
                {currentStep.fields?.map(field => {
                  if (!fieldActive(field, collected)) return null
                  if (currentStep.id === 'channel' && field.id === 'type') return null
                  return (
                    <div key={field.id}>
                      {renderSetupField(field, collected[currentStep.id]?.[field.id], v => updateField(currentStep.id, field.id, v))}
                    </div>
                  )
                })}
              </div>
            )}

            {error && <p className="text-xs text-red-400">{error}</p>}
          </div>
        </div>

        <div className="flex items-center justify-between px-5 py-4" style={{ borderTop: '1px solid var(--border-color)' }}>
          <div className="text-xs" style={{ color: 'var(--text-muted)' }}>
            Step {stepIndex + 1} of {activeSteps.length}: {currentStep?.title}
          </div>
          <div className="flex gap-2">
            <button
              onClick={goBack}
              disabled={stepIndex === 0 || busy}
              className="flex items-center gap-1 px-3 py-1.5 text-xs rounded-lg disabled:opacity-40"
              style={{ background: 'var(--bg-tertiary)', color: 'var(--text-secondary)' }}
            >
              <ChevronLeft size={14} /> Back
            </button>
            <button
              onClick={goNext}
              disabled={busy}
              className="flex items-center gap-1 px-3 py-1.5 text-xs rounded-lg bg-primary hover:bg-primary/90 text-white disabled:opacity-50"
            >
              {busy ? <Loader size={14} className="animate-spin" /> : stepType === 'review' ? 'Save Plan' : <>Next <ChevronRight size={14} /></>}
            </button>
          </div>
        </div>
      </div>
    </div>
  )
}
