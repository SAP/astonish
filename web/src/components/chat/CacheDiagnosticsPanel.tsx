import { useEffect, useState } from 'react'
import { AlertCircle, CheckCircle2, CircleHelp, Copy, Download, XCircle } from 'lucide-react'

import { fetchCacheDiagnostics } from '@/api/studioChat'
import type { CacheDiagnosticRound, CacheDiagnosticsResponse, CacheHitStatus } from '@/api/studioChat'
import { cn } from '@/lib/utils'

function cacheStatus(round: CacheDiagnosticRound): CacheHitStatus {
  if (!round.usage.cacheReported) return 'unknown'
  return round.usage.cachedTokens > 0 ? 'hit' : 'miss'
}

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
      {status === 'unknown' ? 'Provider cache unknown' : status === 'hit' ? 'Provider cache hit' : 'Provider cache miss'}
    </span>
  )
}

function milliseconds(nanoseconds: number) {
  return `${Math.round(nanoseconds / 1_000_000)} ms`
}

function PayloadActions({ payload, call }: { payload: Record<string, unknown>; call: number }) {
  const text = JSON.stringify(payload, null, 2)
  const download = () => {
    const url = URL.createObjectURL(new Blob([text], { type: 'application/json' }))
    const anchor = document.createElement('a')
    anchor.href = url
    anchor.download = `cache-diagnostic-round-${call}.json`
    anchor.click()
    URL.revokeObjectURL(url)
  }
  return (
    <div className="flex gap-1">
      <button type="button" className="rounded p-1 hover:bg-muted" aria-label="Copy sanitized payload" onClick={() => void navigator.clipboard.writeText(text)}><Copy size={13} /></button>
      <button type="button" className="rounded p-1 hover:bg-muted" aria-label="Download sanitized payload" onClick={download}><Download size={13} /></button>
    </div>
  )
}

export default function CacheDiagnosticsPanel({ sessionId, invocationId }: { sessionId: string; invocationId: string }) {
  const [diagnostics, setDiagnostics] = useState<CacheDiagnosticsResponse | null>(null)
  const [error, setError] = useState('')

  useEffect(() => {
    let active = true
    setDiagnostics(null)
    setError('')
    void fetchCacheDiagnostics(sessionId, invocationId)
      .then(data => { if (active) setDiagnostics(data) })
      .catch(err => { if (active) setError(err instanceof Error ? err.message : String(err)) })
    return () => { active = false }
  }, [invocationId, sessionId])

  if (error) return <div role="alert" className="mt-2 flex items-start gap-2 rounded-lg border border-destructive/30 bg-destructive/10 p-3 text-sm text-destructive"><AlertCircle size={16} className="mt-0.5 shrink-0" /><span>{error}</span></div>
  if (!diagnostics) return <div className="mt-2 animate-pulse rounded-lg border border-border bg-muted/40 p-3 text-sm text-muted-foreground">Loading cache diagnostics…</div>
  if (diagnostics.rounds.length === 0) return <div className="mt-2 rounded-lg border border-border bg-muted/30 p-3 text-sm text-muted-foreground">No retained cache diagnostics were recorded for this assistant turn.</div>

  return (
    <div className="mt-2 min-w-0 space-y-2 rounded-lg border border-border bg-muted/20 p-2 sm:p-3" data-testid="cache-diagnostics-panel">
      {diagnostics.rounds.map(round => (
        <section key={round.call} className="min-w-0 rounded-lg border border-border bg-card p-2.5 sm:p-3">
          <div className="flex flex-wrap items-center justify-between gap-2">
            <div><h4 className="text-sm font-semibold text-foreground">Model round {round.call}</h4><p className="text-xs text-muted-foreground">{[round.provider, round.model, round.captureLevel].filter(Boolean).join(' / ')}</p></div>
            <StatusBadge status={cacheStatus(round)} />
          </div>
          <dl className="mt-2 grid grid-cols-2 gap-2 text-xs sm:grid-cols-4">
            <div><dt className="text-muted-foreground">Estimated reusable prefix</dt><dd>{round.stablePrefixElements} elements · {round.stablePrefixBytes} bytes</dd></div>
            <div><dt className="text-muted-foreground">First response</dt><dd>{milliseconds(round.timeToFirstResponse)}</dd></div>
            <div><dt className="text-muted-foreground">Total duration</dt><dd>{milliseconds(round.duration)}</dd></div>
            <div><dt className="text-muted-foreground">Tokens</dt><dd>{round.usage.cachedTokens} cached / {round.usage.promptTokens} input</dd></div>
          </dl>
          <div className="mt-2 break-all font-mono text-[11px] text-muted-foreground">Request SHA-256: {round.inputHash}</div>
          {round.firstDivergence && <div className="mt-1 text-xs text-amber-600 dark:text-amber-400">First divergence: {round.firstDivergence}</div>}
          {(round.payloadTruncated || round.binaryElisions > 0) && <div className="mt-1 text-xs text-muted-foreground">Sanitized capture: {round.payloadCapturedBytes}/{round.payloadOriginalBytes} bytes{round.payloadTruncated ? ' (truncated)' : ''}; {round.binaryElisions} binary value(s) elided.</div>}
          {round.error && <div className="mt-2 text-xs text-destructive">{round.error}</div>}
          {round.payload && <details className="mt-2 min-w-0 rounded-md border border-border bg-background/50"><summary className="flex cursor-pointer items-center justify-between px-2 py-1.5 text-xs font-medium text-foreground">Sanitized payload <PayloadActions payload={round.payload} call={round.call} /></summary><pre className="max-h-72 overflow-auto whitespace-pre-wrap break-words border-t border-border p-2 text-[11px] text-muted-foreground">{JSON.stringify(round.payload, null, 2)}</pre></details>}
        </section>
      ))}
    </div>
  )
}
