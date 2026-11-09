import { beforeEach, describe, expect, it, vi } from 'vitest'
import { botURL, permanentDeleteBot, setupBotHome, createBot, promoteChatToBot, getBotTemplate, forkBotTemplate, botLifecycle, decideBotApproval, type Bot, type BotApproval, type BotTemplate } from '../bots'
import { connectBotEvents, dispatchBotTranscript } from '../botEvents'
import { teamFetch } from '../teamContext'
vi.mock('../teamContext', () => ({ teamFetch: vi.fn() }))
const fetchMock = vi.mocked(teamFetch)
beforeEach(() => vi.clearAllMocks())
describe('scoped Bot transport', () => {
  it.each(['personal', 'team'] as const)('permanently deletes %s with confirmation but no reason', async scope => {
    fetchMock.mockResolvedValueOnce(new Response(null, { status: 204 }))
    await expect(permanentDeleteBot({ id: 'a/b', scope, revision: 4 } as Bot)).resolves.toBeUndefined()
    expect(fetchMock).toHaveBeenCalledWith(`/api/studio/bots/a%2Fb/permanent-delete?scope=${scope}`, expect.objectContaining({ method: 'POST', body: '{"expected_revision":4,"confirm":true}' }))
  })
  it('preserves pending and HTTP failures rather than inventing completion', async () => {
    const bot = { id: 'a', scope: 'personal', revision: 4 } as Bot
    fetchMock.mockResolvedValueOnce(new Response(JSON.stringify({ status: 'pending', bot }), { status: 202 }))
    await expect(permanentDeleteBot(bot)).resolves.toEqual({ status: 'pending', bot })
    for (const status of [403, 404, 409, 503]) {
      fetchMock.mockResolvedValueOnce(new Response('Not deleted', { status }))
      await expect(permanentDeleteBot(bot)).rejects.toMatchObject({ status })
    }
  })
  it.each(['personal', 'team'] as const)('prepares a server-selected home with only the %s revision', async scope => {
    const bot = { id: 'a/b', scope, revision: 4, home_session_id: '82c09d23-63e1-42d8-8baa-5f540fabc645', home: null } as Bot
    const saved: Bot = { ...bot, revision: 5, home: { id: '24c09d23-63e1-42d8-8baa-5f540fabc645', bot_id: bot.id, scope, backend: 'docker', storage_key: 'retained-volume', revision: 1 } }
    fetchMock.mockResolvedValueOnce(new Response(JSON.stringify(saved)))
    await expect(setupBotHome(bot)).resolves.toEqual(saved)
    expect(fetchMock).toHaveBeenCalledWith(`/api/studio/bots/a%2Fb/home?scope=${scope}`, expect.objectContaining({ method: 'POST', headers: { 'Content-Type': 'application/json' }, body: '{"expected_revision":4}' }))
  })
  it.each([409, 503])('preserves home setup HTTP status %s for actionable UI errors', async status => {
    fetchMock.mockResolvedValueOnce(new Response('Bot operation unavailable', { status }))
    await expect(setupBotHome({ id: 'bot', scope: 'personal', revision: 1 } as Bot)).rejects.toMatchObject({ status, message: 'Bot operation unavailable' })
  })
  it('always carries explicit scope and encodes query values', () => {
    expect(botURL('/x', 'team', { q: 'a & b' })).toBe('/api/studio/bots/x?q=a+%26+b&scope=team')
  })
  it('creates only a conservative private draft', async () => {
    fetchMock.mockResolvedValue(new Response('{"id":"bot"}', { status: 201 }))
    await createBot({ name: 'Monitor', idempotency_key: 'key', definition: { charter: 'Observe' } })
    expect(fetchMock).toHaveBeenCalledWith('/api/studio/bots?scope=personal', expect.objectContaining({ method: 'POST', body: '{"name":"Monitor","idempotency_key":"key","definition":{"charter":"Observe"}}' }))
  })
  it('posts only reviewed carryover to the encoded private promotion endpoint', async () => {
    fetchMock.mockResolvedValue(new Response('{"id":"new"}', { status: 201 }))
    const draft = { name: 'Watcher', charter: 'Observe', idempotency_key: 'retry', reviewed: true, reviewed_for_team: false, carryover: { summary: 'Redacted', memories: [{ content: 'Selected fact', category: 'fact' }], goals: [{ title: 'Verify', completion_criteria: 'Evidence' }], artifacts: [{ title: 'Snapshot', content: 'Text only' }] } }
    await expect(promoteChatToBot('source/id', draft)).resolves.toEqual({ id: 'new' })
    expect(fetchMock).toHaveBeenCalledOnce()
    expect(fetchMock).toHaveBeenCalledWith('/api/studio/bots/promote-chat/source%2Fid?scope=personal', expect.objectContaining({ method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(draft) }))
  })
  it('reads team template details with cancellation and pins the fork revision', async () => {
    const template = { id: 't/id', revision: 7 } as BotTemplate
    const controller = new AbortController()
    fetchMock.mockResolvedValueOnce(new Response(JSON.stringify(template)))
    await expect(getBotTemplate(template.id, controller.signal)).resolves.toEqual(template)
    expect(fetchMock).toHaveBeenLastCalledWith('/api/studio/bots/templates/t%2Fid?scope=team', expect.objectContaining({ method: 'GET', signal: controller.signal }))
    fetchMock.mockResolvedValueOnce(new Response('{"scope":"personal"}', { status: 201 }))
    await expect(forkBotTemplate(template, 'Private fork', 'key')).resolves.toEqual({ scope: 'personal' })
    expect(fetchMock).toHaveBeenLastCalledWith('/api/studio/bots/templates/t%2Fid/fork?scope=team', expect.objectContaining({ method: 'POST', body: JSON.stringify({ template_revision: 7, name: 'Private fork', idempotency_key: 'key' }) }))
    fetchMock.mockResolvedValueOnce(new Response('Template no longer available', { status: 404 }))
    await expect(getBotTemplate(template.id)).rejects.toThrow('Template no longer available')
  })
  it('uses revision and digest, never owner authority, and surfaces conflicts', async () => {
    fetchMock.mockResolvedValueOnce(new Response('revision conflict', { status: 409 }))
    await expect(botLifecycle({ id: 'a/b', scope: 'team', revision: 4 } as Bot, 'activate', 'reviewed')).rejects.toThrow('revision conflict')
    expect(fetchMock.mock.calls[0][0]).toContain('/a%2Fb/activate?scope=team')
    fetchMock.mockResolvedValue(new Response('{}'))
    await decideBotApproval({ id: 'a', scope: 'personal' } as Bot, { id: 'p', action_digest: 'digest' } as BotApproval, 'denied')
    expect(JSON.parse(fetchMock.mock.calls[1][1]!.body as string)).toEqual({ action_digest: 'digest', status: 'denied' })
  })
  it('unwraps runtime wire events and excludes thought text', () => {
    const emit = vi.fn()
    dispatchBotTranscript({ ID: 'report-row', InvocationID: 'report-turn', Actions: { StateDelta: { bot_runtime_event: { type: 'report_marker', payload: { path: '/report.md' } } } } }, emit)
    dispatchBotTranscript({ ID: 'text-row', InvocationID: 'text-turn', Content: { parts: [{ thought: true, text: 'secret' }, { text: 'visible' }] } }, emit)
    expect(emit.mock.calls).toEqual([['report_marker', { path: '/report.md', eventId: 'report-row', invocationId: 'report-turn' }], ['text', { text: 'visible', eventId: 'text-row', invocationId: 'text-turn' }], ['turn_complete', { eventId: 'text-row', invocationId: 'text-turn' }]])
  })
  it('frames split CRLF, deduplicates cursor and aborts the reader', async () => {
    const emit = vi.fn(), state = vi.fn(), cancel = vi.fn()
    const event = JSON.stringify({ Actions: { StateDelta: { bot_runtime_event: { type: 'text', payload: { text: 'once' } } } } })
    fetchMock.mockResolvedValue(new Response(new ReadableStream({ start(c) {
      for (const chunk of [`id: sid:1\r\nevent: transcript\r\ndata: ${event}\r`, `\n\r\nid: sid:1\nevent: transcript\ndata: ${event}\n\n`, 'id: sid:1\nevent: replay_complete\ndata: {"persisted":true}\n\n']) c.enqueue(new TextEncoder().encode(chunk))
    }, cancel })))
    const controller = connectBotEvents({ botId: 'a/b', sessionId: 'sid', scope: 'personal', onEvent: emit, onState: state })
    await vi.waitFor(() => expect(emit).toHaveBeenCalledTimes(1))
    expect(state).toHaveBeenCalledWith('live', 'sid:1')
    expect(fetchMock.mock.calls[0][0]).toContain('/a%2Fb/sessions/sid/events?scope=personal')
    controller.abort()
  })
  it('does not retry forbidden streams or call ordinary Chat', async () => {
    fetchMock.mockResolvedValue(new Response('forbidden', { status: 403 }))
    const error = vi.fn()
    const controller = connectBotEvents({ botId: 'bot', sessionId: 'sid', scope: 'team', onEvent: vi.fn(), onError: error })
    await vi.waitFor(() => expect(error).toHaveBeenCalledTimes(1))
    expect(fetchMock).toHaveBeenCalledTimes(1)
    controller.abort()
  })
})
