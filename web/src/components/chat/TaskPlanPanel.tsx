import { useState, useMemo, useCallback, useEffect, useRef } from 'react'
import { ChevronDown, Loader, Check, Wrench, ListTodo, RotateCcw, Code, Globe, GitFork, FileUp } from 'lucide-react'
import DelegationDetail from './DelegationDetail'
import type { SubTaskExecutionMessage, SubTaskEvent } from './chatTypes'
import { fetchSubtaskEvents } from '../../api/studioChat'

import { deriveDelegationTasks } from './toolActivity'
import type { DelegationTask as TaskState } from './toolActivity'

// Format a timestamp to HH:MM
function formatTime(ts?: number): string {
  if (!ts) return ''
  const d = new Date(ts)
  return d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
}

// Pick an icon for a step based on the tool name or event type
function stepIcon(task: TaskState) {
  // Check last tool name used for a hint
  const lastTool = [...task.events].reverse().find(e => e.tool_name)?.tool_name || ''
  if (lastTool.includes('search') || lastTool.includes('web') || lastTool.includes('fetch')) {
    return <Globe size={12} />
  }
  if (lastTool.includes('write') || lastTool.includes('file') || lastTool.includes('save')) {
    return <FileUp size={12} />
  }
  if (lastTool.includes('code') || lastTool.includes('exec') || lastTool.includes('run')) {
    return <Code size={12} />
  }
  if (task.events.some(e => e.type === 'task_tool_call')) {
    return <Wrench size={12} />
  }
  return <ListTodo size={12} />
}

