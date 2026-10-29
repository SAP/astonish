import { useState, useMemo, useCallback, useEffect, useRef } from 'react'
import { ChevronDown, Loader, Check, Wrench, ListTodo, RotateCcw, Code, Globe, GitFork, FileUp } from 'lucide-react'
import ReactMarkdown from 'react-markdown'
import remarkGfm from 'remark-gfm'
import { markdownComponents } from './markdownComponents'
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
  const [expandedContent, setExpandedContent] = useState<Set<string>>(new Set())
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

  const completedCount = taskStates.filter(t => t.status === 'complete').length
  const totalCount = taskStates.length
  const allDone = data.status !== 'running'

  const toggleTask = (task: TaskState) => {
    const key = detailKey(task)
    const willExpand = !expandedTasks[key]
    setExpandedTasks(prev => ({ ...prev, [key]: willExpand }))
    if (willExpand && !fetchedEvents[key] && !task.events.some(e => e.type === 'task_tool_call' || e.type === 'task_tool_result')) loadTaskEvents(task)
  }

  const toggleContent = (eventKey: string) => {
    setExpandedContent(prev => {
      const next = new Set(prev)
      if (next.has(eventKey)) next.delete(eventKey)
      else next.add(eventKey)
      return next
    })
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

  const TRUNCATE_THRESHOLD = 800

  const renderCardContent = (cardData: unknown, eventKey: string) => {
    if (cardData == null || cardData === undefined) return null
    const text = typeof cardData === 'string' ? cardData : JSON.stringify(cardData, null, 2)
    if (!text) return null

    const isTruncatable = text.length > TRUNCATE_THRESHOLD
    const isFullyExpanded = expandedContent.has(eventKey)
    const displayText = (isTruncatable && !isFullyExpanded) ? text.slice(0, TRUNCATE_THRESHOLD) : text

    return (
      <div className="px-3 pb-2">
        <div className="relative">
          <pre
            className="text-xs whitespace-pre-wrap break-words font-mono p-2 rounded"
            style={{
              background: 'rgba(0,0,0,0.3)',
              color: 'var(--text-secondary)',
              maxHeight: isFullyExpanded ? 'none' : '200px',
              overflowY: 'hidden',
            }}
          >
            {displayText}
          </pre>
          {isTruncatable && !isFullyExpanded && (
            <div
              className="absolute bottom-0 left-0 right-0 h-10 flex items-end justify-center rounded-b"
              style={{ background: 'linear-gradient(transparent, rgba(0,0,0,0.5))' }}
            >
              <button
                onClick={() => toggleContent(eventKey)}
                className="text-[10px] hover:opacity-80 px-2 py-0.5 mb-1 rounded bg-black/50 cursor-pointer"
                style={{ color: 'var(--brand)' }}
              >
                Show more ({Math.ceil(text.length / 1000)}k chars)
              </button>
            </div>
          )}
          {isTruncatable && isFullyExpanded && (
            <div className="flex justify-center mt-1">
              <button
                onClick={() => toggleContent(eventKey)}
                className="text-[10px] hover:opacity-80 px-2 py-0.5 rounded bg-black/30 cursor-pointer"
                style={{ color: 'var(--brand)' }}
              >
                Show less
              </button>
            </div>
          )}
        </div>
      </div>
    )
  }

  const renderToolCard = (evt: SubTaskEvent, eventKey: string) => {
    const isCall = evt.type === 'task_tool_call'
    const name = evt.tool_name || 'unknown'
    const cardData = isCall ? (evt.tool_args || null) : (evt.tool_result || null)

    return (
      <div
        key={eventKey}
        className="my-1.5 rounded-lg overflow-hidden"
        style={{ border: '1px solid var(--border-color)', background: 'rgba(255,255,255,0.03)' }}
      >
        <div className="flex items-center gap-2 px-3 py-1.5">
          <Wrench size={12} className={isCall ? 'text-primary' : 'text-green-400'} />
          <span className="text-xs font-medium" style={{ color: 'var(--text-primary)' }}>
            {isCall ? 'Tool Call' : 'Tool Result'}: <code className="bg-primary/15 text-primary px-1 py-0.5 rounded text-[11px]">{name}</code>
          </span>
        </div>
        {renderCardContent(cardData, eventKey)}
      </div>
    )
  }

  const renderTaskText = (evt: SubTaskEvent, eventKey: string) => {
    const textContent = evt.text || ''
    if (!textContent) return null

    return (
      <div key={eventKey} className="my-1.5">
        <div
          className="p-3 rounded-lg text-sm"
          style={{
            background: 'rgba(255,255,255,0.05)',
            border: '1px solid var(--border-color)',
          }}
        >
          <div className="markdown-body text-xs" style={{ color: 'var(--text-primary)' }}>
            <ReactMarkdown remarkPlugins={[remarkGfm]} components={markdownComponents}>{textContent}</ReactMarkdown>
          </div>
        </div>
      </div>
    )
  }

  const renderTaskEvent = (evt: SubTaskEvent, taskIdx: number, evtIdx: number) => {
    const eventKey = `subtask-${taskIdx}-${evtIdx}`
    if (evt.type === 'task_start' || evt.type === 'task_complete' || evt.type === 'task_failed') {
      return null
    }
    if (evt.type === 'task_tool_call' || evt.type === 'task_tool_result') {
      return renderToolCard(evt, eventKey)
    }
    if (evt.type === 'task_text') {
      return renderTaskText(evt, eventKey)
    }
    return null
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
                          if (!attempt.events.some(e => e.type === 'task_tool_call') && !fetchedEvents[detailKey(selected)]) loadTaskEvents(selected)
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
                  {/* Show loading indicator while fetching child session events */}
                  {loadingTasks.has(detailKey(task)) && (
                    <div className="flex items-center gap-2 text-xs py-2" style={{ color: 'var(--text-muted)' }}>
                      <Loader size={10} className="animate-spin" />
                      <span>Loading execution detail...</span>
                    </div>
                  )}
                  {errors[detailKey(task)] && <div role="alert">{errors[detailKey(task)]} <button onClick={() => loadTaskEvents(task)}>Retry</button></div>}
                  {fetchedEvents[detailKey(task)]?.length === 0 && <div>No execution detail recorded.</div>}
                  {!sessionId && !task.events.length && <div>Execution detail unavailable.</div>}
                  {/* Render lazy-fetched events (tool calls from child session) */}
                  {fetchedEvents[detailKey(task)] && fetchedEvents[detailKey(task)].map((evt, evtIdx) =>
                    renderTaskEvent(evt, taskIdx, evtIdx)
                  )}
                  {/* Render inline events — suppress task_text when fetched events are available
                      (the fetched events already include the full output, avoiding duplication) */}
                  {task.events
                    .filter(evt => !(fetchedEvents[detailKey(task)] && evt.type === 'task_text'))
                    .map((evt, evtIdx) => renderTaskEvent(evt, taskIdx, evtIdx + (fetchedEvents[detailKey(task)]?.length || 0)))}
                </div>
              )}
            </div>
          )
        })}
      </div>
    </div>
  )
}
