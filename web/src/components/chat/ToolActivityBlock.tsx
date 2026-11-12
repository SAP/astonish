import { useState, type ReactNode } from 'react'
import { AlertCircle, Check, ChevronDown, ChevronRight, Loader } from 'lucide-react'
import FileEditPreview from './FileEditPreview'
import {
  activityStats,
  activitySummary,
  formatToolPayload,
  previewValue,
  type ActivityEntry,
  type ActivityStats,
  type ToolActivityStep,
} from './toolActivity'

interface ToolActivityBlockProps {
  entries: ActivityEntry[]
  renderMessage: (message: Extract<ActivityEntry, { kind: 'message' }>['message'], index: number) => ReactNode
  /** True when this block is the trailing activity during an active stream. */
  streaming?: boolean
  /** Stable key prefix for the block (e.g. activity start index). */
  blockId: string
}

function StepStatusIcon({ status }: { status: ToolActivityStep['status'] }) {
  if (status === 'running') {
    return <Loader size={12} className="animate-spin shrink-0" style={{ color: 'var(--text-muted)' }} />
  }
  if (status === 'error') {
    return <AlertCircle size={12} className="shrink-0" style={{ color: 'var(--danger)' }} />
  }
  return <Check size={12} className="shrink-0" style={{ color: 'var(--text-muted)' }} />
}

function TrailingMetric({ stats }: { stats: ActivityStats }) {
  if (stats.kind === 'diff') {
    return (
      <span className="flex items-center gap-1.5 shrink-0 text-xs font-medium tabular-nums" data-testid="activity-diff">
        {stats.added > 0 && (
          <span style={{ color: 'var(--success)' }}>+{stats.added}</span>
        )}
        {stats.removed > 0 && (
          <span style={{ color: 'var(--danger)' }}>-{stats.removed}</span>
        )}
      </span>
    )
  }
  return (
    <span
      className="shrink-0 text-[10px] font-medium px-1.5 py-0.5 rounded tabular-nums"
      data-testid="activity-badge"
      style={{
        // --brand is solid text; --accent is a translucent surface tint (unreadable as color).
        color: 'var(--brand)',
        background: 'var(--accent-soft)',
      }}
    >
      {stats.count}
    </span>
  )
}

/**
 * Cursor-style tool activity line with brand lead, secondary rest, and trailing metric.
 * Collapsed by default; expands to paired call/result steps.
 * Uses --brand (not --accent) for lead text so light and dark stay readable.
 */
