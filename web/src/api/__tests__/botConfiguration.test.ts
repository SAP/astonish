import { beforeEach, expect, it, vi } from 'vitest'
import { configureBot, getBotConfigurationVersion, type BotConfigurationRequest } from '../botConfiguration'
import type { Bot } from '../bots'
import { teamFetch } from '../teamContext'
vi.mock('../teamContext', () => ({ teamFetch: vi.fn() }))
beforeEach(() => vi.clearAllMocks())
it('PUTs the exact reviewed configuration with both CAS tokens and scoped encoded ID', async () => {
  vi.mocked(teamFetch).mockResolvedValue(new Response('{"id":"saved"}'))
  const body: BotConfigurationRequest = { expected_revision: 8, expected_version: 3, reviewed: true, reason: 'Reviewed limits', autonomy: { level: 'ask', allowed_tools: [], denied_tools: [], allowed_paths: ['/workspace'], allowed_hosts: ['example.org'], can_contact_people: false, can_publish: false, max_spend_per_run_minor: 0, max_spend_per_month_minor: 0 }, budget: { max_runtime_seconds: 60, max_tokens: 0 }, delivery: { notify: 'never', preferences: '', bindings: [] } }
  await expect(configureBot('a/b', 'team', body)).resolves.toEqual({ id: 'saved' })
  expect(teamFetch).toHaveBeenCalledExactlyOnceWith('/api/studio/bots/a%2Fb/configuration?scope=team', expect.objectContaining({ method: 'PUT', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) }))
})
it.each([409, 403, 501])('preserves status and server explanation (%s)', async status => {
  vi.mocked(teamFetch).mockResolvedValue(new Response('Configuration unsupported', { status }))
  await expect(configureBot('bot', 'personal', {} as BotConfigurationRequest)).rejects.toMatchObject({ status, message: 'Configuration unsupported' })
})
it('loads the exact immutable version with scope and cancellation, preserving failures', async () => {
  const bot = { id: 'a/b', scope: 'team', version: 7 } as Bot
  const signal = new AbortController().signal
  vi.mocked(teamFetch).mockResolvedValueOnce(new Response('{"version":7}'))
  await expect(getBotConfigurationVersion(bot, signal)).resolves.toEqual({ version: 7 })
  expect(teamFetch).toHaveBeenCalledExactlyOnceWith('/api/studio/bots/a%2Fb/versions/7?scope=team', { signal })
  vi.mocked(teamFetch).mockResolvedValueOnce(new Response('Version unavailable', { status: 404 }))
  await expect(getBotConfigurationVersion(bot)).rejects.toMatchObject({ status: 404, message: 'Version unavailable' })
})
