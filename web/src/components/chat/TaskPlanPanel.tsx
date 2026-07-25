import { useState, useMemo, useCallback } from 'react'
import { ChevronDown, Loader, Check, Wrench, ListTodo, RotateCcw, Code, Globe, GitFork, FileUp } from 'lucide-react'
import ReactMarkdown from 'react-markdown'
import remarkGfm from 'remark-gfm'
import { markdownComponents } from './markdownComponents'
import type { SubTaskExecutionMessage, SubTaskEvent } from './chatTypes'
import { fetchSubtaskEvents } from '../../api/studioChat'

// Task status derived from events
interface TaskState {
  name: string
  description: string
  events: SubTaskEvent[]
  status: 'pending' | 'running' | 'complete' | 'failed'
  retrying?: boolean
  duration?: string
  error?: string
  startTimestamp?: number
}

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

  // Lazy-fetch tool events for a task when it's expanded and has no tool detail.
  // This happens after page refresh — only the summary text is available from the
  // delegate_tasks response, but the full child session has all tool calls.
  const loadTaskEvents = useCallback(async (taskName: string) => {
    if (!sessionId || fetchedEvents[taskName] || loadingTasks.has(taskName)) return
    setLoadingTasks(prev => new Set(prev).add(taskName))
    try {
      const events = await fetchSubtaskEvents(sessionId, taskName)
      // Convert API response to SubTaskEvent format
      const mapped: SubTaskEvent[] = events.map(e => ({
        type: e.type,
        task_name: taskName,
        tool_name: e.tool_name,
        tool_args: e.tool_args,
        tool_result: e.tool_result,
        text: e.text,
      }))
      setFetchedEvents(prev => ({ ...prev, [taskName]: mapped }))
    } catch {
      // Silently fail — the summary text is still visible
    } finally {
      setLoadingTasks(prev => {
        const next = new Set(prev)
        next.delete(taskName)
        return next
      })
    }
  }, [sessionId, fetchedEvents, loadingTasks])

  // Derive task states from the event stream
  const taskStates = useMemo(() => {
    const taskMap: Record<string, TaskState> = {}
    const taskOrder: string[] = []

    for (const t of data.tasks) {
      if (!taskMap[t.name]) {
        taskMap[t.name] = {
          name: t.name,
          description: t.description,
          events: [],
          status: 'pending',
        }
        taskOrder.push(t.name)
      }
    }

    for (const evt of data.events) {
      if (evt.type === 'delegation_start' || evt.type === 'delegation_complete') continue

      const taskName = evt.task_name || '_unknown'
      if (!taskMap[taskName]) {
        taskMap[taskName] = {
          name: taskName,
          description: '',
          events: [],
          status: 'pending',
        }
        taskOrder.push(taskName)
      }

      const task = taskMap[taskName]
      task.events.push(evt)

      if (evt.type === 'task_start') {
        task.status = 'running'
        task.retrying = false
        task.startTimestamp = evt.timestamp
      } else if (evt.type === 'task_retry') {
        task.status = 'running'
        task.retrying = true
        task.error = undefined
        task.duration = undefined
      } else if (evt.type === 'task_complete') {
        task.status = 'complete'
        task.retrying = false
        task.duration = evt.duration
      } else if (evt.type === 'task_failed') {
        task.status = 'failed'
        task.retrying = false
        task.duration = evt.duration
        task.error = evt.error
      }
    }

    return taskOrder.map(name => taskMap[name])
  }, [data.events, data.tasks])

  const completedCount = taskStates.filter(t => t.status === 'complete').length
  const totalCount = taskStates.length
  const allDone = data.status !== 'running'

  const toggleTask = (name: string) => {
    const willExpand = !expandedTasks[name]
    setExpandedTasks(prev => ({ ...prev, [name]: willExpand }))
    // Lazy-load detail if expanding a task that has no tool events (post-refresh)
    if (willExpand) {
      const task = taskStates.find(t => t.name === name)
      if (task && !task.events.some(e => e.type === 'task_tool_call' || e.type === 'task_tool_result')) {
        loadTaskEvents(name)
      }
    }
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
        {(data.status === 'complete' || data.status === 'partial') && <Check size={15} className="text-green-400 shrink-0" />}
        {data.status === 'error' && <span className="text-red-400 font-bold shrink-0">!</span>}
        <GitFork size={15} className="shrink-0" style={{ color: 'var(--text-muted)' }} />
        <span className="font-semibold text-sm" style={{ color: 'var(--text-primary)' }}>Task Delegation</span>
        <span className="text-xs ml-auto" style={{ color: 'var(--text-muted)' }}>
          {completedCount}/{totalCount} task{totalCount !== 1 ? 's' : ''}
        </span>
      </div>

      {/* Timeline */}
      <div
        className="px-4 pb-3"
        style={{ borderTop: '1px solid var(--border-color)' }}
      >
        {taskStates.map((task, taskIdx) => {
          const isExpanded = expandedTasks[task.name] === true
          const isLast = taskIdx === taskStates.length - 1

          return (
            <div key={task.name} className="relative group/task">
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
              <button
                onClick={() => toggleTask(task.name)}
                className="w-full flex items-center gap-3 py-2 text-left cursor-pointer relative z-10"
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
                      {task.retrying ? 'retrying' : 'running'}
                    </span>
                  )}
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

              {/* Expanded task details */}
              {isExpanded && (
                <div className="ml-[28px] pb-2 relative z-10">
                  {task.events.length === 0 && task.status === 'pending' && (
                    <div className="text-xs py-1" style={{ color: 'var(--text-muted)' }}>Waiting to start...</div>
                  )}
                  {/* Show loading indicator while fetching child session events */}
                  {loadingTasks.has(task.name) && (
                    <div className="flex items-center gap-2 text-xs py-2" style={{ color: 'var(--text-muted)' }}>
                      <Loader size={10} className="animate-spin" />
                      <span>Loading execution detail...</span>
                    </div>
                  )}
                  {/* Render lazy-fetched events (tool calls from child session) */}
                  {fetchedEvents[task.name] && fetchedEvents[task.name].map((evt, evtIdx) =>
                    renderTaskEvent(evt, taskIdx, evtIdx)
                  )}
                  {/* Render inline events — suppress task_text when fetched events are available
                      (the fetched events already include the full output, avoiding duplication) */}
                  {task.events
                    .filter(evt => !(fetchedEvents[task.name] && evt.type === 'task_text'))
                    .map((evt, evtIdx) => renderTaskEvent(evt, taskIdx, evtIdx + (fetchedEvents[task.name]?.length || 0)))}
                </div>
              )}
            </div>
          )
        })}
      </div>
    </div>
  )
}
