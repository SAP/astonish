import { afterEach, beforeEach, expect, it, vi } from 'vitest'
import { connectBotEvents, type BotStreamOptions } from '../botEvents'
import { teamFetch } from '../teamContext'

vi.mock('../teamContext', () => ({ teamFetch: vi.fn() }))

const controllers: AbortController[] = []
beforeEach(() => { vi.useFakeTimers(); vi.mocked(teamFetch).mockReset() })
afterEach(async () => {
  controllers.splice(0).forEach(controller => controller.abort())
  await vi.advanceTimersByTimeAsync(0)
  vi.useRealTimers()
})
const encoder = new TextEncoder()
const metadata = (cursor = 'home:1', eventId = 'home/row/0') => ({
  event_id: eventId, session_id: 'home', run_id: 'run', turn_id: 'turn', sequence: 1, cursor,
})
const frame = (event: string, payload: unknown, id = 'home:1', newline = '\n') =>
  [`id: ${id}`, `event: ${event}`, `data: ${JSON.stringify(payload)}`, '', ''].join(newline)
const canonical = (event: string, payload: Record<string, unknown> = {}, cursor = 'home:1', eventId = 'home/row/0') =>
  frame(event, { ...payload, _projection: metadata(cursor, eventId) }, cursor)
function source(initial = '', byteSplit = false) {
  let writer!: ReadableStreamDefaultController<Uint8Array>
  const cancel = vi.fn()
  const body = new ReadableStream<Uint8Array>({ start(c) { writer = c }, cancel })
  const push = (text: string) => {
    const bytes = encoder.encode(text)
    if (byteSplit) { for (const byte of bytes) writer.enqueue(new Uint8Array([byte])) }
    else writer.enqueue(bytes)
  }
  if (initial) push(initial)
  return { body, push, cancel, close: () => writer.close() }
}
function connect(extra: Partial<BotStreamOptions> = {}) {
  const callbacks = { onEvent: vi.fn(), onError: vi.fn(), onDone: vi.fn(), onReplayComplete: vi.fn(), onState: vi.fn() }
  const controller = connectBotEvents({ botId: 'bot', sessionId: 'home', scope: 'personal', ...callbacks, ...extra })
  controllers.push(controller)
  return { ...callbacks, controller }
}
const flush = () => vi.advanceTimersByTimeAsync(0)

// Format 3 remains private SQL storage, never a new public SSE envelope.
it('preserves database conversation projection through reconnect and reload without optimistic acknowledgement', async () => {
  const question = canonical('bot_review', { reviewId: 'approval', question: { questionId: 'q', kind: 'text', prompt: 'Which markets?' } }, 'home:1', 'home/question/0')
  const answer = canonical('user_message', { text: 'U.S. markets and major indexes', eventId: 'answer', invocationId: 'turn' }, 'home:2', 'home/answer/0')
  const ack = canonical('text', { text: 'I will research U.S. markets.', invocationId: 'turn' }, 'home:2', 'home/ack/0')
  const final = canonical('text', { text: 'Major indexes rose.', invocationId: 'turn' }, 'home:4', 'home/final/0')
  const done = canonical('turn_complete', { invocationId: 'turn' }, 'home:5', 'home/final/1')
  const first = source(question + answer), second = source(ack + final + done)
  vi.mocked(teamFetch).mockResolvedValueOnce(new Response(first.body)).mockResolvedValueOnce(new Response(second.body))
  const live = connect()
  await flush()
  expect(live.onEvent.mock.calls.map(([type]) => type)).toEqual(['bot_review', 'user_message'])
  expect(live.onDone).not.toHaveBeenCalled()
  first.push(ack)
  await flush()
  first.close()
  await vi.advanceTimersByTimeAsync(1100)
  expect(vi.mocked(teamFetch).mock.calls[1][1]?.headers).toEqual({ 'Last-Event-ID': 'home:2' })
  expect(live.onEvent.mock.calls.map(([type]) => type)).toEqual(['bot_review', 'user_message', 'text', 'text', 'turn_complete'])
  live.controller.abort()
  await flush()
  vi.mocked(teamFetch).mockResolvedValueOnce(new Response(source(question + answer + ack + final + done).body))
  const reload = connect()
  await flush()
  expect(reload.onEvent.mock.calls).toEqual(live.onEvent.mock.calls)
  expect(vi.mocked(teamFetch).mock.calls[2][1]?.headers).toEqual({})
})

