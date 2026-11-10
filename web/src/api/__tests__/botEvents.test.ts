import { expect, it, vi } from 'vitest'
import { connectBotEvents, createBotTranscriptMapper, dispatchBotTranscript, type BotTranscript } from '../botEvents'
import { teamFetch } from '../teamContext'

vi.mock('../teamContext', () => ({ teamFetch: vi.fn() }))

const quotedContext = '[Astonish Per-Turn Context — not user-authored]\n## Relevant Tools For This Request\nannounce_plan / delegate_tasks'
const userRow = (camel: boolean, text: string, marker?: unknown): BotTranscript => JSON.parse(JSON.stringify(camel
  ? { author: 'user', invocationId: 'turn', content: { role: 'user', parts: [{ text }] }, actions: { stateDelta: { _astonish_turn_context: marker } } }
  : { Author: 'user', InvocationID: 'turn', Content: { role: 'user', parts: [{ text }] }, Actions: { StateDelta: { _astonish_turn_context: marker } } }))

it('distinguishes chunked replay completion from per-row live connectivity', async () => {
  const emit = vi.fn(), state = vi.fn(), replay = vi.fn()
  let writer!: ReadableStreamDefaultController<Uint8Array>
  vi.mocked(teamFetch).mockResolvedValueOnce(new Response(new ReadableStream({ start(c) { writer = c } })))
  const controller = connectBotEvents({ botId: 'bot', sessionId: 'home', scope: 'personal', onEvent: emit, onState: state, onReplayComplete: replay })
  const push = (text: string) => writer.enqueue(new TextEncoder().encode(text))
  try {
    push(`id: home:1\nevent: transcript\ndata: ${JSON.stringify(userRow(false, 'initial'))}\n\n`)
    await vi.waitFor(() => expect(state).toHaveBeenCalledWith('live', 'home:1'))
    expect(replay).not.toHaveBeenCalled()
    push('id: home:1\nevent: replay_com')
    await Promise.resolve()
    expect(replay).not.toHaveBeenCalled()
    push('plete\ndata: {}\n\n')
    await vi.waitFor(() => expect(replay).toHaveBeenCalledTimes(1))
  } finally { controller.abort() }
})

it.each([false, true])('hides only boolean-marked context without completion (camel: %s)', camel => {
  for (const runtime of [false, true]) {
    const mapper = createBotTranscriptMapper(), emit = vi.fn()
    if (runtime) dispatchBotTranscript(wire('session', {}, 'turn'), emit, mapper)
    dispatchBotTranscript(userRow(camel, 'real input'), emit, mapper, 'home:1')
    expect(emit.mock.calls).toEqual([
      ['user_message', { text: 'real input', invocationId: 'turn', eventId: 'home:1' }],
      ['turn_complete', { invocationId: 'turn', eventId: 'home:1' }],
    ])
    emit.mockClear()
    dispatchBotTranscript(userRow(camel, quotedContext, true), emit, mapper, 'home:2')
    dispatchBotTranscript(userRow(camel, 'Model-only context without a heading', true), emit, mapper, 'home:2b')
    expect(emit).not.toHaveBeenCalled()
    // Neither quoted headings nor false/string markers are provenance. Equal
    // legitimate messages must survive even inside a correlated runtime turn.
    for (const marker of [undefined, false, 'true']) {
      for (const id of ['home:3', 'home:4']) {
        dispatchBotTranscript(userRow(camel, quotedContext, marker), emit, mapper, id)
      }
    }
    expect(emit.mock.calls.filter(([type]) => type === 'user_message').map(([, p]) => p.text)).toEqual(Array(6).fill(quotedContext))
    expect(emit.mock.calls.filter(([type]) => type === 'turn_complete')).toHaveLength(6)
  }
})

