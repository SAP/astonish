import { BotAPIError, botURL, type Bot, type BotActivity, type BotApproval, type BotRun, type BotScope } from './bots'
import { isBotOperation } from './botOperationResult'
import { teamFetch } from './teamContext'

export interface BotReadCursor { bot_id: string; viewer_id: string; sequence: number }
export interface BotSnapshot {
  bot: Bot; read_cursor: BotReadCursor; latest_sequence: number; unread_count: number
  pending_approvals: BotApproval[]; latest_outcome?: BotRun
  /** Optional scheduler-owned projection; omitted when next-run information is unavailable. */
  run_states?: import('./botRunState').BotRunState[]
  operations?: import('./botOperationResult').BotOperationView[]
  mandate?: { available: boolean } | null
  history_cursor?: number
  next_run_at?: string
}
async function request<T>(id: string, scope: BotScope, team: string | null, suffix: string, signal?: AbortSignal, query?: Record<string, string>, body?: unknown): Promise<T> {
  const response = await teamFetch(botURL(`/${encodeURIComponent(id)}/${suffix}`, scope, query), {
    method: body === undefined ? 'GET' : 'POST', signal,
    ...(body === undefined ? {} : { headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) }),
  }, team)
  if (!response.ok) throw new BotAPIError(response.status, (await response.text()).trim() || 'Bot snapshot request failed')
  return response.status === 204 ? undefined as T : await response.json() as T
}
export const getBotSnapshot = async (id: string, scope: BotScope, team: string | null, signal?: AbortSignal): Promise<BotSnapshot> => {
  const snapshot = await request<BotSnapshot>(id, scope, team, 'snapshot', signal)
  return { ...snapshot, operations: (Array.isArray(snapshot.operations) ? snapshot.operations : []).filter(operation => isBotOperation(operation) && operation.bot_id === id && operation.home_session_id === snapshot.bot.home_session_id), mandate: snapshot.mandate || null }
}
export const getBotReadCursor = (id: string, scope: BotScope, team: string | null, signal?: AbortSignal) => request<BotReadCursor>(id, scope, team, 'read', signal)
export const readBotActivityPage = (id: string, scope: BotScope, team: string | null, after: number, signal?: AbortSignal) => request<{ activity: BotActivity[] }>(id, scope, team, 'activity', signal, { after: String(after), limit: '50' })
export const writeBotReadCursor = (id: string, scope: BotScope, team: string | null, sequence: number, signal?: AbortSignal) => request<void>(id, scope, team, 'read', signal, undefined, { sequence })
