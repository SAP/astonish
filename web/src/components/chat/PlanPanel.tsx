import { Loader, Check, Circle, AlertCircle, ListChecks, File, FileEdit, Trash2, Terminal } from 'lucide-react'
import type { PlanMessage, PlanStepInfo } from './chatTypes'

// Renders the execution plan announced by announce_plan as a human-readable
// document: separator line, "Execution Plan" header, per-phase details with
// files and verify commands. Status icons track live execution progress.
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

  const stepStatusIcon = (status: PlanStepInfo['status']) => {
    switch (status) {
      case 'running':
        return <Loader size={14} className="animate-spin shrink-0 mt-0.5" style={{ color: 'var(--brand)' }} />
      case 'complete':
        return <Check size={14} className="text-green-400 shrink-0 mt-0.5" />
      case 'failed':
        return <AlertCircle size={14} className="text-red-400 shrink-0 mt-0.5" />
      default:
        return <Circle size={10} className="shrink-0 mt-1 ml-0.5 mr-0.5" style={{ color: 'var(--text-muted)' }} />
    }
  }

  const fileKindIcon = (kind: string) => {
    switch (kind) {
      case 'new':
        return <File size={11} className="shrink-0" style={{ color: 'var(--success, #149647)' }} />
      case 'delete':
        return <Trash2 size={11} className="shrink-0" style={{ color: 'var(--warning, #e49425)' }} />
      default:
        return <FileEdit size={11} className="shrink-0" style={{ color: 'var(--text-muted)' }} />
    }
  }

  const fileKindLabel = (kind: string) => {
    switch (kind) {
      case 'new': return 'new'
      case 'delete': return 'delete'
      default: return 'modify'
    }
  }

  return (
    <div className="w-full">
      {/* Separator */}
      <div
        className="flex items-center gap-3 my-3"
        style={{ color: 'var(--text-muted)' }}
      >
        <div className="flex-1 h-px" style={{ background: 'var(--border-color)' }} />
        <span className="text-[11px] font-medium uppercase tracking-wider select-none">Execution Plan</span>
        <div className="flex-1 h-px" style={{ background: 'var(--border-color)' }} />
      </div>

      {/* Plan card */}
      <div
        className="rounded-lg overflow-hidden text-sm"
        style={{
          border: '1px solid var(--border-color)',
          background: 'var(--bg-secondary)',
          opacity: allDone ? 0.7 : 1,
        }}
      >
        {/* Goal header */}
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
        <div style={{ borderTop: '1px solid var(--border-color)' }}>
          {data.steps.map((step, idx) => {
            const isDone = step.status === 'complete'
            const isFailed = step.status === 'failed'
            return (
              <div
                key={step.name}
                className="px-4 py-3"
                style={{
                  borderBottom: idx < data.steps.length - 1 ? '1px solid var(--border-color)' : undefined,
                  opacity: isDone ? 0.6 : 1,
                }}
              >
                {/* Step header: status icon + number + description */}
                <div className="flex items-start gap-2.5">
                  {stepStatusIcon(step.status)}
                  <div className="flex-1 min-w-0">
                    <div className="flex items-baseline gap-1.5">
                      <span
                        className="text-[11px] font-semibold tabular-nums"
                        style={{ color: 'var(--text-muted)' }}
                      >
                        {idx + 1}.
                      </span>
                      <span
                        className="text-xs font-semibold leading-snug"
                        style={{
                          color: isFailed
                            ? 'var(--warning, #e49425)'
                            : step.status === 'running'
                              ? 'var(--text-primary)'
                              : 'var(--text-secondary)',
                          textDecoration: isDone ? 'line-through' : 'none',
                        }}
                      >
                        {step.description || step.name}
                      </span>
                    </div>

                    {/* Files list */}
                    {step.files && step.files.length > 0 && (
                      <div className="mt-1.5 flex flex-col gap-0.5 pl-0.5">
                        {step.files.map((f, fi) => (
                          <div key={fi} className="flex items-center gap-1.5">
                            {fileKindIcon(f.kind)}
                            <span
                              className="text-[10px] font-mono leading-tight"
                              style={{ color: 'var(--text-muted)' }}
                            >
                              <span
                                className="mr-1"
                                style={{
                                  color: f.kind === 'new'
                                    ? 'var(--success, #149647)'
                                    : f.kind === 'delete'
                                      ? 'var(--warning, #e49425)'
                                      : 'var(--text-muted)',
                                }}
                              >
                                {fileKindLabel(f.kind)}
                              </span>
                              {f.path}
                            </span>
                          </div>
                        ))}
                      </div>
                    )}

                    {/* Details */}
                    {step.details && (
                      <p
                        className="mt-1.5 text-[11px] leading-relaxed whitespace-pre-wrap"
                        style={{ color: 'var(--text-muted)' }}
                      >
                        {step.details}
                      </p>
                    )}

                    {/* Verify command */}
                    {step.verify && (
                      <div className="mt-1.5 flex items-center gap-1.5">
                        <Terminal size={10} className="shrink-0" style={{ color: 'var(--text-muted)' }} />
                        <code
                          className="text-[10px]"
                          style={{ color: 'var(--text-muted)' }}
                        >
                          {step.verify}
                        </code>
                      </div>
                    )}
                  </div>
                </div>
              </div>
            )
          })}
        </div>

        {/* Footer */}
        {totalCount > 0 && (
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
              {completedCount}/{totalCount} phases
            </span>
          </div>
        )}
      </div>
    </div>
  )
}
