import { X, Loader, Check, Circle, AlertCircle, ListChecks } from 'lucide-react'
import type { PlanMessage, PlanStepInfo, ChatMsg } from './chatTypes'

interface TodoPanelProps {
  messages: ChatMsg[]
  onClose: () => void
}

// Right-side panel showing plan steps from announce_plan/update_plan.
// Follows the FilePanel pattern: 320px wide, border-left, same theme vars.
export default function TodoPanel({ messages, onClose }: TodoPanelProps) {
  // Extract all plan messages from the chat
  const plans = messages.filter((m): m is PlanMessage => m.type === 'plan')

  // Use the most recent plan (last one)
  const plan = plans.length > 0 ? plans[plans.length - 1] : null

  const completedCount = plan ? plan.steps.filter(s => s.status === 'complete').length : 0
  const failedCount = plan ? plan.steps.filter(s => s.status === 'failed').length : 0
  const totalCount = plan ? plan.steps.length : 0
  const hasRunning = plan ? plan.steps.some(s => s.status === 'running') : false
  const allDone = totalCount > 0 && (completedCount + failedCount) === totalCount

  const statusIcon = (status: PlanStepInfo['status']) => {
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
      className="flex flex-col h-full"
      style={{
        width: '320px',
        minWidth: '280px',
        borderLeft: '1px solid var(--border-color)',
        background: 'var(--bg-primary)',
      }}
    >
      {/* Panel header */}
      <div
        className="flex items-center gap-2 px-4 py-2.5 shrink-0"
        style={{ borderBottom: '1px solid var(--border-color)' }}
      >
        <ListChecks size={16} style={{ color: 'var(--text-muted)' }} />
        <span className="text-sm font-medium flex-1" style={{ color: 'var(--text-primary)' }}>
          Todo {totalCount > 0 && `(${completedCount}/${totalCount})`}
        </span>
        <button
          onClick={onClose}
          className="p-1 rounded hover:opacity-70"
          style={{ color: 'var(--text-muted)' }}
        >
          <X size={16} />
        </button>
      </div>

      {/* Steps list */}
      <div className="flex-1 overflow-y-auto">
        {plan ? (
          <div className="py-2">
            {/* Plan goal */}
            <div className="px-4 py-2 mb-1">
              <div className="flex items-center gap-2">
                {hasRunning && !allDone ? (
                  <Loader size={14} className="animate-spin shrink-0" style={{ color: 'var(--brand)' }} />
                ) : allDone ? (
                  <Check size={14} className="text-green-400 shrink-0" />
                ) : (
                  <ListChecks size={14} className="shrink-0" style={{ color: 'var(--brand)' }} />
                )}
                <span className="text-xs font-bold" style={{ color: 'var(--text-primary)' }}>
                  {plan.goal}
                </span>
              </div>
            </div>

            {/* Steps */}
            {plan.steps.map((step) => (
              <div
                key={step.name}
                className="flex items-start gap-3 px-4 py-2.5 transition-colors"
                style={{
                  borderBottom: '1px solid var(--border-color)',
                  opacity: step.status === 'complete' ? 0.6 : 1,
                }}
              >
                <div className="mt-0.5">{statusIcon(step.status)}</div>
                <div className="flex flex-col gap-0.5 min-w-0 flex-1">
                  <span
                    className="text-xs font-medium"
                    style={{
                      color: step.status === 'running'
                        ? 'var(--text-primary)'
                        : step.status === 'complete'
                          ? 'var(--text-muted)'
                          : 'var(--text-secondary)',
                      textDecoration: step.status === 'complete' ? 'line-through' : 'none',
                    }}
                  >
                    {step.name}
                  </span>
                  {step.description && step.description !== step.name && (
                    <span className="text-[10px] leading-relaxed" style={{ color: 'var(--text-muted)' }}>
                      {step.description}
                    </span>
                  )}
                </div>
              </div>
            ))}

            {/* Footer */}
            {totalCount > 0 && (
              <div className="px-4 py-2.5 flex items-center justify-between">
                <span
                  className="text-[11px] font-medium px-2 py-0.5 rounded-full"
                  style={{
                    background: allDone
                      ? (failedCount > 0 ? 'rgba(234, 179, 8, 0.15)' : 'rgba(34, 197, 94, 0.15)')
                      : hasRunning
                        ? 'var(--brand-muted)'
                        : 'rgba(107, 114, 128, 0.15)',
                    color: allDone
                      ? (failedCount > 0 ? '#e49425' : '#149647')
                      : hasRunning
                        ? 'var(--brand)'
                        : 'var(--text-muted)',
                  }}
                >
                  {allDone ? (failedCount > 0 ? 'Partial' : 'Complete') : hasRunning ? 'In Progress' : 'Submitted'}
                </span>
                <span className="text-[11px]" style={{ color: 'var(--text-muted)' }}>
                  {completedCount}/{totalCount} steps
                </span>
              </div>
            )}
          </div>
        ) : (
          <div className="text-xs text-center py-8" style={{ color: 'var(--text-muted)' }}>
            No plan yet — the agent will share its plan here
          </div>
        )}
      </div>
    </div>
  )
}