// Connected vertical timeline panel showing real-time progress of delegate_tasks sub-agents.
// Perplexity-inspired: continuous vertical line, circular status dots, per-step icons,
// timestamps on hover, click to expand details.
export default function TaskPlanPanel({ data, sessionId }: { data: SubTaskExecutionMessage; sessionId?: string }) {
  const [expandedTasks, setExpandedTasks] = useState<Record<string, boolean>>({})
  // Lazy-loaded tool events for tasks that only have summary after page refresh
  const [fetchedEvents, setFetchedEvents] = useState<Record<string, SubTaskEvent[]>>({})
  const [loadingTasks, setLoadingTasks] = useState<Set<string>>(new Set())

  const [errors, setErrors] = useState<Record<string, string>>({})
  const requests = useRef(new Map<string, AbortController>())
  useEffect(() => {
    setLoadingTasks(new Set())
    setSelectedAttempts({})
    return () => { requests.current.forEach(c => c.abort()); requests.current.clear() }
  }, [sessionId, data.batch_id])
  const [selectedAttempts, setSelectedAttempts] = useState<Record<string, number>>({})
  const taskStates = useMemo(() => deriveDelegationTasks(data), [data])
  const visibleTaskStates = taskStates.map(task => {
    const selected = task.attempts.find(a => a.attempt === (selectedAttempts[task.id] ?? task.attempt))
    return selected ? { ...task, ...selected } : task
  })
  const detailKey = (task: TaskState) => JSON.stringify([sessionId, data.batch_id, task.id, task.attempt, task.child_session_id])
  const visibleDetailKeys = JSON.stringify(visibleTaskStates.map(detailKey))
  useEffect(() => {
    const visible = new Set<string>(JSON.parse(visibleDetailKeys))
    requests.current.forEach((controller, key) => {
      if (!visible.has(key)) {
        controller.abort()
        requests.current.delete(key)
        setLoadingTasks(prev => { const next = new Set(prev); next.delete(key); return next })
      }
    })
  }, [visibleDetailKeys])
  const loadTaskEvents = useCallback(async (task: TaskState) => {
    const key = JSON.stringify([sessionId, data.batch_id, task.id, task.attempt, task.child_session_id])
    if (!sessionId || requests.current.has(key)) return
    const controller = new AbortController()
    requests.current.set(key, controller)
    setLoadingTasks(prev => new Set(prev).add(key))
    setErrors(prev => ({ ...prev, [key]: '' }))
    try {
      const selector = task.child_session_id
        ? { child_session_id: task.child_session_id }
        : task.task_id
          ? { batch_id: data.batch_id, task_id: task.task_id, attempt: task.attempt }
          : { task_name: task.name }
      const events = await fetchSubtaskEvents(sessionId, selector, controller.signal)
      if (!controller.signal.aborted) setFetchedEvents(prev => ({ ...prev, [key]: events.map(e => ({ ...e, task_name: task.name })) }))
    } catch (error) {
      if (!controller.signal.aborted) setErrors(prev => ({ ...prev, [key]: error instanceof Error ? error.message : 'Unable to load execution detail' }))
    } finally {
      if (requests.current.get(key) === controller) {
        requests.current.delete(key)
        setLoadingTasks(prev => { const next = new Set(prev); next.delete(key); return next })
      }
    }
  }, [sessionId, data.batch_id])

  const previousStatuses = useRef(new Map<string, string>())
  useEffect(() => {
    visibleTaskStates.forEach(task => {
      const key = detailKey(task)
      const previous = previousStatuses.current.get(key)
      previousStatuses.current.set(key, task.status)
      if (previous === 'running' && task.status !== 'running') {
        // A snapshot requested while running is incomplete even if the detail is
        // now collapsed. Drop it (and any in-flight response) before it can hide
        // the final activity; collapsed details reload lazily on the next open.
        requests.current.get(key)?.abort()
        requests.current.delete(key)
        setFetchedEvents(prev => { const next = { ...prev }; delete next[key]; return next })
        setLoadingTasks(prev => { const next = new Set(prev); next.delete(key); return next })
        if (expandedTasks[key]) void loadTaskEvents(task)
      }
    })
  }, [visibleTaskStates, expandedTasks, loadTaskEvents])

  const completedCount = taskStates.filter(t => t.status === 'complete').length
  const totalCount = taskStates.length
  const allDone = data.status !== 'running'

  const toggleTask = (task: TaskState) => {
    const key = detailKey(task)
    const willExpand = !expandedTasks[key]
    setExpandedTasks(prev => ({ ...prev, [key]: willExpand }))
    if (willExpand && !fetchedEvents[key]) loadTaskEvents(task)
  }

  // Status dot for the timeline
  const statusDot = (status: string) => {
    if (status === 'running') {
      return (
        <div className="timeline-dot timeline-dot--running">
          <Loader size={10} className="animate-spin" />
        </div>
      )
    }
    if (status === 'complete') {
      return (
        <div className="timeline-dot timeline-dot--complete">
          <Check size={10} />
        </div>
      )
    }
    if (status === 'failed') {
      return (
        <div className="timeline-dot timeline-dot--failed">
          <span className="text-[8px] font-bold">!</span>
        </div>
      )
    }
    return <div className="timeline-dot timeline-dot--pending" />
  }

  return (
    <div
      className="rounded-lg overflow-hidden text-sm transition-opacity duration-300"
      style={{
        border: '1px solid var(--border-color)',
        background: 'var(--bg-secondary)',
        opacity: allDone ? 0.85 : 1,
      }}
    >
      {/* Header */}
      <div className="flex items-center gap-2.5 px-4 py-2.5">
        {data.status === 'running' && <Loader size={15} className="animate-spin shrink-0" style={{ color: 'var(--brand)' }} />}
        {(totalCount > 0 && completedCount === totalCount) && <Check size={15} className="text-green-400 shrink-0" />}
        {data.status === 'error' && <span className="text-red-400 font-bold shrink-0">!</span>}
        <GitFork size={15} className="shrink-0" style={{ color: 'var(--text-muted)' }} />
        <span className="font-semibold text-sm" style={{ color: 'var(--text-primary)' }}>Task Delegation</span>
        <span className="text-xs ml-auto" style={{ color: 'var(--text-muted)' }}>
          {completedCount}/{totalCount} task{totalCount !== 1 ? 's' : ''}
        </span>
      </div>

      {totalCount === 0 && <div role="alert">Delegation details are missing or invalid.</div>}
      {taskStates.some(t => t.status === 'interrupted') && <div role="status">Delegation interrupted</div>}
      {/* Timeline */}
      <div
        className="px-4 pb-3"
        style={{ borderTop: '1px solid var(--border-color)' }}
      >
        {visibleTaskStates.map((task, taskIdx) => {
          const isExpanded = expandedTasks[detailKey(task)] === true
          const isLast = taskIdx === taskStates.length - 1

          return (
            <div key={task.id} className="relative group/task">
              {/* Vertical connector line */}
              {!isLast && (
                <div
                  className="timeline-line"
                  style={{
                    position: 'absolute',
                    left: '9px',
                    top: '20px',
                    bottom: '0',
                    width: '1px',
                    background: 'var(--border-color)',
                  }}
                />
              )}

              {/* Task row */}
              <div data-testid={`delegation-task-row-${task.id}`} className="flex items-center gap-2 relative z-10">
                <button
                  aria-expanded={isExpanded}
                  onClick={() => toggleTask(task)}
                  className="min-w-0 flex-1 flex items-center gap-3 py-2 text-left cursor-pointer"
                >
                {/* Status dot */}
                {statusDot(task.status)}

                {/* Icon */}
                <span style={{ color: 'var(--text-muted)' }}>{stepIcon(task)}</span>

                {/* Task name */}
                <span
                  className="text-xs font-medium flex-1 truncate"
                  style={{
                    color: task.status === 'running'
                      ? 'var(--text-primary)'
                      : task.status === 'complete'
                        ? 'var(--text-secondary)'
                        : task.status === 'failed'
                          ? '#ef4444'
                          : 'var(--text-muted)',
                  }}
                >
                  {task.name}
                  {task.description && (
                    <span className="font-normal ml-1.5" style={{ color: 'var(--text-muted)' }}>
                      {task.description}
                    </span>
                  )}
                </span>

                {/* Right side: status/timestamp/duration (show on hover) */}
                <span className="flex items-center gap-2 shrink-0">
                  {task.status === 'running' && (
                    <span className="text-[11px] flex items-center gap-1" style={{ color: 'var(--brand)' }}>
                      {task.retrying && <RotateCcw size={10} />}
                      {task.evaluating ? '⟳ Evaluating…' : task.retrying ? 'retrying' : 'running'}
                    </span>
                  )}
                  {task.status === 'interrupted' && <span>interrupted</span>}
                  {task.status === 'failed' && (
                    <span className="text-[11px] text-red-400 truncate max-w-[120px]">{task.error || 'failed'}</span>
                  )}
                  {task.duration && (
                    <span className="text-[11px]" style={{ color: 'var(--text-muted)' }}>
                      {task.duration}
                    </span>
                  )}
                  {task.startTimestamp && (
                    <span
                      className="text-[11px] opacity-0 group-hover/task:opacity-100 transition-opacity"
                      style={{ color: 'var(--text-muted)' }}
                    >
                      {formatTime(task.startTimestamp)}
                    </span>
                  )}
                  {isExpanded ? (
                    <ChevronDown size={12} style={{ color: 'var(--text-muted)' }} />
                  ) : (
                    <ChevronDown size={12} className="rotate-[-90deg]" style={{ color: 'var(--text-muted)' }} />
                  )}
                </span>
                </button>

                {task.attempts.length > 1 && (
                  <div aria-label={`Attempts for ${task.name}`} className="flex shrink-0 items-center gap-1 rounded-md border p-0.5" style={{ borderColor: 'var(--border-color)', background: 'var(--surface-secondary)' }}>
                    {task.attempts.map(attempt => (
                      <button
                        key={attempt.attempt}
                        aria-pressed={task.attempt === attempt.attempt}
                        className="rounded px-1.5 py-0.5 text-[10px] whitespace-nowrap"
                        style={{
                          color: task.attempt === attempt.attempt ? 'var(--text-primary)' : 'var(--text-muted)',
                          background: task.attempt === attempt.attempt ? 'var(--bg-tertiary)' : 'transparent',
                        }}
                        onClick={() => {
                          const selected = { ...task, ...attempt }
                          if (detailKey(selected) !== detailKey(task)) {
                            const oldKey = detailKey(task)
                            requests.current.get(oldKey)?.abort()
                            requests.current.delete(oldKey)
                            setLoadingTasks(prev => { const next = new Set(prev); next.delete(oldKey); return next })
                          }
                          setSelectedAttempts(prev => ({ ...prev, [task.id]: attempt.attempt! }))
                          setExpandedTasks(prev => ({ ...prev, [detailKey(selected)]: true }))
                          if (!fetchedEvents[detailKey(selected)]) loadTaskEvents(selected)
                        }}
                      >
                        Attempt {attempt.attempt}
                      </button>
                    ))}
                  </div>
                )}
              </div>
              {/* Expanded task details */}
              {isExpanded && (
                <div className="ml-[28px] pb-2 relative z-10">
                  {task.events.length === 0 && task.status === 'pending' && (
                    <div className="text-xs py-1" style={{ color: 'var(--text-muted)' }}>Waiting to start...</div>
                  )}
                  <DelegationDetail key={detailKey(task)} identity={detailKey(task)}
                    live={task.events} persisted={fetchedEvents[detailKey(task)]}
                    running={task.status === 'running'} loading={loadingTasks.has(detailKey(task))}
                    error={errors[detailKey(task)]} retry={() => loadTaskEvents(task)} />
                </div>
              )}
            </div>
          )
        })}
      </div>
    </div>
  )
}
