import { useState, useEffect, useRef } from 'react'
import { ChevronDown, RotateCcw, Search } from 'lucide-react'

import { Button } from '@/components/ui/button'
import { Label } from '@/components/ui/label'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'

import ProviderModelSelector from '../ProviderModelSelector'

const DEFAULT_PROVIDER_VALUE = '__default__'

interface PreChatModelPickerProps {
  availableProviders: string[]
  provider: string
  model: string
  onChange: (provider: string, model: string) => void
}

export default function PreChatModelPicker({
  availableProviders,
  provider,
  model,
  onChange,
}: PreChatModelPickerProps) {
  const wrapperRef = useRef<HTMLDivElement>(null)
  const [open, setOpen] = useState(false)
  const [localProvider, setLocalProvider] = useState(provider)
  const [localModel, setLocalModel] = useState(model)
  const [showModelSelector, setShowModelSelector] = useState(false)

  useEffect(() => {
    setLocalProvider(provider)
    setLocalModel(model)
  }, [provider, model])

  useEffect(() => {
    function handleClickOutside(e: MouseEvent) {
      const target = e.target as HTMLElement | null
      if (!target) return
      // Radix Select content is portaled outside the wrapper; keep the popover open.
      if (target.closest('[data-slot="select-content"], [data-radix-select-content], [role="listbox"]')) return
      if (wrapperRef.current && !wrapperRef.current.contains(target)) {
        setOpen(false)
      }
    }
    document.addEventListener('mousedown', handleClickOutside)
    return () => document.removeEventListener('mousedown', handleClickOutside)
  }, [])

  const handleApply = () => {
    onChange(localProvider, localModel)
    setOpen(false)
  }

  const handleReset = () => {
    onChange('', '')
    setOpen(false)
  }

  const handleModelSelected = (modelId: string) => {
    setLocalModel(modelId)
    setShowModelSelector(false)
  }

  const displayLabel = provider ? `${provider}${model ? '/' + model : ''}` : 'default'

  return (
    <div ref={wrapperRef} className="relative inline-block">
      <Button
        type="button"
        variant="outline"
        size="sm"
        onClick={() => setOpen(!open)}
        className="h-7 gap-1 px-2 text-xs text-muted-foreground"
      >
        Model: {displayLabel}
        <ChevronDown className="size-3" />
      </Button>
      {open && (
        <div className="absolute top-full left-0 z-50 mt-1 w-max min-w-72 max-w-md rounded-lg border border-border bg-popover p-3 text-popover-foreground shadow-[var(--shadow-elevated)]">
          <div className="mb-2 space-y-1.5">
            <Label className="text-xs">Provider</Label>
            <Select
              value={localProvider || DEFAULT_PROVIDER_VALUE}
              onValueChange={(value) => {
                setLocalProvider(value === DEFAULT_PROVIDER_VALUE ? '' : value)
                setLocalModel('')
              }}
            >
              <SelectTrigger role="combobox" className="h-8 w-full bg-background text-sm">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                <SelectItem value={DEFAULT_PROVIDER_VALUE}>(default — cascade)</SelectItem>
                {availableProviders.map((p) => (
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
              onClick={() => localProvider && setShowModelSelector(true)}
              disabled={!localProvider}
              className="h-8 w-full justify-between bg-background font-normal"
              title={!localProvider ? 'Select a provider first' : 'Browse models'}
            >
              <span className="break-all text-left">
                {localModel || (localProvider ? 'Click to browse models…' : 'Select provider first')}
              </span>
              <Search className="size-3 shrink-0 text-muted-foreground" />
            </Button>
          </div>
          <div className="flex items-center justify-between">
            <Button type="button" variant="ghost" size="sm" onClick={handleReset} className="h-7 px-2 text-xs">
              <RotateCcw className="size-3" /> Reset
            </Button>
            <Button type="button" size="sm" onClick={handleApply} className="h-7 px-3 text-xs">
              Apply
            </Button>
          </div>
        </div>
      )}
      <ProviderModelSelector
        isOpen={showModelSelector}
        onClose={() => setShowModelSelector(false)}
        onSelect={handleModelSelected}
        currentModel={localModel}
        provider={localProvider}
      />
    </div>
  )
}
