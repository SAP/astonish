import { BotAPIError, botURL, type Bot, type BotRun, type BotTrigger } from './bots'
import { teamFetch } from './teamContext'

export const SkillStatus = { Draft: 'draft', Active: 'active', Archived: 'archived' } as const
export interface BotSkill {
  id: string; bot_id: string; key: string; title: string; status: typeof SkillStatus[keyof typeof SkillStatus]
  revision: number; head_revision: number; head_digest: string; goal_id?: string
  legacy?: { trigger_id: string; version: number; instructions_digest: string; grant_digest?: string }
  created_at: string; updated_at: string
}
export interface SkillProvenance {
  level: 'proposed' | 'observed' | 'owner_specified'; bot_id: string; run_id?: string; session_id?: string
  event_id?: string; attachment_id?: string; goal_id?: string; goal_revision?: number; source_digest?: string
  quote?: string; evidence_refs?: string[]; recorded_at: string
}
export interface BotSkillRevision {
  schema_version: number; id: string; bot_id: string; skill_id: string; revision: number; parent_revision: number
  parent_digest?: string; digest: string; responsibility: string
  expectations: { id: string; text: string; provenance: SkillProvenance }[]; method: string[]
  sources?: { url: string; purpose: string; access_method: string; fallback?: string; last_observed_at?: string; limitation?: string; observation?: { status: 'returned' | 'failed'; http_status?: number; call_id: string; truncated?: boolean }; provenance: SkillProvenance }[]
  output_format?: string; evaluation_rules?: string[]; provenance: SkillProvenance[]; change_reason: string; reverts_revision?: number
}
export interface BotSkillReceipt {
  schema_version: number; bot_id: string; run_id: string; skill_id: string
  kind: 'selected' | 'created' | 'updated' | 'unchanged' | 'conflict' | 'deferred' | 'rejected'
  used_revision: number; committed_revision?: number; digest?: string; reason?: string
}
export type SkillRun = BotRun & { report: BotRun['report'] & { skill?: BotSkillReceipt } }
export interface SkillPage { limit?: number; after?: string; include_archived?: boolean }
async function read<T>(bot: Pick<Bot, 'id' | 'scope'>, suffix: string, signal?: AbortSignal, page?: SkillPage): Promise<T> {
  const query: Record<string, string> = {}
  if (page?.limit) query.limit = String(page.limit)
  if (page?.after) query.after = page.after
  if (page?.include_archived !== undefined) query.include_archived = String(page.include_archived)
  const response = await teamFetch(botURL(`/${encodeURIComponent(bot.id)}/skills${suffix}`, bot.scope, query), { signal })
  if (!response.ok) throw new BotAPIError(response.status, (await response.text()).trim() || `Skills unavailable (${response.status})`)
  return response.json() as Promise<T>
}
export async function importLegacyBotSkill(bot: Bot, trigger: BotTrigger, idempotencyKey: string): Promise<BotSkill> {
  const digest = await crypto.subtle.digest('SHA-256', new TextEncoder().encode(trigger.instructions))
  const instructions_digest = Array.from(new Uint8Array(digest), byte => byte.toString(16).padStart(2, '0')).join('')
  const response = await teamFetch(botURL(`/${encodeURIComponent(bot.id)}/skills/import-legacy`, bot.scope), {
    method: 'POST', headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ expected_revision: bot.revision, trigger_id: trigger.id, version: bot.version, instructions_digest, idempotency_key: idempotencyKey }),
  })
  if (!response.ok) throw new BotAPIError(response.status, (await response.text()).trim() || `Import failed (${response.status})`)
  return response.json() as Promise<BotSkill>
}
export const listBotSkills = (bot: Pick<Bot, 'id' | 'scope'>, signal?: AbortSignal, page?: SkillPage) => read<{ skills: BotSkill[]; next?: string }>(bot, '', signal, page)
export const getBotSkill = (bot: Bot, id: string, signal?: AbortSignal) => read<BotSkill>(bot, `/${encodeURIComponent(id)}`, signal)
export const getBotSkillRevision = (bot: Bot, id: string, revision: number | 'head', signal?: AbortSignal) => read<BotSkillRevision>(bot, `/${encodeURIComponent(id)}/${revision === 'head' ? 'head' : `revisions/${revision}`}`, signal)
export const listBotSkillRevisions = (bot: Bot, id: string, signal?: AbortSignal, page?: SkillPage) => read<{ revisions: BotSkillRevision[]; next?: string }>(bot, `/${encodeURIComponent(id)}/revisions`, signal, page)
export const listBotSkillRuns = (bot: Bot, id: string, signal?: AbortSignal, page?: SkillPage) => read<{ runs: SkillRun[]; next?: string }>(bot, `/${encodeURIComponent(id)}/runs`, signal, page)
