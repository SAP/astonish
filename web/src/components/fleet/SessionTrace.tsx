import { useState, useEffect, useCallback, useRef } from 'react'
import {
  ChevronDown, ChevronRight, Loader, Check, X,
  Square, FileText, Eye,
  Wrench, ExternalLink,
  AlertCircle,
} from 'lucide-react'
import ReactMarkdown from 'react-markdown'
import remarkGfm from 'remark-gfm'
import { markdownComponents } from '../chat/markdownComponents'
import {
  fetchFleetTrace, fetchFleetSessions, connectFleetStream,
  stopFleetSession, fetchFleetThreads, fetchFleetMessages,
  listFleetSessionMailbox, listFleetSessionTasks,
} from '../../api/fleetChat'
import type { FleetMailboxMessage, FleetTask, FleetTrace, FleetMessage } from '../../api/fleetChat'
import { buildPath } from '../../hooks/useHashRouter'
import type { TraceEvent, FleetThreadExt } from './fleetUtils'
import { getAgentColor, extractAgentRole } from './fleetUtils'

// ─── Session Execution Trace View ───

interface SessionTraceProps {
  sessionId: string
  onRefresh?: () => void
  onNavigate?: (path: string) => void
}

export default function SessionTrace({ sessionId, onRefresh, onNavigate }: SessionTraceProps) {
  const [trace, setTrace] = useState<FleetTrace | null>(null)
  const [isLoading, setIsLoading] = useState(true)
  const [error, setError] = useState<string | null>(null)
  const [toolsOnly, setToolsOnly] = useState(false)
  const [expandedEntries, setExpandedEntries] = useState<Set<number>>(new Set())
  const [isStopping, setIsStopping] = useState(false)
  const [liveSession, setLiveSession] = useState<import('../../api/fleetChat').FleetSession | null>(null)
  const scrollRef = useRef<HTMLDivElement | null>(null)
  const prevEventCountRef = useRef<number>(0)
  const abortRef = useRef<AbortController | null>(null)
  const pollRef = useRef<ReturnType<typeof setInterval> | null>(null)
  const [activeTraceTab, setActiveTraceTab] = useState<'trace' | 'mailbox' | 'tasks'>('trace')
  const [tasks, setTasks] = useState<FleetTask[]>([])
  const [mailbox, setMailbox] = useState<FleetMailboxMessage[]>([])
  const [auxLoading, setAuxLoading] = useState(false)

  // Thread filtering state
  const [threads, setThreads] = useState<FleetThreadExt[]>([])
  const [selectedThread, setSelectedThread] = useState<string | null>(null) // null = "All" (raw trace view)
  const [threadMessages, setThreadMessages] = useState<FleetMessage[]>([])
  const [threadMsgsLoading, setThreadMsgsLoading] = useState(false)
  const [expandedMsgs, setExpandedMsgs] = useState<Set<number>>(new Set())

  const loadTrace = useCallback(async () => {
    try {
      const data = await fetchFleetTrace(sessionId, { toolsOnly })
      setTrace(data)
      setError(null)
    } catch (err: any) {
      setError(err.message)
    } finally {
      setIsLoading(false)
    }
  }, [sessionId, toolsOnly])

  const loadThreads = useCallback(async () => {
    try {
      const data = await fetchFleetThreads(sessionId)
      setThreads((data.threads || []) as FleetThreadExt[])
    } catch {
      // threads endpoint may fail for very old sessions — just show empty
    }
  }, [sessionId])

  const loadTasks = useCallback(async () => {
    setAuxLoading(true)
    try {
      setTasks(await listFleetSessionTasks(sessionId))
    } catch {
      setTasks([])
    } finally {
      setAuxLoading(false)
    }
  }, [sessionId])

  const loadMailbox = useCallback(async () => {
    const recipients = [...new Set(threads.map(t => t.agent_key || t.thread_key).filter(Boolean))] as string[]
    if (recipients.length === 0) {
      setMailbox([])
      return
    }
    setAuxLoading(true)
    try {
      const perRecipient = await Promise.all(recipients.map(recipient => listFleetSessionMailbox(sessionId, recipient).catch(() => [])))
      setMailbox(perRecipient.flat())
    } finally {
      setAuxLoading(false)
    }
  }, [sessionId, threads])

  // Thread/agent trace state: when an agent tab is selected, we load
  // the trace filtered to that agent (shows tool calls + text).
  // The _system tab still uses fleet messages.
  const [agentTrace, setAgentTrace] = useState<FleetTrace | null>(null)
  const [agentTraceLoading, setAgentTraceLoading] = useState(false)
  const [expandedAgentEntries, setExpandedAgentEntries] = useState<Set<number>>(new Set())

  const loadThreadMessages = useCallback(async (agentKey: string) => {
    if (agentKey === '_system') {
      // System tab: use fleet messages (system messages don't have trace events)
      setThreadMsgsLoading(true)
      setAgentTrace(null)
      try {
        const data = await fetchFleetMessages(sessionId, {})
        let msgs = data.messages || []
        msgs = msgs.filter(m => !m.memory_keys || m.memory_keys.length === 0)
        setThreadMessages(msgs)
      } catch {
        setThreadMessages([])
      } finally {
        setThreadMsgsLoading(false)
      }
    } else {
      // Agent tab: use trace filtered by agent (includes tool calls)
      setAgentTraceLoading(true)
      setThreadMessages([])
      try {
        const data = await fetchFleetTrace(sessionId, { agent: agentKey, toolsOnly })
        setAgentTrace(data)
      } catch {
        setAgentTrace(null)
      } finally {
        setAgentTraceLoading(false)
      }
    }
  }, [sessionId, toolsOnly])

  // Check if session is active and connect to live stream
  useEffect(() => {
    let cancelled = false
    const checkAndConnect = async () => {
      try {
        const data = await fetchFleetSessions()
        const active = (data.sessions || []).find(s => s.id === sessionId)
        if (active && !cancelled) {
          setLiveSession(active)
          // Connect to SSE stream for live state updates
          const controller = connectFleetStream({
            sessionId,
            onEvent: (type: string, eventData: Record<string, unknown>) => {
              if (type === 'fleet_state') {
                setLiveSession(prev => prev ? { ...prev, state: eventData.state as string, active_agent: eventData.active_agent as string } : prev)
              }
              if (type === 'fleet_done') {
                setLiveSession(prev => prev ? { ...prev, state: 'stopped' } : prev)
                // Final trace load to capture remaining events
                loadTrace()
                loadThreads()
              }
            },
            onError: () => {},
            onDone: () => {},
          })
          abortRef.current = controller
        }
      } catch {
        // Session not active, just show trace
      }
    }
    checkAndConnect()
    return () => {
      cancelled = true
      if (abortRef.current) abortRef.current.abort()
    }
  }, [sessionId, loadTrace, loadThreads])

  // Load trace data and threads on mount
  useEffect(() => {
    setIsLoading(true)
    loadTrace()
    loadThreads()
  }, [loadTrace, loadThreads])

  // Poll trace + threads every 5s for active sessions
  useEffect(() => {
    if (!liveSession || liveSession.state === 'stopped' || liveSession.state === 'completed') return
    pollRef.current = setInterval(() => {
      loadTrace()
      loadThreads()
      if (selectedThread !== null) {
        loadThreadMessages(selectedThread)
      }
    }, 5000)
    return () => { if (pollRef.current) clearInterval(pollRef.current) }
  }, [liveSession, loadTrace, loadThreads, selectedThread, loadThreadMessages])

  // Load thread messages when a thread is selected
  useEffect(() => {
    if (selectedThread !== null) {
      loadThreadMessages(selectedThread)
    }
  }, [selectedThread, loadThreadMessages])

  useEffect(() => {
    if (activeTraceTab === 'tasks') loadTasks()
    if (activeTraceTab === 'mailbox') loadMailbox()
  }, [activeTraceTab, loadTasks, loadMailbox])

  // Auto-scroll when new trace events arrive (only if already near bottom)
  useEffect(() => {
    const el = scrollRef.current
    const eventCount = selectedThread === null
      ? (trace?.events?.length || 0)
      : selectedThread === '_system'
        ? threadMessages.length
        : (agentTrace?.events?.length || 0)
    if (el && eventCount > prevEventCountRef.current) {
      const isNearBottom = el.scrollHeight - el.scrollTop - el.clientHeight < 100
      if (isNearBottom) {
        el.scrollTop = el.scrollHeight
      }
    }
    prevEventCountRef.current = eventCount
  }, [trace, threadMessages, agentTrace, selectedThread])

  const handleStop = async () => {
    if (isStopping) return
    setIsStopping(true)
    try {
      await stopFleetSession(sessionId)
      setLiveSession(prev => prev ? { ...prev, state: 'stopped' } : prev)
      if (onRefresh) onRefresh()
    } catch (err: any) {
      alert('Stop failed: ' + err.message)
    } finally {
      setIsStopping(false)
    }
  }

  const toggleEntry = (index: number) => {
    setExpandedEntries(prev => {
      const next = new Set(prev)
      if (next.has(index)) next.delete(index)
      else next.add(index)
      return next
    })
  }

  const toggleMsg = (index: number) => {
    setExpandedMsgs(prev => {
      const next = new Set(prev)
      if (next.has(index)) next.delete(index)
      else next.add(index)
      return next
    })
  }

  const toggleAgentEntry = (index: number) => {
    setExpandedAgentEntries(prev => {
      const next = new Set(prev)
      if (next.has(index)) next.delete(index)
      else next.add(index)
      return next
    })
  }

  // Format thread key for display: "dev+po" -> "dev <-> po", "" -> "system"
  const formatThreadLabel = (agentKey: string | undefined) => {
    if (!agentKey) return 'system'
    return `@${agentKey}`
  }

  // Get the non-system agent memory tabs for the tab bar.
  // The /threads endpoint now returns agent_key instead of thread_key.
  const conversationThreads = threads.filter(t => (t.agent_key || t.thread_key) && (t.agent_key || t.thread_key) !== '_system')
  const systemThread = threads.find(t => (t.agent_key || t.thread_key) === '_system' || (!t.agent_key && !t.thread_key))
  const hasThreads = conversationThreads.length > 0

  if (isLoading && !trace) {
    return (
      <div className="flex-1 flex items-center justify-center">
        <Loader size={24} className="animate-spin text-primary" />
      </div>
    )
  }

  if (error && !trace) {
    return (
      <div className="flex-1 flex items-center justify-center">
        <div className="text-center">
          <AlertCircle size={32} className="mx-auto mb-2 text-red-400" />
          <p className="text-sm" style={{ color: 'var(--text-muted)' }}>{error}</p>
        </div>
      </div>
    )
  }

  const events = (trace?.events || []) as TraceEvent[]
  const summary = trace?.summary || { total_events: 0, tool_calls: 0, errors: 0 }
  const isActive = liveSession && liveSession.state !== 'stopped' && liveSession.state !== 'completed'

  return (
    <div className="flex-1 flex flex-col overflow-hidden">
      {/* Header */}
      <div className="flex items-center justify-between px-6 py-3" style={{ borderBottom: '1px solid var(--border-color)' }}>
        <div className="flex items-center gap-3">
          <div className="flex items-center gap-2">
            {isActive ? (
              <div className="w-2.5 h-2.5 rounded-full bg-green-400 animate-pulse" />
            ) : (
              <div className="w-2.5 h-2.5 rounded-full bg-gray-500" />
            )}
            <span className="text-sm font-semibold" style={{ color: 'var(--text-primary)' }}>
              Session {sessionId.slice(0, 8)}
            </span>
          </div>
          {liveSession?.active_agent && (
            <span className="text-xs px-2 py-0.5 rounded" style={{ background: getAgentColor(liveSession.active_agent).bg, color: getAgentColor(liveSession.active_agent).text }}>
              @{liveSession.active_agent}
            </span>
          )}
          <span className="text-xs" style={{ color: 'var(--text-muted)' }}>
            {summary.total_events || 0} events | {summary.tool_calls || 0} tool calls | {summary.errors || 0} errors
          </span>
        </div>
        <div className="flex items-center gap-2">
          {(selectedThread === null || (selectedThread !== null && selectedThread !== '_system')) && (
            activeTraceTab === 'trace' && <label className="flex items-center gap-1.5 text-xs cursor-pointer" style={{ color: 'var(--text-secondary)' }}>
              <input
                type="checkbox"
                checked={toolsOnly}
                onChange={(e: React.ChangeEvent<HTMLInputElement>) => setToolsOnly(e.target.checked)}
                className="accent-primary"
              />
              Tools only
            </label>
          )}
          {isActive && onNavigate && (
            <button
              onClick={() => onNavigate(buildPath('chat', { sessionId }))}
              className="flex items-center gap-1.5 px-3 py-1.5 text-xs font-medium rounded-lg transition-colors"
              style={{ background: 'var(--brand-muted)', color: 'var(--brand)' }}
              title="Open in Chat to send messages"
            >
              <ExternalLink size={12} /> Open in Chat
            </button>
          )}
          {isActive && (
            <button
              onClick={handleStop}
              disabled={isStopping}
              className="flex items-center gap-1.5 px-3 py-1.5 text-xs font-medium rounded-lg bg-red-600/20 text-red-400 hover:bg-red-600/30 transition-colors disabled:opacity-50"
            >
              <Square size={12} /> {isStopping ? 'Stopping...' : 'Stop'}
            </button>
          )}
        </div>
      </div>

      <div className="flex items-center gap-1.5 px-6 py-2" style={{ borderBottom: '1px solid var(--border-color)' }}>
        {(['trace', 'mailbox', 'tasks'] as const).map(tab => (
          <button
            key={tab}
            onClick={() => setActiveTraceTab(tab)}
            className="px-3 py-1 text-xs font-medium rounded-full capitalize transition-colors"
            style={activeTraceTab === tab
              ? { background: 'color-mix(in oklab, var(--brand) 20%, transparent)', color: 'var(--brand)', border: '1px solid color-mix(in oklab, var(--brand) 40%, transparent)' }
              : { background: 'rgba(255,255,255,0.04)', color: 'var(--text-secondary)', border: '1px solid var(--border-color)' }}
          >
            {tab}
            {tab === 'tasks' && tasks.length > 0 && <span className="ml-1 text-[10px] opacity-70">{tasks.length}</span>}
            {tab === 'mailbox' && mailbox.length > 0 && <span className="ml-1 text-[10px] opacity-70">{mailbox.length}</span>}
          </button>
        ))}
      </div>

      {/* Thread selector tabs */}
      {activeTraceTab === 'trace' && hasThreads && (
        <div className="flex items-center gap-1.5 px-6 py-2 overflow-x-auto" style={{ borderBottom: '1px solid var(--border-color)' }}>
          <button
            onClick={() => setSelectedThread(null)}
            className="flex items-center gap-1.5 px-3 py-1 text-xs font-medium rounded-full transition-colors whitespace-nowrap"
            style={selectedThread === null
              ? { background: 'color-mix(in oklab, var(--brand) 20%, transparent)', color: 'var(--brand)', border: '1px solid color-mix(in oklab, var(--brand) 40%, transparent)' }
              : { background: 'rgba(255,255,255,0.04)', color: 'var(--text-secondary)', border: '1px solid var(--border-color)' }
            }
          >
            All
            <span className="text-[10px] opacity-70">{events.length}</span>
          </button>

          {conversationThreads.map(t => {
            const agentKey = t.agent_key || t.thread_key
            const isSelected = selectedThread === agentKey
            // Use the agent's own color for their memory tab
            const color = getAgentColor(agentKey || 'system')

            return (
              <button
                key={agentKey}
                onClick={() => setSelectedThread(agentKey)}
                className="flex items-center gap-1.5 px-3 py-1 text-xs font-medium rounded-full transition-colors whitespace-nowrap"
                style={isSelected
                  ? { background: color.bg, color: color.text, border: `1px solid ${color.border}` }
                  : { background: 'rgba(255,255,255,0.04)', color: 'var(--text-secondary)', border: '1px solid var(--border-color)' }
                }
              >
                {formatThreadLabel(agentKey)}
                <span className="text-[10px] opacity-70">{t.message_count}</span>
              </button>
            )
          })}

          {systemThread && systemThread.message_count > 0 && (
            <button
              onClick={() => setSelectedThread('_system')}
              className="flex items-center gap-1.5 px-3 py-1 text-xs font-medium rounded-full transition-colors whitespace-nowrap"
              style={selectedThread === '_system'
                ? { background: 'rgba(107, 114, 128, 0.2)', color: '#9ca3af', border: '1px solid rgba(107, 114, 128, 0.4)' }
                : { background: 'rgba(255,255,255,0.04)', color: 'var(--text-secondary)', border: '1px solid var(--border-color)' }
              }
            >
              system
              <span className="text-[10px] opacity-70">{systemThread.message_count}</span>
            </button>
          )}
        </div>
      )}

      {/* Content area: raw trace, agent trace, or system messages */}
      <div className="flex-1 overflow-y-auto px-6 py-4 space-y-1" ref={scrollRef}>
        {activeTraceTab === 'tasks' ? (
          auxLoading ? (
            <div className="flex items-center justify-center py-12">
              <Loader size={20} className="animate-spin text-primary" />
            </div>
          ) : tasks.length === 0 ? (
            <EmptyAux message="No task board entries" />
          ) : (
            tasks.map((task, i) => <TaskRow key={task.id || task.ID || i} task={task} />)
          )
        ) : activeTraceTab === 'mailbox' ? (
          auxLoading ? (
            <div className="flex items-center justify-center py-12">
              <Loader size={20} className="animate-spin text-primary" />
            </div>
          ) : mailbox.length === 0 ? (
            <EmptyAux message="No mailbox messages" />
          ) : (
            mailbox.map((msg, i) => <MailboxRow key={msg.id || msg.ID || i} msg={msg} />)
          )
        ) : selectedThread !== null && selectedThread !== '_system' ? (
          /* Agent trace view (with tool calls) */
          agentTraceLoading && !agentTrace ? (
            <div className="flex items-center justify-center py-12">
              <Loader size={20} className="animate-spin text-primary" />
            </div>
          ) : (!agentTrace || ((agentTrace.events || []) as TraceEvent[]).length === 0) ? (
            <div className="text-center py-12">
              <FileText size={32} className="mx-auto mb-2" style={{ color: 'var(--text-muted)' }} />
              <p className="text-sm" style={{ color: 'var(--text-muted)' }}>No trace events for @{selectedThread}</p>
            </div>
          ) : (
            ((agentTrace.events || []) as TraceEvent[]).map((entry, i) => (
              <TraceEntryRow key={i} entry={entry} index={i} expanded={expandedAgentEntries.has(i)} onToggle={toggleAgentEntry} />
            ))
          )
        ) : selectedThread === '_system' ? (
          /* System messages view */
          threadMsgsLoading && threadMessages.length === 0 ? (
            <div className="flex items-center justify-center py-12">
              <Loader size={20} className="animate-spin text-primary" />
            </div>
          ) : threadMessages.length === 0 ? (
            <div className="text-center py-12">
              <FileText size={32} className="mx-auto mb-2" style={{ color: 'var(--text-muted)' }} />
              <p className="text-sm" style={{ color: 'var(--text-muted)' }}>No system messages</p>
            </div>
          ) : (
            threadMessages.map((msg, i) => (
              <ThreadMessageRow key={msg.id || i} msg={msg} index={i} expanded={expandedMsgs.has(i)} onToggle={toggleMsg} />
            ))
          )
        ) : (
          /* Raw trace view (original behavior) */
          events.length === 0 && !isActive ? (
            <div className="text-center py-12">
              <FileText size={32} className="mx-auto mb-2" style={{ color: 'var(--text-muted)' }} />
              <p className="text-sm" style={{ color: 'var(--text-muted)' }}>No trace events found</p>
            </div>
          ) : (
            events.map((entry, i) => (
              <TraceEntryRow key={i} entry={entry} index={i} expanded={expandedEntries.has(i)} onToggle={toggleEntry} />
            ))
          )
        )}

        {isActive && (
          <div className="flex items-center gap-2 py-3 text-xs" style={{ color: 'var(--text-muted)' }}>
            <Loader size={12} className="animate-spin text-primary" />
            <span>Session is active{selectedThread !== null ? ', thread updates every 5s...' : ', trace updates every 5s...'}</span>
          </div>
        )}
      </div>
    </div>
  )
}

