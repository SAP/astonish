import { BotAPIError, botURL, type Bot, type BotGoal, type BotEvidence } from './bots'
import { teamFetch } from './teamContext'

export const GoalStatus = { Active: 'active', Completed: 'completed' } as const
export const TrackedStatus = { Open: 'open', Blocked: 'blocked', Completed: 'completed', Cancelled: 'cancelled' } as const
export type TrackedStatusValue = typeof TrackedStatus[keyof typeof TrackedStatus]
export interface GoalFields { title: string; description: string; completion_criteria: string; due_at: string | null }
export interface GoalRequest extends GoalFields { expected_revision: number }
export interface OwnerConfirmation { summary: string; source: string; observed_at: string }
export interface CompletionRequest { expected_revision: number; evidence: OwnerConfirmation }
export interface TrackedItem { id: string; goal_id: string; title: string; status: TrackedStatusValue; reason?: string; due_at?: string; evidence: BotEvidence[] | null }
export interface TrackedItemRequest { expected_goal_revision: number; title: string; status: TrackedStatusValue; reason: string; due_at: string | null; evidence: OwnerConfirmation[] }
const segment = encodeURIComponent
async function request<T>(bot: Bot, path: string, method: string, body?: GoalRequest | CompletionRequest | TrackedItemRequest, signal?: AbortSignal): Promise<T> {
  const response = await teamFetch(botURL(`/${segment(bot.id)}/goals${path}`, bot.scope), {
    method, signal, ...(body === undefined ? {} : { headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) }),
  })
  if (!response.ok) throw new BotAPIError(response.status, (await response.text()).trim() || `Goal request failed (${response.status})`)
  if (response.status === 204) return undefined as T
  return await response.json() as T
}
export const createGoal = (bot: Bot, fields: GoalFields) => request<BotGoal>(bot, '', 'POST', { ...fields, expected_revision: 0 })
export const editGoal = (bot: Bot, id: string, body: GoalRequest) => request<BotGoal>(bot, `/${segment(id)}`, 'PUT', body)
export const confirmGoal = (bot: Bot, id: string, body: CompletionRequest) => request<BotGoal>(bot, `/${segment(id)}/complete`, 'POST', body)
export const listTrackedItems = (bot: Bot, goal: string, signal?: AbortSignal) => request<{ items: TrackedItem[] }>(bot, `/${segment(goal)}/tracked-items`, 'GET', undefined, signal)
export const saveTrackedItem = (bot: Bot, goal: string, item: string, body: TrackedItemRequest) => request<void>(bot, `/${segment(goal)}/tracked-items/${segment(item)}`, 'PUT', body)
