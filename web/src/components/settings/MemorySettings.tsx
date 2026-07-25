import { useState, useEffect } from 'react'
import { AlertCircle, Check, Cpu, Loader2, Save } from 'lucide-react'

import { Alert, AlertDescription } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select'
import { Switch } from '@/components/ui/switch'

import { saveFullConfigSection } from './settingsApi'

const LOCAL_PROVIDER_VALUE = '__local__'

// Cloud providers that need model/base_url/api_key fields
const CLOUD_PROVIDERS = ['openai', 'ollama', 'openai-compat']

// Default models per provider
const DEFAULT_MODELS: Record<string, string> = {
  '': 'sentence-transformers/all-MiniLM-L6-v2 (384-dim, ~23 MB)',
  local: 'sentence-transformers/all-MiniLM-L6-v2 (384-dim, ~23 MB)',
  openai: 'text-embedding-3-small',
  ollama: 'nomic-embed-text',
  'openai-compat': 'text-embedding-3-small',
}

export default function MemorySettings({ config, onSaved }: { config: Record<string, any>; onSaved?: () => void }) {
  const [form, setForm] = useState({
    enabled: true,
    memory_dir: '',
    vector_dir: '',
    embedding: { provider: '', model: '', base_url: '', api_key: '' },
    chunking: { max_chars: 1600, overlap: 320 },
    search: { max_results: 6, min_score: 0.35 },
    sync: { watch: true, debounce_ms: 1500 }
  })
  const [saving, setSaving] = useState(false)
  const [saveSuccess, setSaveSuccess] = useState(false)
  const [error, setError] = useState<string | null>(null)

  useEffect(() => {
    if (config) {
      setForm({
        enabled: config.enabled !== false,
        memory_dir: config.memory_dir || '',
        vector_dir: config.vector_dir || '',
        embedding: {
          provider: config.embedding?.provider || '',
          model: config.embedding?.model || '',
          base_url: config.embedding?.base_url || '',
          api_key: config.embedding?.api_key || ''
        },
        chunking: {
          max_chars: config.chunking?.max_chars || 1600,
          overlap: config.chunking?.overlap || 320
        },
        search: {
          max_results: config.search?.max_results || 6,
          min_score: config.search?.min_score || 0.35
        },
        sync: {
          watch: config.sync?.watch !== false,
          debounce_ms: config.sync?.debounce_ms || 1500
        }
      })
    }
  }, [config])

  const handleSave = async () => {
    setSaving(true)
    setSaveSuccess(false)
    setError(null)
    try {
      await saveFullConfigSection('memory', form)
      setSaveSuccess(true)
      if (onSaved) onSaved()
      setTimeout(() => setSaveSuccess(false), 2000)
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : String(err))
    } finally {
      setSaving(false)
    }
  }

  const isCloudProvider = CLOUD_PROVIDERS.includes(form.embedding.provider)
  const effectiveProvider = form.embedding.provider || ''
  const defaultModel = DEFAULT_MODELS[effectiveProvider] || ''

  return (
    <div className="max-w-2xl space-y-6">
      <Card className="border-border bg-card shadow-sm">
        <CardHeader>
          <CardTitle className="text-base">Memory</CardTitle>
          <CardDescription>
            Configure semantic memory, local vector indexes, embeddings, and file watcher behavior.
          </CardDescription>
        </CardHeader>
        <CardContent>
          <div className="flex items-start justify-between gap-4 rounded-xl border border-border bg-background p-4">
            <div>
              <Label htmlFor="memory-enabled">Enable Memory</Label>
              <p className="mt-1 text-xs leading-relaxed text-muted-foreground">
                Semantic memory and RAG system. Indexes memory files for context-aware retrieval. Default: enabled.
              </p>
            </div>
            <Switch
              id="memory-enabled"
              checked={form.enabled}
              onCheckedChange={(checked) => setForm({ ...form, enabled: checked })}
              aria-label="Enable memory"
            />
          </div>
        </CardContent>
      </Card>

      {form.enabled && (
        <>
          <Card className="border-border bg-card shadow-sm">
            <CardHeader>
              <CardTitle className="text-base">Directories</CardTitle>
              <CardDescription>Control where memory source files and vector indexes are stored.</CardDescription>
            </CardHeader>
            <CardContent className="space-y-4">
              <div className="space-y-2">
                <Label htmlFor="memory-directory">Memory Directory</Label>
                <Input
                  id="memory-directory"
                  type="text"
                  value={form.memory_dir}
                  onChange={(e) => setForm({ ...form, memory_dir: e.target.value })}
                  placeholder="~/.config/astonish/memory/ (default)"
                  className="bg-background font-mono"
                />
                <p className="text-xs text-muted-foreground">
                  Directory containing memory markdown files. Default: ~/.config/astonish/memory/.
                </p>
              </div>
              <div className="space-y-2">
                <Label htmlFor="vector-directory">Vector Directory</Label>
                <Input
                  id="vector-directory"
                  type="text"
                  value={form.vector_dir}
                  onChange={(e) => setForm({ ...form, vector_dir: e.target.value })}
                  placeholder="~/.config/astonish/memory/vectors/ (default)"
                  className="bg-background font-mono"
                />
                <p className="text-xs text-muted-foreground">
                  Directory for vector index storage. Default: ~/.config/astonish/memory/vectors/.
                </p>
              </div>
            </CardContent>
          </Card>

          <Card className="border-border bg-card shadow-sm">
            <CardHeader>
              <CardTitle className="text-base">Embedding Provider</CardTitle>
              <CardDescription>
                Use the local built-in embedding model or connect to an external embedding provider.
              </CardDescription>
            </CardHeader>
            <CardContent className="space-y-4">
              <div className="space-y-2">
                <Label htmlFor="embedding-provider">Provider</Label>
                <Select
                  value={form.embedding.provider || LOCAL_PROVIDER_VALUE}
                  onValueChange={(value) => setForm({ ...form, embedding: { ...form.embedding, provider: value === LOCAL_PROVIDER_VALUE ? '' : value } })}
                >
                  <SelectTrigger id="embedding-provider" className="w-full bg-background">
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value={LOCAL_PROVIDER_VALUE}>Local built-in (default)</SelectItem>
                    <SelectItem value="openai">OpenAI</SelectItem>
                    <SelectItem value="ollama">Ollama (local server)</SelectItem>
                    <SelectItem value="openai-compat">OpenAI Compatible API</SelectItem>
                  </SelectContent>
                </Select>
                <p className="text-xs leading-relaxed text-muted-foreground">
                  {!isCloudProvider
                    ? 'Default: Local all-MiniLM-L6-v2 model running in pure Go. Zero cost, no API calls. Downloaded automatically on first use (~23 MB).'
                    : form.embedding.provider === 'ollama'
                      ? 'Uses a locally running Ollama server for embeddings.'
                      : form.embedding.provider === 'openai'
                        ? 'Uses OpenAI API for embeddings (requires API key, incurs cost per request).'
                        : 'Uses any OpenAI-compatible embedding API (requires base URL and API key).'}
                </p>
              </div>

              {!isCloudProvider && (
                <div className="rounded-lg border bg-background p-3">
                  <div className="mb-1 flex items-center gap-2 text-xs font-medium text-muted-foreground">
                    <Cpu className="size-3" /> Active Model
                  </div>
                  <div className="font-mono text-sm text-foreground">sentence-transformers/all-MiniLM-L6-v2</div>
                  <div className="mt-1 text-xs text-muted-foreground">
                    384-dimensional vectors, ~23 MB. Same model used by ChromaDB. Stored at ~/.config/astonish/models/.
                  </div>
                </div>
              )}

              {isCloudProvider && (
                <>
                  <div className="space-y-2">
                    <Label htmlFor="embedding-model">Model</Label>
                    <Input
                      id="embedding-model"
                      type="text"
                      value={form.embedding.model}
                      onChange={(e) => setForm({ ...form, embedding: { ...form.embedding, model: e.target.value } })}
                      placeholder={defaultModel ? `${defaultModel} (default)` : 'Model name'}
                      className="bg-background font-mono"
                    />
                    <p className="text-xs text-muted-foreground">Leave empty to use the default model for this provider.</p>
                  </div>
                  {(form.embedding.provider === 'openai-compat' || form.embedding.provider === 'ollama') && (
                    <div className="space-y-2">
                      <Label htmlFor="embedding-base-url">Base URL</Label>
                      <Input
                        id="embedding-base-url"
                        type="text"
                        value={form.embedding.base_url}
                        onChange={(e) => setForm({ ...form, embedding: { ...form.embedding, base_url: e.target.value } })}
                        placeholder={form.embedding.provider === 'ollama' ? 'http://localhost:11434/api (default)' : 'https://your-api-server.com/v1'}
                        className="bg-background font-mono"
                      />
                      <p className="text-xs text-muted-foreground">
                        {form.embedding.provider === 'ollama'
                          ? 'Ollama API endpoint. Default: http://localhost:11434/api.'
                          : 'Base URL of the OpenAI-compatible API endpoint. Required.'}
                      </p>
                    </div>
                  )}
                  {form.embedding.provider !== 'ollama' && (
                    <div className="space-y-2">
                      <Label htmlFor="embedding-api-key">API Key</Label>
                      <Input
                        id="embedding-api-key"
                        type="password"
                        value={form.embedding.api_key}
                        onChange={(e) => setForm({ ...form, embedding: { ...form.embedding, api_key: e.target.value } })}
                        placeholder={form.embedding.provider === 'openai' ? 'Uses main OpenAI provider key if empty' : 'API key for the embedding endpoint'}
                        className="bg-background font-mono"
                      />
                      <p className="text-xs text-muted-foreground">
                        {form.embedding.provider === 'openai'
                          ? 'Leave empty to reuse the API key from your OpenAI provider configuration.'
                          : 'API key for authenticating with the embedding endpoint.'}
                      </p>
                    </div>
                  )}
                </>
              )}
            </CardContent>
          </Card>

          <Card className="border-border bg-card shadow-sm">
            <CardHeader>
              <CardTitle className="text-base">Retrieval Tuning</CardTitle>
              <CardDescription>Adjust chunking, search defaults, and file watcher timing.</CardDescription>
            </CardHeader>
            <CardContent className="space-y-6">
              <div className="grid gap-4 sm:grid-cols-2">
                <div className="space-y-2">
                  <Label htmlFor="chunk-max-chars">Max Characters</Label>
                  <Input
                    id="chunk-max-chars"
                    type="number"
                    value={form.chunking.max_chars}
                    onChange={(e) => setForm({ ...form, chunking: { ...form.chunking, max_chars: parseInt(e.target.value) || 1600 } })}
                    min="100"
                    className="bg-background"
                  />
                  <p className="text-xs text-muted-foreground">Characters per chunk. Default: 1600.</p>
                </div>
                <div className="space-y-2">
                  <Label htmlFor="chunk-overlap">Overlap</Label>
                  <Input
                    id="chunk-overlap"
                    type="number"
                    value={form.chunking.overlap}
                    onChange={(e) => setForm({ ...form, chunking: { ...form.chunking, overlap: parseInt(e.target.value) || 320 } })}
                    min="0"
                    className="bg-background"
                  />
                  <p className="text-xs text-muted-foreground">Character overlap between chunks. Default: 320.</p>
                </div>
              </div>

              <div className="grid gap-4 border-t pt-6 sm:grid-cols-2">
                <div className="space-y-2">
                  <Label htmlFor="memory-max-results">Max Results</Label>
                  <Input
                    id="memory-max-results"
                    type="number"
                    value={form.search.max_results}
                    onChange={(e) => setForm({ ...form, search: { ...form.search, max_results: parseInt(e.target.value) || 6 } })}
                    min="1"
                    max="50"
                    className="bg-background"
                  />
                  <p className="text-xs text-muted-foreground">Maximum chunks returned per search. Default: 6.</p>
                </div>
                <div className="space-y-2">
                  <Label htmlFor="memory-min-score">Min Similarity Score</Label>
                  <Input
                    id="memory-min-score"
                    type="number"
                    value={form.search.min_score}
                    onChange={(e) => setForm({ ...form, search: { ...form.search, min_score: parseFloat(e.target.value) || 0.35 } })}
                    min="0"
                    max="1"
                    step="0.05"
                    className="bg-background"
                  />
                  <p className="text-xs text-muted-foreground">0.0 to 1.0. Higher = stricter matching. Default: 0.35.</p>
                </div>
              </div>

              <div className="space-y-4 border-t pt-6">
                <div className="flex items-start justify-between gap-4 rounded-xl border border-border bg-background p-4">
                  <div>
                    <Label htmlFor="memory-watch">Watch for Changes</Label>
                    <p className="mt-1 text-xs text-muted-foreground">Auto-reindex when memory files change. Default: enabled.</p>
                  </div>
                  <Switch
                    id="memory-watch"
                    checked={form.sync.watch}
                    onCheckedChange={(checked) => setForm({ ...form, sync: { ...form.sync, watch: checked } })}
                    aria-label="Watch for changes"
                  />
                </div>
                {form.sync.watch && (
                  <div className="space-y-2">
                    <Label htmlFor="memory-debounce">Debounce (ms)</Label>
                    <Input
                      id="memory-debounce"
                      type="number"
                      value={form.sync.debounce_ms}
                      onChange={(e) => setForm({ ...form, sync: { ...form.sync, debounce_ms: parseInt(e.target.value) || 1500 } })}
                      min="100"
                      className="max-w-40 bg-background"
                    />
                    <p className="text-xs text-muted-foreground">Milliseconds to wait after file changes before reindexing. Default: 1500.</p>
                  </div>
                )}
              </div>
            </CardContent>
          </Card>
        </>
      )}

      <div className="flex flex-wrap items-center gap-3">
        <Button onClick={handleSave} disabled={saving}>
          {saving ? <Loader2 className="animate-spin" /> : <Save />}
          {saving ? 'Saving...' : 'Save Changes'}
        </Button>
        {saveSuccess && (
          <span className="flex items-center gap-1 text-sm text-[color:var(--success)]">
            <Check className="size-4" /> Saved
          </span>
        )}
        {error && (
          <Alert variant="destructive" className="w-auto py-2">
            <AlertCircle />
            <AlertDescription>{error}</AlertDescription>
          </Alert>
        )}
      </div>
    </div>
  )
}