function EmptyAux({ message }: { message: string }) {
  return (
    <div className="text-center py-12">
      <FileText size={32} className="mx-auto mb-2" style={{ color: 'var(--text-muted)' }} />
      <p className="text-sm" style={{ color: 'var(--text-muted)' }}>{message}</p>
    </div>
  )
}

function TaskRow({ task }: { task: FleetTask }) {
  const title = task.title || task.Title || task.id || task.ID || 'Task'
  const status = task.status || task.Status || 'open'
  const claimedBy = task.claimed_by || task.ClaimedBy
  const capabilities = task.required_capabilities || task.RequiredCapabilities || []
  const description = task.description || task.Description
  return (
    <div className="rounded-lg p-3 mb-2 text-xs" style={{ background: 'rgba(255,255,255,0.03)', border: '1px solid var(--border-color)' }}>
      <div className="flex items-center gap-2">
        <span className="font-medium" style={{ color: 'var(--text-primary)' }}>{title}</span>
        <span className="px-1.5 py-0.5 rounded text-[10px]" style={{ background: 'var(--brand-muted)', color: 'var(--brand)' }}>{status}</span>
        {claimedBy && <span style={{ color: getAgentColor(claimedBy).text }}>@{claimedBy}</span>}
      </div>
      {description && <p className="mt-1" style={{ color: 'var(--text-secondary)' }}>{description}</p>}
      {capabilities.length > 0 && <p className="mt-1" style={{ color: 'var(--text-muted)' }}>Capabilities: {capabilities.join(', ')}</p>}
    </div>
  )
}

