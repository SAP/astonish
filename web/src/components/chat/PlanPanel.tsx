import { Loader, Check, Circle, AlertCircle, ListChecks } from 'lucide-react'
import type { PlanMessage } from './chatTypes'

// Enhanced plan card showing the high-level execution plan.
// Announced by the orchestrator via announce_plan before starting work.
// Steps transition through pending -> running -> complete/failed as work progresses.
// Perplexity-inspired: bordered card, bold title, status badge footer, reduced opacity when done.
export default function PlanPanel({ data }: { data: PlanMessage }) {
  const completedCount = data.steps.filter(s => s.status === 'complete').length
  const failedCount = data.steps.filter(s => s.status === 'failed').length
  const totalCount = data.steps.length
  const hasRunning = data.steps.some(s => s.status === 'running')
  const allDone = (completedCount + failedCount) === totalCount && totalCount > 0

  const statusLabel = allDone
    ? (failedCount > 0 ? 'Partial' : 'Complete')
    : hasRunning
      ? 'In Progress'
      : 'Submitted'

  const statusColor = allDone
    ? (failedCount > 0 ? 'var(--warning, #e49425)' : 'var(--success, #149647)')
    : hasRunning
      ? 'var(--brand)'
      : 'var(--text-muted)'

  const statusIcon = (status: string) => {
    switch (status) {
      case 'running':
        return <Loader size={14} className="animate-spin shrink-0" style={{ color: 'var(--brand)' }} />
      case 'complete':
        return <Check size={14} className="text-green-400 shrink-0" />
      case 'failed':
        return <AlertCircle size={14} className="text-red-400 shrink-0" />
      default:
        return <Circle size={10} className="shrink-0 ml-0.5 mr-0.5" style={{ color: 'var(--text-muted)' }} />
    }
  }

  return (
    <div
      className="rounded-lg overflow-hidden text-sm transition-opacity duration-300"
      style={{
        border: '1px solid var(--border-color)',
        background: 'var(--bg-secondary)',
        opacity: allDone ? 0.6 : 1,
      }}
    >
      {/* Header */}
      <div className="flex items-center gap-2.5 px-4 py-3">
        {hasRunning && !allDone ? (
          <Loader size={16} className="animate-spin shrink-0" style={{ color: 'var(--brand)' }} />
        ) : allDone ? (
          <Check size={16} className="text-green-400 shrink-0" />
        ) : (
          <ListChecks size={16} className="shrink-0" style={{ color: 'var(--brand)' }} />
        )}
        <span className="font-bold text-sm" style={{ color: 'var(--text-primary)' }}>
          {data.goal}
        </span>
      </div>

      {/* Steps */}
      <div
        className="px-4 pb-2 space-y-1"
        style={{ borderTop: '1px solid var(--border-color)' }}
      >
        {data.steps.map((step) => (
          <div key={step.name} className="flex items-start gap-2.5 py-1.5">
            <div className="mt-0.5">{statusIcon(step.status)}</div>
            <span
              className="text-xs leading-relaxed"
              style={{
                color: step.status === 'complete'
                  ? 'var(--text-muted)'
                  : step.status === 'running'
                    ? 'var(--text-primary)'
                    : 'var(--text-secondary)',
                textDecoration: step.status === 'complete' ? 'line-through' : 'none',
                opacity: step.status === 'pending' ? 0.7 : 1,
              }}
            >
              {step.description || step.name}
            </span>
          </div>
        ))}
      </div>

      {/* Footer with status badge */}
      <div
        className="flex items-center justify-between px-4 py-2"
        style={{ borderTop: '1px solid var(--border-color)' }}
      >
        <span
          className="text-[11px] font-medium px-2 py-0.5 rounded-full"
          style={{
            background: `color-mix(in srgb, ${statusColor} 15%, transparent)`,
            color: statusColor,
          }}
        >
          {statusLabel}
        </span>
        <span className="text-[11px]" style={{ color: 'var(--text-muted)' }}>
          {completedCount}/{totalCount} steps
        </span>
      </div>
    </div>
  )
}
