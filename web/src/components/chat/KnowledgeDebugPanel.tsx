import { useEffect, useState } from 'react'
import { AlertCircle, Brain, Search, Tag } from 'lucide-react'

import { fetchKnowledgeDebug } from '@/api/studioChat'
import type { KnowledgeDebugResponse, KnowledgeDebugResult } from '@/api/studioChat'
import { cn } from '@/lib/utils'

function CategoryBadge({ category }: { category: string }) {
  const styles: Record<string, string> = {
    guidance: 'border-amber-500/30 bg-amber-500/10 text-amber-600 dark:text-amber-400',
    knowledge: 'border-blue-500/30 bg-blue-500/10 text-blue-600 dark:text-blue-400',
    skill: 'border-purple-500/30 bg-purple-500/10 text-purple-600 dark:text-purple-400',
    flow: 'border-cyan-500/30 bg-cyan-500/10 text-cyan-600 dark:text-cyan-400',
  }
  return (
    <span className={cn('inline-flex items-center gap-1 rounded-full border px-2 py-0.5 text-xs font-medium', styles[category] || 'border-border bg-muted text-muted-foreground')}>
      {category || 'unknown'}
    </span>
  )
}

function ScopeBadge({ scope }: { scope: string }) {
  const styles: Record<string, string> = {
    personal: 'border-green-500/30 bg-green-500/10 text-green-600 dark:text-green-400',
    team: 'border-blue-500/30 bg-blue-500/10 text-blue-600 dark:text-blue-400',
    org: 'border-purple-500/30 bg-purple-500/10 text-purple-600 dark:text-purple-400',
  }
  return (
    <span className={cn('inline-flex items-center rounded-full border px-2 py-0.5 text-xs font-medium', styles[scope] || 'border-border bg-muted text-muted-foreground')}>
      {scope}
    </span>
  )
}

function ResultRow({ result }: { result: KnowledgeDebugResult }) {
  return (
    <div className="rounded-lg border border-border bg-card p-2.5 sm:p-3">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="min-w-0 flex-1">
          <span className="text-sm font-medium text-foreground break-all">{result.path}</span>
        </div>
        <div className="flex items-center gap-1.5">
          <CategoryBadge category={result.category} />
          {result.scope && <ScopeBadge scope={result.scope} />}
        </div>
      </div>
      <dl className="mt-2 grid grid-cols-2 gap-2 text-xs sm:grid-cols-4">
        <div><dt className="text-muted-foreground">Relevance</dt><dd className="font-medium">{Math.round(result.score * 100)}%</dd></div>
        {result.created_by && <div><dt className="text-muted-foreground">Created by</dt><dd>{result.created_by}</dd></div>}
        {result.created_at && <div><dt className="text-muted-foreground">Created at</dt><dd>{new Date(result.created_at).toLocaleDateString()}</dd></div>}
        {result.session_id && <div><dt className="text-muted-foreground">Source session</dt><dd className="truncate" title={result.session_id}>{result.session_id.slice(0, 8)}…</dd></div>}
      </dl>
    </div>
  )
}

function ToolResultRow({ result }: { result: { name: string; group: string; score: number } }) {
  return (
    <div className="flex items-center justify-between gap-2 rounded-md border border-border bg-card px-3 py-2 text-xs">
      <div className="flex items-center gap-2">
        <code className="rounded bg-muted px-1.5 py-0.5 font-mono text-foreground">{result.name}</code>
        {result.group && <span className="text-muted-foreground">{result.group}</span>}
      </div>
      <span className="text-muted-foreground">{Math.round(result.score * 100)}%</span>
    </div>
  )
}