function MailboxRow({ msg }: { msg: FleetMailboxMessage }) {
  const sender = msg.sender || msg.Sender || 'system'
  const recipient = msg.recipient || msg.Recipient || 'unknown'
  const body = msg.body || msg.Body || ''
  const status = msg.delivery_status || msg.DeliveryStatus || 'delivered'
  const time = msg.created_at || msg.CreatedAt
  return (
    <div className="rounded-lg p-3 mb-2 text-xs" style={{ background: 'rgba(255,255,255,0.03)', border: '1px solid var(--border-color)' }}>
      <div className="flex items-center gap-2 mb-1">
        <span style={{ color: getAgentColor(sender).text }}>@{sender}</span>
        <span style={{ color: 'var(--text-muted)' }}>to</span>
        <span style={{ color: getAgentColor(recipient).text }}>@{recipient}</span>
        <span className="px-1.5 py-0.5 rounded text-[10px]" style={{ background: 'rgba(255,255,255,0.06)', color: 'var(--text-muted)' }}>{status}</span>
        {time && <span className="ml-auto text-[10px]" style={{ color: 'var(--text-muted)' }}>{new Date(time).toLocaleTimeString()}</span>}
      </div>
      <div className="rounded p-2" style={{ background: 'rgba(0,0,0,0.18)', color: 'var(--text-secondary)' }}>
        <ReactMarkdown remarkPlugins={[remarkGfm]} components={markdownComponents}>{body}</ReactMarkdown>
      </div>
    </div>
  )
}