export default function ToolActivityBlock({ entries, renderMessage, streaming = false, blockId }: ToolActivityBlockProps) {
  const [expanded, setExpanded] = useState(false)
  const [expandedSteps, setExpandedSteps] = useState<Set<number>>(new Set())

  const steps = entries.flatMap(entry => entry.kind === 'tool' ? [entry.step] : [])
  const summary = activitySummary(steps, { streaming })
  const stats = activityStats(steps)
  const showSpinner = summary.variant === 'running'
  if (!entries.length) return null

  const toggleStep = (stepIndex: number) => {
    setExpandedSteps(prev => {
      const next = new Set(prev)
      if (next.has(stepIndex)) next.delete(stepIndex)
      else next.add(stepIndex)
      return next
    })
  }

  return (
    <div
      className="my-0.5 text-sm"
      data-testid="tool-activity-block"
      data-block-id={blockId}
    >
      <button
        type="button"
        onClick={() => setExpanded(v => !v)}
        className="group inline-flex max-w-full items-center gap-1.5 py-0.5 text-left"
        style={{
          cursor: 'pointer',
          background: 'transparent',
          border: 'none',
        }}
        aria-expanded={expanded}
        aria-label={summary.text}
      >
        {showSpinner && (
          <Loader size={12} className="animate-spin shrink-0" style={{ color: 'var(--brand)' }} />
        )}
        {summary.variant === 'error' && !showSpinner && (
          <AlertCircle size={12} className="shrink-0" style={{ color: 'var(--danger)' }} />
        )}
        <span className="text-xs truncate min-w-0">
          {/* --brand works in light + dark; --accent is only a soft fill, not text */}
          <span style={{ color: 'var(--brand)', fontWeight: 500 }}>
            {summary.lead}
          </span>
          {summary.rest && (
            <span style={{ color: 'var(--text-secondary)', fontWeight: 400 }}>
              {summary.rest}
            </span>
          )}
          {summary.errorSuffix && (
            <span style={{ color: 'var(--danger)', fontWeight: 400 }}>
              {summary.errorSuffix}
            </span>
          )}
        </span>
        {!showSpinner && <TrailingMetric stats={stats} />}
        <span
          className="shrink-0 opacity-0 group-hover:opacity-100 transition-opacity"
          data-testid="activity-chevron"
          aria-hidden
        >
          {expanded ? (
            <ChevronDown size={14} style={{ color: 'var(--text-secondary)' }} />
          ) : (
            <ChevronRight size={14} style={{ color: 'var(--text-secondary)' }} />
          )}
        </span>
      </button>

      {expanded && (
        <div
          className="mt-1 rounded-lg overflow-hidden"
          style={{
            border: '1px solid var(--border-color)',
            background: 'var(--bg-secondary)',
          }}
        >
          {entries.map((entry) => {
            if (entry.kind === 'message') return (
              <div key={`${blockId}-message-${entry.index}`} className="p-3 min-w-0" data-activity-index={entry.index}>
                {renderMessage(entry.message, entry.index)}
              </div>
            )
            if (entry.kind === 'process') return (
              <div key={`${blockId}-process-${entry.index}`} className="px-3 py-2 text-xs text-muted-foreground whitespace-pre-wrap" data-activity-index={entry.index}>
                {entry.note.text}
              </div>
            )
            const step = entry.step
            const stepIndex = step.callIndex
            const stepOpen = expandedSteps.has(stepIndex)
            const preview = step.status === 'running'
              ? previewValue(step.args)
              : previewValue(step.result ?? step.args)
            return (
              <div key={`${blockId}-step-${stepIndex}`} data-activity-index={entry.index}>
                {stepIndex > 0 && (
                  <div style={{ borderTop: '1px solid var(--border-color)' }} />
                )}
                <button
                  type="button"
                  onClick={() => toggleStep(stepIndex)}
                  className="w-full flex items-center gap-2 px-3 py-1.5 text-left hover:bg-black/5 dark:hover:bg-white/5"
                  style={{ color: 'var(--text-secondary)' }}
                  aria-expanded={stepOpen}
                >
                  {stepOpen ? (
                    <ChevronDown size={12} className="shrink-0" style={{ color: 'var(--text-muted)' }} />
                  ) : (
                    <ChevronRight size={12} className="shrink-0" style={{ color: 'var(--text-muted)' }} />
                  )}
                  <StepStatusIcon status={step.status} />
                  <code
                    className="text-xs px-1 py-0.5 rounded shrink-0"
                    style={{
                      background: 'var(--bg-tertiary, rgba(0,0,0,0.06))',
                      color: 'var(--text-primary)',
                    }}
                  >
                    {step.toolName}
                  </code>
                  {preview && (
                    <span className="text-xs truncate flex-1" style={{ color: 'var(--text-muted)' }}>
                      {preview}
                    </span>
                  )}
                </button>
                {stepOpen && (
                  <div className="px-3 pb-2 space-y-2">
                    <FileEditPreview step={step} />
                    {step.args !== undefined && (
                      <div>
                        <div className="text-[10px] uppercase tracking-wide mb-1" style={{ color: 'var(--text-muted)' }}>
                          Arguments
                        </div>
                        <pre
                          className="text-xs whitespace-pre-wrap break-words font-mono p-2 rounded"
                          style={{
                            background: 'var(--bg-primary, rgba(0,0,0,0.2))',
                            color: 'var(--text-secondary)',
                            maxHeight: 240,
                            overflowY: 'auto',
                          }}
                        >
                          {formatToolPayload(step.args)}
                        </pre>
                      </div>
                    )}
                    {step.result !== undefined && (
                      <div>
                        <div className="text-[10px] uppercase tracking-wide mb-1" style={{ color: 'var(--text-muted)' }}>
                          Result
                        </div>
                        <pre
                          className="text-xs whitespace-pre-wrap break-words font-mono p-2 rounded"
                          style={{
                            background: 'var(--bg-primary, rgba(0,0,0,0.2))',
                            color: 'var(--text-secondary)',
                            maxHeight: 240,
                            overflowY: 'auto',
                          }}
                        >
                          {formatToolPayload(step.result)}
                        </pre>
                      </div>
                    )}
                    {step.status === 'running' && step.result === undefined && (
                      <div className="text-xs flex items-center gap-1.5 py-1" style={{ color: 'var(--text-muted)' }}>
                        <Loader size={12} className="animate-spin" />
                        Waiting for result…
                      </div>
                    )}
                  </div>
                )}
              </div>
            )
          })}

        </div>
      )}
    </div>
  )
}