it('forwards canonical Chat payloads without a Bot presentation mapper', async () => {
  const events: [string, Record<string, unknown>][] = [
    ['user_message', { text: 'Readable owner answer', invocationId: 'turn', eventId: 'answer' }],
    ['bot_review', { kind: 'approval', reviewId: 'review', invocationId: 'turn', eventId: 'question', question: { questionId: 'q', kind: 'text', prompt: 'Which topic?' } }],
    ['tool_call', { name: 'read_file', args: { path: 'report.md' }, id: 'call' }],
    ['tool_result', { name: 'read_file', result: { content: 'report' }, id: 'call' }],
    ['text', { text: 'report' }], ['turn_complete', {}],
    ['bot_operation', { operation_id: 'op', status: 'pending_approval' }],
    ['bot_run_state', { run_id: 'r', status: 'running' }],
    ['bot_file_publication', { event_id: 'publication', artifacts: [] }],
    ['report_marker', { path: '/report.md' }], ['error', { error: 'Tool failed' }],
  ]
  const stream = source(events.map(([type, data], i) => canonical(type, data, `home:${i + 1}`, `home/row-${i}/0`)).join(''))
  vi.mocked(teamFetch).mockResolvedValueOnce(new Response(stream.body))
  const result = connect()
  await flush()
  expect(result.onEvent.mock.calls).toEqual(events)
  expect(result.onDone).not.toHaveBeenCalled()
})

it('shares byte-split UTF8, CRLF and multiline framing; malformed JSON never advances', async () => {
  const data = { text: '雪🌍', _projection: metadata() }
  const encoded = JSON.stringify(data, null, 2).split('\n').map(line => `data: ${line}`).join('\r\n')
  const first = source(`: heartbeat\r\nid: home:1\r\nevent: user_message\r\n${encoded}\r\n\r\nid: home:2\r\nevent: text\r\ndata: {invalid\r\n\r\n`, true)
  const second = source()
  vi.mocked(teamFetch).mockResolvedValueOnce(new Response(first.body)).mockResolvedValueOnce(new Response(second.body))
  const result = connect()
  await vi.advanceTimersByTimeAsync(1100)
  expect(result.onEvent.mock.calls).toEqual([['user_message', { text: '雪🌍' }]])
  expect(result.onState).not.toHaveBeenCalledWith('live', 'home:2')
  expect(vi.mocked(teamFetch).mock.calls[1][1]?.headers).toEqual({ 'Last-Event-ID': 'home:1' })
  expect(first.cancel).toHaveBeenCalledTimes(1)
  expect(first.body.locked).toBe(false)
})

it('replays an interrupted row by stable part identity, not shared cursor or equal text', async () => {
  const firstPart = canonical('text', { text: 'again' }, 'home:0', 'home/row/0')
  const first = source(firstPart)
  first.close()
  const second = source(firstPart + canonical('text', { text: 'again' }, 'home:0', 'home/row/1') + canonical('turn_complete', {}, 'home:1', 'home/row/2'))
  vi.mocked(teamFetch).mockResolvedValueOnce(new Response(first.body)).mockResolvedValueOnce(new Response(second.body))
  const result = connect()
  await vi.advanceTimersByTimeAsync(1100)
  expect(vi.mocked(teamFetch).mock.calls[1][1]?.headers).toEqual({ 'Last-Event-ID': 'home:0' })
  expect(result.onEvent.mock.calls).toEqual([['text', { text: 'again' }], ['text', { text: 'again' }], ['turn_complete', {}]])
  expect(result.onState).toHaveBeenLastCalledWith('live', 'home:1')
})

it('commits a duplicate final part while suppressing its already applied identity', async () => {
  const first = source(canonical('text', { text: 'once' }, 'home:0'))
  first.close()
  const second = source(canonical('text', { text: 'once' }, 'home:2'))
  second.close()
  const third = source()
  vi.mocked(teamFetch).mockResolvedValueOnce(new Response(first.body)).mockResolvedValueOnce(new Response(second.body)).mockResolvedValueOnce(new Response(third.body))
  const result = connect()
  await vi.advanceTimersByTimeAsync(2100)
  expect(result.onEvent.mock.calls).toEqual([['text', { text: 'once' }]])
  expect(vi.mocked(teamFetch).mock.calls[2][1]?.headers).toEqual({ 'Last-Event-ID': 'home:2' })
})