// Single trace entry row

interface TraceEntryRowProps {
  entry: TraceEvent
  index: number
  expanded: boolean
  onToggle: (index: number) => void
}

function TraceEntryRow({ entry, index, expanded, onToggle }: TraceEntryRowProps) {
  const time = entry.timestamp ? new Date(entry.timestamp).toLocaleTimeString() : ''
  const sessionLabel = entry.session || ''
  const agentRole = extractAgentRole(sessionLabel)
  // Fall back to entry.author (set on parent-level fleet events) when
  // no child-session label is available. This ensures messages from agents
  // like "po" or "architect" show the correct persona instead of "router".
  const effectiveRole = agentRole || (entry.author && entry.author !== 'user' ? String(entry.author) : null)
  const roleColor = effectiveRole ? getAgentColor(effectiveRole) : getAgentColor('system')

  if (entry.type === 'user' || entry.type === 'model' || entry.type === 'thinking') {
    const isUser = entry.type === 'user'
    const isThinking = entry.type === 'thinking'
    const textPreview = entry.text && entry.text.length > 300 ? entry.text.slice(0, 300) + '...' : entry.text

    // Determine the role label and color for this entry
    let roleLabel: string, labelColor: { text: string; bg: string }
    if (isUser) {
      roleLabel = 'customer'
      labelColor = getAgentColor('system')
    } else if (isThinking) {
      roleLabel = effectiveRole ? `@${effectiveRole}` : 'thinking'
      labelColor = { text: '#9ca3af', bg: 'rgba(107, 114, 128, 0.1)' }
    } else {
      roleLabel = effectiveRole ? `@${effectiveRole}` : 'router'
      labelColor = effectiveRole ? roleColor : { text: '#9ca3af', bg: 'rgba(107, 114, 128, 0.1)' }
    }

    return (
      <div className="py-1">
        <div className="flex items-start gap-2 text-xs">
          <span className="text-[10px] font-mono flex-shrink-0 w-16 text-right" style={{ color: 'var(--text-muted)' }}>{time}</span>
          <span className="text-[10px] px-1.5 py-0.5 rounded flex-shrink-0 min-w-[60px] text-center font-medium" style={{ background: labelColor.bg, color: labelColor.text }}>
            {roleLabel}
          </span>
          {isThinking && (
            <span className="text-[10px] italic flex-shrink-0" style={{ color: '#9ca3af' }}>thinking</span>
          )}
          <div className="flex-1 min-w-0">
            {expanded ? (
              <div className="rounded p-2 text-xs" style={{ background: 'rgba(255,255,255,0.03)', color: 'var(--text-secondary)' }}>
                <ReactMarkdown remarkPlugins={[remarkGfm]} components={markdownComponents}>{entry.text || ''}</ReactMarkdown>
              </div>
            ) : (
              <button
                onClick={() => onToggle(index)}
                className="text-left w-full hover:underline cursor-pointer"
                style={{ color: 'var(--text-secondary)' }}
              >
                {textPreview}
              </button>
            )}
            {expanded && (
              <button onClick={() => onToggle(index)} className="text-[10px] text-primary hover:text-primary mt-1 cursor-pointer">
                Collapse
              </button>
            )}
          </div>
        </div>
      </div>
    )
  }

  if (entry.type === 'tool_call') {
    const argsStr = entry.args ? JSON.stringify(entry.args) : ''
    const argsPreview = argsStr.length > 100 ? argsStr.slice(0, 100) + '...' : argsStr
    const roleLabel = effectiveRole ? `@${effectiveRole}` : 'router'

    return (
      <div className="py-0.5">
        <div className="flex items-start gap-2 text-xs">
          <span className="text-[10px] font-mono flex-shrink-0 w-16 text-right" style={{ color: 'var(--text-muted)' }}>{time}</span>
          <span className="text-[10px] px-1.5 py-0.5 rounded flex-shrink-0 min-w-[60px] text-center font-medium" style={{ background: roleColor.bg, color: roleColor.text }}>
            {roleLabel}
          </span>
          <Wrench size={10} className="text-primary flex-shrink-0 mt-0.5" />
          <span className="font-medium" style={{ color: 'var(--brand)' }}>{entry.tool_name}</span>
          {expanded ? (
            <div className="flex-1 min-w-0">
              <pre className="text-[11px] font-mono p-2 rounded whitespace-pre-wrap break-words" style={{ background: 'rgba(0,0,0,0.3)', color: 'var(--text-muted)' }}>
                {JSON.stringify(entry.args, null, 2)}
              </pre>
              <button onClick={() => onToggle(index)} className="text-[10px] text-primary hover:text-primary mt-1 cursor-pointer">Collapse</button>
            </div>
          ) : (
            <button
              onClick={() => onToggle(index)}
              className="text-left flex-1 min-w-0 truncate hover:underline cursor-pointer"
              style={{ color: 'var(--text-muted)' }}
            >
              {argsPreview}
            </button>
          )}
        </div>
      </div>
    )
  }

  if (entry.type === 'tool_result') {
    const isError = entry.error
    const durationMs = entry.duration_ms || 0
    const durationStr = durationMs > 0 ? (durationMs < 1000 ? `${durationMs}ms` : `${(durationMs / 1000).toFixed(1)}s`) : ''
    const resultStr = entry.result ? JSON.stringify(entry.result) : ''
    const resultPreview = resultStr.length > 100 ? resultStr.slice(0, 100) + '...' : resultStr
    const roleLabel = effectiveRole ? `@${effectiveRole}` : 'router'

    return (
      <div className="py-0.5">
        <div className="flex items-start gap-2 text-xs">
          <span className="text-[10px] font-mono flex-shrink-0 w-16 text-right" style={{ color: 'var(--text-muted)' }}>{time}</span>
          <span className="text-[10px] px-1.5 py-0.5 rounded flex-shrink-0 min-w-[60px] text-center font-medium" style={{ background: roleColor.bg, color: roleColor.text }}>
            {roleLabel}
          </span>
          {isError ? (
            <X size={10} className="text-red-400 flex-shrink-0 mt-0.5" />
          ) : (
            <Check size={10} className="text-green-400 flex-shrink-0 mt-0.5" />
          )}
          <span className="font-medium" style={{ color: isError ? '#f87171' : '#4ade80' }}>
            {entry.tool_name || 'result'}
          </span>
          {durationStr && (
            <span className="text-[10px]" style={{ color: 'var(--text-muted)' }}>({durationStr})</span>
          )}
          {expanded ? (
            <div className="flex-1 min-w-0">
              <pre className="text-[11px] font-mono p-2 rounded whitespace-pre-wrap break-words" style={{ background: 'rgba(0,0,0,0.3)', color: isError ? '#fca5a5' : 'var(--text-muted)' }}>
                {isError ? entry.error : JSON.stringify(entry.result, null, 2)}
              </pre>
              <button onClick={() => onToggle(index)} className="text-[10px] text-primary hover:text-primary mt-1 cursor-pointer">Collapse</button>
            </div>
          ) : (
            <button
              onClick={() => onToggle(index)}
              className="text-left flex-1 min-w-0 truncate hover:underline cursor-pointer"
              style={{ color: isError ? '#fca5a5' : 'var(--text-muted)' }}
            >
              {isError ? entry.error : (resultPreview || 'OK')}
            </button>
          )}
        </div>
      </div>
    )
  }

  return null
}

