// Public, data-only checkpoint projection. Never carries execution traces or authority.
export const ProgressPhase = { Working: 'working', Evaluating: 'evaluating', Replanning: 'replanning', Synthesizing: 'synthesizing', Checkpointing: 'checkpointing', WaitingUser: 'waiting_user', WaitingBudget: 'waiting_budget', WaitingAuthorization: 'waiting_authorization', Suspended: 'suspended', Reconciling: 'reconciling' } as const
export const ProgressReason = { Cadence: 'cadence', Recovery: 'recovery', ToolFailure: 'tool_failure', ContextPressure: 'context_pressure', Ready: 'ready_to_synthesize', OwnerInput: 'owner_input', Budget: 'budget', Authorization: 'authorization', Paused: 'paused', UncertainEffect: 'uncertain_effect' } as const
export interface BotProgressProjection {
  schema_version: number; bot_id: string; run_id: string; session_id: string; revision: number
  phase: typeof ProgressPhase[keyof typeof ProgressPhase]; reason?: string; summary?: string
  coalesced_occurrences?: number
  evaluated_at?: string; active_duration_nanos: number; active_since?: string; next_eligible_at?: string
}
export function validBotProgress(progress: BotProgressProjection | undefined, state: { bot_id: string; run_id: string; session_id: string }): progress is BotProgressProjection {
  return !!progress && progress.schema_version === 1 && progress.bot_id === state.bot_id && progress.run_id === state.run_id && progress.session_id === state.session_id && Number.isSafeInteger(progress.revision) && progress.revision > 0 && Object.values(ProgressPhase).includes(progress.phase) && Number.isFinite(progress.active_duration_nanos) && progress.active_duration_nanos >= 0 && (progress.coalesced_occurrences === undefined || Number.isSafeInteger(progress.coalesced_occurrences) && progress.coalesced_occurrences >= 0) && (progress.summary === undefined || typeof progress.summary === 'string') && [progress.evaluated_at, progress.active_since, progress.next_eligible_at].every(time => time === undefined || typeof time === 'string' && Number.isFinite(Date.parse(time)))
}
export const progressLabels: Record<BotProgressProjection['phase'], string> = {
  working: 'Working', evaluating: 'Checking progress', replanning: 'Adjusting approach', synthesizing: 'Preparing result', checkpointing: 'Saving progress', waiting_user: 'Waiting for your input', waiting_budget: 'Waiting for budget', waiting_authorization: 'Authorization required', suspended: 'Paused', reconciling: 'Checking an unconfirmed action',
}
