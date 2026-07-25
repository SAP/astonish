import { useState, useMemo } from 'react'
import { ChevronDown, Loader, Check, Wrench, Users, Globe, Code, GitFork } from 'lucide-react'
import ReactMarkdown from 'react-markdown'
import remarkGfm from 'remark-gfm'
import { markdownComponents } from './markdownComponents'
import type { FleetExecutionMessage, FleetEvent } from './chatTypes'

// Format a timestamp to HH:MM
function formatTime(ts?: number): string {
  if (!ts) return ''
  const d = new Date(ts)
  return d.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
}

// Pick an icon for a phase based on agent name or event content
function phaseIcon(phase: { name: string; agent: string; events: FleetEvent[] }) {
  const agent = phase.agent.toLowerCase()
  if (agent.includes('search') || agent.includes('web') || agent.includes('research')) {
    return <Globe size={12} />
  }
  if (agent.includes('code') || agent.includes('dev') || agent.includes('engineer')) {
    return <Code size={12} />
  }
  if (agent.includes('orchestrat') || phase.name === '_orchestrator') {
    return <GitFork size={12} />
  }
  return <Users size={12} />
}

// Connected vertical timeline for fleet execution.
// Perplexity-inspired: continuous vertical line, circular status dots, per-phase icons,
// timestamps on hover, click to expand details.
export default function FleetExecutionPanel({ data }: { data: FleetExecutionMessage }) {
  const [expandedPhases, setExpandedPhases] = useState<Record<string, boolean>>({})
  const [expandedContent, setExpandedContent] = useState<Set<string>>(new Set())

  interface Phase {
    name: string
    agent: string
    events: FleetEvent[]
    status: string
    startTimestamp?: number
    laneIndex?: number
  }

  const phases = useMemo(() => {
    const phaseMap: Record<string, Phase> = {}
    const phaseOrder: string[] = []
    for (const evt of data.events.filter(evt => !evt.type.startsWith('fleet_task_'))) {
      if (evt.type === 'fleet_start' || evt.type === 'fleet_complete') continue
      const key = evt.phase || '_orchestrator'
      if (!phaseMap[key]) {
        phaseMap[key] = { name: key, agent: evt.agent || '', events: [], status: 'pending', laneIndex: typeof evt.lane_index === 'number' ? evt.lane_index : undefined }
        phaseOrder.push(key)
      }
      if (typeof evt.lane_index === 'number') phaseMap[key].laneIndex = evt.lane_index
      phaseMap[key].events.push(evt)
      if (evt.type === 'phase_start' || evt.type === 'conversation_start') {
        phaseMap[key].status = 'running'
        if (!phaseMap[key].startTimestamp) phaseMap[key].startTimestamp = evt.timestamp
      }
      if (evt.type === 'phase_complete' || evt.type === 'conversation_complete') phaseMap[key].status = 'complete'
      if (evt.type === 'phase_failed' || evt.type === 'conversation_turn_failed') phaseMap[key].status = 'failed'
      if (evt.agent) phaseMap[key].agent = evt.agent
    }
    return phaseOrder.map(k => phaseMap[k])
  }, [data.events])

  const taskEvents = useMemo(() => data.events.filter(evt => evt.type.startsWith('fleet_task_')), [data.events])
  const hasLaneEvents = data.events.some(evt => typeof evt.lane_index === 'number' && evt.lane_index >= 0)
  const configuredParallelism = data.maxParallelAgents || 1
  const useMultiColumn = hasLaneEvents && configuredParallelism > 1
  const laneCount = Math.max(configuredParallelism, ...data.events.map(evt => typeof evt.lane_index === 'number' ? evt.lane_index + 1 : 0), 1)
  const coordinatorPhases = phases.filter(phase => phase.name === '_orchestrator' || phase.laneIndex === undefined || phase.laneIndex < 0)
  const lanePhases = Array.from({ length: laneCount }, (_, laneIndex) => phases.filter(phase => phase.laneIndex === laneIndex))
  const agentPhaseCount = phases.filter(p => p.name !== '_orchestrator').length
  const allDone = data.status !== 'running'

  const togglePhase = (name: string) => {
    setExpandedPhases(prev => ({ ...prev, [name]: !prev[name] }))
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

  const renderFleetToolCard = (evt: FleetEvent, eventKey: string) => {
    const isCall = evt.type === 'worker_tool_call' || evt.type === 'tool_call'
    const name = evt.detail || 'unknown'
    const cardData = isCall ? (evt.args || evt.text || null) : (evt.result || evt.text || null)

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

  const renderFleetText = (evt: FleetEvent, eventKey: string) => {
    const textContent = evt.text || evt.message || ''
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

  const renderConversationTurn = (evt: FleetEvent, eventKey: string) => {
    const isComplete = evt.type === 'conversation_turn_complete'
    const isFailed = evt.type === 'conversation_turn_failed'
    const role = evt.detail || 'agent'

    return (
      <div key={eventKey} className="my-1.5">
        <div
          className="flex items-center gap-2 px-3 py-1.5 rounded-lg text-xs"
          style={{
            background: isFailed ? 'rgba(239,68,68,0.1)' : 'var(--brand-muted)',
            border: `1px solid ${isFailed ? 'rgba(239,68,68,0.3)' : 'color-mix(in oklab, var(--brand) 20%, transparent)'}`,
          }}
        >
          {isComplete ? (
            <Check size={12} className="text-green-400" />
          ) : isFailed ? (
            <span className="text-red-400 text-xs font-bold">!</span>
          ) : (
            <Loader size={12} className="animate-spin text-primary" />
          )}
          <span className="text-primary font-medium">{evt.agent || 'agent'}</span>
          <span className="text-gray-400">({role})</span>
          {isComplete && evt.text && (
            <span className="text-gray-500 ml-2 truncate max-w-[300px]">{evt.text}</span>
          )}
        </div>
      </div>
    )
  }

  const renderPhaseEvent = (evt: FleetEvent, phaseIdx: number, evtIdx: number) => {
    const eventKey = `fleet-${phaseIdx}-${evtIdx}`
    if (evt.type === 'phase_start' || evt.type === 'phase_complete' || evt.type === 'phase_failed') return null
    if (evt.type === 'conversation_start' || evt.type === 'conversation_complete') return null
    if (evt.type === 'conversation_turn' || evt.type === 'conversation_turn_complete' || evt.type === 'conversation_turn_failed') {
      return renderConversationTurn(evt, eventKey)
    }
    if (evt.type === 'worker_tool_call' || evt.type === 'tool_call') {
      return renderFleetToolCard(evt, eventKey)
    }
    if (evt.type === 'worker_tool_result' || evt.type === 'tool_result') {
      return renderFleetToolCard(evt, eventKey)
    }
    if (evt.type === 'worker_text' || evt.type === 'text') {
      return renderFleetText(evt, eventKey)
    }
    return null
  }

  const renderTaskStrip = () => {
    if (taskEvents.length === 0) return null
    return (
      <div className="px-4 pb-3" style={{ borderTop: '1px solid var(--border-color)' }}>
        <div className="pt-3">
          <div className="text-xs font-semibold mb-2" style={{ color: 'var(--text-primary)' }}>Tasks</div>
          <div className="flex gap-2 overflow-x-auto pb-1">
            {taskEvents.map((evt, idx) => {
              const isDone = evt.type === 'fleet_task_completed'
              const isClaimed = evt.type === 'fleet_task_claimed'
              return (
                <div
                  key={`task-${idx}`}
                  className="min-w-[180px] rounded-lg p-2 text-xs"
                  style={{
                    background: isDone ? 'rgba(34,197,94,0.08)' : isClaimed ? 'rgba(6,182,212,0.08)' : 'rgba(255,255,255,0.04)',
                    border: `1px solid ${isDone ? 'rgba(34,197,94,0.25)' : isClaimed ? 'rgba(6,182,212,0.25)' : 'var(--border-color)'}`,
                  }}
                >
                  <div className="font-medium truncate" style={{ color: 'var(--text-primary)' }}>
                    {evt.title || evt.detail || evt.task_id || 'Task'}
                  </div>
                  <div className="mt-1" style={{ color: 'var(--text-muted)' }}>
                    {evt.type.replace('fleet_task_', '')}
                    {evt.claimed_by ? ` by @${evt.claimed_by}` : ''}
                  </div>
                  {evt.required_capabilities && evt.required_capabilities.length > 0 && (
                    <div className="mt-1 truncate" style={{ color: 'var(--text-muted)' }}>
                      {evt.required_capabilities.join(', ')}
                    </div>
                  )}
                </div>
              )
            })}
          </div>
        </div>
      </div>
    )
  }

  const renderLanePhase = (phase: Phase, phaseIdx: number) => {
    const isExpanded = expandedPhases[phase.name] === true || (phase.status === 'running' && expandedPhases[phase.name] !== false)
    return (
      <div key={phase.name} className="rounded-lg p-2" style={{ background: 'rgba(255,255,255,0.03)', border: '1px solid var(--border-color)' }}>
        <button
          onClick={() => togglePhase(phase.name)}
          className="w-full flex items-center gap-2 text-left cursor-pointer"
        >
          {statusDot(phase.status)}
          <span style={{ color: 'var(--text-muted)' }}>{phaseIcon(phase)}</span>
          <span className="text-xs font-medium flex-1 truncate" style={{ color: 'var(--text-primary)' }}>
            {phase.name}
            {phase.agent && <span className="font-normal ml-1.5" style={{ color: 'var(--text-muted)' }}>({phase.agent})</span>}
          </span>
          {phase.startTimestamp && <span className="text-[10px]" style={{ color: 'var(--text-muted)' }}>{formatTime(phase.startTimestamp)}</span>}
          <ChevronDown size={12} className={isExpanded ? '' : 'rotate-[-90deg]'} style={{ color: 'var(--text-muted)' }} />
        </button>
        {isExpanded && (
          <div className="mt-2">
            {phase.events.map((evt, evtIdx) => renderPhaseEvent(evt, phaseIdx, evtIdx))}
          </div>
        )}
      </div>
    )
  }

  if (useMultiColumn) {
    return (
      <div
        className="rounded-lg overflow-hidden text-sm transition-opacity duration-300"
        style={{
          border: '1px solid var(--border-color)',
          background: 'var(--bg-secondary)',
          opacity: allDone ? 0.85 : 1,
        }}
      >
        <div className="flex items-center gap-2.5 px-4 py-2.5">
          {data.status === 'running' && <Loader size={15} className="animate-spin shrink-0" style={{ color: 'var(--brand)' }} />}
          {data.status === 'complete' && <Check size={15} className="text-green-400 shrink-0" />}
          <Users size={15} className="shrink-0" style={{ color: 'var(--text-muted)' }} />
          <span className="font-semibold text-sm" style={{ color: 'var(--text-primary)' }}>Fleet Execution</span>
          <span className="text-xs ml-auto" style={{ color: 'var(--text-muted)' }}>
            {laneCount} lane{laneCount !== 1 ? 's' : ''} · {agentPhaseCount} phase{agentPhaseCount !== 1 ? 's' : ''}
          </span>
        </div>
        <div className="px-4 py-3 overflow-x-auto" style={{ borderTop: '1px solid var(--border-color)' }}>
          <div className="grid gap-3" style={{ gridTemplateColumns: `minmax(180px, 0.8fr) repeat(${laneCount}, minmax(220px, 1fr))` }}>
            <div>
              <div className="text-[11px] font-semibold uppercase tracking-wide mb-2" style={{ color: 'var(--text-muted)' }}>Coordinator</div>
              <div className="space-y-2">
                {coordinatorPhases.length === 0
                  ? <div className="text-xs rounded-lg p-3" style={{ color: 'var(--text-muted)', border: '1px dashed var(--border-color)' }}>No coordinator events</div>
                  : coordinatorPhases.map((phase, idx) => renderLanePhase(phase, idx))}
              </div>
            </div>
            {lanePhases.map((lane, laneIndex) => (
              <div key={laneIndex}>
                <div className="text-[11px] font-semibold uppercase tracking-wide mb-2" style={{ color: 'var(--text-muted)' }}>Lane {laneIndex + 1}</div>
                <div className="space-y-2">
                  {lane.length === 0
                    ? <div className="text-xs rounded-lg p-3" style={{ color: 'var(--text-muted)', border: '1px dashed var(--border-color)' }}>Idle</div>
                    : lane.map((phase, idx) => renderLanePhase(phase, laneIndex * 100 + idx))}
                </div>
              </div>
            ))}
          </div>
        </div>
        {renderTaskStrip()}
      </div>
    )
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
        {data.status === 'complete' && <Check size={15} className="text-green-400 shrink-0" />}
        <Users size={15} className="shrink-0" style={{ color: 'var(--text-muted)' }} />
        <span className="font-semibold text-sm" style={{ color: 'var(--text-primary)' }}>Fleet Execution</span>
        <span className="text-xs ml-auto" style={{ color: 'var(--text-muted)' }}>
          {agentPhaseCount} phase{agentPhaseCount !== 1 ? 's' : ''}
        </span>
      </div>

      {/* Timeline */}
      <div
        className="px-4 pb-3"
        style={{ borderTop: '1px solid var(--border-color)' }}
      >
        {phases.map((phase, phaseIdx) => {
          // Orchestrator events render inline (no timeline row)
          if (phase.name === '_orchestrator') {
            return (
              <div key={phase.name} className="py-1">
                {phase.events.map((evt, evtIdx) => renderPhaseEvent(evt, phaseIdx, evtIdx))}
              </div>
            )
          }

          const isExpanded = expandedPhases[phase.name] === true || (phase.status === 'running' && expandedPhases[phase.name] !== false)
          const isLast = phaseIdx === phases.length - 1

          return (
            <div key={phase.name} className="relative group/phase">
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

              {/* Phase row */}
              <button
                onClick={() => togglePhase(phase.name)}
                className="w-full flex items-center gap-3 py-2 text-left cursor-pointer relative z-10"
              >
                {/* Status dot */}
                {statusDot(phase.status)}

                {/* Icon */}
                <span style={{ color: 'var(--text-muted)' }}>{phaseIcon(phase)}</span>

                {/* Phase name */}
                <span
                  className="text-xs font-medium flex-1 truncate"
                  style={{
                    color: phase.status === 'running'
                      ? 'var(--text-primary)'
                      : phase.status === 'complete'
                        ? 'var(--text-secondary)'
                        : phase.status === 'failed'
                          ? '#ef4444'
                          : 'var(--text-muted)',
                  }}
                >
                  {phase.name}
                  {phase.agent && (
                    <span className="font-normal ml-1.5" style={{ color: 'var(--text-muted)' }}>
                      ({phase.agent})
                    </span>
                  )}
                </span>

                {/* Right side: timestamp on hover */}
                <span className="flex items-center gap-2 shrink-0">
                  {phase.status === 'running' && (
                    <span className="text-[11px]" style={{ color: 'var(--brand)' }}>running</span>
                  )}
                  {phase.startTimestamp && (
                    <span
                      className="text-[11px] opacity-0 group-hover/phase:opacity-100 transition-opacity"
                      style={{ color: 'var(--text-muted)' }}
                    >
                      {formatTime(phase.startTimestamp)}
                    </span>
                  )}
                  {isExpanded ? (
                    <ChevronDown size={12} style={{ color: 'var(--text-muted)' }} />
                  ) : (
                    <ChevronDown size={12} className="rotate-[-90deg]" style={{ color: 'var(--text-muted)' }} />
                  )}
                </span>
              </button>

              {/* Expanded phase details */}
              {isExpanded && (
                <div className="ml-[28px] pb-2 relative z-10">
                  {phase.events.map((evt, evtIdx) => renderPhaseEvent(evt, phaseIdx, evtIdx))}
                </div>
              )}
            </div>
          )
        })}
      </div>
    </div>
  )
}