it('hidden cursor controls update only the cursor; optional zero provenance is allowed', async () => {
  const stream = source(frame('cursor', { _projection: { session_id: 'home', run_id: '00000000-0000-0000-0000-000000000000', cursor: 'home:2' } }, 'home:2'))
  vi.mocked(teamFetch).mockResolvedValueOnce(new Response(stream.body))
  const result = connect()
  await flush()
  expect(result.onState).toHaveBeenLastCalledWith('live', 'home:2')
  expect(result.onEvent).not.toHaveBeenCalled()
  expect(result.onDone).not.toHaveBeenCalled()
  expect(result.onReplayComplete).not.toHaveBeenCalled()
})

it('separates chunked catch-up, durable turn done and finite persisted completion', async () => {
  const stream = source(canonical('done', { sessionId: 'home', invocationId: 'turn' }))
  vi.mocked(teamFetch).mockResolvedValueOnce(new Response(stream.body))
  const result = connect()
  await flush()
  expect(result.onEvent.mock.calls).toEqual([['done', { sessionId: 'home', invocationId: 'turn' }]])
  expect(result.onDone).not.toHaveBeenCalled()
  stream.push('id: home:1\nevent: replay_com')
  await flush()
  expect(result.onReplayComplete).not.toHaveBeenCalled()
  stream.push('plete\ndata: {"persisted":true}\n\n')
  await flush()
  expect(result.onReplayComplete).toHaveBeenCalledTimes(1)
  expect(result.onDone).not.toHaveBeenCalled()
  stream.push(frame('done', { persisted: true }))
  await flush()
  expect(result.onDone).toHaveBeenCalledTimes(1)
  expect(result.onEvent).toHaveBeenCalledTimes(1)
  expect(stream.cancel).toHaveBeenCalled()
  await vi.advanceTimersByTimeAsync(20000)
  expect(teamFetch).toHaveBeenCalledTimes(1)
})

it.each([
  ['non-object', null], ['array', []], ['missing projection', { text: 'bad' }],
  ...Object.entries({ session_id: 'elsewhere', cursor: 'home:3', event_id: '', run_id: 42, turn_id: [], sequence: -1 })
    .map(([key, value]): [string, unknown] => [key, { text: 'bad', _projection: { ...metadata('home:2'), [key]: value } }]),
  ['fractional sequence', { _projection: { ...metadata('home:2'), sequence: 1.5 } }],
  ['foreign identity', { _projection: metadata('home:2', 'elsewhere/row/0') }],
  ['identity missing stored row', { _projection: metadata('home:2', 'home//0') }],
])('does not commit malformed durable data (%s) or any subsequent frame', async (_name, invalid) => {
  const first = source(canonical('text', { text: 'valid' }) + frame('text', invalid, 'home:2') + canonical('text', { text: 'skipped' }, 'home:3', 'home/later/0'))
  vi.mocked(teamFetch).mockResolvedValueOnce(new Response(first.body)).mockResolvedValueOnce(new Response(source().body))
  const result = connect()
  await vi.advanceTimersByTimeAsync(1100)
  expect(result.onEvent.mock.calls).toEqual([['text', { text: 'valid' }]])
  expect(vi.mocked(teamFetch).mock.calls[1][1]?.headers).toEqual({ 'Last-Event-ID': 'home:1' })
})

it.each(['other:2', 'home:-1', 'home:1.2', 'home:02', 'home:9223372036854775808', 'home:0', ''])('rejects invalid or regressive cursor %s before updating dedup state', async cursor => {
  const first = source(canonical('text', { text: 'valid' }) + canonical('text', { text: 'bad' }, cursor, 'home/bad/0'))
  vi.mocked(teamFetch).mockResolvedValueOnce(new Response(first.body)).mockResolvedValueOnce(new Response(source().body))
  const result = connect()
  await vi.advanceTimersByTimeAsync(1100)
  expect(result.onEvent.mock.calls).toEqual([['text', { text: 'valid' }]])
  expect(vi.mocked(teamFetch).mock.calls[1][1]?.headers).toEqual({ 'Last-Event-ID': 'home:1' })
})

