import { beforeEach, describe, expect, it, vi } from 'vitest'
import { botURL, permanentDeleteBot, setupBotHome, createBot, promoteChatToBot, getBotTemplate, forkBotTemplate, botLifecycle, decideBotApproval, listBotApprovals, BotStatus, type Bot, type BotApproval, type BotTemplate } from '../bots'
import { connectBotEvents } from '../botEvents'
import { teamFetch } from '../teamContext'
vi.mock('../teamContext', () => ({ teamFetch: vi.fn() }))
const fetchMock = vi.mocked(teamFetch)
beforeEach(() => fetchMock.mockReset())
describe('scoped Bot transport', () => {
  const approvalRows = (count: number, start = 1) => Array.from({ length: count }, (_, i) => ({ id: `00000000-0000-4000-8000-${(start + i).toString(16).padStart(12, '0')}`, status: 'consumed' } as BotApproval))
  it.each([200, 201, 400])('drains %s approvals in cursor order including exact multiples', async count => {
    const rows = approvalRows(count)
    for (let i = 0; i <= count; i += 200) fetchMock.mockResolvedValueOnce(new Response(JSON.stringify({ approvals: rows.slice(i, i + 200) })))
    const controller = new AbortController()
    await expect(listBotApprovals('a/b', 'team', controller.signal)).resolves.toEqual({ approvals: rows })
    expect(fetchMock).toHaveBeenCalledTimes(Math.floor(count / 200) + 1)
    fetchMock.mock.calls.forEach(([url, init], i) => {
      const query = new URL(String(url), 'https://fixture.test').searchParams
      expect(query.get('limit')).toBe('200'); expect(query.get('scope')).toBe('team')
      expect(query.get('after')).toBe(i ? rows[i * 200 - 1].id : null)
      expect(init?.signal).toBe(controller.signal)
    })
  })
  it('rejects cyclic approval pages without returning partial data', async () => {
    const rows = approvalRows(200)
    fetchMock.mockResolvedValueOnce(new Response(JSON.stringify({ approvals: rows })))
    fetchMock.mockResolvedValueOnce(new Response(JSON.stringify({ approvals: rows })))
    await expect(listBotApprovals('a', 'personal')).rejects.toThrow(/cursor/i)
    expect(fetchMock).toHaveBeenCalledTimes(2)
  })
  it('honors approval cancellation between pages even if fetch ignores the signal', async () => {
    const controller = new AbortController()
    fetchMock.mockImplementationOnce(async () => { controller.abort(); return new Response(JSON.stringify({ approvals: approvalRows(200) })) })
    await expect(listBotApprovals('a', 'personal', controller.signal)).rejects.toMatchObject({ name: 'AbortError' })
    expect(fetchMock).toHaveBeenCalledOnce()
  })
  it.each(['personal', 'team'] as const)('permanently deletes %s with confirmation but no reason', async scope => {
    fetchMock.mockResolvedValueOnce(new Response(null, { status: 204 }))
    await expect(permanentDeleteBot({ id: 'a/b', scope, revision: 4 } as Bot)).resolves.toBeUndefined()
    expect(fetchMock).toHaveBeenCalledWith(`/api/studio/bots/a%2Fb/permanent-delete?scope=${scope}`, expect.objectContaining({ method: 'POST', body: '{"expected_revision":4,"confirm":true}' }))
  })
  it('preserves the deletion-pending code separately from its human message', async () => {
    fetchMock.mockResolvedValueOnce(new Response('{"code":"deletion_pending","error":"Cleanup pending"}', { status: 410 }))
    await expect(permanentDeleteBot({ id: 'a', scope: 'personal', revision: 4 } as Bot)).rejects.toMatchObject({ status: 410, code: 'deletion_pending', message: 'Cleanup pending' })
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
  it('creates an Active private Bot without requesting work or schedules', async () => {
    fetchMock.mockResolvedValue(new Response('{"id":"bot","status":"active"}', { status: 201 }))
    await expect(createBot({ name: 'Monitor', idempotency_key: 'key', definition: { charter: 'Observe' } })).resolves.toEqual({ id: 'bot', status: BotStatus.Active })
    expect(Object.values(BotStatus)).toEqual(['active', 'paused', 'stopped', 'deleted'])
    expect(fetchMock).toHaveBeenCalledOnce()
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
    await expect(botLifecycle({ id: 'a/b', scope: 'team', revision: 4 } as Bot, 'resume', 'reviewed')).rejects.toThrow('revision conflict')
    expect(fetchMock.mock.calls[0][0]).toContain('/a%2Fb/resume?scope=team')
    fetchMock.mockResolvedValue(new Response('{}'))
    await decideBotApproval({ id: 'a', scope: 'personal' } as Bot, { id: 'p', action_digest: 'digest' } as BotApproval, 'denied')
    expect(JSON.parse(fetchMock.mock.calls[1][1]!.body as string)).toEqual({ action_digest: 'digest', status: 'denied' })
  })
  it('forwards backend-projected reports and public text with stable identities', async () => {
    const emit = vi.fn(), done = vi.fn(), error = vi.fn()
    const events: [string, object][] = [
      ['report_marker', { path: '/report.md', eventId: 'report-row', invocationId: 'report-turn' }],
      ['text', { text: 'visible', eventId: 'text-row', invocationId: 'text-turn' }],
      ['turn_complete', { eventId: 'text-row', invocationId: 'text-turn' }],
    ]
    // A text row has two public parts; only its final part commits the row cursor.
    const frames = events.map(([type, data], index) => {
      const cursor = `sid:${index === 2 ? 2 : 1}`
      const eventId = index === 0 ? 'sid/report-row/0' : `sid/text-row/${index - 1}`
      return `id: ${cursor}\nevent: ${type}\ndata: ${JSON.stringify({ ...data, _projection: { event_id: eventId, session_id: 'sid', run_id: 'run', cursor } })}\n\n`
    }).join('')
    fetchMock.mockResolvedValueOnce(new Response(`${frames}id: sid:2\nevent: done\ndata: {"persisted":true}\n\n`))
    const controller = connectBotEvents({ botId: 'bot', sessionId: 'sid', scope: 'personal', onEvent: emit, onDone: done, onError: error })
    try {
      await vi.waitFor(() => expect(done).toHaveBeenCalledOnce())
      expect(error).not.toHaveBeenCalled()
      expect(emit.mock.calls).toEqual(events)
      expect(JSON.stringify(emit.mock.calls)).not.toContain('secret')
      expect(JSON.stringify(emit.mock.calls)).not.toContain('_projection')
    } finally { controller.abort() }
  })
  it('frames split CRLF, deduplicates stable identity and aborts the reader', async () => {
    const emit = vi.fn(), state = vi.fn(), cancel = vi.fn(), replay = vi.fn(), error = vi.fn()
    const event = JSON.stringify({ text: 'once', _projection: { event_id: 'sid/row/0', session_id: 'sid', run_id: 'run', cursor: 'sid:1' } })
    fetchMock.mockResolvedValueOnce(new Response(new ReadableStream({ start(c) {
      for (const chunk of [`id: sid:1\r\nevent: text\r\ndata: ${event}\r`, `\n\r\nid: sid:1\nevent: text\ndata: ${event}\n\n`, 'id: sid:1\nevent: replay_complete\ndata: {"persisted":true}\n\n']) c.enqueue(new TextEncoder().encode(chunk))
    }, cancel })))
    const controller = connectBotEvents({ botId: 'a/b', sessionId: 'sid', scope: 'personal', onEvent: emit, onState: state, onReplayComplete: replay, onError: error })
    try {
      await vi.waitFor(() => expect(replay).toHaveBeenCalledOnce())
      expect(emit.mock.calls).toEqual([['text', { text: 'once' }]])
      expect(error).not.toHaveBeenCalled()
      expect(state).toHaveBeenCalledWith('live', 'sid:1')
      expect(fetchMock.mock.calls[0][0]).toContain('/a%2Fb/sessions/sid/events?scope=personal')
    } finally { controller.abort() }
    await vi.waitFor(() => expect(cancel).toHaveBeenCalledOnce())
  })
  it('reconnects from the committed row cursor and deduplicates replayed parts by stable identity', async () => {
    vi.useFakeTimers()
    const emit = vi.fn(), state = vi.fn(), done = vi.fn(), error = vi.fn()
    const frame = (type: string, data: object, row: string, part: number, cursor: string) =>
      `id: ${cursor}\nevent: ${type}\ndata: ${JSON.stringify({ ...data, _projection: { event_id: `sid/${row}/${part}`, session_id: 'sid', run_id: 'run', cursor } })}\n\n`
    const report = frame('report_marker', { path: '/report.md' }, 'report', 0, 'sid:1')
    // The first text part retains the previous row cursor until turn_complete.
    const text = frame('text', { text: 'once' }, 'text', 0, 'sid:1')
    const complete = frame('turn_complete', { eventId: 'text' }, 'text', 1, 'sid:2')
    fetchMock.mockResolvedValueOnce(new Response(report + text))
    fetchMock.mockResolvedValueOnce(new Response(text + complete + 'id: sid:2\nevent: done\ndata: {"persisted":true}\n\n'))
    const controller = connectBotEvents({ botId: 'bot', sessionId: 'sid', scope: 'personal', onEvent: emit, onState: state, onDone: done, onError: error })
    try {
      await vi.waitFor(() => expect(state).toHaveBeenCalledWith('reconnecting', 'sid:1'))
      expect(emit.mock.calls).toEqual([['report_marker', { path: '/report.md' }], ['text', { text: 'once' }]])
      expect(done).not.toHaveBeenCalled()
      await vi.advanceTimersByTimeAsync(1000)
      await vi.waitFor(() => expect(done).toHaveBeenCalledOnce())
      expect(fetchMock).toHaveBeenCalledTimes(2)
      expect(fetchMock.mock.calls[1][1]?.headers).toEqual({ 'Last-Event-ID': 'sid:1' })
      expect(emit.mock.calls).toEqual([['report_marker', { path: '/report.md' }], ['text', { text: 'once' }], ['turn_complete', { eventId: 'text' }]])
      expect(state).toHaveBeenLastCalledWith('live', 'sid:2')
      expect(error).not.toHaveBeenCalled()
    } finally { controller.abort(); vi.useRealTimers() }
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
