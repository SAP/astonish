import { isBotOperation } from './botOperationResult'
import { teamFetch } from './teamContext'
import { botURL, BotAPIError, type BotScope } from './bots'
import type { SSEEventCallback } from './studioChat'

interface RuntimeEnvelope { type: string; payload: Record<string, unknown>; turn_id?: string; sequence?: number }
export interface BotTranscript {
  ID?: string; InvocationID?: string; Author?: string
  Actions?: { StateDelta?: { bot_operation?: import('./botOperationResult').BotOperationView; bot_runtime_event?: RuntimeEnvelope; bot_run_state?: import('./botRunState').BotRunState; _astonish_turn_context?: boolean } }
  Content?: { role?: string; parts?: { text?: string; thought?: boolean; functionCall?: { name: string; args: Record<string, unknown> }; functionResponse?: { name: string; response: Record<string, unknown> } }[] }
}
type ReviewKind = 'proposal' | 'approval'
type ReviewKey = `${ReviewKind}:${string}`
// A session announces the Chat wire projection before its ADK rows arrive.
// Control/reference metadata is not evidence that a turn has a wire projection.
const conversationWireTypes = new Set(['session', 'text', 'tool_call', 'tool_result'])
export interface BotTranscriptMapper { turns: Map<string, number>; runtimeTurns: Set<string>; reviews: Set<ReviewKey> }
export const createBotTranscriptMapper = (): BotTranscriptMapper => ({ turns: new Map(), runtimeTurns: new Set(), reviews: new Set() })
export function dispatchBotTranscript(event: BotTranscript, emit: SSEEventCallback, mapper = createBotTranscriptMapper(), eventId = event.ID || '') {
  const raw = event as BotTranscript & { id?: string; invocationId?: string; author?: string; actions?: { stateDelta?: { bot_operation?: import('./botOperationResult').BotOperationView; bot_runtime_event?: RuntimeEnvelope; bot_run_state?: import('./botRunState').BotRunState; _astonish_turn_context?: boolean } }; content?: BotTranscript['Content'] }
  event = { ...event, ID: event.ID || raw.id, InvocationID: event.InvocationID || raw.invocationId, Author: event.Author || raw.author, Content: event.Content || raw.content, Actions: event.Actions || { StateDelta: raw.actions?.stateDelta } }
  // Internal ADK user-role context is model history, not human input. Require
  // boolean provenance; genuine quotations and malformed/false markers stay visible.
  if (event.Actions?.StateDelta?._astonish_turn_context === true) return
  const operation = event.Actions?.StateDelta?.bot_operation
  if (operation) { if (isBotOperation(operation)) emit('bot_operation', operation as unknown as Record<string, unknown>); return }
  const state = event.Actions?.StateDelta?.bot_run_state
  if (state) { emit('bot_run_state', state as unknown as Record<string, unknown>); return }
  eventId = eventId || event.ID || ''
  const runtime = event.Actions?.StateDelta?.bot_runtime_event
  const turn = runtime?.turn_id || event.InvocationID || ''
  const review = (kind: ReviewKind, reviewId: unknown) => {
    if (typeof reviewId !== 'string' || !reviewId.trim()) return
    const key: ReviewKey = `${kind}:${reviewId}`
    if (mapper.reviews.has(key)) return
    mapper.reviews.add(key)
    // First observed reference owns provenance, regardless of replay spelling.
    emit('bot_review', { kind, reviewId, eventId, invocationId: turn })
  }
  const send: SSEEventCallback = (type, payload) => {
    if (type === 'bot_review') {
      if (payload.kind === 'proposal' || payload.kind === 'approval') review(payload.kind, payload.review_id ?? payload.reviewId)
      return
    }
    const result = payload.result as Record<string, unknown> | undefined
    review('proposal', payload.proposal_id || result?.proposal_id)
    review('approval', payload.approval_id || result?.approval_id)
    if (['approval', 'plan_approval', 'chat_question', 'auto_approved', 'session'].includes(type)) return
    emit(type, { ...payload, invocationId: payload.invocationId || turn, eventId })
  }
  if (runtime) {
    if (!runtime.type || !runtime.payload || typeof runtime.payload !== 'object') throw new Error('Malformed Bot runtime envelope')
    if (turn && conversationWireTypes.has(runtime.type)) mapper.runtimeTurns.add(turn)
    if (turn && runtime.sequence) {
      if (runtime.sequence <= (mapper.turns.get(turn) || 0)) return
      mapper.turns.set(turn, runtime.sequence)
    }
    send(runtime.type, runtime.payload)
    return
  }
  const user = event.Author === 'user' || event.Content?.role === 'user' && !event.Content.parts?.some(p => p.functionResponse)
  if (!user && mapper.runtimeTurns.has(turn)) return
  for (const part of event.Content?.parts || []) {
    if (part.thought) continue
    if (part.text) {
      let text = part.text
      if (user) {
        try {
          const answer = JSON.parse(text)
          if (answer.type === 'bot_owner_answer') {
            text = typeof answer.display?.answer === 'string' ? answer.display.answer : typeof answer.response?.answer === 'string' ? answer.response.answer : 'Answer saved'
          }
        } catch { /* ordinary user text */ }
      }
      send(user ? 'user_message' : 'text', { text })
    }
    if (part.functionCall) send('tool_call', { name: part.functionCall.name, args: part.functionCall.args })
    if (part.functionResponse) send('tool_result', { name: part.functionResponse.name, result: part.functionResponse.response })
  }
  if (event.Content?.parts?.some(p => p.text)) send('turn_complete', {})
}
export interface BotStreamOptions {
  botId: string; sessionId: string; scope: BotScope
  onEvent: SSEEventCallback; onError?: (error: Error) => void; onDone?: () => void
  onState?: (state: 'loading' | 'live' | 'reconnecting' | 'error', cursor: string) => void
}
// One cursor per mounted thread. Reconnect resumes after the last applied row;
// unmount aborts both the read and retry timer. No runner endpoint is used.
export function connectBotEvents(options: BotStreamOptions): AbortController {
  const controller = new AbortController()
  const mapper = createBotTranscriptMapper()
  let cursor = ''
  let retry = 0
  let timer: ReturnType<typeof setTimeout> | undefined
  const run = async () => {
    options.onState?.(cursor ? 'reconnecting' : 'loading', cursor)
    let reader: ReadableStreamDefaultReader<Uint8Array> | undefined
    try {
      const response = await teamFetch(botURL(`/${encodeURIComponent(options.botId)}/sessions/${encodeURIComponent(options.sessionId)}/events`, options.scope), {
        signal: controller.signal, headers: cursor ? { 'Last-Event-ID': cursor } : {},
      })
      if (!response.ok) throw new BotAPIError(response.status, await response.text())
      if (!response.body) throw new Error('Bot transcript stream unavailable')
      reader = response.body.getReader()
      const activeReader = reader
      controller.signal.addEventListener('abort', () => { void activeReader.cancel().catch(() => {}) }, { once: true })
      const decoder = new TextDecoder()
      let buffer = ''
      while (!controller.signal.aborted) {
        const { done, value } = await reader.read()
        if (done) throw new Error('Bot transcript connection closed')
        buffer += decoder.decode(value, { stream: true })
        // Normalize complete lines, including CRLF split between chunks.
        let match: RegExpExecArray | null
        while ((match = /\r?\n\r?\n/.exec(buffer))) {
          const frame = buffer.slice(0, match.index)
          buffer = buffer.slice(match.index + match[0].length)
          const lines = frame.split(/\r?\n/)
          const field = (name: string) => lines.filter(l => l.startsWith(`${name}:`)).map(l => l.slice(name.length + 1).replace(/^ /, '')).join('\n')
          const type = field('event'), id = field('id'), data = field('data')
          if (type === 'replay_complete') { cursor = id || cursor; retry = 0; options.onState?.('live', cursor); continue }
          if (type !== 'transcript' || !id || id === cursor) continue
          // Malformed durable data fails closed: never advance past an unapplied row.
          dispatchBotTranscript(JSON.parse(data) as BotTranscript, options.onEvent, mapper, id)
          cursor = id
          retry = 0
          options.onState?.('live', cursor)
        }
      }
    } catch (error) {
      if (controller.signal.aborted) return
      const err = error instanceof Error ? error : new Error(String(error))
      if (err instanceof BotAPIError && [400, 401, 403, 404].includes(err.status) || retry >= 3) {
        options.onState?.('error', cursor); options.onError?.(err); return
      }
      options.onState?.('reconnecting', cursor)
      timer = setTimeout(() => void run(), Math.min(1000 * 2 ** retry++, 8000))
    } finally {
      if (reader) { await reader.cancel().catch(() => {}); reader.releaseLock() }
    }
  }
  controller.signal.addEventListener('abort', () => clearTimeout(timer), { once: true })
  void run()
  return controller
}
