import { beforeEach, describe, expect, it, vi } from 'vitest'
import { actOnTrigger, saveTrigger, triggerDefinition, type TriggerSpec } from '../botTriggers'
import { type Bot } from '../bots'
import { teamFetch } from '../teamContext'
vi.mock('../teamContext', () => ({ teamFetch: vi.fn() }))
const bot = { id: 'bot/id', scope: 'team', revision: 9, version: 4, status: 'active' } as Bot
const trigger: TriggerSpec = { id: 'trigger/id', kind: 'webhook', instructions: 'Observe', enabled: false, catch_up_once: false, credential_ref: 'credential-ref', hook_status: 'disabled' }
beforeEach(() => { vi.mocked(teamFetch).mockReset(); vi.mocked(teamFetch).mockResolvedValue(new Response(JSON.stringify(bot), { status: 200 })) })
describe('trigger wire contract', () => {
  it('sends exact signed proof fields and scoped POST', async () => {
    const proof = { bot_id: bot.id, trigger_id: trigger.id, version: 4, event_id: 'event', timestamp: 1730000000, signature: 'signed-digest', payload: 'aGVsbG8=' }
    await actOnTrigger(bot, trigger, 'dry-run', 'Reviewed proof', proof)
    expect(teamFetch).toHaveBeenCalledWith('/api/studio/bots/bot%2Fid/triggers/trigger%2Fid/dry-run?scope=team', expect.objectContaining({ method: 'POST', body: JSON.stringify({ expected_revision: 9, reason: 'Reviewed proof', proof }) }))
  })
  it('rejects enabling without a server dry-run; allows passed state', async () => {
    await expect(actOnTrigger(bot, trigger, 'enable', 'Review')).rejects.toThrow('dry-run')
    expect(teamFetch).not.toHaveBeenCalled()
    await actOnTrigger(bot, { ...trigger, hook_status: 'dry_run_passed' }, 'enable', 'Review')
    expect(teamFetch).toHaveBeenCalledWith(expect.stringContaining('/enable?scope=team'), expect.objectContaining({ method: 'POST' }))
  })
  it('removes through DELETE, not a fabricated POST route', async () => {
    await actOnTrigger(bot, trigger, 'remove', 'Retired')
    expect(teamFetch).toHaveBeenCalledWith('/api/studio/bots/bot%2Fid/triggers/trigger%2Fid?scope=team', expect.objectContaining({ method: 'DELETE', body: JSON.stringify({ expected_revision: 9, reason: 'Retired' }) }))
  })
  it('edits via PUT disabled and strips server-owned hook approval', async () => {
    const edited = { ...trigger, enabled: true, hook_status: 'enabled', fingerprint: 'old', schedule_id: 'schedule', hook_watermark: 'old' }
    await saveTrigger(bot, edited, 'Changed')
    expect(teamFetch).toHaveBeenCalledWith(expect.stringContaining('/triggers/trigger%2Fid?scope=team'), expect.objectContaining({ method: 'PUT', body: JSON.stringify({ expected_revision: 9, reason: 'Changed', definition: { id: trigger.id, kind: 'webhook', instructions: 'Observe', enabled: false, catch_up_once: false, credential_ref: 'credential-ref' } }) }))
  })
  it('preserves goal linkage and exact channel destination without unrelated cron fields', () => {
    expect(triggerDefinition({ ...trigger, kind: 'goal_deadline', goal_id: 'goal', cron: 'old' })).toEqual({ id: trigger.id, kind: 'goal_deadline', instructions: 'Observe', enabled: false, catch_up_once: false, goal_id: 'goal' })
    const binding = { id: 'linked', channel: 'slack', chat_id: 'room', thread_id: 'thread' }
    expect(triggerDefinition({ ...trigger, kind: 'channel', channel_binding: binding }).channel_binding).toEqual(binding)
  })
})
