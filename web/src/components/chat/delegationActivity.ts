import type { SubTaskEvent } from './chatTypes'
import { isToolResultError, type ActivityEntry, type ToolActivityStep } from './toolActivity'

/** Pair by provider call identity, not by tool name (parallel calls can finish backwards). */
export function delegationActivity(events: SubTaskEvent[]): ActivityEntry[] {
  const entries: ActivityEntry[] = []
  const pending: { event: SubTaskEvent; step: ToolActivityStep }[] = []
  events.forEach((event, index) => {
    if (event.type === 'task_text' && event.text) {
      entries.push({ kind: 'message', index, message: { type: 'agent', content: event.text } })
    } else if (event.type === 'task_tool_call') {
      const step: ToolActivityStep = { toolName: event.tool_name || 'unknown', args: event.tool_args, callIndex: index, status: 'running' }
      pending.push({ event, step })
      entries.push({ kind: 'tool', index, step })
    } else if (event.type === 'task_tool_result') {
      const match = pending.findIndex(p => event.call_id
        ? p.event.call_id === event.call_id
        : !p.event.call_id && p.event.tool_name === event.tool_name)
      const step = match >= 0 ? pending.splice(match, 1)[0].step : { toolName: event.tool_name || 'unknown', callIndex: index, status: 'complete' as const }
      step.result = event.tool_result
      step.resultIndex = index
      step.status = isToolResultError(event.tool_result) ? 'error' : 'complete'
      if (match < 0) entries.push({ kind: 'tool', index, step })
    }
  })
  return entries
}

/** REST transcript sequence and SSE sequence are different clocks: never concatenate them. */
export function selectDelegationActivity(live: SubTaskEvent[], persisted: SubTaskEvent[] | undefined, running: boolean): SubTaskEvent[] {
  const isActivity = (e: SubTaskEvent) => ['task_text', 'task_tool_call', 'task_tool_result'].includes(e.type)
  if (running && live.some(isActivity)) return live.filter(isActivity)
  return (persisted ?? live).filter(isActivity)
}
