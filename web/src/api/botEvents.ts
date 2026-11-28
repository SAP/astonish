import { teamFetch } from './teamContext'
import { botURL, BotAPIError, type BotScope } from './bots'
import { readSSEStream, type SSEEventCallback } from './studioChat'

export interface BotStreamOptions {
  botId: string; sessionId: string; scope: BotScope
  onEvent: SSEEventCallback; onError?: (error: Error) => void; onDone?: () => void
  onReplayComplete?: () => void
  onState?: (state: 'loading' | 'live' | 'reconnecting' | 'error', cursor: string) => void
}

const control = { cursor: 'cursor', replayComplete: 'replay_complete', done: 'done' } as const
const isRecord = (value: unknown): value is Record<string, unknown> => value !== null && typeof value === 'object' && !Array.isArray(value)

function cursorPosition(value: unknown, sessionId: string): bigint {
  const prefix = `${sessionId}:`
  if (typeof value !== 'string' || !value.startsWith(prefix) || !/^(0|[1-9]\d*)$/.test(value.slice(prefix.length))) {
    throw new Error('Invalid Bot event cursor')
  }
  const position = BigInt(value.slice(prefix.length))
  if (position > BigInt('9223372036854775807')) throw new Error('Invalid Bot event cursor')
  return position
}

function validateProjection(value: unknown, type: string, id: string, sessionId: string): string | undefined {
  if (!isRecord(value) || value.session_id !== sessionId || value.cursor !== id || typeof value.run_id !== 'string' || !value.run_id.trim()
    || (value.turn_id !== undefined && typeof value.turn_id !== 'string')
    || (value.sequence !== undefined && (!Number.isSafeInteger(value.sequence) || Number(value.sequence) < 0))) {
    throw new Error('Malformed Bot event projection')
  }
  if (type === control.cursor) return undefined
  const eventId = value.event_id
  const prefix = `${sessionId}/`
  if (typeof eventId !== 'string' || !eventId.startsWith(prefix)
    || eventId.lastIndexOf('/') <= prefix.length || !/^(0|[1-9]\d*)$/.test(eventId.slice(eventId.lastIndexOf('/') + 1))) {
    throw new Error('Invalid Bot presentation identity')
  }
  return eventId
}

// The backend owns presentation. This transport only validates scope/provenance,
// commits row cursors, and suppresses replay by stable presentation identity.
// A multi-part row shares its PREVIOUS cursor until the last part: cursor equality
// must never suppress a new event, nor may a duplicate prevent a cursor commit.
export function connectBotEvents(options: BotStreamOptions): AbortController {
  const controller = new AbortController()
  const seen = new Set<string>()
  let cursor = ''
  let position = BigInt(0)
  let retry = 0
  let timer: ReturnType<typeof setTimeout> | undefined
  const run = async () => {
    if (controller.signal.aborted) return
    options.onState?.(cursor ? 'reconnecting' : 'loading', cursor)
    try {
      const response = await teamFetch(botURL(`/${encodeURIComponent(options.botId)}/sessions/${encodeURIComponent(options.sessionId)}/events`, options.scope), {
        signal: controller.signal, headers: cursor ? { 'Last-Event-ID': cursor } : {},
      })
      if (!response.ok) throw new BotAPIError(response.status, await response.text())
      if (!response.body) throw new Error('Bot event stream unavailable')
      await readSSEStream(response.body, controller.signal, ({ event: type, id, data }) => {
        const payload: unknown = JSON.parse(data)
        if (!isRecord(payload)) throw new Error('Malformed Bot event payload')
        const next = cursorPosition(id, options.sessionId)
        if (next < position) throw new Error('Regressive Bot event cursor')
        // Finite persisted-stream completion is a transport control, not a Chat
        // turn's durable done event. Catch-up alone never completes the stream.
        const finiteDone = type === control.done && payload._projection === undefined && payload.persisted === true
        const replayComplete = type === control.replayComplete
        let eventId: string | undefined
        if (finiteDone || replayComplete) {
          if (payload.persisted !== true || payload._projection !== undefined) throw new Error('Malformed Bot stream control')
        } else {
          if (type === 'transcript' || type === 'message') throw new Error('Expected canonical Chat event')
          eventId = validateProjection(payload._projection, type, id, options.sessionId)
          if (eventId && !seen.has(eventId)) {
            const { _projection, ...chatPayload } = payload
            void _projection
            options.onEvent(type, chatPayload)
            seen.add(eventId)
          }
        }
        // Commit only after validation and successful application, including for
        // duplicate final parts and hidden rows. A failed frame aborts this read.
        cursor = id
        position = next
        retry = 0
        options.onState?.('live', cursor)
        if (replayComplete) options.onReplayComplete?.()
        if (finiteDone) { controller.abort(); options.onDone?.() }
      })
      if (!controller.signal.aborted) throw new Error('Bot event connection closed')
    } catch (error) {
      if (controller.signal.aborted) return
      const err = error instanceof Error ? error : new Error(String(error))
      if (err instanceof BotAPIError && [400, 401, 403, 404, 410].includes(err.status) || retry >= 3) {
        options.onState?.('error', cursor); options.onError?.(err); return
      }
      options.onState?.('reconnecting', cursor)
      timer = setTimeout(() => void run(), Math.min(1000 * 2 ** retry++, 8000))
    }
  }
  controller.signal.addEventListener('abort', () => clearTimeout(timer), { once: true })
  void run()
  return controller
}
