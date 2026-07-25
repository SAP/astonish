import { useState, useEffect, useRef } from 'react'
import { Zap, Clock, ArrowDownToLine, ArrowUpFromLine } from 'lucide-react'

export interface TokenUsage {
  inputTokens: number
  outputTokens: number
  totalTokens: number
}

interface UsagePopoverProps {
  usage: TokenUsage
  isStreaming: boolean
  sessionStartTime: number | null  // ms timestamp when the current turn started
}

function formatTokenCount(n: number): string {
  if (n >= 1_000_000) return `${(n / 1_000_000).toFixed(1)}M`
  if (n >= 1_000) return `${(n / 1_000).toFixed(1)}k`
  return String(n)
}

function formatDuration(ms: number): string {
  const secs = Math.floor(ms / 1000)
  if (secs < 60) return `${secs}s`
  const mins = Math.floor(secs / 60)
  const remainSecs = secs % 60
  return `${mins}m ${remainSecs}s`
}

// Floating popover triggered by the Usage toolbar button.
// Shows actual LLM token usage (from API UsageMetadata) and elapsed time.
export default function UsagePopover({ usage, isStreaming, sessionStartTime }: UsagePopoverProps) {
  const [open, setOpen] = useState(false)
  const [elapsed, setElapsed] = useState(0)
  const popoverRef = useRef<HTMLDivElement>(null)
  const buttonRef = useRef<HTMLButtonElement>(null)

  // Live elapsed timer
  useEffect(() => {
    if (!isStreaming || !sessionStartTime) {
      return
    }
    const tick = () => setElapsed(Date.now() - sessionStartTime)
    const interval = setInterval(tick, 1000)
    // Initial tick after a microtask to avoid synchronous setState in effect body
    const raf = requestAnimationFrame(tick)
    return () => { clearInterval(interval); cancelAnimationFrame(raf) }
  }, [isStreaming, sessionStartTime])

  // Close on outside click
  useEffect(() => {
    if (!open) return
    const handler = (e: MouseEvent) => {
      if (
        popoverRef.current && !popoverRef.current.contains(e.target as Node) &&
        buttonRef.current && !buttonRef.current.contains(e.target as Node)
      ) {
        setOpen(false)
      }
    }
    document.addEventListener('mousedown', handler)
    return () => document.removeEventListener('mousedown', handler)
  }, [open])

  const displayElapsed = isStreaming && sessionStartTime ? elapsed : 0
  const hasUsage = usage.totalTokens > 0

  return (
    <div className="relative">
      <button
        ref={buttonRef}
        onClick={() => setOpen(!open)}
        className={`flex items-center gap-1.5 rounded border px-2 py-1 text-xs transition-colors ${
          open
            ? 'border-primary/30 bg-primary/15 text-primary'
            : 'border-transparent text-muted-foreground hover:bg-secondary hover:text-foreground'
        }`}
        title="Token usage"
      >
        <Zap size={13} />
        <span>Usage</span>
        {hasUsage && (
          <span className="rounded bg-primary/15 px-1 py-0 text-[10px] font-medium text-primary">
            {formatTokenCount(usage.totalTokens)}
          </span>
        )}
      </button>

      {open && (
        <div
          ref={popoverRef}
          className="absolute right-0 top-full mt-1.5 z-50 rounded-lg shadow-lg text-xs"
          style={{
            background: 'var(--bg-secondary)',
            border: '1px solid var(--border-color)',
            minWidth: '220px',
          }}
        >
          {/* Header */}
          <div
            className="px-3 py-2 font-medium text-xs"
            style={{
              borderBottom: '1px solid var(--border-color)',
              color: 'var(--text-primary)',
            }}
          >
            Token Usage
          </div>

          {/* Stats */}
          <div className="px-3 py-2.5 space-y-2.5">
            {/* Input tokens */}
            <div className="flex items-center justify-between">
              <div className="flex items-center gap-2" style={{ color: 'var(--text-secondary)' }}>
                <ArrowDownToLine size={12} />
                <span>Input</span>
              </div>
              <span className="font-mono font-medium" style={{ color: 'var(--text-primary)' }}>
                {formatTokenCount(usage.inputTokens)}
              </span>
            </div>

            {/* Output tokens */}
            <div className="flex items-center justify-between">
              <div className="flex items-center gap-2" style={{ color: 'var(--text-secondary)' }}>
                <ArrowUpFromLine size={12} />
                <span>Output</span>
              </div>
              <span className="font-mono font-medium" style={{ color: 'var(--text-primary)' }}>
                {formatTokenCount(usage.outputTokens)}
              </span>
            </div>

            {/* Divider */}
            <div style={{ borderTop: '1px solid var(--border-color)' }} />

            {/* Total */}
            <div className="flex items-center justify-between">
              <div className="flex items-center gap-2" style={{ color: 'var(--text-secondary)' }}>
                <Zap size={12} />
                <span>Total</span>
              </div>
              <span className="font-mono font-medium" style={{ color: 'var(--accent-color, #60a5fa)' }}>
                {formatTokenCount(usage.totalTokens)}
              </span>
            </div>

            {/* Elapsed time (shown when streaming or has a recent time) */}
            {displayElapsed > 0 && (
              <>
                <div style={{ borderTop: '1px solid var(--border-color)' }} />
                <div className="flex items-center justify-between">
                  <div className="flex items-center gap-2" style={{ color: 'var(--text-secondary)' }}>
                    <Clock size={12} />
                    <span>Elapsed</span>
                  </div>
                  <span className="font-mono font-medium" style={{ color: 'var(--text-primary)' }}>
                    {formatDuration(displayElapsed)}
                  </span>
                </div>
              </>
            )}
          </div>

          {/* Footer note */}
          {!hasUsage && (
            <div
              className="px-3 py-2 text-[10px]"
              style={{
                borderTop: '1px solid var(--border-color)',
                color: 'var(--text-muted)',
              }}
            >
              Send a message to see token usage
            </div>
          )}
        </div>
      )}
    </div>
  )
}
