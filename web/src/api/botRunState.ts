import type { BotRun } from './bots'
import type { BotMessageReceipt } from './botReviews'

export const RunStatus = { Queued: 'queued', Running: 'running', Waiting: 'waiting_approval', Interrupted: 'interrupted', Succeeded: 'succeeded', Partial: 'partial', Failed: 'failed', Skipped: 'skipped', Cancelled: 'cancelled' } as const
export interface BotRunState {
  work?: BotRun['work']
  schema_version: number; bot_id: string; run_id: string; session_id: string; home_session_id: string
  presentation: string; status: BotRun['status']; state_sequence: number; reason?: string; user_event_id?: string
}
export type BotRunStates = Record<string, BotRunState>
export function mergeBotRunState(states: BotRunStates, state: BotRunState, botId: string, home: string): BotRunStates {
  if (state.schema_version !== 1 || state.bot_id !== botId || state.home_session_id !== home || state.presentation !== 'home_interactive' || !state.run_id || !state.session_id || !Object.values(RunStatus).includes(state.status) || !Number.isSafeInteger(state.state_sequence) || state.state_sequence <= 0) return states
  const prior = states[state.run_id]
  if ((prior?.state_sequence || 0) >= state.state_sequence || prior && prior.session_id !== state.session_id) return states
  if (state.work && (state.work.bot_id !== botId || state.work.run_id !== state.run_id || !Number.isSafeInteger(state.work.work_revision) || state.work.work_revision < 1 || state.work.work_revision < (prior?.work?.work_revision || 0))) return states
  return { ...states, [state.run_id]: { ...state, work: state.work || prior?.work } }
}
export function receiptRunState(receipt: BotMessageReceipt): BotRunState {
  return { schema_version: 1, bot_id: receipt.run.bot_id, run_id: receipt.run.id, session_id: receipt.run.session_id, home_session_id: receipt.home_session_id, presentation: 'home_interactive', status: receipt.run.status, state_sequence: 0, user_event_id: receipt.user_event_id }
}
export const botRunsBusy = (states: BotRunStates) => Object.values(states).some(s => s.status === RunStatus.Queued || s.status === RunStatus.Running)

// A receipt has a transcript cursor, NOT a run-state sequence. It can seed an
// unseen run only; replay/snapshot metadata always wins, including terminal state.
export function mergeBotReceipt(states: BotRunStates, receipt: BotMessageReceipt, botId: string, home: string): BotRunStates {
  if (receipt.run.bot_id !== botId || receipt.home_session_id !== home || states[receipt.run.id]) return states
  return { ...states, [receipt.run.id]: receiptRunState(receipt) }
}
