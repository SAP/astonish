import { useState, useEffect, useMemo, useCallback } from 'react'
import { Check, Database, DollarSign, Loader2, Search, Zap } from 'lucide-react'

import { Badge } from '@/components/ui/badge'
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { cn } from '@/lib/utils'

import { teamFetch } from '../api/teamContext'

interface ModelPricing {
  prompt?: string
  completion?: string
}

interface Model {
  id: string
  name?: string
  pricing?: ModelPricing
  context_length?: number
  max_completion_tokens?: number
}

interface ProviderModelSelectorProps {
  isOpen: boolean
  onClose: () => void
  onSelect: (modelId: string) => void
  currentModel?: string
  provider?: string
}

const providerTitles: Record<string, string> = {
  openrouter: 'Select OpenRouter Model',
  anthropic: 'Select Anthropic Model',
  gemini: 'Select Google AI Model',
  groq: 'Select Groq Model',
  litellm: 'Select LiteLLM Model',
  openai: 'Select OpenAI Model',
  poe: 'Select Poe Model',
  sap_ai_core: 'Select SAP AI Core Model',
  xai: 'Select xAI Grok Model',
  lm_studio: 'Select LM Studio Model',
  ollama: 'Select Ollama (Local) Model',
}

/**
 * Enhanced model selector with search for various providers
 */
export default function ProviderModelSelector({ isOpen, onClose, onSelect, currentModel, provider = 'openrouter' }: ProviderModelSelectorProps) {
  const [models, setModels] = useState<Model[]>([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)
  const [searchQuery, setSearchQuery] = useState('')

  const fetchModels = useCallback(async () => {
    setLoading(true)
    setError(null)
    try {
      const res = await teamFetch(`/api/providers/${provider}/models-metadata`)
      if (!res.ok) throw new Error('Failed to fetch models')
      const data = await res.json() as { models?: Model[] }
      setModels(data.models || [])
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to fetch models')
    } finally {
      setLoading(false)
    }
  }, [provider])

  useEffect(() => {
    if (isOpen) {
      setSearchQuery('')
      void fetchModels()
    }
  }, [isOpen, fetchModels])

  const filteredModels = useMemo(() => {
    if (!searchQuery.trim()) return models
    const query = searchQuery.toLowerCase()
    return models.filter(m =>
      m.name?.toLowerCase().includes(query) ||
      m.id?.toLowerCase().includes(query)
    )
  }, [models, searchQuery])

  const formatPrice = (price: string | undefined) => {
    if (!price || price === '0') return 'Free'
    const num = parseFloat(price)
    if (num === 0) return 'Free'
    const perMillion = num * 1000000
    if (perMillion < 0.01) return `$${perMillion.toFixed(4)}/M`
    if (perMillion < 1) return `$${perMillion.toFixed(3)}/M`
    return `$${perMillion.toFixed(2)}/M`
  }

  const formatContextLength = (length: number | undefined) => {
    if (!length) return '-'
    if (length >= 1000000) return `${(length / 1000000).toFixed(1)}M`
    if (length >= 1000) return `${(length / 1000).toFixed(0)}K`
    return length.toString()
  }

  const handleSelect = (model: Model) => {
    onSelect(model.id)
    onClose()
  }

  return (
    <Dialog open={isOpen} onOpenChange={(open) => { if (!open) onClose() }}>
      <DialogContent
        className="flex max-h-[80vh] w-full max-w-3xl flex-col gap-0 overflow-hidden border-panel-border bg-panel-background p-0 shadow-[var(--shadow-elevated)] sm:max-w-3xl"
        showCloseButton
      >
        <DialogHeader className="border-b px-4 py-4 text-left">
          <DialogTitle>{providerTitles[provider] || 'Select Model'}</DialogTitle>
          <DialogDescription>
            Search and choose a model for the selected provider.
          </DialogDescription>
        </DialogHeader>

        <div className="space-y-2 border-b px-4 py-4">
          <div className="relative">
            <Search className="absolute top-1/2 left-3 size-4 -translate-y-1/2 text-muted-foreground" />
            <Input
              type="text"
              value={searchQuery}
              onChange={(e) => setSearchQuery(e.target.value)}
              placeholder="Search models..."
              className="bg-background pl-10"
              autoFocus
            />
          </div>
          <div className="text-xs text-muted-foreground">
            {filteredModels.length} models available
          </div>
        </div>

        <div className="min-h-0 flex-1 overflow-y-auto p-4">
          {loading && (
            <div className="flex items-center justify-center gap-2 py-12 text-sm text-muted-foreground">
              <Loader2 className="size-5 animate-spin text-primary" />
              Loading models...
            </div>
          )}

          {error && (
            <div className="rounded-lg border border-destructive/30 bg-destructive/10 px-4 py-8 text-center text-sm text-destructive">
              {error}
            </div>
          )}

          {!loading && !error && (
            <div className="space-y-2">
              {filteredModels.map(model => {
                const isSelected = model.id === currentModel
                return (
                  <button
                    key={model.id}
                    type="button"
                    onClick={() => handleSelect(model)}
                    className={cn(
                      'w-full rounded-lg border bg-background p-3 text-left transition-all hover:border-primary/50',
                      isSelected && 'border-primary bg-primary/10 ring-1 ring-primary/30'
                    )}
                  >
                    <div className="flex items-start justify-between gap-3">
                      <div className="min-w-0 flex-1">
                        <div className="flex items-center gap-2">
                          <span className="truncate font-medium text-foreground">
                            {model.name || model.id}
                          </span>
                          {isSelected && <Check className="size-4 shrink-0 text-primary" />}
                        </div>
                        <code className="mt-1 block truncate text-xs text-muted-foreground">
                          {model.id}
                        </code>
                      </div>

                      <div className="flex shrink-0 flex-col items-end gap-1 text-xs">
                        {model.pricing && (
                          <div className="flex items-center gap-2">
                            <Badge variant="secondary" className="gap-1 bg-[color:var(--success)]/10 text-[color:var(--success)]">
                              <DollarSign className="size-3" />
                              In: {formatPrice(model.pricing.prompt)}
                            </Badge>
                            <Badge variant="secondary" className="gap-1 bg-primary/10 text-primary">
                              <DollarSign className="size-3" />
                              Out: {formatPrice(model.pricing.completion)}
                            </Badge>
                          </div>
                        )}
                        <div className="mt-1 flex items-center gap-2">
                          {(model.context_length ?? 0) > 0 && (
                            <Badge variant="outline" className="gap-1">
                              <Database className="size-3" />
                              {formatContextLength(model.context_length)} ctx
                            </Badge>
                          )}
                          {(model.max_completion_tokens ?? 0) > 0 && (
                            <Badge variant="outline" className="gap-1">
                              <Zap className="size-3" />
                              {formatContextLength(model.max_completion_tokens)} out
                            </Badge>
                          )}
                        </div>
                      </div>
                    </div>
                  </button>
                )
              })}

              {filteredModels.length === 0 && (
                <div className="rounded-lg border border-dashed py-8 text-center text-sm text-muted-foreground">
                  No models match your search
                </div>
              )}
            </div>
          )}
        </div>
      </DialogContent>
    </Dialog>
  )
}