// Single thread message row (conversation-level view for pairwise threads)

interface ThreadMessageRowProps {
  msg: FleetMessage
  index: number
  expanded: boolean
  onToggle: (index: number) => void
}

function ThreadMessageRow({ msg, index, expanded, onToggle }: ThreadMessageRowProps) {
  const time = msg.timestamp ? new Date(msg.timestamp).toLocaleTimeString() : ''
  const isCustomer = msg.sender === 'customer'
  const isSystem = msg.sender === 'system'
  const roleColor = isCustomer
    ? getAgentColor('system')
    : isSystem
      ? { text: '#9ca3af', bg: 'rgba(107, 114, 128, 0.1)' }
      : getAgentColor(msg.sender)
  const roleLabel = isCustomer ? 'customer' : isSystem ? 'system' : `@${msg.sender}`
  const textPreview = msg.text && msg.text.length > 400 ? msg.text.slice(0, 400) + '...' : msg.text

  // Check if message has metadata indicating it's intermediate
  const isIntermediate = msg.metadata?.intermediate === true

  return (
    <div className={`py-1.5 ${isIntermediate ? 'opacity-60' : ''}`}>
      <div className="flex items-start gap-2 text-xs">
        <span className="text-[10px] font-mono flex-shrink-0 w-16 text-right" style={{ color: 'var(--text-muted)' }}>{time}</span>
        <span className="text-[10px] px-1.5 py-0.5 rounded flex-shrink-0 min-w-[60px] text-center font-medium" style={{ background: roleColor.bg, color: roleColor.text }}>
          {roleLabel}
        </span>
        {isIntermediate && (
          <span className="text-[10px] italic flex-shrink-0" style={{ color: 'var(--text-muted)' }}>progress</span>
        )}
        <div className="flex-1 min-w-0">
          {expanded ? (
            <div className="rounded p-3 text-xs" style={{ background: 'rgba(255,255,255,0.03)', color: 'var(--text-secondary)' }}>
              <ReactMarkdown remarkPlugins={[remarkGfm]} components={markdownComponents}>{msg.text || ''}</ReactMarkdown>
              {msg.artifacts && Object.keys(msg.artifacts).length > 0 && (
                <div className="mt-2 pt-2" style={{ borderTop: '1px solid var(--border-color)' }}>
                  <span className="text-[10px] font-medium" style={{ color: 'var(--text-muted)' }}>Artifacts: </span>
                  {Object.entries(msg.artifacts).map(([name, path]) => (
                    <span key={name} className="text-[10px] ml-1" style={{ color: 'var(--brand)' }}>
                      {name} &rarr; <code>{String(path)}</code>
                    </span>
                  ))}
                </div>
              )}
              {msg.mentions && msg.mentions.length > 0 && (
                <div className="mt-1">
                  <span className="text-[10px]" style={{ color: 'var(--text-muted)' }}>
                    Mentions: {msg.mentions.map(m => `@${m}`).join(', ')}
                  </span>
                </div>
              )}
            </div>
          ) : (
            <button
              onClick={() => onToggle(index)}
              className="text-left w-full hover:underline cursor-pointer"
              style={{ color: 'var(--text-secondary)' }}
            >
              {textPreview}
            </button>
          )}
          {expanded && (
            <button onClick={() => onToggle(index)} className="text-[10px] text-primary hover:text-primary mt-1 cursor-pointer">
              Collapse
            </button>
          )}
        </div>
      </div>
    </div>
  )
}