it.each([
  ['replay_complete', { persisted: false }, 'home:2'],
  ['replay_complete', { persisted: true }, 'foreign:2'],
  ['cursor', { _projection: { ...metadata('home:2'), session_id: 'foreign' } }, 'home:2'],
  ['text', { text: 'duplicate', _projection: { ...metadata('home:2'), sequence: 'invalid' } }, 'home:2'],
] as [string, unknown, string][])('validates %s controls and duplicates before cursor advancement', async (type, payload, id) => {
  const first = source(canonical('text', { text: 'valid' }) + frame(type, payload, id))
  vi.mocked(teamFetch).mockResolvedValueOnce(new Response(first.body)).mockResolvedValueOnce(new Response(source().body))
  const result = connect()
  await vi.advanceTimersByTimeAsync(1100)
  expect(result.onEvent.mock.calls).toEqual([['text', { text: 'valid' }]])
  expect(result.onReplayComplete).not.toHaveBeenCalled()
  expect(vi.mocked(teamFetch).mock.calls[1][1]?.headers).toEqual({ 'Last-Event-ID': 'home:1' })
})

it('retains exact int64 cursor values beyond JavaScript safe integers', async () => {
  const cursor = 'home:9223372036854775807'
  const first = source(canonical('text', { text: 'valid' }, cursor))
  first.close()
  vi.mocked(teamFetch).mockResolvedValueOnce(new Response(first.body)).mockResolvedValueOnce(new Response(source().body))
  connect()
  await vi.advanceTimersByTimeAsync(1100)
  expect(vi.mocked(teamFetch).mock.calls[1][1]?.headers).toEqual({ 'Last-Event-ID': cursor })
})

it('does not mark a failed application as seen or advance past it', async () => {
  const text = canonical('text', { text: 'retry me' })
  const first = source(text), second = source(text)
  vi.mocked(teamFetch).mockResolvedValueOnce(new Response(first.body)).mockResolvedValueOnce(new Response(second.body))
  const onEvent = vi.fn().mockImplementationOnce(() => { throw new Error('apply failed') })
  connect({ onEvent })
  await vi.advanceTimersByTimeAsync(1100)
  expect(vi.mocked(teamFetch).mock.calls[1][1]?.headers).toEqual({})
  expect(onEvent).toHaveBeenCalledTimes(2)
})

it('rejects legacy transcript rows instead of providing a compatibility mapper', async () => {
  vi.mocked(teamFetch).mockResolvedValue(new Response(source(frame('transcript', { Content: { parts: [{ text: 'legacy' }] } })).body))
  const result = connect()
  await flush()
  expect(result.onEvent).not.toHaveBeenCalled()
  expect(result.onState).toHaveBeenLastCalledWith('reconnecting', '')
})

it('cancels pending reads, drops unfinished frames and never treats cancellation as done', async () => {
  const stream = source(canonical('text', { text: 'incomplete' }).slice(0, -2))
  vi.mocked(teamFetch).mockResolvedValueOnce(new Response(stream.body))
  const result = connect()
  await flush()
  result.controller.abort()
  await flush()
  expect(stream.cancel).toHaveBeenCalledTimes(1)
  expect(stream.body.locked).toBe(false)
  expect(result.onEvent).not.toHaveBeenCalled()
  expect(result.onDone).not.toHaveBeenCalled()
  await vi.advanceTimersByTimeAsync(20000)
  expect(teamFetch).toHaveBeenCalledTimes(1)
})

it('cancels a scheduled reconnect without any resubmission', async () => {
  vi.mocked(teamFetch).mockRejectedValueOnce(new Error('network failed'))
  const result = connect()
  await flush()
  result.controller.abort()
  await vi.advanceTimersByTimeAsync(20000)
  expect(teamFetch).toHaveBeenCalledTimes(1)
  expect(result.onDone).not.toHaveBeenCalled()
})

it.each([400, 401, 403, 404, 410])('uses scoped teamFetch and stops after terminal HTTP %s', async status => {
  vi.mocked(teamFetch).mockResolvedValueOnce(new Response('denied', { status }))
  const result = connect({ botId: 'bot/id', sessionId: 'session/id', scope: 'team' })
  await vi.advanceTimersByTimeAsync(20000)
  expect(teamFetch).toHaveBeenCalledTimes(1)
  expect(vi.mocked(teamFetch).mock.calls[0][0]).toContain('/bot%2Fid/sessions/session%2Fid/events?scope=team')
  expect(result.onError).toHaveBeenCalledWith(expect.objectContaining({ status }))
  expect(result.onState).toHaveBeenLastCalledWith('error', '')
})

it('bounds retries for unavailable connections', async () => {
  vi.mocked(teamFetch).mockRejectedValue(new Error('offline'))
  const result = connect()
  await vi.advanceTimersByTimeAsync(20000)
  expect(teamFetch).toHaveBeenCalledTimes(4)
  expect(result.onError).toHaveBeenCalledWith(expect.objectContaining({ message: 'offline' }))
})
