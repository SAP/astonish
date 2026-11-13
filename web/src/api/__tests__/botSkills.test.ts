import { beforeEach, expect, it, vi } from 'vitest'
import { importLegacyBotSkill, listBotSkills, listBotSkillRevisions, listBotSkillRuns, getBotSkillRevision } from '../botSkills'
import { teamFetch } from '../teamContext'
import type { Bot, BotTrigger } from '../bots'
vi.mock('../teamContext', () => ({ teamFetch: vi.fn() }))
const bot = { id: 'bot/id', scope: 'team' } as Bot
beforeEach(() => { vi.mocked(teamFetch).mockReset(); vi.mocked(teamFetch).mockImplementation(async () => new Response('{}')) })
it('encodes scoped GETs and server cursors without admission', async () => {
  const controller = new AbortController()
  await listBotSkills(bot, controller.signal, { after: 'cursor', limit: 2, include_archived: true })
  expect(teamFetch).toHaveBeenLastCalledWith('/api/studio/bots/bot%2Fid/skills?limit=2&after=cursor&include_archived=true&scope=team', { signal: controller.signal })
  await listBotSkillRevisions(bot, 'skill/id')
  expect(teamFetch).toHaveBeenLastCalledWith('/api/studio/bots/bot%2Fid/skills/skill%2Fid/revisions?scope=team', { signal: undefined })
  await listBotSkillRuns(bot, 'skill/id')
  await getBotSkillRevision(bot, 'skill/id', 'head')
  expect(teamFetch).toHaveBeenLastCalledWith('/api/studio/bots/bot%2Fid/skills/skill%2Fid/head?scope=team', { signal: undefined })
})
it('posts only the pinned legacy conversion contract with a SHA-256 digest', async () => {
  await importLegacyBotSkill({ ...bot, version: 3, revision: 4 }, { id: 'trigger', instructions: 'abc' } as BotTrigger, 'retry-key')
  expect(teamFetch).toHaveBeenCalledExactlyOnceWith('/api/studio/bots/bot%2Fid/skills/import-legacy?scope=team', {
    method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ expected_revision: 4, trigger_id: 'trigger', version: 3, instructions_digest: 'ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad', idempotency_key: 'retry-key' }),
  })
})
it('does not convert a failed read into an empty result', async () => {
  vi.mocked(teamFetch).mockResolvedValue(new Response('unavailable', { status: 503 }))
  await expect(listBotSkills(bot)).rejects.toThrow('unavailable')
})