it('advances the stream cursor past hidden context without emitting a completion', async () => {
  const emit = vi.fn(), state = vi.fn()
  vi.mocked(teamFetch).mockResolvedValueOnce(new Response(new ReadableStream({ start(c) {
    c.enqueue(new TextEncoder().encode(`id: home:2\nevent: transcript\ndata: ${JSON.stringify(userRow(true, quotedContext, true))}\n\nid: home:2\nevent: replay_complete\ndata: {"persisted":true}\n\n`))
  } })))
  const controller = connectBotEvents({ botId: 'bot', sessionId: 'home', scope: 'personal', onEvent: emit, onState: state })
  try {
    await vi.waitFor(() => expect(state).toHaveBeenCalledWith('live', 'home:2'))
    expect(emit).not.toHaveBeenCalled()
  } finally {
    controller.abort()
  }
})

const wire = (type: string, payload: Record<string, unknown>, turn?: string, id = 'review-row'): BotTranscript => JSON.parse(JSON.stringify({
  id, invocationId: turn, author: 'bot_runtime',
  actions: { stateDelta: { bot_runtime_event: { type, payload, turn_id: turn, sequence: 1 } } },
}))

it.each(['proposal', 'approval'])('normalizes canonical persisted %s references to the StudioChat contract', kind => {
  const emit = vi.fn()
  dispatchBotTranscript(wire('bot_review', { kind, review_id: 'review-1' }, 'review-turn'), emit)
  expect(emit.mock.calls).toEqual([['bot_review', { kind, reviewId: 'review-1', eventId: 'review-row', invocationId: 'review-turn' }]])
})

it.each([false, true])('deduplicates typed reviews across canonical, camel and legacy rows (legacy first: %s)', legacyFirst => {
  const mapper = createBotTranscriptMapper(), emit = vi.fn()
  const canonical = wire('bot_review', { kind: 'proposal', review_id: 'same-id' }, 'review-turn')
  const legacy: BotTranscript = { ID: 'tool-row', InvocationID: 'model-turn', Content: { parts: [{ functionResponse: { name: 'bot_propose', response: { proposal_id: 'same-id' } } }] } }
  const rows = legacyFirst ? [legacy, canonical] : [canonical, legacy]
  rows.push(wire('bot_review', { kind: 'proposal', reviewId: 'same-id' }, 'camel-turn'))
  rows.push(wire('tool_result', { name: 'bot_propose', result: { proposal_id: 'same-id' } }, 'wire-tool-turn'))
  rows.push(wire('approval', { approval_id: 'same-id' }, 'approval-turn', 'approval-row'))
  rows.push(wire('bot_review', { kind: 'approval', review_id: 'same-id' }, 'approval-reference-turn'))
  for (const row of rows) dispatchBotTranscript(row, emit, mapper)
  expect(emit.mock.calls.filter(([type]) => type === 'bot_review')).toEqual([
    ['bot_review', { kind: 'proposal', reviewId: 'same-id', eventId: legacyFirst ? 'tool-row' : 'review-row', invocationId: legacyFirst ? 'model-turn' : 'review-turn' }],
    ['bot_review', { kind: 'approval', reviewId: 'same-id', eventId: 'approval-row', invocationId: 'approval-turn' }],
  ])
  expect(emit.mock.calls.filter(([type]) => type === 'tool_result')).toHaveLength(2)
})

it.each(['bot_review', 'approval', 'plan_approval', 'chat_question', 'auto_approved', 'status'])('keeps later ADK narrative after scoped and unscoped %s control metadata', type => {
  const mapper = createBotTranscriptMapper(), emit = vi.fn()
  dispatchBotTranscript(wire(type, { kind: 'proposal', review_id: 'p1' }), emit, mapper)
  dispatchBotTranscript(wire(type, { kind: 'proposal', review_id: 'p2' }, 'model-turn'), emit, mapper)
  for (const turn of ['model-turn', 'next-turn', 'next-turn']) {
    dispatchBotTranscript({ InvocationID: turn, Author: 'agent', Content: { parts: [{ text: 'Still discussing.' }] } }, emit, mapper)
  }
  expect(emit.mock.calls.filter(([event]) => event === 'text').map(([, payload]) => payload.text)).toEqual(['Still discussing.', 'Still discussing.', 'Still discussing.'])
})

