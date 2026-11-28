import { validBotProgress, progressLabels, ProgressPhase, ProgressReason, type BotProgressProjection } from './botProgress'
import type { BotRun } from './bots'
import type { BotMessageReceipt } from './botReviews'

export const RunStatus = { Queued: 'queued', Running: 'running', Waiting: 'waiting_approval', Interrupted: 'interrupted', Succeeded: 'succeeded', Partial: 'partial', Failed: 'failed', Skipped: 'skipped', Cancelled: 'cancelled' } as const
export const BotTerminalReason = { NoAnswer: 'no_eligible_answer' } as const
export interface BotRunState {
  terminal_reason?: typeof BotTerminalReason[keyof typeof BotTerminalReason]
  progress?: BotProgressProjection; committed_at?: string
  work?: BotRun['work']
  schema_version: number; bot_id: string; run_id: string; session_id: string; home_session_id: string
  presentation: string; status: BotRun['status']; state_sequence: number; reason?: string; user_event_id?: string
}
export type BotRunStates = Record<string, BotRunState>
export function botNeutralTerminalLabel(state: BotRunState): string {
  if (state.terminal_reason !== BotTerminalReason.NoAnswer) return ''
  if (state.status === RunStatus.Partial) return 'Partially completed — no Employee answer available'
  if (state.status === RunStatus.Failed) return 'Failed — no Employee answer available'
  return ''
}
export const botRunOpen = (state: BotRunState) => ([RunStatus.Queued, RunStatus.Running, RunStatus.Waiting, RunStatus.Interrupted] as BotRun['status'][]).includes(state.status)
export function botRunStatusLabel(state: BotRunState): string {
  if (botRunOpen(state) && validBotProgress(state.progress, state)) return progressLabels[state.progress.phase]
  const labels: Record<BotRun['status'], string> = { queued: 'Queued', running: 'Working', waiting_approval: 'Waiting for your input', interrupted: 'Interrupted', succeeded: 'Completed', partial: 'Partially completed', failed: 'Failed', skipped: 'Skipped', cancelled: 'Cancelled' }
  return labels[state.status] || 'Status unavailable'
}
export const canContinueBotProgress = (state: BotRunState) => botRunOpen(state) && validBotProgress(state.progress, state) && state.progress.phase === ProgressPhase.Suspended && state.progress.reason === ProgressReason.Paused

export function mergeBotRunState(states: BotRunStates, state: BotRunState, botId: string, home: string): BotRunStates {
  const prior = states[state.run_id]
  const knownProgressRun = prior && validBotProgress(prior.progress, prior)
  if (state.schema_version !== 1 || state.bot_id !== botId || state.home_session_id !== home || (state.presentation !== 'home_interactive' && !botNeutralTerminalLabel(state) && !((state.presentation === '' || state.presentation === undefined) && (validBotProgress(state.progress, state) || knownProgressRun))) || !state.run_id || !state.session_id || !Object.values(RunStatus).includes(state.status) || !Number.isSafeInteger(state.state_sequence) || state.state_sequence <= 0) return states
  if ((prior?.state_sequence || 0) >= state.state_sequence || prior && prior.session_id !== state.session_id) return states
  if (state.work && (state.work.bot_id !== botId || state.work.run_id !== state.run_id || !Number.isSafeInteger(state.work.work_revision) || state.work.work_revision < 1 || state.work.work_revision < (prior?.work?.work_revision || 0))) return states
  const valid = validBotProgress(state.progress, state)
  const progress = valid && (state.progress!.revision < (prior?.progress?.revision || 0) || state.progress!.active_duration_nanos < (prior?.progress?.active_duration_nanos || 0)) ? prior?.progress : valid ? state.progress : undefined
  return { ...states, [state.run_id]: { ...state, progress: state.progress ? progress : prior?.progress, work: state.work || prior?.work } }
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
