import { useEffect, useState } from 'react'
import { AlertCircle, CheckCircle2, CircleHelp, XCircle } from 'lucide-react'

import { fetchCacheDiagnostics } from '@/api/studioChat'
import type { CacheDiagnosticDiff, CacheDiagnosticsResponse, CacheHitStatus } from '@/api/studioChat'
import { cn } from '@/lib/utils'

function StatusBadge({ status }: { status: CacheHitStatus }) {
  const styles = {
    hit: 'border-green-500/30 bg-green-500/10 text-green-600 dark:text-green-400',
    miss: 'border-red-500/30 bg-red-500/10 text-red-600 dark:text-red-400',
    unknown: 'border-border bg-muted text-muted-foreground',
  }
  const Icon = status === 'hit' ? CheckCircle2 : status === 'miss' ? XCircle : CircleHelp
  return (
    <span className={cn('inline-flex items-center gap-1 rounded-full border px-2 py-0.5 text-xs font-medium', styles[status])}>
      <Icon size={12} />
      {status === 'unknown' ? 'Unknown' : status === 'hit' ? 'Cache hit' : 'Cache miss'}
    </span>
  )
}

function HashDiff({ label, diff, count }: { label: string; diff: CacheDiagnosticDiff; count?: number }) {
  return (
    <div className="min-w-0 rounded-md border border-border bg-background/50 p-2">
      <div className="flex flex-wrap items-center justify-between gap-1 text-xs">
        <span className="font-medium text-foreground">{label}{count === undefined ? '' : ` (${count})`}</span>
        <span className={diff.changed ? 'text-amber-600 dark:text-amber-400' : 'text-green-600 dark:text-green-400'}>
          {diff.changed ? 'Changed' : 'Stable'}
        </span>
      </div>
      <div className="mt-1 grid gap-1 font-mono text-[11px] text-muted-foreground sm:grid-cols-2">
        {diff.previousHash && <div className="min-w-0 break-all"><span className="font-sans">Previous: </span>{diff.previousHash}</div>}
        <div className="min-w-0 break-all"><span className="font-sans">Current: </span>{diff.currentHash}</div>
      </div>
    </div>
  )
}

export default function CacheDiagnosticsPanel({ sessionId, assistantTurn }: { sessionId: string; assistantTurn: number }) {
  const [diagnostics, setDiagnostics] = useState<CacheDiagnosticsResponse | null>(null)
  const [error, setError] = useState('')

  useEffect(() => {
    let active = true
    setDiagnostics(null)
    setError('')
    const load = async () => {
      try {
        const data = await fetchCacheDiagnostics(sessionId, assistantTurn)
        if (active) setDiagnostics(data)
      } catch (err) {
        if (active) setError(err instanceof Error ? err.message : String(err))
      }
    }
    void load()
    return () => { active = false }
  }, [assistantTurn, sessionId])

  if (error) {
    return (
      <div role="alert" className="mt-2 flex items-start gap-2 rounded-lg border border-destructive/30 bg-destructive/10 p-3 text-sm text-destructive">
        <AlertCircle size={16} className="mt-0.5 shrink-0" />
        <span>{error}</span>
      </div>
    )
  }

  if (!diagnostics) {
    return <div className="mt-2 animate-pulse rounded-lg border border-border bg-muted/40 p-3 text-sm text-muted-foreground">Loading cache diagnostics…</div>
  }

  if (diagnostics.rounds.length === 0) {
    return <div className="mt-2 rounded-lg border border-border bg-muted/30 p-3 text-sm text-muted-foreground">No cache diagnostics were recorded for this assistant turn.</div>
  }

  return (
    <div className="mt-2 min-w-0 space-y-2 rounded-lg border border-border bg-muted/20 p-2 sm:p-3" data-testid="cache-diagnostics-panel">
      {diagnostics.rounds.map((round, index) => (
        <section key={`${round.round}-${index}`} className="min-w-0 rounded-lg border border-border bg-card p-2.5 sm:p-3">
          <div className="flex flex-wrap items-center justify-between gap-2">
            <div>
              <h4 className="text-sm font-semibold text-foreground">Model round {round.round}</h4>
              {(round.provider || round.model) && <p className="text-xs text-muted-foreground">{[round.provider, round.model].filter(Boolean).join(' / ')}</p>}
            </div>
            <StatusBadge status={round.cacheStatus} />
          </div>
          <div className="mt-2 grid min-w-0 gap-2 lg:grid-cols-2">
            <HashDiff label="System instruction" diff={round.systemInstruction} />
            <HashDiff label="Tool declarations" diff={round.toolDeclarations} count={round.toolDeclarations.count} />
          </div>
          {round.payload && (
            <details className="mt-2 min-w-0 rounded-md border border-border bg-background/50">
              <summary className="cursor-pointer px-2 py-1.5 text-xs font-medium text-foreground">Payload</summary>
              <pre className="max-h-72 overflow-auto whitespace-pre-wrap break-words border-t border-border p-2 text-[11px] text-muted-foreground">{JSON.stringify(round.payload, null, 2)}</pre>
            </details>
          )}
        </section>
      ))}
    </div>
  )
}