export default function KnowledgeDebugPanel({ sessionId, invocationId }: { sessionId: string; invocationId: string }) {
  const [data, setData] = useState<KnowledgeDebugResponse | null>(null)
  const [error, setError] = useState('')

  useEffect(() => {
    let active = true
    setData(null)
    setError('')
    void fetchKnowledgeDebug(sessionId, invocationId)
      .then(res => { if (active) setData(res) })
      .catch(err => { if (active) setError(err instanceof Error ? err.message : String(err)) })
    return () => { active = false }
  }, [invocationId, sessionId])

  if (error) return <div role="alert" className="mt-2 flex items-start gap-2 rounded-lg border border-destructive/30 bg-destructive/10 p-3 text-sm text-destructive"><AlertCircle size={16} className="mt-0.5 shrink-0" /><span>{error}</span></div>
  if (!data) return <div className="mt-2 animate-pulse rounded-lg border border-border bg-muted/40 p-3 text-sm text-muted-foreground">Loading knowledge debug…</div>

  const knowledge = data.knowledge
  const tools = data.tools

  if (!knowledge && !tools) {
    return <div className="mt-2 rounded-lg border border-border bg-muted/30 p-3 text-sm text-muted-foreground">No knowledge or tool injection data was recorded for this assistant turn.</div>
  }

  return (
    <div className="mt-2 min-w-0 space-y-3 rounded-lg border border-border bg-muted/20 p-2 sm:p-3" data-testid="knowledge-debug-panel">
      {/* Knowledge injection section */}
      {knowledge && (
        <section className="space-y-2">
          <div className="flex flex-wrap items-center justify-between gap-2 px-1">
            <div className="flex items-center gap-2">
              <Brain size={14} className="text-foreground" />
              <h3 className="text-sm font-semibold text-foreground">Knowledge injection</h3>
              <span className={cn(
                'rounded-full border px-2 py-0.5 text-xs font-medium',
                knowledge.type === 'none'
                  ? 'border-border bg-muted text-muted-foreground'
                  : 'border-green-500/30 bg-green-500/10 text-green-600 dark:text-green-400',
              )}>
                {knowledge.type}
              </span>
            </div>
            <div className="flex items-center gap-2 text-xs text-muted-foreground">
              <span>~{knowledge.estimated_tokens} tokens</span>
              <span>{knowledge.result_count} result{knowledge.result_count !== 1 ? 's' : ''}</span>
            </div>
          </div>

          {knowledge.query && (
            <div className="flex items-start gap-2 rounded-md border border-border bg-background/50 px-3 py-2 text-xs">
              <Search size={12} className="mt-0.5 shrink-0 text-muted-foreground" />
              <div>
                <span className="text-muted-foreground">Search query: </span>
                <span className="text-foreground">{knowledge.query}</span>
              </div>
            </div>
          )}

          {knowledge.results && knowledge.results.length > 0 && (
            <div className="space-y-1.5">
              {knowledge.results.map((result, i) => (
                <ResultRow key={`${result.path}-${i}`} result={result} />
              ))}
            </div>
          )}
        </section>
      )}

      {/* Tool injection section */}
      {tools && tools.result_count > 0 && (
        <section className="space-y-2">
          <div className="flex flex-wrap items-center justify-between gap-2 px-1">
            <div className="flex items-center gap-2">
              <Tag size={14} className="text-foreground" />
              <h3 className="text-sm font-semibold text-foreground">Tool discovery</h3>
            </div>
            <span className="text-xs text-muted-foreground">{tools.result_count} tool{tools.result_count !== 1 ? 's' : ''}</span>
          </div>

          {tools.query && (
            <div className="flex items-start gap-2 rounded-md border border-border bg-background/50 px-3 py-2 text-xs">
              <Search size={12} className="mt-0.5 shrink-0 text-muted-foreground" />
              <div>
                <span className="text-muted-foreground">Tool query: </span>
                <span className="text-foreground">{tools.query}</span>
              </div>
            </div>
          )}

          {tools.results && tools.results.length > 0 && (
            <div className="space-y-1">
              {tools.results.map((result, i) => (
                <ToolResultRow key={`${result.name}-${i}`} result={result} />
              ))}
            </div>
          )}
        </section>
      )}
    </div>
  )
}
