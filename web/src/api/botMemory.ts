import { teamFetch } from './teamContext'
import { BotAPIError, botURL, type Bot } from './bots'

export interface BotDocument { name: string; content: string; source_version?: string; read_only?: boolean; write_unsupported_reason?: string; record_mappings?: MemoryReviewSource[] }
export interface BotDocuments { bot_id: string; version: number; revision: number; documents: BotDocument[] }
export interface MemoryRecord { id?: string; snippet: string; path: string; session_id?: string; category?: string; created_by?: string; created_at?: string; attribution?: Record<string, unknown> }
export interface MemoryReviewSource { id: string; session_id: string; expected_fingerprint: string }
export interface BotMemoryHealth {
  bot_id: string; scope: string; review_only: boolean; apply_supported: boolean
  snapshot: { records: MemoryRecord[]; fingerprints: Record<string, string>; fingerprint: string; truncated: boolean; evaluated_at: string }
  health: { evaluated_at: string; recommendation_count: number; recommendations: { id: string; title: string; description: string; severity: string; memory_ids: string[]; card?: unknown }[] }
}
export interface DocumentEdit { expected_revision: number; content: string; reason: string; confirmed: boolean }
export interface CorrectionRequest { id: string; expected_revision: number; superseded_source: string; replacement: string; goal_ids: string[]; evidence: { summary: string; source: string; observed_at: string } }
export interface MemoryReview { id: string; expected_revision: number; kind: 'document' | 'consolidate'; document?: string; reason: string; confirmed: boolean; edits: { session_id: string; sources: MemoryReviewSource[]; content: string; category: string }[] }
export interface MemoryReviewReceipt { id: string; bot_id: string; revision: number; actor_id: string; request_fingerprint: string; replacement_ids: string[]; sources: MemoryRecord[]; reason: string }
async function request<T>(bot: Pick<Bot, 'id' | 'scope'>, path: string, method = 'GET', body?: unknown, signal?: AbortSignal): Promise<T> {
  const response = await teamFetch(botURL(`/${encodeURIComponent(bot.id)}${path}`, bot.scope), { method, signal, ...(body === undefined ? {} : { headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) }) })
  const text = await response.text()
  if (!response.ok) throw new BotAPIError(response.status, text.trim() || `Bot memory request failed (${response.status})`)
  return text ? JSON.parse(text) as T : undefined as T
}
export const getBotDocuments = (bot: Pick<Bot, 'id' | 'scope'>, signal?: AbortSignal) => request<BotDocuments>(bot, '/documents', 'GET', undefined, signal)
export const saveBotDocument = (bot: Bot, name: string, edit: DocumentEdit) => request<{ revision: number; version: number }>(bot, `/documents/${name.split('/').map(encodeURIComponent).join('/')}`, 'PUT', edit)
export const getBotMemoryHealth = (bot: Pick<Bot, 'id' | 'scope'>, signal?: AbortSignal) => request<BotMemoryHealth>(bot, '/memory-health', 'GET', undefined, signal)
export const submitBotCorrection = (bot: Bot, correction: CorrectionRequest) => request<{ id: string; intent: string }>(bot, '/corrections', 'POST', correction)
export const reconcileBotCorrection = (bot: Bot, id: string) => request<{ id: string; memory_applied: boolean }>(bot, `/corrections/${encodeURIComponent(id)}/reconcile`, 'POST')
export const applyBotMemoryReview = (bot: Bot, review: MemoryReview) => request<MemoryReviewReceipt>(bot, review.kind === 'consolidate' ? '/memory-health/consolidate' : `/documents/${(review.document || '').split('/').map(encodeURIComponent).join('/')}/ingest`, 'POST', review)