it.each(['session', 'text', 'tool_call', 'tool_result'])('selects %s runtime projection only for a correlated turn', type => {
  const mapper = createBotTranscriptMapper(), emit = vi.fn()
  dispatchBotTranscript(wire(type, { text: 'wire' }, 'runtime-turn'), emit, mapper)
  emit.mockClear()
  dispatchBotTranscript({ InvocationID: 'runtime-turn', Author: 'agent', Content: { parts: [{ text: 'duplicate' }] } }, emit, mapper)
  dispatchBotTranscript({ InvocationID: 'legacy-turn', Author: 'agent', Content: { parts: [{ text: 'legacy' }] } }, emit, mapper)
  expect(emit.mock.calls.filter(([event]) => event === 'text').map(([, payload]) => payload.text)).toEqual(['legacy'])
})

it('does not apply an uncorrelated runtime projection to future turns', () => {
  const mapper = createBotTranscriptMapper(), emit = vi.fn()
  dispatchBotTranscript(wire('session', {}), emit, mapper)
  dispatchBotTranscript({ InvocationID: 'later', Content: { parts: [{ text: 'visible' }] } }, emit, mapper)
  expect(emit).toHaveBeenCalledWith('text', { text: 'visible', invocationId: 'later', eventId: '' })
})

it.each([{ kind: 'other', review_id: 'p' }, { kind: 'proposal', review_id: '' }, { kind: 'approval', review_id: 42 }])('rejects invalid review references: %j', payload => {
  const emit = vi.fn()
  dispatchBotTranscript(wire('bot_review', payload), emit)
  expect(emit).not.toHaveBeenCalled()
})

it('preserves equal semantic text chunks with distinct wire sequences and transport provenance', () => {
  const mapper = createBotTranscriptMapper(), emit = vi.fn()
  for (const sequence of [1, 2, 2]) {
    dispatchBotTranscript({ InvocationID: 't', Actions: { StateDelta: { bot_runtime_event: { type: 'text', sequence, payload: { text: 'again' } } } } }, emit, mapper, `home:${sequence}`)
  }
  expect(emit.mock.calls).toEqual([
    ['text', { text: 'again', invocationId: 't', eventId: 'home:1' }],
    ['text', { text: 'again', invocationId: 't', eventId: 'home:2' }],
  ])
})

it('suppresses raw ADK only for the correlated runtime turn and keeps users', () => {
  const mapper = createBotTranscriptMapper(), emit = vi.fn()
  dispatchBotTranscript({ InvocationID: 'a', Actions: { StateDelta: { bot_runtime_event: { type: 'session', turn_id: 'a', sequence: 1, payload: {} } } } }, emit, mapper)
  dispatchBotTranscript({ InvocationID: 'a', Author: 'agent', Content: { parts: [{ text: 'duplicate' }] } }, emit, mapper)
  dispatchBotTranscript({ InvocationID: 'a', Author: 'user', Content: { parts: [{ text: 'input' }] } }, emit, mapper)
  dispatchBotTranscript({ InvocationID: 'b', Author: 'agent', Content: { parts: [{ text: 'legacy' }] } }, emit, mapper)
  expect(emit.mock.calls.filter(([type]) => type !== 'turn_complete').map(([type, p]) => [type, p.text])).toEqual([['user_message', 'input'], ['text', 'legacy']])
})
it('maps lower camel ADK JSON and deduplicates wire sequences across replay', () => {
  const mapper = createBotTranscriptMapper(), emit = vi.fn()
  const event = { invocationId: 't', actions: { stateDelta: { bot_runtime_event: { type: 'text', turn_id: 't', sequence: 2, payload: { text: 'answer' } } } } } as unknown as BotTranscript
  dispatchBotTranscript(event, emit, mapper, 'home:2'); dispatchBotTranscript(event, emit, mapper, 'home:3')
  expect(emit).toHaveBeenCalledTimes(1)
  expect(emit).toHaveBeenCalledWith('text', { text: 'answer', eventId: 'home:2', invocationId: 't' })
})
it('does not turn replayed legacy approval prose into authority', () => {
  const emit = vi.fn()
  dispatchBotTranscript({ Actions: { StateDelta: { bot_runtime_event: { type: 'approval', payload: { name: 'shell' } } } } }, emit)
  expect(emit).not.toHaveBeenCalled()
})
