import { useState, useEffect, useRef, useMemo } from 'react'
import { AlertTriangle, ChevronDown, RotateCcw, Search } from 'lucide-react'

import { Button } from '@/components/ui/button'
import { Label } from '@/components/ui/label'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'

import { patchSessionModel } from '../../api/studioChat'
import type { SessionModelStatus } from '../../api/studioChat'
import ProviderModelSelector from '../ProviderModelSelector'

const DEFAULT_PROVIDER_VALUE = '__default__'

interface SessionModelPickerProps {
  sessionId: string
  modelStatus: SessionModelStatus
  /** Extra provider names to union with modelStatus.availableProviders (e.g. pre-chat list). */
  availableProviders?: string[]
  /** Increment to force-open the popover (e.g. from ModelCredentialBanner). */
  openSignal?: number
  onUpdate: (status: SessionModelStatus) => void
}

/**
 * In-session model picker — same visual structure as PreChatModelPicker
 * (trigger button + popover with provider select + model browse), but persists
 * via patchSessionModel instead of local onChange.
 */
export default function SessionModelPicker({
  sessionId,
  modelStatus,
  availableProviders,
  openSignal,
  onUpdate,
}: SessionModelPickerProps) {
  const wrapperRef = useRef<HTMLDivElement>(null)
  const [open, setOpen] = useState(false)
  const [provider, setProvider] = useState(modelStatus.pinnedProvider || '')
  const [model, setModel] = useState(modelStatus.pinnedModel || '')
  const [error, setError] = useState<string | null>(null)
  const [showModelSelector, setShowModelSelector] = useState(false)

  useEffect(() => {
    if (openSignal) setOpen(true)
  }, [openSignal])

  const providers = useMemo(() => {
    const set = new Set<string>([
      ...(modelStatus.availableProviders || []),
      ...(availableProviders || []),
    ])
    if (modelStatus.pinnedProvider) set.add(modelStatus.pinnedProvider)
    return Array.from(set).sort()
  }, [modelStatus.availableProviders, modelStatus.pinnedProvider, availableProviders])

  useEffect(() => {
    setProvider(modelStatus.pinnedProvider || '')
    setModel(modelStatus.pinnedModel || '')
  }, [modelStatus.pinnedProvider, modelStatus.pinnedModel])

  useEffect(() => {
    function handleClickOutside(e: MouseEvent) {
      const target = e.target as HTMLElement | null
      if (!target) return
      // Radix Select content is portaled outside the wrapper; keep the popover open.
      if (target.closest('[data-slot="select-content"], [data-radix-select-content], [role="listbox"]')) return
      if (wrapperRef.current && !wrapperRef.current.contains(target)) {
        setProvider(modelStatus.pinnedProvider || '')
        setModel(modelStatus.pinnedModel || '')
        setError(null)
        setShowModelSelector(false)
        setOpen(false)
      }
    }
    document.addEventListener('mousedown', handleClickOutside)
    return () => document.removeEventListener('mousedown', handleClickOutside)
  }, [modelStatus.pinnedProvider, modelStatus.pinnedModel])

  const handleSave = async () => {
    setError(null)
    try {
      const patched = await patchSessionModel(sessionId, provider, model)
      onUpdate({
        ...modelStatus,
        ...patched,
        availableProviders: modelStatus.availableProviders,
      })
      setOpen(false)
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Failed to save')
      setTimeout(() => setError(null), 4000)
    }
  }

  const handleReset = async () => {
    setError(null)
    try {
      const patched = await patchSessionModel(sessionId, '', '')
      onUpdate({
        ...modelStatus,
        ...patched,
        availableProviders: modelStatus.availableProviders,
      })
      setOpen(false)
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Failed to reset')
      setTimeout(() => setError(null), 4000)
    }
  }

  const handleModelSelected = (modelId: string) => {
    setModel(modelId)
    setShowModelSelector(false)
  }

  const displayLabel = modelStatus.pinnedProvider
    ? `${modelStatus.pinnedProvider}${modelStatus.pinnedModel ? '/' + modelStatus.pinnedModel : ''}`
    : 'default'

  const noProviders = providers.length === 0

  return (
    <div ref={wrapperRef} className="relative inline-block">
      <Button
        type="button"
        variant="outline"
        size="sm"
        onClick={() => setOpen(!open)}
        className="h-7 gap-1 px-2 text-xs text-muted-foreground"
        title={`Model: ${modelStatus.effectiveProvider}/${modelStatus.effectiveModel}${modelStatus.pinnedProvider ? ' (pinned)' : ''}`}
      >
        Model: {displayLabel}
        <ChevronDown className="size-3" />
      </Button>
      {open && (
        <div className="absolute top-full left-0 z-50 mt-1 w-max min-w-72 max-w-md rounded-lg border border-border bg-popover p-3 text-popover-foreground shadow-[var(--shadow-elevated)]">
          <p className="mb-2 text-[10px] text-muted-foreground">
            Currently: {modelStatus.effectiveProvider}/{modelStatus.effectiveModel}
          </p>
          <div className="mb-2 space-y-1.5">
            <Label className="text-xs">Provider</Label>
            <Select
              value={provider || DEFAULT_PROVIDER_VALUE}
              onValueChange={(value) => {
                setProvider(value === DEFAULT_PROVIDER_VALUE ? '' : value)
                setModel('')
              }}
            >
              <SelectTrigger role="combobox" className="h-8 w-full bg-background text-sm">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value={DEFAULT_PROVIDER_VALUE}>(default — cascade)</SelectItem>
                {providers.map((p) => (
                  <SelectItem key={p} value={p}>{p}</SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
          <div className="mb-2 space-y-1.5">
            <Label className="text-xs">Model</Label>
            <Button
              type="button"
              variant="outline"
              onClick={() => provider && setShowModelSelector(true)}
              disabled={!provider}
              className="h-8 w-full justify-between gap-2 bg-background font-normal"
              title={!provider ? 'Select a provider first' : 'Browse models'}
            >
              <span className="break-all text-left">{model || (provider ? 'Click to browse models…' : 'Select provider first')}</span>
              <Search className="size-3 shrink-0 text-muted-foreground" />
            </Button>
          </div>
          <div className="flex items-center justify-between">
            {modelStatus.pinnedProvider ? (
              <Button type="button" variant="ghost" size="sm" onClick={handleReset} className="h-7 px-2 text-xs">
                <RotateCcw className="size-3" /> Reset
              </Button>
            ) : (
              <span />
            )}
            <Button
              type="button"
              size="sm"
              onClick={handleSave}
              disabled={noProviders}
              title={noProviders ? 'No providers configured' : undefined}
              className="ml-auto h-7 px-3 text-xs"
            >
              Save
            </Button>
          </div>
          {error && (
            <div className="mt-2 flex items-center gap-1.5 text-xs text-destructive">
              <AlertTriangle className="size-3" />
              <span>{error}</span>
            </div>
          )}
        </div>
      )}
      <ProviderModelSelector
        isOpen={showModelSelector}
        onClose={() => setShowModelSelector(false)}
        onSelect={handleModelSelected}
        currentModel={model}
        provider={provider}
      />
    </div>
  )
}
