export const OperationStatus = { Pending: 'pending', Applying: 'applying', Applied: 'applied', Blocked: 'blocked', Failed: 'failed', Cancelled: 'cancelled' } as const
export const OperationDecision = { Execute: 'execute', Clarify: 'clarify', RequireAuthorization: 'require_authorization', Deny: 'deny' } as const
export interface BotOperationView {
  schema_version: 1; bot_id: string; home_session_id: string; operation_id: string
  revision: number; state_sequence: number; source_run_id?: string; source_user_event_id?: string
  operation: string; status: typeof OperationStatus[keyof typeof OperationStatus]
  decision: typeof OperationDecision[keyof typeof OperationDecision]; summary: string; blocker?: string
  verified_schedule?: { trigger_id: string; schedule_id: string; version: number; enabled: boolean; removed: boolean; next_run?: string }
  exception?: { operation_id: string; payload_digest: string; expires_at: string; fields?: { caption: string; value: string }[] }
}
export type BotOperations = Record<string, BotOperationView>
export function isBotOperation(value: unknown): value is BotOperationView {
  if (!value || typeof value !== 'object') return false
  const s = value as BotOperationView
  return s.schema_version === 1 && typeof s.bot_id === 'string' && typeof s.home_session_id === 'string' && !!s.operation_id && typeof s.operation_id === 'string' && typeof s.operation === 'string' && typeof s.summary === 'string' && Object.values(OperationStatus).includes(s.status) && Object.values(OperationDecision).includes(s.decision) && Number.isSafeInteger(s.revision) && s.revision > 0 && Number.isSafeInteger(s.state_sequence) && s.state_sequence > 0
}
// Operation heads are independent: terminal A cannot clear queued B. A snapshot
// and reconnect replay share this same reducer; neither may roll a head back.
export function mergeBotOperation(states: BotOperations, state: unknown, botId: string, home: string): BotOperations {
  if (!isBotOperation(state) || state.bot_id !== botId || state.home_session_id !== home) return states
  const previous = states[state.operation_id]
  if (previous && (state.revision < previous.revision || state.state_sequence <= previous.state_sequence)) return states
  return { ...states, [state.operation_id]: state }
}
export const botOperationsBusy = (states: BotOperations) => Object.values(states).some(s => s.status === OperationStatus.Pending || s.status === OperationStatus.Applying)
export const operationSource = (source?: string) => source && source !== '00000000-0000-0000-0000-000000000000' ? source : undefined
export const operationLabel = (operation: string) => ({ schedule_create: 'Create schedule', schedule_edit: 'Update schedule', schedule_pause: 'Pause schedule', schedule_remove: 'Remove schedule', goal_create: 'Create goal', goal_edit: 'Update goal', task_once: 'Run task', memory_remember: 'Remember information', memory_correct: 'Correct memory', memory_forget: 'Forget information', responsibility: 'Update responsibility', persona: 'Update persona', configuration: 'Update configuration', activate: 'Activate Bot', resume: 'Resume Bot' } as Record<string, string>)[operation]
