import { beforeEach, expect, it, vi } from 'vitest'
import { botFileAccess, downloadBotFile, isBotFilePublication, listBotFiles, previewBotFile, statBotFile, type BotFilePublication } from '../botFiles'
import { teamFetch } from '../teamContext'
vi.mock('../teamContext', () => ({ teamFetch: vi.fn(), getActiveTeam: () => 'team' }))
const target = { botId: 'bot', scope: 'team' as const, path: 'work/a b.bin' }
beforeEach(() => vi.clearAllMocks())

function publicationWithPath(path: string): BotFilePublication {
  return {
    schema_version: 1, bot_id: 'bot', run_id: 'run', home_session_id: 'session',
    home_id: 'home', report_sequence: 1, event_id: 'event', summary: 'Saved report',
    artifacts: [{ id: 'artifact', home_id: 'home', relative_path: path,
      sha256: 'a'.repeat(64), size_bytes: 10, media_type: 'text/plain', title: 'Report', preview: 'text' }],
  }
}
const controlCodes = [...Array.from({ length: 32 }, (_, code) => code), 127]
it.each(controlCodes)('preserves publication path validation for control code %i', code => {
  const path = `work/before${String.fromCharCode(code)}after.txt`
  expect(isBotFilePublication(publicationWithPath(path))).toBe(![0, 10, 13].includes(code))
})
it.each(controlCodes)('percent-encodes control code %i in file request URLs', async code => {
  vi.mocked(teamFetch).mockImplementation(async () => new Response('{}'))
  const path = `work/before${String.fromCharCode(code)}after.txt`
  await previewBotFile({ ...target, path })
  const requestURL = String(vi.mocked(teamFetch).mock.calls[0][0])
  expect(requestURL).toContain(`%${code.toString(16).padStart(2, '0').toUpperCase()}`)
  expect(new URL(requestURL, window.location.origin).searchParams.get('path')).toBe(path)
  expect(requestURL).not.toContain(String.fromCharCode(code))
})
it.each(['', '/absolute.txt', '../outside.txt', 'work/../outside.txt', 'work\\outside.txt', 'work/report\r\nX-Injected: yes.txt'])('rejects unsafe publication path %j', path => {
  expect(isBotFilePublication(publicationWithPath(path))).toBe(false)
})
it.each(['work/报告 café 😀.txt', 'work/a..b.txt', 'work/tab\tname.txt', 'work/\u0085\u2028\u2029.txt'])('preserves supported publication path %j', path => {
  expect(isBotFilePublication(publicationWithPath(path))).toBe(true)
})
it('encodes Unicode paths and CRLF injection attempts without creating headers or extra query parameters', async () => {
  vi.mocked(teamFetch).mockImplementation(async () => new Response('{}'))
  const path = 'work/报告 café 😀\r\nX-Injected: yes&scope=personal.txt'
  await previewBotFile({ ...target, path })
  const [requestURL, options] = vi.mocked(teamFetch).mock.calls[0]
  const url = new URL(String(requestURL), window.location.origin)
  expect(url.searchParams.get('path')).toBe(path)
  expect(url.searchParams.getAll('scope')).toEqual(['team'])
  expect(String(requestURL)).toContain('%0D%0AX-Injected%3A')
  expect(options).toEqual({ signal: undefined, method: 'GET' })
})
it('scopes list/stat/preview and propagates abort signals', async () => {
  vi.mocked(teamFetch).mockImplementation(async () => new Response('{}'))
  const signal = new AbortController().signal
  await listBotFiles(target, signal, 'next'); await statBotFile(target, signal); await previewBotFile(target, signal)
  expect(teamFetch).toHaveBeenCalledWith(expect.stringContaining('scope=team'), { signal, method: 'GET' })
  expect(vi.mocked(teamFetch).mock.calls.map(([url]) => String(url))).toEqual([
    '/api/studio/bots/bot/files?scope=team&path=work%2Fa+b.bin&cursor=next',
    '/api/studio/bots/bot/files/stat?scope=team&path=work%2Fa+b.bin',
    '/api/studio/bots/bot/files/preview?scope=team&path=work%2Fa+b.bin',
  ])
})
it('requests hidden entries only with explicit opt-in', async () => {
  vi.mocked(teamFetch).mockImplementation(async () => new Response('{}'))
  await listBotFiles(target, undefined, undefined, true)
  expect(teamFetch).toHaveBeenCalledWith(expect.stringContaining('show_hidden=true'), expect.anything())
  vi.mocked(teamFetch).mockClear()
  await listBotFiles(target)
  expect(String(vi.mocked(teamFetch).mock.calls[0][0])).not.toContain('show_hidden')
})
it('downloads natively after a scoped ticket POST, never buffers binary bytes', async () => {
  vi.mocked(teamFetch).mockResolvedValue(new Response(JSON.stringify({ url: '/api/studio/bots/bot/files/download?scope=team&path=work%2Fa+b.bin' })))
  const click = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {})
  await downloadBotFile(target)
  expect(teamFetch).toHaveBeenCalledTimes(1)
  expect(teamFetch).toHaveBeenCalledWith(expect.stringContaining('/download-ticket?'), { signal: undefined, method: 'POST' })
  expect(click).toHaveBeenCalledTimes(1); click.mockRestore()
})
it.each([401, 403, 404, 410, 501])('surfaces HTTP %s without attempting binary navigation', async status => {
  vi.mocked(teamFetch).mockResolvedValue(new Response('unavailable', { status }))
  await expect(downloadBotFile(target)).rejects.toMatchObject({ status })
})
it('rejects off-origin tickets and suppresses aborted native navigation', async () => {
  vi.mocked(teamFetch).mockResolvedValue(new Response(JSON.stringify({ url: 'https://evil.invalid/download' })))
  await expect(downloadBotFile(target)).rejects.toThrow('Invalid download destination')
  const controller = new AbortController(); controller.abort()
  await downloadBotFile(target, controller.signal)
})
it('immutable adapter uses publication identity, not mutable working path', async () => {
  vi.mocked(teamFetch).mockResolvedValue(new Response(JSON.stringify({ content: 'saved', truncated: false })))
  const adapter = botFileAccess({ ...target, runId: 'run', reportSequence: 7, artifactId: 'artifact' }, 'text', 'digest')
  await adapter.readPreview(new AbortController().signal)
  expect(teamFetch).toHaveBeenCalledWith('/api/studio/bots/bot/runs/run/reports/7/artifacts/artifact/preview?scope=team', expect.anything())
})
